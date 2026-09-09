package rstr

// Hand-written, beside the generated. `.g.go` and `.pb.go` are a generator's
// and this is not; `pd gen` leaves it alone.
//
// What earns a file here is a question about a **generated type's meaning**
// that more than one side of the wire has to answer the same way. There is no
// other package for that: `server/vouch` may not import a consumer, and
// `login/` and `account/` may not import a server package, so a predicate they
// must agree on has nowhere else to live.

// OffersPassword is whether a password is a way into this tenant.
//
// **Unset is yes.** `TenantConfig.password` has presence where everything
// around it does not, precisely so that this can be true: a tenant written
// before the field existed, or one nobody has configured, offers what it always
// offered. A nil tenant is the same answer -- a caller that did not select the
// row has not been told no.
//
// Three callers, which is why it is one function:
//
//   - `server/vouch` refuses a password, and a recovery link, when this is
//     false. That is the enforcement, and the reason this is a fact rather
//     than an instruction to a screen.
//   - the Login App's `/flow` and the account page's `/providers` draw a form
//     only when it is true. They are drawing what is already true, which is
//     the difference D22 asks for.
//
// Two of those are on the far side of the wire from the third, so the sentence
// travels rather than being written twice.
func (x *Tenant) OffersPassword() bool {
	c := x.GetConfig()

	return !c.HasPassword() || c.GetPassword()
}
