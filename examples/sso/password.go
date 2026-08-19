package sso

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	rstr "github.com/lesomnus/roster/rstr"
)

// The pages this app draws about the person who signed in.
//
// # What is not here any more
//
// The sign-in itself. Two forms, a half session, the delegation held beside the
// session and the pair of headers a call to roster carries are the same in
// every app that does this, so they are `frontdoor` -- which is D24 §6, and it
// was written last for the reason D24 gives: *extracting first means guessing
// what to extract.*
//
// What stayed is what is this app's: which provider it uses, which pages it
// draws, and what it asks roster on the person's behalf.
//
// # And what this app deliberately does not reach
//
// A sign-in through the provider never calls `Vouch` -- the secret is somebody
// else's to check -- so there is nothing for a delegation to ride back on, and
// `/me` answers a refusal rather than falling back to this app's own
// credential. D23 recorded that and said to leave the seam: exchanging an
// `id_token` for a delegation is roster accepting somebody else's assertion as
// proof, which is a D19 question and takes its own entry.

// record is the person's own page, as a page needs it.
//
// Identities carry their identifier, because the one act on this screen that
// names a row is removing one. Nothing else here does, and nothing else here
// carries one.
type recordOf struct {
	Alias      string   `json:"alias"`
	Name       string   `json:"name"`
	Emails     []string `json:"emails"`
	SignsIn    []wayIn  `json:"signs_in"`
	MayCallAll []string `json:"may_call"`
	Teams      []string `json:"teams"`
}

type wayIn struct {
	// Kind is "password" or "totp" for something roster holds, and the provider
	// for something somebody else does.
	Kind string `json:"kind"`

	// Id is set only for an identity, which is the only kind this screen can
	// remove: a password is changed rather than taken away, and taking away a
	// second factor is an act with its own name.
	Id string `json:"id,omitempty"`

	Which string `json:"which,omitempty"`
	Since string `json:"since,omitempty"`
}

func record(v *rstr.MeGetResponse) recordOf {
	out := recordOf{
		Alias:      v.GetAlias(),
		Name:       v.GetName(),
		Emails:     addresses(v),
		MayCallAll: v.GetMethods(),
		SignsIn:    []wayIn{},
		Teams:      []string{},
	}
	for _, c := range v.GetCredentials() {
		out.SignsIn = append(out.SignsIn, wayIn{Kind: c.GetKind(), Which: c.GetName()})
	}
	for _, i := range v.GetIdentities() {
		out.SignsIn = append(out.SignsIn, wayIn{
			Kind:  i.GetProvider(),
			Id:    base64.RawURLEncoding.EncodeToString(i.GetId()),
			Which: i.GetSubject(),
			Since: i.GetDateCreated().AsTime().Format("2006-01-02"),
		})
	}
	for _, t := range v.GetTeams() {
		out.Teams = append(out.Teams, t.GetAlias())
	}

	return out
}

// unlink is `DELETE /me/ways/{id}`: one of the person's own ways in.
//
// Through `MeService`, which is the whole point of it existing: `Identity`
// narrows by the **tenant**, so removing one through that service would need a
// permission that reaches everybody else's -- the leak D23 exists to remove,
// arriving on the one screen it is most tempting on.
func (a *App) unlink(w http.ResponseWriter, r *http.Request) {
	ctx, err := a.acting(r.Context(), r)
	if err != nil {
		http.Error(w, "no", http.StatusForbidden)
		return
	}

	id, err := base64.RawURLEncoding.DecodeString(r.PathValue("id"))
	if err != nil {
		http.Error(w, "no", http.StatusBadRequest)
		return
	}

	if _, err := a.me_.Unlink(ctx, rstr.MeUnlinkRequest_builder{Id: id}.Build()); err != nil {
		// The refusals a person may act on and the ones they may not are one
		// answer here, with one exception: the last way in. Somebody told
		// "removed" and finding they cannot sign in tomorrow is the failure
		// this rule exists to prevent, so they are told now.
		if status.Code(err) == codes.FailedPrecondition {
			http.Error(w, "this is the only way you can sign in", http.StatusConflict)
			return
		}

		http.Error(w, "no", http.StatusBadRequest)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// everywhere is `POST /me/sign-out-everywhere`.
//
// Two halves that belong to two places. roster answers *invalid since when*,
// which is one write and reaches every app; this app answers *what is still
// alive*, which is its own cookie -- so the button ends both, and neither knows
// how the other did it.
func (a *App) everywhere(w http.ResponseWriter, r *http.Request) {
	ctx, err := a.acting(r.Context(), r)
	if err != nil {
		http.Error(w, "no", http.StatusForbidden)
		return
	}

	if _, err := a.me_.SignOutEverywhere(ctx,
		rstr.MeSignOutEverywhereRequest_builder{}.Build()); err != nil {
		http.Error(w, "no", http.StatusBadGateway)
		return
	}

	// And this app's own, because roster does not know it exists.
	a.door.SignOut(w, r)
}

func addresses(v *rstr.MeGetResponse) []string {
	out := make([]string, 0, len(v.GetEmails()))
	for _, e := range v.GetEmails() {
		out = append(out, e.GetAddress())
	}

	return out
}

func providers(v *rstr.MeGetResponse) []string {
	out := make([]string, 0, len(v.GetIdentities()))
	for _, i := range v.GetIdentities() {
		out = append(out, i.GetProvider())
	}

	return out
}

func kinds(v *rstr.MeGetResponse) []string {
	out := make([]string, 0, len(v.GetCredentials()))
	for _, c := range v.GetCredentials() {
		out = append(out, c.GetKind())
	}

	return out
}

// me is `GET /me`: the person's own record, read **as them**.
//
// This is the whole reason the delegation exists, so it is worth saying what
// the alternatives were. This app holds a credential that can read every tenant
// it serves; drawing somebody's own page with it means filtering in this
// process, and D17 already named what that costs -- *the kind of thing that
// leaks by being forgotten* -- with one bug here exposing everybody, on reads
// roster answered correctly.
//
// So the call goes out narrowed to one person and to one method, and if this
// code asked for anything else it would be refused by roster rather than by
// remembering.
func (a *App) me(w http.ResponseWriter, r *http.Request) {
	ctx, err := a.acting(r.Context(), r)
	if err != nil {
		// Signed in through the provider, which does not produce one. See the
		// note at the top of this file: the exchange that would is a decision
		// nobody has taken.
		http.Error(w, "this session cannot read its own record; sign in with a password", http.StatusForbidden)
		return
	}

	v, err := a.me_.Get(ctx, rstr.MeGetRequest_builder{}.Build())
	if err != nil {
		http.Error(w, "cannot read", http.StatusBadGateway)
		return
	}

	w.Header().Set("content-type", "application/json")
	w.Header().Set("cache-control", "no-store")
	_ = json.NewEncoder(w).Encode(record(v))
}

// acting is [frontdoor.Door.Acting], and it is one line because that is the
// point of the package.
//
// Kept as a method rather than called through `a.door` at each site so that
// what this app's own pages depend on is one name, and so the three handlers
// below read as they did when the sign-in was written here.
func (a *App) acting(ctx context.Context, r *http.Request) (context.Context, error) {
	return a.door.Acting(ctx, r)
}
