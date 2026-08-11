package cmd

import (
	"slices"

	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
)

// everyMethod is every RPC this deployment serves, read off the descriptors it
// was built with.
//
// # Where it is used, and where it deliberately is not
//
// Only to **answer a question**, never to decide one. `MeService` says what a
// caller may call so that a page can decide what to draw, and a page needs the
// list; the gate decides with `Role.every_method` itself and never looks here.
//
// That split is the whole reason the flag exists. A list is what was true when
// somebody wrote it down and the flag is what is true now, so the enforcement
// reads the flag and cannot fall behind an upgrade. If this were the
// enforcement, a role would allow exactly the methods that existed when the
// binary that answered was built -- which is nearly the same thing, until a
// rolling deploy has two versions answering at once and a caller's permissions
// depend on which replica they reached.
//
// # It reads the registry rather than a list
//
// Every service registered by anything linked into this binary declares itself
// there at init. So an entity added to the schema is here the moment it is
// generated, which is what "every RPC" has to mean to be worth saying.
//
// Filtered to this app's proto package, which leaves out payday's own --
// `BatchService` and `TokenService`. That is right and not an oversight: the
// batch is a way of calling the methods below and is guarded per operation, and
// introspection is asked by a product app holding a key rather than by anybody
// a role is written for.
func everyMethod() []string {
	var vs []string

	protoregistry.GlobalFiles.RangeFiles(func(fd protoreflect.FileDescriptor) bool {
		if fd.Package() != protoPackage {
			return true
		}

		ss := fd.Services()
		for i := range ss.Len() {
			s := ss.Get(i)
			ms := s.Methods()
			for j := range ms.Len() {
				vs = append(vs, "/"+string(s.FullName())+"/"+string(ms.Get(j).Name()))
			}
		}

		return true
	})

	slices.Sort(vs)

	return vs
}

// protoPackage is what this app's entities declare, and the one place it is
// written down outside the schema.
const protoPackage protoreflect.FullName = "roster"
