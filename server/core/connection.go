package core

import (
	"context"
	"errors"
	"net/url"
	"strings"
	"unicode"

	"github.com/lesomnus/z"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/lesomnus/payday/pderr"
	"github.com/lesomnus/payday/slug"

	app "github.com/lesomnus/roster/rstr"
	"github.com/lesomnus/roster/server/front"
)

// coreConnection is the layer over the generated `ConnectionService`, for its
// one overlay: `Update`, which `connection_svc.ext.proto` argues for.
type coreConnection struct {
	Core
	app.ConnectionServiceServer
}

func (s Core) Connection() app.ConnectionServiceServer {
	return coreConnection{s, s.Next().Connection()}
}

// A connection's secret is the deployment's, and goes to the connection's
// issuer.
//
// `secret_ref` names one of the deployment's secrets, and the front door sends
// what it names to the issuer, as the client secret of the code exchange. Both
// halves were a tenant's to write, so a tenant's administrator could name any
// secret the front door holds -- another tenant's client secret, the front
// door's own key -- beside an issuer of their own, start a sign-in through it,
// and be handed the secret by the front door.
//
// So the two writes that decide where a secret goes are the deployment's: a
// reference that is new, and a new issuer for a connection that has one. The
// deployment is the file it declares its tenants in, the CLI on its database,
// a key of its own, and its operators ([Core.deployment]). A tenant's
// administrator may keep the
// reference they were given or take it away, which hands nothing to anybody,
// may change everything else about the connection, and a connection with no
// secret -- a public client, which PKCE is for -- is entirely theirs.
//
// The same answer `TenantProfile.slack_secret_ref` gives, for the same reason:
// a reference is the deployment naming its own secret.
func (s Core) maySend(ctx context.Context, was *app.Connection, ref, issuer string) error {
	if ref == "" || s.deployment(ctx) {
		return nil
	}
	if ref != was.GetSecretRef() {
		return status.Error(codes.PermissionDenied,
			"secret_ref: names one of the deployment's secrets, which the deployment writes; a tenant may keep it or take it away")
	}
	if issuer != was.GetIssuer() {
		return status.Error(codes.PermissionDenied,
			"issuer: a connection with a secret sends it to its issuer, so moving it is the deployment's; take the secret away first, or ask the deployment")
	}

	return nil
}

// A connection is held to a form: a name that is an alias, an issuer that is
// an address, and a client that is called something.
//
// None of it was. A second connection `Entra` was taken beside `entra`, and so
// were a name `a/b c?`, an issuer `not a url` and no `client_id` at all: rows
// that sign nobody in, found out at the door by whoever tried. And none of it
// can be asked for later -- a rule that comes after rows it would refuse
// breaks whoever wrote them -- so it is asked from the start, of the
// deployment as of a tenant, since what a row has to be to work is the same
// whoever wrote it.

// named holds what a provider is called to the grammar an alias is held to
// (`slug.Validate`): lower-case letters, digits and single hyphens, beginning
// with a letter.
//
// A connection's name is every one of its identities' `provider`, and a
// segment of the path its answer comes back to (`arrives.Trip`); both are
// compared as written. So one directory under two names that differ by a
// capital is one person twice.
//
// Refused, and not folded as an alias somebody typed is (`slug.ParseAlias`):
// the name is written again wherever the provider is told its redirect, and a
// row called something other than what was typed is not found by it.
func named(field, v string) error {
	err := slug.Validate(v)
	if err == nil {
		return nil
	}

	var broke *slug.AliasError
	if errors.As(err, &broke) {
		return pderr.Invalidf(field, "%s", broke.Why)
	}

	return pderr.At(field, err)
}

// addressed holds a connection's issuer to being where a provider is asked: an
// absolute `https` or `http` address, with a host, and with neither a query
// nor a fragment -- which is what OpenID Connect says an issuer is, and what
// GitHub's is spelled as (`https://github.com`).
//
// And with nobody's name and password in it. An issuer is public -- it is
// answered to whoever reads the row -- and a front door would send what is
// written there with every request.
//
// The scheme is written in small letters, as a provider publishes it. A front
// door finds a provider by comparing its issuer with the one a document or a
// token says, exactly, so another spelling is another issuer.
//
// # Plain `http` is not refused here
//
// Whether a provider may be asked in the clear depends on where it is: a
// directory on the premises often is, over a network its deployment trusts.
// Which hosts those are is said to a front door (`private_hosts`) and to
// nothing else, so it is the front door that refuses (`arrives/dial.go`).
func addressed(field, v string) error {
	if v == "" {
		return pderr.Invalidf(field, "must not be empty: where the provider publishes what it is, as `https://accounts.google.com`")
	}

	u, err := url.Parse(v)
	if err != nil ||
		!(strings.HasPrefix(v, "https://") || strings.HasPrefix(v, "http://")) ||
		u.Hostname() == "" || u.User != nil ||
		strings.ContainsAny(v, "?#") || strings.ContainsFunc(v, unicode.IsSpace) {
		return pderr.Invalidf(field,
			"%q is not an issuer: an address written `https://` (or `http://`) with a host, and no query, fragment, password or space, "+
				"as the provider publishes it", v)
	}

	return nil
}

// called holds a connection to naming this app as its provider knows it. A
// connection with no `client_id` is a round trip the provider refuses.
func called(field, v string) error {
	if v == "" {
		return pderr.Invalidf(field, "must not be empty: what the provider calls this app")
	}

	return nil
}

// The claims a connection may name as a person's subject. `Connection` says
// why there are two: `sub` is the person to most providers, and Entra's is
// pairwise, so it names them as `oid`.
const (
	claimSub = "sub"
	claimOid = front.ClaimOid
)

// subjectClaimOf is the claim a connection names, with empty read as the `sub`
// it means.
func subjectClaimOf(v string) string {
	if v == "" {
		return claimSub
	}

	return v
}

// maySubject holds a connection's `subject_claim` to the claims a front door
// knows how to read, and its moves to the one direction a tenant may take.
//
// Forward is anybody's who may write the connection: everybody already signed
// in moves at their next sign-in, by the token that carries both claims
// (`IdentityService.Resubject`), and nobody is lost -- where the door issues
// the claim at all, which [mayNameBy] asks. Back is the deployment's:
// everybody already moved has an identity the old claim never names, and would
// be a stranger at their next sign-in -- refused, or enrolled a second time.
func (s Core) maySubject(ctx context.Context, was, now string) error {
	switch now {
	case "", claimSub, claimOid:
	default:
		return pderr.Invalidf("subject_claim", "%q is not a claim a front door reads as a subject; `sub`, or `oid` for Entra", now)
	}
	if subjectClaimOf(now) == subjectClaimOf(was) || s.deployment(ctx) {
		return nil
	}
	if subjectClaimOf(was) == claimSub && now == claimOid {
		return nil
	}

	return status.Error(codes.PermissionDenied,
		"subject_claim: going back would leave everybody already moved unknown at their next sign-in, so it is the deployment's")
}

// mayNameBy refuses a connection that names its people by `oid` at a door
// known to issue none, whichever half of that a write moves: the claim, or the
// door under a claim already named. `field` is the half it moved.
//
// [Core.maySubject] read the claim and never the issuer, so `oid` was taken at
// GitHub's, which issues no ID token to carry a claim, and at Google's, whose
// tokens carry no `oid`. A front door then refuses every sign-in through the
// connection -- one person under two subjects is two people, so it does not
// fall back to `sub` -- and going back is the deployment's. A tenant's
// administrator could close a door of their tenant and not open it again.
//
// Refused to the deployment as well: it is as true of it. And only where the
// door is one of the two this can tell ([spelt]); a claim some other provider
// does not issue is found out at that provider's first sign-in, and a GitHub
// Enterprise Server is one only the front door knows (`arrives/github.go`).
func mayNameBy(field, claim, issuer string) error {
	if subjectClaimOf(claim) != claimOid {
		return nil
	}

	why := ""
	switch spelt(issuer) {
	case gitHubs:
		why = "GitHub issues no ID token, so its people are their account's id and no claim names them"
	case googles:
		why = "Google's ID token carries no `oid`"
	default:
		return nil
	}

	return pderr.Invalidf(field,
		"%s, and a connection at %s that named its people by `oid` would sign nobody in; `oid` is Entra's", why, issuer)
}

// claimMoved is the half a write moved, for [mayNameBy] to name: the claim,
// when it says one, and otherwise the door.
func claimMoved(says bool) string {
	if says {
		return "subject_claim"
	}

	return "issuer"
}

// The two doors this layer knows by their issuer alone, as `arrives` reads
// them (`arrives/github.go`, `arrives/google.go`).
const (
	gitHubs = "https://github.com"
	googles = "https://accounts.google.com"
)

// spelt is an issuer as two spellings of it compare here: without the space
// around it or the slash after it, in small letters.
func spelt(issuer string) string {
	return strings.ToLower(strings.TrimSuffix(strings.TrimSpace(issuer), "/"))
}

// oneProvisions refuses a second connection a tenant's directory provisions
// through. A directory names the people it makes once, so two would be two
// places the same person could be linked, and the second the wrong one.
func (s Core) oneProvisions(ctx context.Context, tenant []byte, self []byte) error {
	after := ""
	for {
		vs, err := s.Next().Connection().List(ctx, app.ConnectionListRequest_builder{
			Filters: []*app.ConnectionFilter{app.ConnectionFilter_builder{
				Tenant: app.TenantRef_builder{Id: tenant}.Build(),
			}.Build()},
			After: after,
		}.Build())
		if err != nil {
			return err
		}
		for _, v := range vs.GetItems() {
			if v.GetProvisions() && !bytesEq(v.GetId(), self) {
				return status.Errorf(codes.FailedPrecondition,
					"provisions: %s already does for this tenant, and a directory provisions through one", v.GetName())
			}
		}

		after = vs.GetNext()
		if after == "" {
			return nil
		}
	}
}

// A directory provisions the people of one organisation, linked at the door
// they sign in through. What lets a key a directory's cloud presents write that
// link (`HolderService.Provision`) is that it lets in nobody the organisation
// had not: a subject at the organisation's own issuer is an account the
// organisation made. At an issuer anybody can make an account at, the same
// write is a way in for whoever holds the account it names -- a key that leaked
// names its holder's own GitHub id, and they sign in as somebody new.
//
// openTo is who may hold an account at an issuer when that is anybody, and
// empty for one organisation's. The shapes are the ones `arrives` reads a
// provider off (`arrives/github.go`, `arrives/microsoft.go`); a door that lets
// anybody sign up of its own accord is the same, and cannot be told from here.
func openTo(issuer string) string {
	at := spelt(issuer)
	switch at {
	case gitHubs:
		return "anybody with a GitHub account"
	case googles:
		return "anybody with a Google account"
	}
	for _, v := range []struct{ path, who string }{
		{"/common/v2.0", "anybody with a Microsoft account"},
		{"/organizations/v2.0", "anybody with a work or school Microsoft account"},
		{"/consumers/v2.0", "anybody with a personal Microsoft account"},
		{"/" + personalAccounts + "/v2.0", "anybody with a personal Microsoft account"},
	} {
		if strings.HasSuffix(at, v.path) {
			return v.who
		}
	}

	return ""
}

// personalAccounts is the one directory every personal Microsoft account is in.
const personalAccounts = "9188040d-6c67-4c5b-b112-36a304b66dad"

// mayProvision refuses a connection that provisions at a door anybody can hold
// an account at, whichever half of that a write moves: marking such a door as
// the one, or moving the one to such a door. `field` is the half it moved.
//
// Refused to the deployment as well. Nothing about who is asking changes what
// the write would be: a way in for strangers, held by a key on the internet.
func mayProvision(field string, provisions bool, issuer string) error {
	if !provisions {
		return nil
	}
	who := openTo(issuer)
	if who == "" {
		return nil
	}

	return pderr.Invalidf(field,
		"%s signs in %s, so an identity a directory wrote there would be a way in for whoever holds the account it names; "+
			"a directory provisions through its own organisation's door", issuer, who)
}

// movedOf is the half a write moved, for [mayProvision] to name: the mark, when
// it sets one, and otherwise the door.
func movedOf(marks bool) string {
	if marks {
		return "provisions"
	}

	return "issuer"
}

// tenantOfConnection is the tenant a connection belongs to, through the wall.
func (s Core) tenantOfConnection(ctx context.Context, ref *app.ConnectionRef) ([]byte, []byte, error) {
	v, err := s.Next().Connection().Get(ctx, app.ConnectionGetRequest_builder{
		Ref:    ref,
		Select: app.ConnectionSelect_builder{Tenant: app.TenantSelect_builder{}.Build()}.Build(),
	}.Build())
	if err != nil {
		return nil, nil, err
	}

	return v.GetTenant().GetId(), v.GetId(), nil
}

// Add refuses a secret from a tenant, for [maySend]'s reason: a new connection
// is a new reference and a new issuer at once. And a row that is not of the
// form a connection is held to ([named], [addressed], [called]).
func (s coreConnection) Add(ctx context.Context, req *app.ConnectionAddRequest) (*app.Connection, error) {
	if err := s.acrossTheTenant(ctx, app.ConnectionService_Add_FullMethodName); err != nil {
		return nil, err
	}
	if err := named("name", req.GetName()); err != nil {
		return nil, err
	}
	if err := addressed("issuer", req.GetIssuer()); err != nil {
		return nil, err
	}
	if err := called("client_id", req.GetClientId()); err != nil {
		return nil, err
	}
	if err := s.maySend(ctx, nil, req.GetSecretRef(), req.GetIssuer()); err != nil {
		return nil, err
	}
	if err := s.maySubject(ctx, "", req.GetSubjectClaim()); err != nil {
		return nil, err
	}
	if err := mayNameBy("subject_claim", req.GetSubjectClaim(), req.GetIssuer()); err != nil {
		return nil, err
	}
	if err := mayProvision("provisions", req.GetProvisions(), req.GetIssuer()); err != nil {
		return nil, err
	}
	if req.GetProvisions() {
		t, err := s.Next().Tenant().Get(ctx, app.TenantGetRequest_builder{
			Ref: req.GetTenant(), Select: app.TenantSelect_builder{}.Build(),
		}.Build())
		if err != nil {
			return nil, err
		}
		if err := s.oneProvisions(ctx, t.GetId(), nil); err != nil {
			return nil, err
		}
	}

	return s.ConnectionServiceServer.Add(ctx, req)
}

// Patch is closed at the transport, and is what the deployment's own writers
// use -- `cmd/resources.go` declares a connection with it. So it is held only
// to what is true of a connection whoever wrote it: the form of one, the
// claims a front door can read, and where each may be named. Everything that
// reaches it is the deployment, which [Core.maySubject] lets move either way.
func (s coreConnection) Patch(ctx context.Context, req *app.ConnectionPatchRequest) (*app.Connection, error) {
	if err := s.acrossTheTenant(ctx, app.ConnectionService_Patch_FullMethodName); err != nil {
		return nil, err
	}
	if req.HasName() {
		if err := named("name", req.GetName()); err != nil {
			return nil, err
		}
	}
	if req.HasIssuer() {
		if err := addressed("issuer", req.GetIssuer()); err != nil {
			return nil, err
		}
	}
	if req.HasClientId() {
		if err := called("client_id", req.GetClientId()); err != nil {
			return nil, err
		}
	}
	if req.HasSubjectClaim() {
		if err := s.maySubject(ctx, req.GetSubjectClaim(), req.GetSubjectClaim()); err != nil {
			return nil, err
		}
	}
	if req.HasIssuer() || req.HasSubjectClaim() || req.GetProvisions() {
		// Whichever half the patch moves, the other is what the row holds.
		was, err := s.ConnectionServiceServer.Get(ctx, app.ConnectionGetRequest_builder{
			Ref: req.GetRef(),
			Select: app.ConnectionSelect_builder{
				Issuer: z.Ptr(true), SubjectClaim: z.Ptr(true), Provisions: z.Ptr(true),
			}.Build(),
		}.Build())
		if err != nil {
			return nil, err
		}
		issuer, claim, provisions := was.GetIssuer(), was.GetSubjectClaim(), was.GetProvisions()
		if req.HasIssuer() {
			issuer = req.GetIssuer()
		}
		if req.HasSubjectClaim() {
			claim = req.GetSubjectClaim()
		}
		if req.HasProvisions() {
			provisions = req.GetProvisions()
		}
		if req.HasIssuer() || req.HasSubjectClaim() {
			if err := mayNameBy(claimMoved(req.HasSubjectClaim()), claim, issuer); err != nil {
				return nil, err
			}
		}
		if req.HasIssuer() || req.GetProvisions() {
			if err := mayProvision(movedOf(req.GetProvisions()), provisions, issuer); err != nil {
				return nil, err
			}
		}
	}
	if req.GetProvisions() {
		tenant, self, err := s.tenantOfConnection(ctx, req.GetRef())
		if err != nil {
			return nil, err
		}
		if err := s.oneProvisions(ctx, tenant, self); err != nil {
			return nil, err
		}
	}

	return s.ConnectionServiceServer.Patch(ctx, req)
}

// Update is `Patch` with the name held back -- and refused outright on a row a
// file declared, which `declared.go` argues for -- and where a secret goes held
// to [maySend].
func (s coreConnection) Update(ctx context.Context, req *app.ConnectionUpdateRequest) (*app.Connection, error) {
	if err := s.acrossTheTenant(ctx, app.ConnectionService_Update_FullMethodName); err != nil {
		return nil, err
	}
	got, err := s.ConnectionServiceServer.Get(ctx, app.ConnectionGetRequest_builder{
		Ref: req.GetRef(),
		Select: app.ConnectionSelect_builder{
			Labels: z.Ptr(true), Issuer: z.Ptr(true), SecretRef: z.Ptr(true), SubjectClaim: z.Ptr(true),
			Provisions: z.Ptr(true),
		}.Build(),
	}.Build())
	if err != nil {
		return nil, err
	}
	if err := s.mayWriteDeclared(ctx, "ref", got.GetLabels()); err != nil {
		return nil, err
	}

	ref, issuer := got.GetSecretRef(), got.GetIssuer()
	if req.HasSecretRef() {
		ref = req.GetSecretRef()
	}
	if req.HasIssuer() {
		issuer = req.GetIssuer()
	}
	if req.HasIssuer() {
		if err := addressed("issuer", issuer); err != nil {
			return nil, err
		}
	}
	if req.HasClientId() {
		if err := called("client_id", req.GetClientId()); err != nil {
			return nil, err
		}
	}
	if err := s.maySend(ctx, got, ref, issuer); err != nil {
		return nil, err
	}
	if req.HasSubjectClaim() {
		if err := s.maySubject(ctx, got.GetSubjectClaim(), req.GetSubjectClaim()); err != nil {
			return nil, err
		}
	}
	if req.HasSubjectClaim() || req.HasIssuer() {
		claim := got.GetSubjectClaim()
		if req.HasSubjectClaim() {
			claim = req.GetSubjectClaim()
		}
		if err := mayNameBy(claimMoved(req.HasSubjectClaim()), claim, issuer); err != nil {
			return nil, err
		}
	}
	if req.HasIssuer() || req.GetProvisions() {
		provisions := got.GetProvisions()
		if req.HasProvisions() {
			provisions = req.GetProvisions()
		}
		if err := mayProvision(movedOf(req.GetProvisions()), provisions, issuer); err != nil {
			return nil, err
		}
	}
	if req.GetProvisions() {
		tenant, self, err := s.tenantOfConnection(ctx, req.GetRef())
		if err != nil {
			return nil, err
		}
		if err := s.oneProvisions(ctx, tenant, self); err != nil {
			return nil, err
		}
	}

	patch := app.ConnectionPatchRequest_builder{
		Ref:         req.GetRef(),
		DateUpdated: req.GetDateUpdated(),
	}
	if req.HasIssuer() {
		patch.Issuer = z.Ptr(req.GetIssuer())
	}
	if req.HasClientId() {
		patch.ClientId = z.Ptr(req.GetClientId())
	}
	// A list has no presence: given, it replaces; empty, it is left as it is.
	if len(req.GetScopes()) > 0 {
		patch.Scopes = req.GetScopes()
	}
	if req.HasSecretRef() {
		patch.SecretRef = z.Ptr(req.GetSecretRef())
	}
	if req.HasDesc() {
		patch.Desc = z.Ptr(req.GetDesc())
	}
	if req.HasSubjectClaim() {
		patch.SubjectClaim = z.Ptr(req.GetSubjectClaim())
	}
	if req.HasProvisions() {
		patch.Provisions = z.Ptr(req.GetProvisions())
	}

	// The generated one, not [coreConnection.Patch]: what that holds a patch to
	// is said above, against the row as read.
	return s.ConnectionServiceServer.Patch(ctx, patch.Build())
}
