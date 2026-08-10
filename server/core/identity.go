package core

import (
	"context"
	"strings"

	"github.com/lesomnus/payday/pderr"

	app "github.com/lesomnus/roster/rstr"
)

type coreIdentity struct {
	Core
	app.IdentityServiceServer
}

func (s Core) Identity() app.IdentityServiceServer {
	return coreIdentity{s, s.Next().Identity()}
}

// Add links an external identity to somebody, and refuses the two ways that go
// wrong quietly.
//
// Neither is something the schema can state. Uniqueness of the pair is a column
// constraint; these are judgements about what a good pair looks like and about
// what a second one means.
func (s coreIdentity) Add(ctx context.Context, req *app.IdentityAddRequest) (*app.Identity, error) {
	if err := subjectIsStable(req.GetSubject()); err != nil {
		return nil, err
	}

	if err := s.oneAccountPerProvider(ctx, req); err != nil {
		return nil, err
	}

	return s.IdentityServiceServer.Add(ctx, req)
}

// subjectIsStable refuses a subject that is obviously the wrong value.
//
// A provider's subject has to be the identifier that provider treats as
// immutable -- a numeric ID for GitHub, `objectGUID` or `entryUUID` for LDAP,
// `oid` for Entra. What gets written by mistake is the username or the email
// address, because both are visible in the same claims and both read like a
// name.
//
// Getting it wrong is not noticed at link time. It is noticed months later,
// when somebody changes their address and cannot sign in -- or, far worse, when
// their old address is reassigned to a new joiner, who signs in and is served
// as them. That is the case this refuses.
//
// It cannot catch everything: a username with no "@" in it looks like anything
// else. What it catches is the mistake that has an unrecoverable failure at the
// end of it.
func subjectIsStable(v string) error {
	if v == "" {
		return pderr.Invalidf("subject", "must not be empty")
	}
	if strings.Contains(v, "@") {
		return pderr.Invalidf("subject",
			"looks like an address; it must be the identifier the provider treats as immutable, "+
				"because an address is reassigned and whoever gets it next would be served as this person")
	}

	return nil
}

// oneAccountPerProvider refuses a second identity at a provider this person
// already has one at.
//
// The pair is unique, so this is not about two people sharing a subject -- the
// column handles that. It is about **one** person accumulating two accounts at
// one provider, which is what a link that went to the wrong row looks like from
// here.
//
// A person genuinely has one account at each provider. If they have two, one of
// them belongs to somebody else, and the design is explicit that a failed link
// must not quietly become a new record: an explicit failure is better.
func (s coreIdentity) oneAccountPerProvider(ctx context.Context, req *app.IdentityAddRequest) error {
	ref := req.GetHolder()
	if ref == nil {
		return nil
	}

	// Read through this stack rather than around it, so the wall applies: an
	// identity of somebody the caller cannot see is not a reason they can
	// discover.
	vs, err := s.Next().Identity().List(ctx, app.IdentityListRequest_builder{}.Build())
	if err != nil {
		return err
	}

	for _, v := range vs.GetItems() {
		if v.GetProvider() != req.GetProvider() {
			continue
		}
		if !sameHolder(v, ref) {
			continue
		}

		return pderr.Invalidf("provider",
			"this person already has a %s identity; a second one is a link that found the wrong row, "+
				"and linking it would join two people into one", req.GetProvider())
	}

	return nil
}

func sameHolder(v *app.Identity, ref *app.HolderRef) bool {
	id := ref.GetId()
	if len(id) == 0 {
		return false
	}

	return string(v.GetHolder().GetId()) == string(id)
}
