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
// # And both halves reach it now
//
// This used to say the opposite: a sign-in through the provider never calls
// `Vouch`, so there was nothing for a delegation to ride back on and `/me`
// answered a refusal rather than falling back to this app's own credential.
// D23 recorded that and said to leave the seam -- *exchanging an `id_token` for
// a delegation is roster accepting somebody else's assertion as proof, which is
// a D19 question and takes its own entry.*
//
// It has one: **D49**. roster does not check the token, because being the
// relying party is what `connection.proto` says roster is not -- so this app
// checks it, `Vouch.Accept` hands the claim over, and `frontdoor` holds the
// delegation beside the session exactly as it does after a password.
//
// What did not change is the thing the refusal was protecting: the page is
// drawn with a credential **for the person**, never with this app's own.

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

	// Keys is what acts **as** them, which is a different list from `SignsIn`
	// and is on this page for the reason D51 put it on the operator's: a key is
	// a string in somebody's script, and the question a person asks about one
	// is *is this still being used* rather than *what is it*.
	Keys []keyOf `json:"keys"`
}

// keyOf is one `rt_`, and never the key.
//
// What is stored is a hash, so there is nowhere the secret could come from --
// it is readable exactly once, in the answer to minting it.
type keyOf struct {
	Id      string   `json:"id"`
	Alias   string   `json:"alias"`
	Methods []string `json:"methods"`

	// Used is when it was last presented, empty for never -- which is the field
	// that answers *is anything still calling with this* before somebody
	// revokes it. Expires is when it stops working, empty for one that does
	// not.
	Used    string `json:"used,omitempty"`
	Expires string `json:"expires,omitempty"`
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
		Keys:       []keyOf{},
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
	for _, k := range v.GetKeys() {
		out.Keys = append(out.Keys, key(k))
	}

	return out
}

func key(v *rstr.SignInKey) keyOf {
	k := keyOf{
		Id:      base64.RawURLEncoding.EncodeToString(v.GetId()),
		Alias:   v.GetAlias(),
		Methods: v.GetMethods(),
	}
	if u := v.GetDateUsed(); u != nil {
		k.Used = u.AsTime().Format("2006-01-02")
	}
	if u := v.GetDateExpires(); u != nil {
		k.Expires = u.AsTime().Format("2006-01-02")
	}

	return k
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

// mintKey is `POST /me/keys`: an `rt_` that acts as the person, made by them.
//
// The self-service half of what an operator's console has had since D51, and
// through `MeService` for the reason `unlink` is: `IssueService.IssueKey` takes
// a `HolderRef`, so the smallest role covering *mint a key for myself* would be
// *mint one for anybody in this tenant*.
//
// The **secret comes back once and is not stored here.** This app writes it
// into the response and keeps no copy: what roster holds is a hash, and a
// reference app that cached it to show again would be teaching the opposite of
// what the field is for.
func (a *App) mintKey(w http.ResponseWriter, r *http.Request) {
	ctx, err := a.acting(r.Context(), r)
	if err != nil {
		http.Error(w, "no", http.StatusForbidden)
		return
	}

	var in struct {
		Alias   string   `json:"alias"`
		Methods []string `json:"methods"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		http.Error(w, "no", http.StatusBadRequest)
		return
	}

	v, err := a.me_.IssueKey(ctx, rstr.MeIssueKeyRequest_builder{
		Alias:   in.Alias,
		Methods: in.Methods,
	}.Build())
	if err != nil {
		switch status.Code(err) {
		case codes.InvalidArgument:
			http.Error(w, "a name, and at least one method", http.StatusBadRequest)
		case codes.PermissionDenied:
			// The refusal a person can act on: they asked for a method they do
			// not hold, and `server/core` is what said so. Told rather than
			// flattened, because "no" here reads as a bug in the page.
			http.Error(w, "you cannot hand out something you do not hold", http.StatusForbidden)
		default:
			http.Error(w, "no", http.StatusBadGateway)
		}

		return
	}

	w.Header().Set("content-type", "application/json")
	w.Header().Set("cache-control", "no-store")
	_ = json.NewEncoder(w).Encode(struct {
		Token string `json:"token"`
		Key   keyOf  `json:"key"`
	}{Token: v.GetToken(), Key: key(v.GetKey())})
}

// revokeKey is `DELETE /me/keys/{id}`: one of the person's own.
//
// A *which* and never a *whose*, exactly as `unlink` is -- and with no
// last-one rule, because a key is not a way **in**. Revoking every one of them
// locks nobody out of anything.
func (a *App) revokeKey(w http.ResponseWriter, r *http.Request) {
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

	if _, err := a.me_.RevokeKey(ctx, rstr.MeRevokeKeyRequest_builder{Id: id}.Build()); err != nil {
		http.Error(w, "no", http.StatusBadRequest)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// changePassword is the person changing their own, through `Credential.Set`
// with their **own** reference -- the same verb an operator calls about somebody
// else, and this app is the layer that passes only the session's person. That
// is the line: RBAC grants the method, *whose row* is roster's `mayReach` (your
// own always passes), and anything narrower is the app's to keep, which it does
// by never taking a reference from the request.
//
// The current password is the reauth: roster requires it for your own row and
// verifies it before writing the new one, so a stolen delegation that could act
// as this person still cannot change what they sign in with without knowing it.
//
// Nothing is answered with. A `CredentialSetResponse` is empty, because what
// would go in it is the password the caller just chose.
func (a *App) changePassword(w http.ResponseWriter, r *http.Request) {
	ctx, err := a.acting(r.Context(), r)
	if err != nil {
		http.Error(w, "no", http.StatusForbidden)
		return
	}

	var in struct {
		Current string `json:"current"`
		Secret  string `json:"secret"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		http.Error(w, "no", http.StatusBadRequest)
		return
	}

	who, ok := a.door.Who(r.Context(), r)
	if !ok {
		http.Error(w, "no", http.StatusForbidden)
		return
	}

	if _, err := a.roster.Credential().Set(ctx, rstr.CredentialSetRequest_builder{
		Ref:     rstr.HolderRef_builder{Id: who.Bytes()}.Build(),
		Current: []byte(in.Current),
		Secret:  []byte(in.Secret),
	}.Build()); err != nil {
		switch status.Code(err) {
		case codes.InvalidArgument:
			http.Error(w, "a new password", http.StatusBadRequest)
		case codes.PermissionDenied:
			http.Error(w, "that is not your current password", http.StatusForbidden)
		case codes.FailedPrecondition:
			http.Error(w, "you have no password to change; a first one is set for you, not by you", http.StatusConflict)
		default:
			http.Error(w, "no", http.StatusBadGateway)
		}
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// enrolFactor is the person adding a second factor of their own, through
// `Credential.Enrol` with their own reference -- `changePassword`'s shape, one
// verb over.
//
// TOTP only from this page: a `webauthn` enrolment is a browser ceremony that
// needs `navigator.credentials.create()` and the attestation it answers, which
// is a page this example does not draw. What comes back for `totp` is the seed
// as an `otpauth://` URI, shown once -- the page turns it into a QR code and
// tells the person to prove it with one code, which is what confirms it.
func (a *App) enrolFactor(w http.ResponseWriter, r *http.Request) {
	ctx, err := a.acting(r.Context(), r)
	if err != nil {
		http.Error(w, "no", http.StatusForbidden)
		return
	}

	var in struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		http.Error(w, "no", http.StatusBadRequest)
		return
	}

	who, ok := a.door.Who(r.Context(), r)
	if !ok {
		http.Error(w, "no", http.StatusForbidden)
		return
	}

	v, err := a.roster.Credential().Enrol(ctx, rstr.CredentialEnrolRequest_builder{
		Ref:  rstr.HolderRef_builder{Id: who.Bytes()}.Build(),
		Kind: "totp",
		Name: in.Name,
	}.Build())
	if err != nil {
		switch status.Code(err) {
		case codes.InvalidArgument:
			http.Error(w, "a name for the factor", http.StatusBadRequest)
		case codes.Unimplemented:
			http.Error(w, "this deployment holds no key, so it cannot hold a second factor", http.StatusNotImplemented)
		default:
			http.Error(w, "no", http.StatusBadGateway)
		}
		return
	}

	w.Header().Set("content-type", "application/json")
	w.Header().Set("cache-control", "no-store")
	_ = json.NewEncoder(w).Encode(struct {
		Uri string `json:"uri"`
	}{Uri: v.GetUri()})
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
		// No delegation, which now means no session rather than the wrong kind
		// of one: both halves mint one. See the note at the top of this file.
		http.Error(w, "not signed in", http.StatusForbidden)
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
