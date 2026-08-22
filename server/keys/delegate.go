package keys

import (
	"context"
	"crypto/subtle"
	"time"

	"github.com/lesomnus/z"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/lesomnus/payday/pdid"

	app "github.com/lesomnus/roster/rstr"
)

// DelegateFor is how long a delegation lives when nothing says otherwise.
//
// **Provisional.** D24 puts the delegation token first precisely so that a page
// decides this rather than an argument does: *its lifetime, its scope and where
// it is refreshed are decided by a page that uses it, not by reasoning.* Until
// `examples/sso` names one, this is the number, and it is short for the reason
// D21 gives about the other short-lived thing -- a bearer credential minted
// without the person present is acceptable because it is barely alive.
const DelegateFor = 15 * time.Minute

// Delegated is what a delegation is minted from.
//
// A struct rather than four arguments, because three of them are opaque and two
// of those are `[]byte`: a call whose arguments can be swapped without the
// compiler noticing is a call somebody will swap.
type Delegated struct {
	// Whose it is: the person the caller has just authenticated.
	Holder pdid.Id

	// Which caller is being given it, so that another cannot present it. See
	// `delegation.proto` for why this is bytes and not a reference.
	Issuer []byte

	// What it may call. It only ever narrows the person; see the field.
	Methods []string

	// How long it lives. Zero takes [DelegateFor]; a negative one is refused
	// rather than falling back to it, because a caller that computed a
	// duration and got the sign wrong is a caller whose credential would
	// otherwise quietly live for the default.
	For time.Duration
}

// Delegate mints a delegation and writes what verifies it, answering with the
// token once.
//
// It is not on the wire and is not meant to be. D23 says the answer rides back
// with the yes -- `VouchService.Verify` has already proved the person, and this
// is the credential that goes with that answer -- so what calls this is a
// service in this process, holding the frame of the app that asked. An RPC that
// minted one for anybody named would be an RPC that hands out a credential for
// a person nobody proved.
//
// `s` is the unwalled server for the reason `vouch.Verify` reads one: this
// happens while somebody is being worked out, so there is no frame to narrow
// by, and the row being written is roster's own answer rather than a caller's
// write.
func Delegate(ctx context.Context, s app.Server, req Delegated) (string, *app.Delegation, error) {
	if req.Holder == pdid.Nil {
		return "", nil, status.Error(codes.InvalidArgument, "holder: a delegation is for somebody")
	}
	if len(req.Issuer) == 0 {
		// Refused rather than stored as empty, because empty is not a state
		// this can hold: `subtle.ConstantTimeCompare` answers 1 for two empty
		// slices, so a delegation bound to nobody would match a caller whose
		// own identifier failed to resolve. See `delegation.proto`.
		return "", nil, status.Error(codes.InvalidArgument, "issuer: a delegation nobody is bound to is one anybody may use")
	}
	if len(req.Methods) == 0 {
		// The same refusal `roster key add` makes about `--allow`, for the same
		// reason: everything hands out more than was asked for, and nothing
		// mints a credential that silently does not work.
		return "", nil, status.Error(codes.InvalidArgument, "methods: a delegation that allows nothing opens no door")
	}

	d := req.For
	switch {
	case d < 0:
		return "", nil, status.Error(codes.InvalidArgument, "for: a delegation cannot have already expired")
	case d == 0:
		d = DelegateFor
	}

	token, sum, err := Mint(PrefixDelegation)
	if err != nil {
		return "", nil, err
	}

	v, err := s.Delegation().Add(ctx, app.DelegationAddRequest_builder{
		Holder:      app.HolderRef_builder{Id: req.Holder.Bytes()}.Build(),
		Secret:      sum,
		Issuer:      req.Issuer,
		Methods:     req.Methods,
		DateExpires: timestamppb.New(time.Now().Add(d)),
	}.Build())
	if err != nil {
		return "", nil, err
	}

	return token, v, nil
}

// bearer is a token resolved, in the words both answers need.
//
// Two tables answer this and neither has the other's shape, so what is the same
// about them is written once here rather than twice at the call sites. What
// differs is in the two `find` functions, and each says what it differs in.
type bearer struct {
	// The row itself. It is the actor for a deployment key and is nothing to
	// anybody else.
	Id []byte

	// Whose it is. Required and immutable on both entities, so a nil one is a
	// row that should not exist -- checked anyway, because serving a key as
	// itself when it was meant to be served as somebody is the one mistake the
	// prefix switch exists to prevent.
	Holder *app.Holder

	Methods []string

	// When it stops working, and the zero value means it does not. A delegation
	// never answers with the zero value; see [findDelegation].
	Expires time.Time

	// Which caller was given it, empty for a key. Compared where a frame
	// exists, which is not where the token is looked up.
	Issuer []byte
}

// findKey is [lookup] in the words [bearer] uses.
// findKey is [lookup] as a bearer, plus the one holder-level refusal both
// tables share.
//
// **A suspended holder's token stands for nobody.** D26's table puts
// `date_disabled` at `cmd.Resolver`, on the grounds that it is where every
// credential resolving to a holder arrives -- which is true of every credential
// arriving *here* and not of a product app's. custody is handed an `rt_` and
// asks `TokenService/Introspect`; nothing about that call passes through the
// resolver, so suspending somebody stopped them signing in and left them
// working in every app in front until the token expired, which for a key is
// possibly never.
//
// So it is here, where the two answers this package gives are both built:
// [Store]'s, which the resolver would have caught a moment later anyway, and
// [Service]'s, which nothing else was going to catch at all. One place rather
// than two that have to agree.
//
// It is not the epoch and does not read like it. `date_invalidated` voids what
// was issued before a moment and is compared against this row's own timestamp;
// this is a fact about the person now, so enabling them gives the token back.
func findKey(ctx context.Context, s app.Server, token string) (*bearer, error) {
	v, err := lookup(ctx, s, token)
	if err != nil {
		return nil, err
	}
	if err := signable(v.GetHolder()); err != nil {
		return nil, err
	}

	return &bearer{
		Id:      v.GetId(),
		Holder:  v.GetHolder(),
		Methods: v.GetMethods(),
		Expires: expiryOf(v),
	}, nil
}

// signable refuses a token whose holder is suspended. See [findKey].
//
// The same `NotFound` everything else about a token is refused with, so that
// telling them apart says nothing: an app hearing it stops trusting the string
// and sends the person to authenticate again, which is where they find out.
func signable(v *app.Holder) error {
	if v.GetDateDisabled() != nil {
		return status.Error(codes.NotFound, "no such token")
	}

	return nil
}

// findDelegation is [lookup] over the other table, and differs in three things.
//
// **An absent expiry is refused rather than meaning forever.** A key with none
// is a service running unattended, which is what `ApiKey.date_expires` is
// nullable for; a delegation with none is a row nothing should have written,
// and treating it as endless would turn a minting bug into a credential that
// outlives everybody. The schema cannot say required -- D6 and F3 -- so this
// is where it is said.
//
// **Nothing is touched.** `ApiKey.date_used` exists to find the key nobody
// needs any more; a row that is gone within the hour has no such question, and
// a write on every request to answer it would be the more expensive half of
// this whole path.
//
// **The holder's epoch is read here**, and it can only be read here: `Holder`'s
// `date_invalidated` says everything issued before a moment is void, and the
// only thing that knows when this credential was issued is this row. So the
// comparison is between two columns that are never in the same place again --
// which is why the resolver, which sees the holder and not the credential,
// cannot cover this. It does cover `date_disabled`, for every credential that
// reaches roster -- and a product app's does not reach it, so that is read
// here too; see [findKey].
//
// **The issuer comes back unchecked**, because there is nothing here to check
// it against: `auth.TokenStore.Lookup` is handed the token and nothing else --
// no caller, no peer, no frame. A comparison written here compiles, runs, and
// binds nothing.
func findDelegation(ctx context.Context, s app.Server, token string) (*bearer, error) {
	v, err := s.Delegation().Get(ctx, app.DelegationGetRequest_builder{
		Ref: app.DelegationRef_builder{Secret: Sum(token)}.Build(),
		Select: app.DelegationSelect_builder{
			Secret:      z.Ptr(true),
			Methods:     z.Ptr(true),
			Issuer:      z.Ptr(true),
			DateExpires: z.Ptr(true),
			DateCreated: z.Ptr(true),

			Holder: app.HolderSelect_builder{
				Tenant:          app.TenantSelect_builder{}.Build(),
				DateInvalidated: z.Ptr(true),
				DateDisabled:    z.Ptr(true),
			}.Build(),
		}.Build(),
	}.Build())
	if err != nil {
		return nil, err
	}

	// Compared again in constant time, for [lookup]'s reason: the index did the
	// finding, and this is what makes the answer independent of how a
	// mismatched hash sorts.
	if subtle.ConstantTimeCompare(v.GetSecret(), Sum(token)) != 1 {
		return nil, status.Error(codes.NotFound, "no such token")
	}

	u := v.GetDateExpires()
	if u == nil || !time.Now().Before(u.AsTime()) {
		return nil, status.Error(codes.NotFound, "no such token")
	}

	// Everything issued before the holder's epoch is void, which is what "sign
	// out everywhere" writes and what a password reset will. Not before or
	// equal: two writes in the same millisecond are a delegation minted by the
	// sign-in that the invalidation was about, and the safe reading of a tie is
	// that it is too old.
	if w := v.GetHolder().GetDateInvalidated(); w != nil && !v.GetDateCreated().AsTime().After(w.AsTime()) {
		return nil, status.Error(codes.NotFound, "no such token")
	}

	// And whoever it is about has to be somebody who may sign in at all, which
	// is a different question from when this was issued. See [findKey].
	if err := signable(v.GetHolder()); err != nil {
		return nil, err
	}

	return &bearer{
		Id:      v.GetId(),
		Holder:  v.GetHolder(),
		Methods: v.GetMethods(),
		Expires: u.AsTime(),
		Issuer:  v.GetIssuer(),
	}, nil
}

// issued says whether this is the caller the delegation was minted for.
//
// A delegation with no issuer never reaches this -- [Delegate] refuses to write
// one -- and neither does a caller who could not be named: both are the empty
// slice, and two empty slices compare **equal**. So each is refused before the
// comparison rather than by it.
func issued(v *bearer, to pdid.Id) error {
	if len(v.Issuer) == 0 || to == pdid.Nil {
		return status.Error(codes.NotFound, "no such token")
	}
	if subtle.ConstantTimeCompare(v.Issuer, to.Bytes()) != 1 {
		// The same refusal a token that was never here gets. Told apart, this
		// answers "that string is a real delegation, just not yours".
		return status.Error(codes.NotFound, "no such token")
	}

	return nil
}

// Undelegate ends a delegation, if it is the caller's to end.
//
// Erased rather than deleted, so that a trail naming the row still finds
// something; `<Entity>Pick` narrows to the live rows, so it is out of reach the
// moment this returns, and [Sweep] collects it when its own clock runs out.
//
// # Everything answers the same
//
// A token that was never here, one that expired, one issued to somebody else:
// all of them return nil and remove nothing. That is the rule the generated
// `Erase` states -- *erasing what is not there succeeds*, and out of reach is
// not there -- and here it does a second job, which is to say nothing to
// whoever is holding a string they found.
func Undelegate(ctx context.Context, s app.Server, token string, by pdid.Id) error {
	v, err := findDelegation(ctx, s, token)
	if err != nil {
		if status.Code(err) == codes.NotFound {
			return nil
		}

		return err
	}
	if issued(v, by) != nil {
		return nil
	}

	_, err = s.Delegation().Erase(ctx, app.DelegationRef_builder{Id: v.Id}.Build())

	return err
}
