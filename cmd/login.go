package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/lesomnus/payday/auth/authsession"
	"github.com/lesomnus/payday/frame"
	"github.com/lesomnus/payday/pdid"

	app "github.com/lesomnus/roster/rstr"
	"github.com/lesomnus/roster/server/vouch"
)

// Login is what a console posts a password to.
//
// # Who signs in here
//
// An **operator**: a holder of the control plane, which is where the people who
// run this deployment live. Not a customer. roster's data plane is a store of
// other people's people, and nothing in it signs in to roster itself -- a
// customer signs in to whatever product serves them, and that product asks
// `VouchService`. See `docs/login.md`.
//
// So a deployment with no control plane has no console and this is not served.
// There would be nobody to be.
//
// # It is the seam payday left and could not fill
//
// `auth` reads a credential and does not issue one, and issuing is an HTTP
// endpoint: a browser has nowhere safe to keep a secret, so what it gets is an
// opaque cookie naming a session this server keeps. `authsession` is both
// halves of that and takes one thing from the app -- how to check a secret --
// which for roster is the control plane's own `VouchService`.
//
// # Why it reads the ungated server
//
// Because working out who somebody is cannot require knowing who they are.
// This runs before there is a frame at all, which is the same reason
// `cmd.Resolver` and `vouch.Verify` read one; and the control plane's rows are
// unreachable any other way, since nothing serves them.
func Login(s *Server) authsession.Verify {
	v := vouch.New(s.Ungated, s.Ungated)

	return func(ctx context.Context, r *http.Request) (authsession.Session, error) {
		var body struct {
			Alias    string `json:"alias"`
			Password string `json:"password"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(nil, r.Body, 4<<10)).Decode(&body); err != nil {
			return authsession.Session{}, fmt.Errorf("what was posted is not a sign-in: %w", err)
		}
		if body.Alias == "" || body.Password == "" {
			return authsession.Session{}, errors.New("both an alias and a password")
		}

		// No tenant field on the request. A control plane has exactly one, so
		// asking a console which to sign in to would be asking a question with
		// one answer -- and a field with one answer is one somebody eventually
		// fills in wrongly.
		//
		// Its **alias**, because that is what `VouchWho` names a tenant by: the
		// pair a username field and a tenant selector make, rather than an
		// identifier a form would have to be told.
		who, err := s.Ent.Tenant.Query().First(ctx)
		if err != nil {
			return authsession.Session{}, fmt.Errorf("this deployment has no owner: %w", err)
		}

		res, err := v.Verify(ctx, app.VouchVerifyRequest_builder{
			Who: app.VouchWho_builder{
				Tenant: who.Alias,
				Alias:  body.Alias,
			}.Build(),
			Secret: []byte(body.Password),
		}.Build())
		if err != nil {
			return authsession.Session{}, err
		}
		if !res.GetOk() {
			// One answer however it was wrong, and `authsession` gives the same
			// one for every failure besides. Which of "no such person", "wrong
			// password" and "locked" it was is an oracle, and the lockout in
			// `server/vouch` is what makes guessing expensive rather than this.
			return authsession.Session{}, errors.New("no")
		}

		k, err := pdid.From(res.GetHolder())
		if err != nil {
			return authsession.Session{}, err
		}
		t, err := pdid.From(res.GetTenant())
		if err != nil {
			return authsession.Session{}, err
		}

		return authsession.Session{
			Id:       k.String(),
			TenantId: t.String(),

			// Whatever this operator may do, which their bindings decide on
			// every call. A session is not the place to narrow it: a grant here
			// would be a second answer to a question the policy already
			// answers, frozen at the moment somebody signed in.
			Grant: frame.Whole(),
		}, nil
	}
}
