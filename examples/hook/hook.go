// Package main is a hook a deployment names in `hook:`, which roster asks
// about the writes its patterns cover before each commits
// (`proto/hook/v1/hook.proto`, `docs/operating.md` § *A hook a deployment refuses
// a write by*).
//
// What it decides is one rule a deployment might want and roster has no
// opinion about: a row labelled `frozen: "true"` is not changed or erased by
// anybody, until the label is taken off at the box -- where no hook is asked.
// It reads the labels roster lifts out of the row as it was, so it unpacks no
// row and asks roster nothing.
//
//	go run ./examples/hook --addr 127.0.0.1:9443
//
// and, in roster's file:
//
//	hook:
//	  addr: 127.0.0.1:9443
//	  insecure: true
//	  writes: [/roster.GroupMembershipService/*, /roster.HolderService/Disable]
package main

import (
	"context"
	"flag"
	"log"
	"net"

	"google.golang.org/grpc"

	"github.com/lesomnus/roster/rstr"
)

type hook struct {
	rstr.UnimplementedHookServiceServer
}

// Review refuses a write of a row that was frozen, and lets every other be.
func (hook) Review(ctx context.Context, r *rstr.WriteReview) (*rstr.WriteVerdict, error) {
	if r.GetLabelsBefore()["frozen"] == "true" {
		return rstr.WriteVerdict_builder{
			Refused: true,
			Why:     "this row is frozen; the people team takes the label off",
		}.Build(), nil
	}

	return &rstr.WriteVerdict{}, nil
}

func main() {
	addr := flag.String("addr", "127.0.0.1:9443", "where roster dials this hook")
	flag.Parse()

	l, err := net.Listen("tcp", *addr)
	if err != nil {
		log.Fatal(err)
	}

	g := grpc.NewServer()
	rstr.RegisterHookServiceServer(g, hook{})
	log.Fatal(g.Serve(l))
}
