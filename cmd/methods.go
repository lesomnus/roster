package cmd

import "google.golang.org/protobuf/reflect/protoreflect"

// protoPackage is what this app's entities declare, and the one place it is
// written down outside the schema.
//
// It exists for `/roster.*/*`: the pattern the first role holds, which is this
// app's own package and not payday's besides. See `everyRosterMethod`.
//
// # What used to be here
//
// A function that walked `protoregistry.GlobalFiles` and answered with every
// RPC in this package, because the widest role was a boolean and something had
// to turn it into a list a page could read.
//
// A pattern needs neither half. The gate matches it with `frame.Covers`, and
// `MeService` answers with the pattern itself -- which a page can evaluate the
// same three ways, and which does not depend on which binary answered. That
// last part mattered more than it looked: an enumeration is what exists in
// *this* build, so during a rolling deploy two replicas would have told a page
// two different things about the same person.
const protoPackage protoreflect.FullName = "roster"
