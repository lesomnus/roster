package rstr

// Hand-written, beside the generated, for the reason `tenant.go` gives: what a
// holder's labels say about it is read by the server and by the consumers on
// the far side of the wire, and each must read it the same way.

// LabelDeclared is the label a row a file or a front door's own configuration
// declared carries, and its value is what declared it: `config: login`, a
// file's name. Written by what applies that file, in process, and by no
// caller (`cmd.Undeclared`).
const LabelDeclared = "roster.declared"

// LabelApp is the label `roster app install` writes on the holder it makes for
// an app in a tenant, or takes with `--adopt`, and its value is the name the
// app was installed under. Written by that command, at the box, and by no
// caller (`cmd.Undeclared`). Nothing reads it to decide what anybody may do.
const LabelApp = "roster.app"

// NoPerson is whether a holder's own row says it is a machine: a front door's
// ([LabelDeclared]) or an app's ([LabelApp]).
//
// A tenant's holders are not all people, and a directory built on roster lists
// people. The LDAP directory, the SCIM endpoint and `roster scim adopt` leave
// such a holder out by this, and so may a directory of an app's own
// (`docs/apps.md`). It is one test for the two labels, so that the readers do
// not each keep a copy that one of them forgets to widen.
//
// It reads `labels` alone, so a holder read without them is no machine to it.
// What it cannot see -- a holder nothing labelled, like the one `roster scim
// provision` makes -- each reader leaves out by what it knows of its own.
func (x *Holder) NoPerson() bool {
	l := x.GetLabels()

	return l[LabelDeclared] != "" || l[LabelApp] != ""
}
