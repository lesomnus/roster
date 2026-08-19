package vouch

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"strings"
	"time"

	"github.com/lesomnus/z"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/lesomnus/payday/pdid"

	app "github.com/lesomnus/roster/rstr"
)

// A way in that roster mints and somebody else delivers.
//
// `link.proto` is the row and PLAN.md's item 3 is the subject. What is here is
// the pair of RPCs and the one property that is easy to lose: **minting says
// nothing about whether anybody is there.**

// PrefixLink is what a link looks like, so that one in a log is recognisable.
const PrefixLink = "rl_"

// KindLink is what a link answers as, in `satisfied`.
//
// It is not a `Credential` kind -- nothing is stored under it and
// `verifierOf` does not know it -- and it is in the same vocabulary because
// that vocabulary is *how somebody got in*, which is the question `satisfied`
// asks.
const KindLink = "link"

// LinkFor is how long a link lasts when nothing says otherwise.
//
// Long enough for mail to arrive and somebody to notice it, and short enough
// that one left in a mailbox is not a way in next week. A caller may ask for
// less and not for more.
const LinkFor = 15 * time.Minute

// Link mints a way in for somebody and answers with it once.
func (s *Server) Link(ctx context.Context, req *app.VouchLinkRequest) (*app.VouchLinkResponse, error) {
	issuer, err := issuerOf(ctx)
	if err != nil {
		return nil, err
	}

	expires := time.Now().Add(LinkFor)
	if u := req.GetExpires(); u != nil {
		if at := u.AsTime(); at.After(time.Now()) && at.Before(expires) {
			// Less, and not more. How long the channel takes is the caller's to
			// know; how long a way into somebody's account may lie around is
			// not.
			expires = at
		}
	}

	token, sum, err := mintLink()
	if err != nil {
		return nil, err
	}

	ref, err := refOf(req.GetWho())
	if err != nil {
		return nil, err
	}
	if ref == nil {
		ref, err = s.byAddress(ctx, req.GetWho().GetTenant(), req.GetWho().GetAddress())
		if err != nil {
			if status.Code(err) != codes.NotFound {
				return nil, err
			}

			// Nobody. **Answered anyway**, with a token that resolves to
			// nothing, because the alternative is that asking for a link
			// answers *is this address here* -- through a form that is meant to
			// be filled in by strangers, and about the one question every other
			// refusal here is careful not to answer.
			//
			// The cost is a token nobody can spend, which is what the caller
			// was going to send to a mailbox that does not belong to anybody
			// here anyway.
			return app.VouchLinkResponse_builder{
				Token:   token,
				Expires: timestamppb.New(expires),
			}.Build(), nil
		}
	}

	who, err := s.open.Holder().Get(ctx, app.HolderGetRequest_builder{
		Ref: ref,
		Select: app.HolderSelect_builder{
			DateErased:   z.Ptr(true),
			DateDisabled: z.Ptr(true),
		}.Build(),
	}.Build())
	if err != nil {
		if status.Code(err) != codes.NotFound {
			return nil, err
		}

		return app.VouchLinkResponse_builder{
			Token:   token,
			Expires: timestamppb.New(expires),
		}.Build(), nil
	}
	if who.GetDateDisabled() != nil {
		// Somebody not to sign in is somebody not to send a way in to, and the
		// answer is the same one a stranger gets.
		return app.VouchLinkResponse_builder{
			Token:   token,
			Expires: timestamppb.New(expires),
		}.Build(), nil
	}

	if _, err := s.open.Link().Add(ctx, app.LinkAddRequest_builder{
		Holder:      app.HolderRef_builder{Id: who.GetId()}.Build(),
		Secret:      sum,
		Issuer:      issuer.Bytes(),
		DateExpires: timestamppb.New(expires),
	}.Build()); err != nil {
		return nil, err
	}

	return app.VouchLinkResponse_builder{
		Token:   token,
		Expires: timestamppb.New(expires),
	}.Build(), nil
}

// Redeem spends a link, which proves the person and nothing more.
func (s *Server) Redeem(ctx context.Context, req *app.VouchRedeemRequest) (*app.VouchDelegateResponse, error) {
	methods := req.GetMethods()
	if len(methods) == 0 {
		return nil, status.Error(codes.InvalidArgument,
			"methods: a delegation that allows nothing opens no door")
	}
	if err := mayDelegate(ctx, methods); err != nil {
		return nil, err
	}

	issuer, err := issuerOf(ctx)
	if err != nil {
		return nil, err
	}

	v, err := s.link(ctx, req.GetToken(), issuer)
	if err != nil {
		if status.Code(err) != codes.NotFound {
			return nil, err
		}

		// A link that was never here, one that was spent, one that expired, one
		// somebody else was issued, and one minted for a stranger: one answer.
		return app.VouchDelegateResponse_builder{Verified: no()}.Build(), nil
	}

	// Spent whatever happens next, which is the whole of what single-use means:
	// *used* is *not there*.
	if _, err := s.open.Link().Erase(ctx, app.LinkRef_builder{Id: v.GetId()}.Build()); err != nil {
		return nil, err
	}

	// And then it is a first factor like any other. If they have a second one
	// it is asked for exactly as it would have been after a password -- a link
	// that skipped it would be a way to turn a mailbox into an account.
	//
	// `metered_by` is empty because there is no first-factor row to meter
	// against: a link is not a `Credential` and guessing one is guessing 256
	// bits. `step` falls back to the row being tried, and the note there says
	// why that is enough here -- restarting costs a fresh link, which costs the
	// channel.
	res, err := s.answer(ctx, v.GetHolder(), []string{KindLink}, nil, issuer)
	if err != nil {
		return nil, err
	}
	if !res.GetOk() {
		return app.VouchDelegateResponse_builder{Verified: res}.Build(), nil
	}

	holder, err := pdid.From(v.GetHolder().GetId())
	if err != nil {
		return nil, err
	}

	return s.mint(ctx, res, holder, issuer, methods, req.GetExpires())
}

// link finds the row a token names, if it is the caller's and still open.
func (s *Server) link(ctx context.Context, token string, by pdid.Id) (*app.Link, error) {
	if !strings.HasPrefix(token, PrefixLink) {
		return nil, status.Error(codes.NotFound, "no such link")
	}

	sum := sha256.Sum256([]byte(token))

	v, err := s.open.Link().Get(ctx, app.LinkGetRequest_builder{
		Ref: app.LinkRef_builder{Secret: sum[:]}.Build(),
		Select: app.LinkSelect_builder{
			Secret:      z.Ptr(true),
			Issuer:      z.Ptr(true),
			DateExpires: z.Ptr(true),

			Holder: app.HolderSelect_builder{
				Tenant:       app.TenantSelect_builder{}.Build(),
				DateErased:   z.Ptr(true),
				DateDisabled: z.Ptr(true),
			}.Build(),
		}.Build(),
	}.Build())
	if err != nil {
		return nil, err
	}

	if subtle.ConstantTimeCompare(v.GetSecret(), sum[:]) != 1 {
		return nil, status.Error(codes.NotFound, "no such link")
	}

	u := v.GetDateExpires()
	if u == nil || !time.Now().Before(u.AsTime()) {
		return nil, status.Error(codes.NotFound, "no such link")
	}

	// Somebody erased or suspended between the sending and the clicking is
	// somebody this must not let in.
	if h := v.GetHolder(); h.GetDateErased() != nil || h.GetDateDisabled() != nil {
		return nil, status.Error(codes.NotFound, "no such link")
	}

	if len(v.GetIssuer()) == 0 || by == pdid.Nil ||
		subtle.ConstantTimeCompare(v.GetIssuer(), by.Bytes()) != 1 {
		return nil, status.Error(codes.NotFound, "no such link")
	}

	return v, nil
}

// mintLink is the token and the verifier stored for it.
func mintLink() (string, []byte, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", nil, status.Error(codes.Internal, "a link cannot be made just now")
	}

	token := PrefixLink + base64.RawURLEncoding.EncodeToString(b)
	sum := sha256.Sum256([]byte(token))

	return token, sum[:], nil
}
