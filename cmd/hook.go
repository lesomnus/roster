package cmd

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"os"
	"slices"
	"strings"
	"sync"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/known/anypb"
	"uuid"

	"github.com/lesomnus/payday/admission"
	"github.com/lesomnus/payday/auth"
	"github.com/lesomnus/payday/auth/authsession"
	"github.com/lesomnus/payday/frame"
	"github.com/lesomnus/payday/pdid"
	"github.com/lesomnus/xli/cfg/ref"

	"github.com/lesomnus/roster/internal/ent"
	entgroup "github.com/lesomnus/roster/internal/ent/group"
	entholder "github.com/lesomnus/roster/internal/ent/holder"
	entrole "github.com/lesomnus/roster/internal/ent/role"
	app "github.com/lesomnus/roster/rstr"
	"github.com/lesomnus/roster/server/bare"
	"github.com/lesomnus/roster/server/pd"
	"github.com/lesomnus/roster/server/quote"
)

// # A hook a deployment refuses a write by (#305)
//
// roster's rules are roster's: who may write what, whose row, what a write
// hands out. What they leave to a deployment is listed in
// `docs/usage/rules.md`, and some of it a deployment wants a say in -- which
// groups its people may leave, who may take whom out of one. This is where it
// says so: a policy asked about a write once every rule of roster's has let it
// through, inside the write's transaction, that can refuse it and do nothing
// else.
//
// It is payday's admission seam (`payday/admission`, `pd.Admission`), which
// roster generates and installs only here. Two ways to hand it a policy, and
// one is built on the other: an app that runs roster in its own process puts
// one in [Config.Admission], and a deployment that runs roster's binary names
// a service in its file ([HookConfig]), whose client is such a policy.
//
// # Where it is asked, and where not
//
// On the data plane's rows, which are the stacks a caller's write reaches:
// `server.addr`, and `admin.addr`, where an operator writes a customer's rows
// past a gate of the control plane's. Not the control plane's own rows -- its
// keys and operators are the deployment's -- and not at the box: a command at
// a shell has no caller, and is the deployment's own ([Config.AtTheBox]).
//
// Not on [Server.Ungated] either, which is the deployment's own by the same
// argument: it is the stack the wall was never installed on, what `init`, the
// sweeps, a sign-in counting a wrong answer and a key noting its use write
// through, and nothing a caller reaches writes through it ([trustedUnary]).
//
// Never about a read. And not about a way in ([waysIn]) -- how somebody gets
// in or stays in -- unless the file says so in a key of its own,
// [HookConfig.WaysIn]: a hook that is down there shuts out everybody who signs
// in that way, an administrator at a console among them. A write somebody
// makes about another person's way in is not one of those, and is asked about
// as any write is.

// Admission is a policy an app that runs roster in its own process hands it,
// and the writes it is asked about.
//
// Not in the file, as [ClientConfig.Local] is not: a policy is Go, and a file
// names a service instead ([HookConfig]). Zero is none.
type Admission struct {
	// Policy is asked about each row a write covered by [Admission.Writes]
	// is about to commit. Nil asks nothing. It answers nil to go on, and a
	// gRPC status to refuse, in words its caller is told; anything else --
	// a plain error, a panic, no answer in [HookConfig.Timeout] -- refuses
	// the write as this deployment's hook not answering.
	Policy admission.Policy

	// Writes is which writes it is asked about, as [HookConfig.Writes] says
	// them.
	Writes []string
}

// HookConfig is `hook:`, a service this deployment names to be asked about
// writes roster's rules let through (`proto/hook/v1/hook.proto`).
type HookConfig struct {
	// Addr is the service to ask, a gRPC target. Empty is no hook, and then
	// nothing of this is installed.
	Addr string `yaml:"addr"`

	// Insecure dials it without TLS, as `ldap.insecure` dials roster: written
	// down rather than inferred from the address.
	Insecure bool `yaml:"insecure"`

	// Ca is the authority the hook's certificate is verified against, in
	// place of the system's: a PEM bundle, a value of the file as a
	// listener's `tls.client_ca` is -- `${file:/path}`, `${env:NAME}` or the
	// PEM itself -- since a deployment's hook is usually behind an authority
	// of its own. Empty is the system's roots. Beside `insecure` it is
	// refused: one says TLS and the other says none.
	Ca ref.String `yaml:"ca"`

	// Timeout is how long a write waits for the answer, and never longer than
	// its caller's own deadline. Empty is [DefaultHookTimeout]; more than
	// [MaxHookTimeout] is refused.
	Timeout time.Duration `yaml:"timeout"`

	// Writes is which writes it is asked about: patterns over methods, as a
	// role's methods are written -- `/roster.GroupMembershipService/*`,
	// `/roster.HolderService/Disable` -- where a `*` is a whole part. A write
	// no pattern covers asks nobody.
	Writes []string `yaml:"writes"`

	// WaysIn says the hook is asked about ways in ([waysIn]) as about any
	// other write: what `writes` covers of them is taken, where without it a
	// pattern naming one is refused and a wider one leaves them out. Said, a
	// hook that does not answer shuts out everybody who signs in that way,
	// and the box, which asks no hook, is where that is put right.
	WaysIn bool `yaml:"ways_in"`
}

// DefaultHookTimeout is [HookConfig.Timeout] where nothing says, which is
// payday's own default for a decision.
const DefaultHookTimeout = 2 * time.Second

// MaxHookTimeout is the longest [HookConfig.Timeout] a file may say: a write
// holds its rows locked while it waits, and a hook that takes longer than this
// to answer is one whose callers are better told it did not.
const MaxHookTimeout = 30 * time.Second

// timeout is [HookConfig.Timeout] with its default.
func (c HookConfig) timeout() time.Duration {
	if c.Timeout > 0 {
		return c.Timeout
	}

	return DefaultHookTimeout
}

// AtTheBox is this configuration as a command at a shell builds it: the hook
// written in the file is held to its rules as `serve` holds it, and asked by
// nothing. A command at the box has no caller -- whoever runs it already holds
// the database -- so what it writes is the deployment's own, as `init`'s is.
// Every command but `serve` builds through this (`cli.built`).
func (c Config) AtTheBox() Config {
	c.box = true

	return c
}

// waysIn is every verb by which somebody gets in or stays in: what a person or
// a front door calls to sign in, to redeem a link, to accept a provider's word,
// to mint or refresh a session, and what a person calls to set or recover
// their own way in. A hook that is down there shuts everybody out, an
// administrator at a console among them, so no hook is asked about these
// unless the file says so ([HookConfig.WaysIn]): without it, a pattern that
// names one is refused at start, and a wider one (`/roster.*/*`) is taken as
// everything but these and the reads.
//
// A verb a person calls for themselves and an administrator for somebody else
// is one of these: the same method is both, and a hook asked about it is asked
// at the person's own door too.
//
// What is not here is a write somebody makes about another person's way in,
// an administrator's or an operator's act, which no person makes for themselves
// and no front door makes at a sign-in: `BadgeService/Enrol`, a badge and its
// PIN given to somebody at a console or a shell. A hook that is down holds up
// that write and lets everybody in, so it is taken in `hook.writes` as any
// write is (it was refused here as a way in, #606, for a reason true only of
// signing in).
//
// Read from what `docs/usage/ways-in.md` says a way in is and from what the
// Login App (`cli.LoginMethods`), the account app (`account.Calls`) and the
// front door call to let somebody in. `TestEveryWayInIsOneNoHookIsAskedAbout`
// holds the list to what those apps call.
var waysIn = []string{
	// Signing in, proving a secret, spending a link, accepting an account at
	// a provider, leaving by a mailed link: the whole of the service, which
	// is what a front door calls to let somebody in.
	"/roster.VouchService/*",
	// A station asking after a badge's PIN: the sign-in at a station.
	app.BadgeService_Verify_FullMethodName,

	// Signing in at roster's own door, and out of it: what either writes is
	// the session the store mints and ends, which is its own write.
	"/roster.AuthService/*",

	// The exchange of a token or a delegation, and the delegation a person
	// hands a terminal or an app at a sign-in: a session minted.
	app.DelegationService_Exchange_FullMethodName,
	app.DelegationService_Issue_FullMethodName,

	// A person setting their own password or second factor, and minting a key
	// that acts as them (`self-service`, the account app's page): each is also
	// what an administrator calls for somebody else, and so of this kind.
	app.CredentialService_Set_FullMethodName,
	app.CredentialService_Enrol_FullMethodName,
	app.ApiKeyService_Issue_FullMethodName,

	// The password the account app gives somebody who spent a recovery link
	// (`account`'s redeem): the link redeemed, and the way back in. It is also
	// an administrator's reset of somebody else, and so of this kind.
	app.CredentialService_Issue_FullMethodName,

	// An account at a provider proved by a person's own round trip, and an
	// address proved by a link they were mailed: each a person's own.
	app.IdentityService_Attest_FullMethodName,
	app.EmailService_Claim_FullMethodName,
	app.EmailService_Confirm_FullMethodName,

	// What a sign-in writes on a provider's or a directory's word: the
	// account linked at a first sign-in (which an administrator also writes
	// for somebody, and so of this kind), the claim moved to, the address
	// handed over and the profile filled.
	app.IdentityService_Add_FullMethodName,
	app.IdentityService_Resubject_FullMethodName,
	app.EmailService_Attest_FullMethodName,
	app.HolderService_Fill_FullMethodName,
}

// hookReads is every read a port serves under a name that is not a generated
// one's (`Get`, `List`, `Watch`): what a pattern covering only reads is
// refused for, and what a wide pattern leaves out. A read writes nothing, so
// nothing would ask; a pattern naming one says a hook is asked about it.
var hookReads = []string{
	app.AuthService_Offers_FullMethodName,
	app.AuthService_Which_FullMethodName,
	app.ConsentService_Settled_FullMethodName,
	app.FrontService_WhereFrom_FullMethodName,
	app.FrontService_WhoseHost_FullMethodName,
	app.HolderService_Reaches_FullMethodName,
	app.HolderService_Search_FullMethodName,
	app.HolderService_SignsIn_FullMethodName,
	app.MeService_Get_FullMethodName,
	"/payday.TokenService/Introspect",
}

// isHookRead is whether a method is a read: a generated read, or one of
// [hookReads].
func isHookRead(method string) bool {
	i := strings.LastIndex(method, "/")
	switch method[i+1:] {
	case "Get", "List", "Watch":
		return true
	}

	return slices.Contains(hookReads, method)
}

// isWayIn is whether a method is one of [waysIn].
func isWayIn(method string) bool {
	return slices.ContainsFunc(waysIn, func(p string) bool { return frame.Covers(p, method) })
}

// hookWrites is the methods `patterns` asks about, of the ones this build
// serves, refused by name where a pattern cannot be meant: none at all, one
// that covers nothing roster has, one that covers only reads, and one that
// names a way in where `waysKey` is not said -- the key that says it, or empty
// where nothing can ([Config.Admission]).
func hookWrites(key string, patterns []string, served []string, waysKey string, waysSaid bool) (map[string]bool, error) {
	if len(patterns) == 0 {
		return nil, fmt.Errorf("%s: names no write, so the hook would be asked about nothing. name the writes it "+
			"is asked about, as a role's methods are written: /roster.GroupMembershipService/*", key)
	}

	asks := map[string]bool{}
	for i, p := range patterns {
		var covered, ways, reads []string
		for _, m := range served {
			if !frame.Covers(p, m) {
				continue
			}
			covered = append(covered, m)
			switch {
			case isHookRead(m):
				reads = append(reads, m)
			case isWayIn(m) && !waysSaid:
				ways = append(ways, m)
			default:
				asks[m] = true
			}
		}

		at := fmt.Sprintf("%s[%d]", key, i)
		switch {
		case len(covered) == 0:
			return nil, fmt.Errorf("%s: %s covers no method roster serves. write a method as gRPC names it, "+
				"/roster.HolderService/Disable, or every one of a service's with /*", at, quote.Sent(p))
		case len(covered) > len(ways)+len(reads):
		case len(ways) > 0 && waysKey == "":
			return nil, fmt.Errorf("%s: %s is a way in (%s): a policy in the process is not asked about signing "+
				"in, proving or recovering, so that one that fails shuts nobody out. take it out", at, quote.Sent(p), strings.Join(ways, ", "))
		case len(ways) > 0:
			return nil, fmt.Errorf("%s: %s is a way in (%s): a hook that does not answer then shuts out everybody "+
				"who signs in that way, and the box is where it is put right. say %s: true to have it asked, "+
				"or take it out", at, quote.Sent(p), strings.Join(ways, ", "), waysKey)
		default:
			return nil, fmt.Errorf("%s: %s covers only reads (%s), and a hook is asked about writes. name a write",
				at, quote.Sent(p), strings.Join(reads, ", "))
		}
	}

	return asks, nil
}

// hookMeant refuses a `hook:` block, and an [Admission], that cannot be
// meant, as the rest of the file is refused ([Config.Meant]).
func hookMeant(c Config) error {
	h := c.Hook
	if h.Addr == "" {
		for _, v := range []struct {
			key  string
			said bool
		}{
			{"hook.writes", len(h.Writes) > 0},
			{"hook.timeout", h.Timeout != 0},
			{"hook.insecure", h.Insecure},
			{"hook.ca", !h.Ca.IsZero()},
			{"hook.ways_in", h.WaysIn},
		} {
			if v.said {
				return fmt.Errorf("%s: says something of a hook, and hook.addr names no service to ask, so nothing "+
					"is asked. name the service, or take the block out", v.key)
			}
		}
	}
	if h.Insecure && !h.Ca.IsZero() {
		return errors.New("hook.ca: names an authority to verify the hook by, and hook.insecure dials it with no TLS " +
			"to verify. take one out")
	}
	if !h.Ca.IsZero() {
		if _, err := h.roots(); err != nil {
			return err
		}
	}
	if h.Timeout < 0 || h.Timeout > MaxHookTimeout {
		return fmt.Errorf("hook.timeout: %s cannot be meant. a write waits at most %s for its hook, and leave it "+
			"out for %s", h.Timeout, MaxHookTimeout, DefaultHookTimeout)
	}

	served := Served(pd.Sink{})
	if h.Addr != "" {
		if _, err := hookWrites("hook.writes", h.Writes, served, "hook.ways_in", h.WaysIn); err != nil {
			return err
		}
	}
	if c.Admission.Policy != nil {
		if _, err := hookWrites("Config.Admission.Writes", c.Admission.Writes, served, "", false); err != nil {
			return err
		}
	} else if len(c.Admission.Writes) > 0 {
		return errors.New("Config.Admission.Writes: names writes, and Config.Admission.Policy is nil, so nothing " +
			"is asked about them. hand it a policy, or name none")
	}

	return nil
}

// asking is one policy and the writes it is asked about.
type asking struct {
	asks   map[string]bool
	policy admission.Policy
}

// hooked is what a write is asked: each policy whose writes cover its method,
// in turn, the in-process one first. The first that refuses is the answer.
//
// What each answers is put in this deployment's words ([hookAnswer]), so a
// policy in this process and a service elsewhere tell a caller the same.
type hooked struct {
	policies []asking
	timeout  time.Duration
	log      *slog.Logger
}

// asked is whether any of the policies is asked about `method`: what payday
// asks before anything else ([admissionOf]). Each policy's writes are a set
// of the methods its patterns cover less the reads, and the ways in where
// the file does not say them ([hookWrites]), so this is a lookup and never blocks.
func (h hooked) asked(method string) bool {
	for _, p := range h.policies {
		if p.asks[method] {
			return true
		}
	}

	return false
}

func (h hooked) Admit(ctx context.Context, c admission.Call) error {
	for _, p := range h.policies {
		// Still asked here, after payday asked [hooked.asked]: with a policy
		// in the process and a service both, a write one of them covers
		// reaches this and is asked of that one alone.
		if !p.asks[c.Method] {
			continue
		}
		if err := h.ask(ctx, p.policy, c); err != nil {
			return err
		}
	}

	return nil
}

// ask is one policy asked, within the hook's own deadline -- under payday's,
// which is a backstop for a policy that would not stop -- and its answer as
// the caller is told it.
func (h hooked) ask(ctx context.Context, p admission.Policy, c admission.Call) (err error) {
	ctx, cancel := context.WithTimeout(ctx, h.timeout)
	defer cancel()

	defer func() {
		if r := recover(); r != nil {
			err = h.unanswered(ctx, c, fmt.Errorf("the policy panicked: %v", r))
		}
	}()

	said := p.Admit(ctx, c)
	if said == nil {
		return nil
	}
	if d, ok := said.(hookDown); ok {
		return h.unanswered(ctx, c, d.err)
	}
	if v, ok := status.FromError(said); ok && ctx.Err() == nil {
		switch v.Code() {
		case codes.Unavailable, codes.DeadlineExceeded, codes.Canceled, codes.Unknown, codes.Internal:
		default:
			h.log.WarnContext(ctx, "hook: refused a write", "method", c.Method, "entity", c.Entity,
				"object", c.Object.String(), "why", quote.Sent(v.Message()))

			return status.Error(codes.PermissionDenied, "refused by this deployment's hook: "+quote.Sent(v.Message()))
		}
	}

	return h.unanswered(ctx, c, said)
}

// unanswered is a hook that did not answer: the write is refused, and the log
// says so at an error's level, with the method and the entity and nothing of
// the request.
func (h hooked) unanswered(ctx context.Context, c admission.Call, err error) error {
	h.log.ErrorContext(ctx, "hook: did not answer, and the write was refused", "method", c.Method,
		"entity", c.Entity, "err", err)

	return status.Error(codes.Unavailable, "this deployment's hook did not answer, and nothing was written")
}

// hookSecrets are the fields of hand-written requests that hold a secret,
// cleared from what a hook is told as payday clears what the schema declares
// (`admission.Options.Secrets`). Most are of ways in, which no hook is asked
// about unless the file says so; they are declared anyway, since a request reaches a policy through
// whatever pattern a deployment writes tomorrow.
// `TestNoSecretOfARequestReachesAHook` holds every served request to it.
var hookSecrets = map[protoreflect.FullName][]protoreflect.FieldNumber{
	"roster.AuthSignInRequest":       {9},
	"roster.BadgeEnrolRequest":       {3},
	"roster.BadgeVerifyRequest":      {2},
	"roster.CredentialSetRequest":    {3, 4},
	"roster.DelegationRevokeRequest": {1},
	"roster.EmailConfirmRequest":     {1},
	"roster.VouchContinueRequest":    {9},
	"roster.VouchDelegateRequest":    {9},
	"roster.VouchLeaveRequest":       {1},
	"roster.VouchRedeemRequest":      {1},
	"roster.VouchVerifyRequest":      {9},
}

// admitting is the policy this plane is built with, and nil where none was
// handed over: no hook, an [Admission] with no policy, a command at the box.
//
// And the connection to the hook's service, which [Server.Close] closes with
// the rest of what [Build] opened; nil where there is none.
func (c Config) admitting(log *slog.Logger, db *ent.Client) (*hooked, *grpc.ClientConn, error) {
	if c.box {
		return nil, nil, nil
	}

	served := Served(pd.Sink{})
	h := hooked{timeout: c.Hook.timeout(), log: log}
	if c.Admission.Policy != nil {
		asks, err := hookWrites("Config.Admission.Writes", c.Admission.Writes, served, "", false)
		if err != nil {
			return nil, nil, err
		}
		h.policies = append(h.policies, asking{asks, c.Admission.Policy})
	}

	var conn *grpc.ClientConn
	if c.Hook.Addr != "" {
		asks, err := hookWrites("hook.writes", c.Hook.Writes, served, "hook.ways_in", c.Hook.WaysIn)
		if err != nil {
			return nil, nil, err
		}
		svc, err := dialHook(c.Hook, db)
		if err != nil {
			return nil, nil, err
		}
		conn = svc.conn
		h.policies = append(h.policies, asking{asks, svc})
	}
	if v, ok := admittingEverything(served); ok {
		h.policies = append(h.policies, v)
	}
	if len(h.policies) == 0 {
		return nil, conn, nil
	}

	return &h, conn, nil
}

// admissionOf is the option the data plane's sink is built with: `policy`,
// over every entity payday lets a policy be asked about.
//
// Every one, and not the entities of the methods the patterns cover: a verb
// writes rows of other entities underneath -- `Tenant.Add` writes the holder,
// the role and the binding that administer it -- and which those are is the
// verb's layer's to say and changes with it. Asked of every entity, a covered
// verb is asked about each row it writes, and an uncovered one is answered
// *go on* without anybody being asked ([hooked]).
//
// payday's deadline is a backstop, a second past the hook's: the hook's own
// is what a caller is told about ([hooked.ask]).
//
// And payday is told which calls the policy is asked about at all
// ([hooked.asked]): a write no pattern covers, a read and a way in the file
// does not say are admitted before payday copies anything or takes one of its slots, so a hook
// that hangs and fills them holds up only the writes it is asked about -- a
// sign-in, and every write nobody's pattern names, go on at once.
func admissionOf(h *hooked, c Config, log *slog.Logger) bare.Option {
	return pd.Admission(h, admission.Options{
		Asked:    h.asked,
		Entities: []string{"roster.*"},
		Timeout:  c.Hook.timeout() + time.Second,
		Secrets:  hookSecrets,
		Log:      log,
	})
}

// trustedUnary marks every call into the stack it is installed on as the
// deployment's own (`bare.TrustAdmission`), which is what [Server.Ungated] is.
//
// A frame is what admission asks a policy about, and a call through that stack
// is either one with none -- `init`, a sweep, a sign-in that has not found
// anybody yet, a key noting when it was used -- or what a way in writes for
// itself there, which is roster's own write whatever the file says of ways
// in. The trust is in which server this is, which is wiring, and in
// nothing a caller sent.
func trustedUnary(ctx context.Context, req any, _ *grpc.UnaryServerInfo, next grpc.UnaryHandler) (any, error) {
	return next(bare.TrustAdmission(ctx), req)
}

// hookDown is a hook's service that answered with an error, which is no
// answer. Not a status, and wrapping none, so that no code of the transport's
// is taken for a verdict.
type hookDown struct{ err error }

func (e hookDown) Error() string { return "the hook answered with an error" }

// hookService is the client of a deployment's [HookConfig]: a policy that asks
// `HookService.Review` and answers what it is told.
type hookService struct {
	conn    *grpc.ClientConn
	client  app.HookServiceClient
	tenants *rowTenants
}

// roots is [HookConfig.Ca] read, as a listener's `tls.client_ca` is read
// before anything is opened ([certificates]): a value that can be read and
// holds a certificate, refused by its key otherwise.
func (c HookConfig) roots() (*x509.CertPool, error) {
	ca, err := c.Ca.Value()
	if err != nil {
		return nil, fmt.Errorf("hook.ca: %w", err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM([]byte(ca)) {
		return nil, errors.New("hook.ca: holds no certificate")
	}

	return pool, nil
}

func dialHook(c HookConfig, db *ent.Client) (hookService, error) {
	creds := credentials.NewTLS(nil)
	switch {
	case c.Insecure:
		creds = insecure.NewCredentials()
	case !c.Ca.IsZero():
		pool, err := c.roots()
		if err != nil {
			return hookService{}, err
		}
		creds = credentials.NewTLS(&tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12})
	}
	conn, err := grpc.NewClient(c.Addr, grpc.WithTransportCredentials(creds))
	if err != nil {
		return hookService{}, fmt.Errorf("hook.addr: %w", err)
	}

	return hookService{conn, app.NewHookServiceClient(conn), &rowTenants{db: db}}, nil
}

// Admit asks the hook. The context is payday's, which carries the deadline
// and no value of the caller's -- no metadata, so nothing of their credential
// is sent on.
func (h hookService) Admit(ctx context.Context, c admission.Call) error {
	review, err := reviewOf(c)
	if err != nil {
		return err
	}
	review.SetTenant(h.tenants.of(ctx, c))

	v, err := h.client.Review(ctx, review)
	if err != nil {
		// Whatever the code: an error is no answer, and its words are the
		// hook's transport's, not a verdict for a caller to be told.
		return hookDown{err}
	}
	if v.GetRefused() {
		return status.Error(codes.PermissionDenied, v.GetWhy())
	}

	return nil
}

// reviewOf is what a hook is told of one row of one write.
func reviewOf(c admission.Call) (*app.WriteReview, error) {
	r := app.WriteReview_builder{
		Method:       c.Method,
		Asker:        askerOf(c.Frame),
		Entity:       c.Entity,
		Id:           c.Object.Bytes(),
		LabelsBefore: rowLabels(c.Previous),
		LabelsAfter:  rowLabels(c.Value),
	}.Build()

	for _, v := range []struct {
		m   proto.Message
		set func(*anypb.Any)
	}{
		{c.Request, r.SetRequest},
		{c.Previous, r.SetBefore},
		{c.Value, r.SetAfter},
	} {
		if v.m == nil || !v.m.ProtoReflect().IsValid() {
			continue
		}
		a, err := anypb.New(v.m)
		if err != nil {
			return nil, err
		}
		v.set(a)
	}

	return r, nil
}

// askerOf is who is making a write, as the frame says it: the caller, how
// they arrived, and whom they act for where they act by a delegation.
func askerOf(f *frame.Frame) *app.WriteAsker {
	if f == nil {
		return nil
	}

	if f.Caller != nil {
		a := app.WriteAsker_builder{
			Kind:    kindOf(f.Caller.Actor),
			Id:      f.Caller.Actor.Bytes(),
			Tenant:  idOrNil(f.Caller.Tenant),
			Arrived: app.WriteArrival_WRITE_ARRIVAL_DELEGATION,
			OnBehalfOf: app.WriteParty_builder{
				Kind:   kindOf(f.Actor),
				Id:     f.Actor.Bytes(),
				Tenant: idOrNil(f.Tenant),
			}.Build(),
		}.Build()
		if d, ok := strings.CutPrefix(f.Presence, "roster-as:"); ok {
			if id, err := pdid.Parse(d); err == nil {
				a.SetDelegation(id.Bytes())
			}
		}

		return a
	}

	arrived := app.WriteArrival_WRITE_ARRIVAL_UNSPECIFIED
	switch f.Authentication.Method {
	case authsession.Method:
		arrived = app.WriteArrival_WRITE_ARRIVAL_SESSION
	case auth.MethodBearer:
		// A bearer token here is a key, a deployment's or a tenant's: no
		// other credential roster reads travels in `authorization`.
		arrived = app.WriteArrival_WRITE_ARRIVAL_KEY
	}

	return app.WriteAsker_builder{
		Kind:    kindOf(f.Actor),
		Id:      f.Actor.Bytes(),
		Tenant:  idOrNil(f.Tenant),
		Arrived: arrived,
	}.Build()
}

// kindOf is the entity an identifier is of, by its domain.
func kindOf(id pdid.Id) string {
	switch id.Domain() {
	case pd.HolderDomain:
		return "roster.Holder"
	case pd.ApiKeyDomain:
		return "roster.ApiKey"
	}

	return ""
}

func idOrNil(id pdid.Id) []byte {
	if id.IsZero() {
		return nil
	}

	return id.Bytes()
}

// rowTenants is what says which tenant a row is in, for the rows that do not
// say it themselves.
//
// Not inside the write's transaction: payday hands a policy the call and a
// context that carries nothing of the caller's -- the transaction among it --
// and no server to read through. So `WriteReview.tenant` says, of a row it
// cannot find this way, that roster could not tell.
//
// A row of most entities names its tenant. The rest are in it through a row
// they hang off, as the wall reads them (`pd.Wall`): a person's (an address, a
// place in a group), a role's (a binding) or a group's (where it is exposed);
// and a row's image names that one by identifier alone. So the tenant is read
// of that row -- in the database, past the write's transaction, which a policy
// is not handed -- or remembered from a row asked about a moment before,
// which is how one written in the same transaction is found: `Tenant.Add`
// writes the role before the binding to it.
type rowTenants struct {
	db *ent.Client

	mu   sync.Mutex
	seen map[pdid.Id][]byte
}

// mostRemembered bounds [rowTenants.seen]: what it is for is the rows of one
// write, asked about one after another.
const mostRemembered = 4096

func (t *rowTenants) remember(id pdid.Id, tenant []byte) {
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.seen == nil || len(t.seen) >= mostRemembered {
		t.seen = map[pdid.Id][]byte{}
	}
	t.seen[id] = tenant
}

func (t *rowTenants) remembered(id pdid.Id) ([]byte, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()

	v, ok := t.seen[id]

	return v, ok
}

// of is the tenant of the row a call is about: the tenant itself for a
// tenant's row, the `tenant` the row names, or that of the row it hangs off;
// and nil where none of those can be read.
func (t *rowTenants) of(ctx context.Context, c admission.Call) []byte {
	if c.Entity == "roster.Tenant" {
		return c.Object.Bytes()
	}
	if v := edgeId(c, "tenant"); v != nil {
		t.remember(c.Object, v)

		return v
	}

	for _, edge := range []protoreflect.Name{"holder", "role", "group"} {
		v := edgeId(c, edge)
		if v == nil {
			continue
		}
		id, err := pdid.From(v)
		if err != nil {
			return nil
		}
		if tenant, ok := t.remembered(id); ok {
			return tenant
		}

		var u uuid.UUID
		switch edge {
		case "holder":
			r, err := t.db.Holder.Query().Where(entholder.IdEQ(id.Uuid())).Select(entholder.FieldTenantId).Only(ctx)
			if err != nil {
				return nil
			}
			u = r.TenantId
		case "role":
			r, err := t.db.Role.Query().Where(entrole.IdEQ(id.Uuid())).Select(entrole.FieldTenantId).Only(ctx)
			if err != nil {
				return nil
			}
			u = r.TenantId
		case "group":
			r, err := t.db.Group.Query().Where(entgroup.IdEQ(id.Uuid())).Select(entgroup.FieldTenantId).Only(ctx)
			if err != nil {
				return nil
			}
			u = r.TenantId
		}
		tenant := pdid.Id(u).Bytes()
		t.remember(id, tenant)

		return tenant
	}

	return nil
}

// edgeId is the identifier of what a row's `name` names, of the row after the
// write or else before it, and nil where it names none.
func edgeId(c admission.Call, name protoreflect.Name) []byte {
	for _, m := range []proto.Message{c.Value, c.Previous} {
		if m == nil || !m.ProtoReflect().IsValid() {
			continue
		}
		r := m.ProtoReflect()
		f := r.Descriptor().Fields().ByName(name)
		if f == nil || f.Message() == nil || !r.Has(f) {
			continue
		}
		e := r.Get(f).Message()
		id := e.Descriptor().Fields().ByName("id")
		if id == nil || id.Kind() != protoreflect.BytesKind {
			continue
		}
		if v := e.Get(id).Bytes(); len(v) > 0 {
			return v
		}
	}

	return nil
}

// rowLabels is a row's `labels`, lifted out, and nil for no row or none.
func rowLabels(m proto.Message) map[string]string {
	if m == nil || !m.ProtoReflect().IsValid() {
		return nil
	}
	r := m.ProtoReflect()
	f := r.Descriptor().Fields().ByName("labels")
	if f == nil || !f.IsMap() || f.MapKey().Kind() != protoreflect.StringKind || f.MapValue().Kind() != protoreflect.StringKind {
		return nil
	}
	out := map[string]string{}
	r.Get(f).Map().Range(func(k protoreflect.MapKey, v protoreflect.Value) bool {
		out[k.String()] = v.String()
		return true
	})
	if len(out) == 0 {
		return nil
	}

	return maps.Clone(out)
}

// AdmitEverything is the variable a run of the suite is told by to build every
// data plane with a policy that lets every write through, asked about every
// write a hook could be (`/roster.*/*`), over every entity payday lets one be
// asked about. With it set the suite passes as it does without: that is what
// says every write roster makes underneath its own servers either goes through
// them or says it is the deployment's own (`docs/development.md`).
//
// Read in a test binary alone, so that nothing a deployment's environment says
// installs it.
const AdmitEverything = "ROSTERTEST_ADMIT_EVERYTHING"

// AdmittingEverything is whether this process was told [AdmitEverything]: a
// test binary, with the variable set.
func AdmittingEverything() bool {
	return os.Getenv(AdmitEverything) != "" && strings.HasSuffix(strings.TrimSuffix(os.Args[0], ".exe"), ".test")
}

func admittingEverything(served []string) (asking, bool) {
	if !AdmittingEverything() {
		return asking{}, false
	}
	asks, err := hookWrites(AdmitEverything, []string{"/roster.*/*"}, served, "", false)
	if err != nil {
		panic(err)
	}

	return asking{asks, admission.Func(func(context.Context, admission.Call) error { return nil })}, true
}

// admitted is `pd.AdmissionUnary` where this plane's writes are asked of a
// hook, after the gate has let the call through, so that what the hook is
// told is the request the caller sent and the method they called rather than
// what a layer made of it; and nothing where they are not.
func (s *Server) admitted() []grpc.UnaryServerInterceptor {
	if !s.admits {
		return nil
	}

	return []grpc.UnaryServerInterceptor{pd.AdmissionUnary}
}

// WayIn is whether `method` is a way in ([waysIn]), which no hook is asked
// about unless the file says so ([HookConfig.WaysIn]).
func WayIn(method string) bool { return isWayIn(method) }

// Reads is whether `method` is a read, which no hook is asked about: a pattern
// that covers only reads is refused, and a wider one leaves them out.
func Reads(method string) bool { return isHookRead(method) }
