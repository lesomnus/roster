package core

import (
	"context"

	"github.com/lesomnus/payday/pderr"

	app "github.com/lesomnus/roster/rstr"
)

// coreRelation is the layer over the generated `RelationService`: two people
// of one tenant, who are not the same person.
//
// # What it does not ask
//
// What the relation is, or whether the caller holds anything over either of
// them. A relation is a record no rule reads (`relation.proto`), so writing
// one hands out nothing: the wall, and the role that names the write, are the
// whole of who may.
type coreRelation struct {
	Core
	app.RelationServiceServer
}

func (s Core) Relation() app.RelationServiceServer {
	return coreRelation{s, s.Next().Relation()}
}

func (s coreRelation) Add(ctx context.Context, req *app.RelationAddRequest) (*app.Relation, error) {
	who, err := s.holderOf(ctx, req.GetHolder())
	if err != nil {
		return nil, err
	}
	whom, err := s.holderOf(ctx, req.GetOther())
	if err != nil {
		return nil, err
	}
	if who == whom {
		return nil, pderr.Invalidf("other", "somebody is nothing to themselves; a relation is between two people")
	}

	// The wall reaches the row by `holder.tenant`, and would not see the other
	// end: somebody of another tenant named here would be a row of this
	// tenant's saying something about a customer it cannot see.
	of, err := s.tenantOfHolder(ctx, req.GetHolder())
	if err != nil {
		return nil, err
	}
	with, err := s.tenantOfHolder(ctx, req.GetOther())
	if err != nil {
		return nil, err
	}
	if err := tenantsAgree("other", of, with); err != nil {
		return nil, err
	}

	return s.RelationServiceServer.Add(ctx, req)
}
