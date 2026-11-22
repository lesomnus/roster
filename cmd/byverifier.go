package cmd

import (
	"context"
	"fmt"
	"strings"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"

	"github.com/lesomnus/payday/grpcx"
)

// NotByVerifier refuses a request that names a row by its verifier: the
// `secret` arm of a reference, which `ApiKeyRef` and `DelegationRef` have.
//
// # Why
//
// The arm is how `server/keys` finds the row a token stands for, in process:
// the hash of what somebody presented. At a port it answered too.
// `ApiKeyService/Get`, `List` and `Erase` and `DelegationService/Get` by a
// verifier found the row, and an erase erased it, so whoever held those methods
// and a hash had a question to ask of every row -- *is this somebody's key,
// and whose* -- that nothing on the wire was meant to answer, and nothing in
// this repository asks it over a port. The verifier itself is answered by no
// read ([Register]); this is the other half, that no read is asked by one.
//
// # Why here, and not in a layer
//
// For the reason [Undeclared] gives: one rule about every request of every
// service on every listener, said where they arrive. What uses the arm --
// `keys.Store`, `keys.Service`, a delegation's checks -- reads the stack in
// process, where no interceptor is, and goes on doing so.
//
// # What it reads
//
// Any reference in the request, at any depth, and each message of a stream as
// it arrives: a reference is a message whose name ends in `Ref`, and its arm
// is the field `secret` of its oneof.
func NotByVerifier() grpcx.Chain {
	return grpcx.Chain{
		Unary: []grpc.UnaryServerInterceptor{func(ctx context.Context, req any, _ *grpc.UnaryServerInfo, next grpc.UnaryHandler) (any, error) {
			if err := byVerifier(req); err != nil {
				return nil, err
			}

			return next(ctx, req)
		}},
		Stream: []grpc.StreamServerInterceptor{func(srv any, ss grpc.ServerStream, _ *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
			return handler(srv, notByVerifierStream{ss})
		}},
	}
}

type notByVerifierStream struct{ grpc.ServerStream }

func (s notByVerifierStream) RecvMsg(m any) error {
	if err := s.ServerStream.RecvMsg(m); err != nil {
		return err
	}

	return byVerifier(m)
}

// byVerifier is the refusal of a request that names a row by its verifier,
// and nil for one that does not.
func byVerifier(req any) error {
	m, ok := req.(proto.Message)
	if !ok {
		return nil
	}
	at := verifierArm(m.ProtoReflect(), "")
	if at == "" {
		return nil
	}

	return status.Errorf(codes.InvalidArgument,
		"%s: a row is named here by its identifier or its name; its verifier finds it only inside roster", at)
}

// verifierArm is the path of the first reference in `m` that is set to its
// `secret` arm, and nothing where none is.
func verifierArm(m protoreflect.Message, path string) string {
	d := m.Descriptor()
	if strings.HasSuffix(string(d.Name()), "Ref") {
		if f := d.Fields().ByName("secret"); f != nil && f.ContainingOneof() != nil && m.Has(f) {
			return path + "secret"
		}
	}

	found := ""
	m.Range(func(fd protoreflect.FieldDescriptor, v protoreflect.Value) bool {
		switch {
		case fd.Message() == nil:
		case fd.IsMap():
			if fd.MapValue().Message() == nil {
				break
			}
			v.Map().Range(func(k protoreflect.MapKey, v protoreflect.Value) bool {
				found = verifierArm(v.Message(), fmt.Sprintf("%s%s[%s].", path, fd.Name(), k))

				return found == ""
			})
		case fd.IsList():
			for i := range v.List().Len() {
				if found = verifierArm(v.List().Get(i).Message(), fmt.Sprintf("%s%s[%d].", path, fd.Name(), i)); found != "" {
					break
				}
			}
		default:
			found = verifierArm(v.Message(), path+string(fd.Name())+".")
		}

		return found == ""
	})

	return found
}
