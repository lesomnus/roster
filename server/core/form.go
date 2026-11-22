package core

import (
	"context"
	"fmt"
	"unicode/utf8"

	"github.com/protobuf-orm/ent/dialect"
	"github.com/protobuf-orm/protoc-gen-orm-ent/runtime/enttx"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/known/anypb"

	"github.com/lesomnus/payday/header"
	"github.com/lesomnus/payday/pderr"
	"github.com/lesomnus/payday/slug"

	app "github.com/lesomnus/roster/rstr"
	"github.com/lesomnus/roster/server/front"
	"github.com/lesomnus/roster/server/keys"
	"github.com/lesomnus/roster/server/portrait"
)

// What is written of a row has a length.
//
// # Why
//
// Nothing had one. A name, a note, a label and each field of a profile were
// whatever their caller sent, up to what one message carries, and what is
// written once is answered at every read after it -- a page of a hundred rows
// at a time, in one message of the same size. So a row was a way to make a
// list unanswerable, and the rows somebody writes about themselves were the
// worst of it: `HolderService/Update` is everybody's own to call. Five people
// who held nothing each wrote a display name of 900 KiB, and their
// administrator's list of people answered `ResourceExhausted`; nobody could
// page past them, since the page that failed is the one that names the next.
//
// # The numbers
//
// Each is past anything the thing it bounds is, and all of them together are
// small enough that a full page of rows at their longest is answered: a
// hundred people, each with every field at its bound in four-byte characters,
// are some three megabytes of the four a message may be.
//
// A name and a note are counted in characters, since a name is as long in
// Hangul as in Latin; a label, an address and `data` in bytes, since those are
// compared and carried rather than read. `docs/usage/customers.md` has the
// table a caller reads.
//
// # And the rest of what a caller writes
//
// The first of these were what is written of a person, since that is what
// anybody writes. What was left had no length either, and two of them are
// everybody's own to write as well: an account at a provider
// (`IdentityService/Add`) and a key (`ApiKeyService/Issue`), whose subject and
// whose list of methods made the list of accounts and the list of keys
// unanswerable as a name made the list of people. So a subject, an
// identifier, an address something is asked at and a list -- of methods
// ([keys.Listed]), of scopes -- each have one, by the same logic: bytes, since
// every one of them is compared and none is read.
const (
	// MaxName is the most characters a `name` has, of anything -- a person, a
	// team, a tenant -- and the most a profile's display name or department
	// has. What a directory lets a display name be (Entra's is 256), and what
	// `Fill` has always written at most.
	MaxName = 256

	// MaxDesc is the most characters a `desc` has: a note of a paragraph.
	MaxDesc = 1024

	// MaxLabels is how many labels one row carries, and MaxLabelKey and
	// MaxLabelValue the most bytes each half of one is.
	MaxLabels     = 32
	MaxLabelKey   = 128
	MaxLabelValue = 256

	// MaxEmployeeNo is the most characters a profile's employee number has.
	MaxEmployeeNo = 64

	// MaxLocale is the most characters a profile's locale has: what RFC 5646
	// says to leave room for in a language tag (section 4.4.1).
	MaxLocale = 35

	// MaxData is the most bytes `Holder.data` is, its type's name and its
	// value together. It is one slot an app keeps a few facts in, and what
	// has to be looked up or is large is kept by the app beside the person's
	// identifier.
	MaxData = 4096

	// MaxSubject is the most bytes a subject at a provider is: what OpenID
	// Connect lets one be. MaxDirectorySubject is the same of what a
	// directory calls somebody (`Holder.directory_subject`), which is a
	// subject somebody has yet to be linked by.
	MaxSubject          = 255
	MaxDirectorySubject = MaxSubject

	// MaxIdentifier is the most bytes an identifier of somebody else's
	// making is, compared and never read: the id an issuer gave a client,
	// the name of a provider, the name of one of the deployment's secrets, a
	// scope, and each of the words an app says a consent in.
	MaxIdentifier = 255

	// MaxLocation is the most bytes an address something is asked at is: a
	// connection's issuer, as a picture's address is ([portrait.MaxURL]).
	MaxLocation = portrait.MaxURL

	// MaxScopes is how many scopes a connection asks its provider for.
	MaxScopes = 32
)

// form is the last thing in front of the store: what a write says of a row is
// held to a form here -- its text to a length, its alias to a spelling
// ([spelled]) -- whichever verb made the write.
//
// # Why it is under everything else of this layer, and not in each verb
//
// A rule about a caller is asked where the caller arrives. This is about the
// value, and a value reaches a row by many verbs: `Add`; a narrow write that
// ends in `Patch` -- a person's `Update`, a tenant's, a group's; a directory's
// `Provision` and what it tells; the rows `Tenant.Add` writes beside the
// tenant; the deployment's own work at the box. Said in each, it is said again
// for the next verb written, and the one that forgets is the megabyte, or the
// name nobody finds. So [New] puts every `Core` over this, and nothing of
// this package reaches the store except through it.
//
// # What it does not read
//
// `Apply`. It is the other general write, shut at every port
// (`cmd/serve.go`, `closed`), and what it carries is a document and not the
// fields of a request.
type form struct {
	app.Overlay
}

// formed puts the form in front of `next`.
func formed(next app.Server) form { return form{Overlay: app.NewOverlay(next)} }

// WithDriver is what nothing inherits; see `CLAUDE.md`, "Writing a layer".
func (s form) WithDriver(drv dialect.Driver) (app.Server, error) {
	next, err := enttx.Rebind(s.Next(), drv)
	if err != nil {
		return nil, err
	}

	return formed(next), nil
}

var (
	_ app.Server               = form{}
	_ enttx.Binder[app.Server] = form{}
)

// written holds what a request says of a row to its form: the `alias` to its
// spelling, and the `name`, the `desc` and the `labels` to their lengths,
// wherever the request has them.
//
// Read by name, as payday's `header` reads a row whose type it does not know:
// the four are the same field at the same number in every entity, so the rule
// is written once and holds for the entity somebody adds next -- once that
// entity is listed below, which `TestEveryRowIsHeldToTheForm` asks of each.
//
// A field a patch does not mention reads as empty, and passes.
func written(req interface {
	ProtoReflect() protoreflect.Message
}) error {
	h := header.Of(req)
	if err := spelled("alias", h.Alias); err != nil {
		return err
	}
	if err := lettered("name", h.Name, MaxName); err != nil {
		return err
	}
	if err := lettered("desc", h.Desc, MaxDesc); err != nil {
		return err
	}

	return labelled(h.Labels)
}

// spelled refuses an alias that is not written as it will be read: in small
// letters, with nothing before or after it.
//
// # Why it is refused, and was folded
//
// payday folds an alias on its way in, and says why: `"  Acme "` and `acme`
// are one row to a person and two to a unique index. That is right about the
// index and it left the caller out. A reference compares an alias as it is
// sent, so `Add` with `ACME` answered OK and wrote `acme`, and `Get` by
// `ACME` was `NotFound`; adding `SEOUL` beside `seoul` was `AlreadyExists`,
// about a name its caller never wrote. `normalised` argues the rest at a
// hostname: a row that comes back different from what was written is a
// console that cannot find what somebody just typed.
//
// So the index gets what the fold gave it -- one spelling -- and the caller is
// told which, by the refusal every other name here makes.
//
// # Why it is here
//
// The fold is payday's, in the generated store below this. For an `Add` it is
// what the store's namer does and an app may hand the store another
// (`Sink.WithNamer`); for a `Patch` it is not the app's to switch off. One
// rule in two places with two wirings is not one rule, so it is asked here for
// both, in front of the store, where the lengths are: the fold below is left
// with nothing to fold. It reads `alias` wherever a request has one, which is
// every entity that is named by one.
//
// # What it leaves alone
//
// An `Add` that says no alias, for which the store makes one up as it always
// did. And an alias that is not one in any spelling -- a space inside it, a
// digit first -- which is refused in the words the grammar has for it, since
// there is no spelling to name.
func spelled(field, v string) error {
	if v == "" {
		return nil
	}

	w, err := slug.ParseAlias(v)
	if err != nil {
		return pderr.At(field, err)
	}
	if w != v {
		return pderr.Invalidf(field, "written as it is read, so %q rather than %s", w, quoted(v))
	}

	return nil
}

// lettered refuses text of more than `most` characters.
func lettered(field, v string, most int) error {
	// Nothing of `most` bytes or fewer has more characters than that, and most
	// of what arrives is that short.
	if len(v) <= most {
		return nil
	}
	if n := utf8.RuneCountInString(v); n > most {
		return pderr.Invalidf(field, "at most %d characters, and this is %d", most, n)
	}

	return nil
}

// sized refuses a value of more than `most` bytes.
func sized(field, v string, most int) error {
	if len(v) > most {
		return pderr.Invalidf(field, "at most %d bytes, and this is %d", most, len(v))
	}

	return nil
}

// labelled refuses more labels than a row carries, and a key or a value
// longer than one is.
func labelled(labels map[string]string) error {
	if len(labels) > MaxLabels {
		return pderr.Invalidf("labels", "at most %d labels on one row, and these are %d", MaxLabels, len(labels))
	}
	for k, v := range labels {
		if len(k) > MaxLabelKey {
			return pderr.Invalidf("labels", "a key is at most %d bytes, and %s is %d", MaxLabelKey, quoted(k), len(k))
		}
		if len(v) > MaxLabelValue {
			return pderr.Invalidf("labels", "a value is at most %d bytes, and that of %s is %d", MaxLabelValue, quoted(k), len(v))
		}
	}

	return nil
}

// personal holds what is written of a person beyond the three every row has:
// each field of their profile, and `data` as a whole.
//
// The picture's address has had its length since it had a rule
// ([portrait.CheckURL]), asked where a caller changes it. It is asked again
// here for the roads that do not pass that.
func personal(p *app.Profile, data *anypb.Any) error {
	if p != nil {
		for _, f := range []struct {
			field string
			v     string
			most  int
		}{
			{"display_name", p.GetDisplayName(), MaxName},
			{"department", p.GetDepartment(), MaxName},
			{"employee_no", p.GetEmployeeNo(), MaxEmployeeNo},
			{"locale", p.GetLocale(), MaxLocale},
		} {
			if err := lettered(f.field, f.v, f.most); err != nil {
				return pderr.At("profile", err)
			}
		}
		if err := sized("picture", p.GetPicture(), portrait.MaxURL); err != nil {
			return pderr.At("profile", err)
		}
	}
	if n := len(data.GetTypeUrl()) + len(data.GetValue()); n > MaxData {
		return pderr.Invalidf("data", "at most %d bytes, its type and its value together, and this is %d", MaxData, n)
	}
	if data.GetTypeUrl() != "" || len(data.GetValue()) > 0 {
		// Read as the store will write it, which is as a message of a type
		// this server knows. Not asked, `Add` handed it to the store, whose
		// encoder failed in words of its own that read as `Unknown`, where
		// `Update` refused the same as the caller's mistake. One that says
		// nothing at all is stored as nothing, as it was.
		if _, err := data.UnmarshalNew(); err != nil {
			return pderr.Invalidf("data", "roster cannot read a message of type %s from it", quoted(data.GetTypeUrl()))
		}
	}

	return nil
}

// bounded is the first of several refusals, or none.
func bounded(refusals ...error) error {
	for _, err := range refusals {
		if err != nil {
			return err
		}
	}

	return nil
}

// scoped refuses more scopes than a connection asks for, and one longer than
// an identifier is.
func scoped(scopes []string) error {
	if len(scopes) > MaxScopes {
		return pderr.Invalidf("scopes", "at most %d of them, and these are %d", MaxScopes, len(scopes))
	}
	for i, v := range scopes {
		if err := sized(fmt.Sprintf("scopes[%d]", i), v, MaxIdentifier); err != nil {
			return err
		}
	}

	return nil
}

// connected holds what a connection says beyond the three every row has:
// where its provider is asked, what the provider calls this app, which of the
// deployment's secrets it sends, and what it asks for.
func connected(issuer, client, secret string, scopes []string) error {
	return bounded(
		sized("issuer", issuer, MaxLocation),
		sized("client_id", client, MaxIdentifier),
		sized("secret_ref", secret, MaxIdentifier),
		scoped(scopes),
	)
}

// held is a write made once what it says has passed [written].
func held[Q interface {
	ProtoReflect() protoreflect.Message
}, R any](ctx context.Context, req Q, write func(context.Context, Q) (R, error)) (R, error) {
	if err := written(req); err != nil {
		var none R

		return none, err
	}

	return write(ctx, req)
}

// A person: the three, their profile and `data`, and what their directory
// calls them.

type formHolder struct{ app.HolderServiceServer }

func (s form) Holder() app.HolderServiceServer { return formHolder{s.Next().Holder()} }

func (s formHolder) Add(ctx context.Context, req *app.HolderAddRequest) (*app.Holder, error) {
	if err := personal(req.GetProfile(), req.GetData()); err != nil {
		return nil, err
	}

	return held(ctx, req, s.HolderServiceServer.Add)
}

func (s formHolder) Patch(ctx context.Context, req *app.HolderPatchRequest) (*app.Holder, error) {
	if err := personal(req.GetProfile(), req.GetData()); err != nil {
		return nil, err
	}
	if err := sized("directory_subject", req.GetDirectorySubject(), MaxDirectorySubject); err != nil {
		return nil, err
	}
	if err := sized("directory_address", req.GetDirectoryAddress(), front.MaxAddress); err != nil {
		return nil, err
	}
	if err := lettered("directory_name", req.GetDirectoryName(), MaxName); err != nil {
		return nil, err
	}

	return held(ctx, req, s.HolderServiceServer.Patch)
}

// An account at a provider, which is somebody's own to write: its subject, and
// the name of the provider.

type formIdentity struct{ app.IdentityServiceServer }

func (s form) Identity() app.IdentityServiceServer { return formIdentity{s.Next().Identity()} }

func (s formIdentity) Add(ctx context.Context, req *app.IdentityAddRequest) (*app.Identity, error) {
	err := bounded(sized("provider", req.GetProvider(), MaxIdentifier), sized("subject", req.GetSubject(), MaxSubject))
	if err != nil {
		return nil, err
	}

	return s.IdentityServiceServer.Add(ctx, req)
}

func (s formIdentity) Patch(ctx context.Context, req *app.IdentityPatchRequest) (*app.Identity, error) {
	err := bounded(sized("provider", req.GetProvider(), MaxIdentifier), sized("subject", req.GetSubject(), MaxSubject))
	if err != nil {
		return nil, err
	}

	return s.IdentityServiceServer.Patch(ctx, req)
}

// The rows that are written once and have neither a name nor a note: a
// delegation's list, the client a group is shown to, and the four words an app
// says a consent in.

type (
	formConsent       struct{ app.ConsentServiceServer }
	formDelegation    struct{ app.DelegationServiceServer }
	formGroupExposure struct{ app.GroupExposureServiceServer }
)

func (s form) Consent() app.ConsentServiceServer       { return formConsent{s.Next().Consent()} }
func (s form) Delegation() app.DelegationServiceServer { return formDelegation{s.Next().Delegation()} }
func (s form) GroupExposure() app.GroupExposureServiceServer {
	return formGroupExposure{s.Next().GroupExposure()}
}

func (s formConsent) Add(ctx context.Context, req *app.ConsentAddRequest) (*app.Consent, error) {
	err := bounded(
		sized("scope", req.GetScope(), MaxIdentifier),
		sized("document", req.GetDocument(), MaxIdentifier),
		sized("version", req.GetVersion(), MaxIdentifier),
		sized("client", req.GetClient(), MaxIdentifier),
	)
	if err != nil {
		return nil, err
	}

	return s.ConsentServiceServer.Add(ctx, req)
}

func (s formDelegation) Add(ctx context.Context, req *app.DelegationAddRequest) (*app.Delegation, error) {
	if err := keys.Listed("methods", req.GetMethods()); err != nil {
		return nil, err
	}

	return s.DelegationServiceServer.Add(ctx, req)
}

func (s formGroupExposure) Add(ctx context.Context, req *app.GroupExposureAddRequest) (*app.GroupExposure, error) {
	if err := sized("client", req.GetClient(), MaxIdentifier); err != nil {
		return nil, err
	}

	return s.GroupExposureServiceServer.Add(ctx, req)
}

// And every other row that has a name, a note or labels: one type each, since
// each service is a type of its own, and one rule -- with what a key, a role,
// a connection and a mail domain say beside those three.

type (
	formApiKey     struct{ app.ApiKeyServiceServer }
	formBinding    struct{ app.BindingServiceServer }
	formConnection struct{ app.ConnectionServiceServer }
	formCredential struct{ app.CredentialServiceServer }
	formGroup      struct{ app.GroupServiceServer }
	formHost       struct{ app.HostServiceServer }
	formHostProof  struct{ app.HostProofServiceServer }
	formMailDomain struct{ app.MailDomainServiceServer }
	formNomination struct{ app.NominationServiceServer }
	formRelation   struct{ app.RelationServiceServer }
	formRole       struct{ app.RoleServiceServer }
	formSite       struct{ app.SiteServiceServer }
	formTeam       struct{ app.TeamServiceServer }
	formTenant     struct{ app.TenantServiceServer }
)

func (s form) ApiKey() app.ApiKeyServiceServer         { return formApiKey{s.Next().ApiKey()} }
func (s form) Binding() app.BindingServiceServer       { return formBinding{s.Next().Binding()} }
func (s form) Connection() app.ConnectionServiceServer { return formConnection{s.Next().Connection()} }
func (s form) Credential() app.CredentialServiceServer { return formCredential{s.Next().Credential()} }
func (s form) Group() app.GroupServiceServer           { return formGroup{s.Next().Group()} }
func (s form) Host() app.HostServiceServer             { return formHost{s.Next().Host()} }
func (s form) HostProof() app.HostProofServiceServer   { return formHostProof{s.Next().HostProof()} }
func (s form) MailDomain() app.MailDomainServiceServer { return formMailDomain{s.Next().MailDomain()} }
func (s form) Nomination() app.NominationServiceServer { return formNomination{s.Next().Nomination()} }
func (s form) Relation() app.RelationServiceServer     { return formRelation{s.Next().Relation()} }
func (s form) Role() app.RoleServiceServer             { return formRole{s.Next().Role()} }
func (s form) Site() app.SiteServiceServer             { return formSite{s.Next().Site()} }
func (s form) Team() app.TeamServiceServer             { return formTeam{s.Next().Team()} }
func (s form) Tenant() app.TenantServiceServer         { return formTenant{s.Next().Tenant()} }

func (s formApiKey) Add(ctx context.Context, req *app.ApiKeyAddRequest) (*app.ApiKey, error) {
	if err := keys.Listed("methods", req.GetMethods()); err != nil {
		return nil, err
	}

	return held(ctx, req, s.ApiKeyServiceServer.Add)
}

func (s formApiKey) Patch(ctx context.Context, req *app.ApiKeyPatchRequest) (*app.ApiKey, error) {
	if err := keys.Listed("methods", req.GetMethods()); err != nil {
		return nil, err
	}

	return held(ctx, req, s.ApiKeyServiceServer.Patch)
}

func (s formBinding) Add(ctx context.Context, req *app.BindingAddRequest) (*app.Binding, error) {
	return held(ctx, req, s.BindingServiceServer.Add)
}

func (s formBinding) Patch(ctx context.Context, req *app.BindingPatchRequest) (*app.Binding, error) {
	return held(ctx, req, s.BindingServiceServer.Patch)
}

func (s formConnection) Add(ctx context.Context, req *app.ConnectionAddRequest) (*app.Connection, error) {
	if err := connected(req.GetIssuer(), req.GetClientId(), req.GetSecretRef(), req.GetScopes()); err != nil {
		return nil, err
	}

	return held(ctx, req, s.ConnectionServiceServer.Add)
}

func (s formConnection) Patch(ctx context.Context, req *app.ConnectionPatchRequest) (*app.Connection, error) {
	if err := connected(req.GetIssuer(), req.GetClientId(), req.GetSecretRef(), req.GetScopes()); err != nil {
		return nil, err
	}

	return held(ctx, req, s.ConnectionServiceServer.Patch)
}

func (s formCredential) Add(ctx context.Context, req *app.CredentialAddRequest) (*app.Credential, error) {
	return held(ctx, req, s.CredentialServiceServer.Add)
}

func (s formCredential) Patch(ctx context.Context, req *app.CredentialPatchRequest) (*app.Credential, error) {
	return held(ctx, req, s.CredentialServiceServer.Patch)
}

func (s formGroup) Add(ctx context.Context, req *app.GroupAddRequest) (*app.Group, error) {
	return held(ctx, req, s.GroupServiceServer.Add)
}

func (s formGroup) Patch(ctx context.Context, req *app.GroupPatchRequest) (*app.Group, error) {
	return held(ctx, req, s.GroupServiceServer.Patch)
}

func (s formHost) Add(ctx context.Context, req *app.HostAddRequest) (*app.Host, error) {
	return held(ctx, req, s.HostServiceServer.Add)
}

func (s formHost) Patch(ctx context.Context, req *app.HostPatchRequest) (*app.Host, error) {
	return held(ctx, req, s.HostServiceServer.Patch)
}

func (s formHostProof) Add(ctx context.Context, req *app.HostProofAddRequest) (*app.HostProof, error) {
	return held(ctx, req, s.HostProofServiceServer.Add)
}

func (s formHostProof) Patch(ctx context.Context, req *app.HostProofPatchRequest) (*app.HostProof, error) {
	return held(ctx, req, s.HostProofServiceServer.Patch)
}

func (s formMailDomain) Add(ctx context.Context, req *app.MailDomainAddRequest) (*app.MailDomain, error) {
	if err := sized("provider", req.GetProvider(), MaxIdentifier); err != nil {
		return nil, err
	}

	return held(ctx, req, s.MailDomainServiceServer.Add)
}

func (s formMailDomain) Patch(ctx context.Context, req *app.MailDomainPatchRequest) (*app.MailDomain, error) {
	if err := sized("provider", req.GetProvider(), MaxIdentifier); err != nil {
		return nil, err
	}

	return held(ctx, req, s.MailDomainServiceServer.Patch)
}

func (s formNomination) Add(ctx context.Context, req *app.NominationAddRequest) (*app.Nomination, error) {
	return held(ctx, req, s.NominationServiceServer.Add)
}

func (s formNomination) Patch(ctx context.Context, req *app.NominationPatchRequest) (*app.Nomination, error) {
	return held(ctx, req, s.NominationServiceServer.Patch)
}

func (s formRelation) Add(ctx context.Context, req *app.RelationAddRequest) (*app.Relation, error) {
	return held(ctx, req, s.RelationServiceServer.Add)
}

func (s formRelation) Patch(ctx context.Context, req *app.RelationPatchRequest) (*app.Relation, error) {
	return held(ctx, req, s.RelationServiceServer.Patch)
}

func (s formRole) Add(ctx context.Context, req *app.RoleAddRequest) (*app.Role, error) {
	if err := keys.Listed("methods", req.GetMethods()); err != nil {
		return nil, err
	}

	return held(ctx, req, s.RoleServiceServer.Add)
}

func (s formRole) Patch(ctx context.Context, req *app.RolePatchRequest) (*app.Role, error) {
	if err := keys.Listed("methods", req.GetMethods()); err != nil {
		return nil, err
	}

	return held(ctx, req, s.RoleServiceServer.Patch)
}

func (s formSite) Add(ctx context.Context, req *app.SiteAddRequest) (*app.Site, error) {
	return held(ctx, req, s.SiteServiceServer.Add)
}

func (s formSite) Patch(ctx context.Context, req *app.SitePatchRequest) (*app.Site, error) {
	return held(ctx, req, s.SiteServiceServer.Patch)
}

func (s formTeam) Add(ctx context.Context, req *app.TeamAddRequest) (*app.Team, error) {
	return held(ctx, req, s.TeamServiceServer.Add)
}

func (s formTeam) Patch(ctx context.Context, req *app.TeamPatchRequest) (*app.Team, error) {
	return held(ctx, req, s.TeamServiceServer.Patch)
}

func (s formTenant) Add(ctx context.Context, req *app.TenantAddRequest) (*app.Tenant, error) {
	return held(ctx, req, s.TenantServiceServer.Add)
}

func (s formTenant) Patch(ctx context.Context, req *app.TenantPatchRequest) (*app.Tenant, error) {
	return held(ctx, req, s.TenantServiceServer.Patch)
}
