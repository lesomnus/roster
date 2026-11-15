package cmd_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/lesomnus/payday/pdid"

	app "github.com/lesomnus/roster/rstr"
	"github.com/lesomnus/roster/server/keys"
)

// TestAKeyIsReadByWhoseItIsAndByWhoeverIsToldTo: what keys somebody has -- what
// each is called, what it may call, when it was last used -- is read by them,
// and by whoever is told to read how everybody signs in
// (`HolderService/SignsIn`, across the tenant). Nobody else, and nobody by
// comparison.
//
// # What it was
//
// A read of a key asked the rule a **write** of a way in asks: the caller
// holds everything the key's holder holds (`mayReach`). And asked it only
// where the request named whose keys it wanted. So `Get` of an
// administrator's key was `403`, a `List` filtered to them was `403`, and a
// `List` that named nobody answered every key of the tenant (#274).
//
// The rule did not fit a read besides. It let two people who hold nothing read
// each other's, since nothing is a subset of nothing; and it kept an auditor,
// who holds the reads and nothing else, from reading an administrator's.
//
// So the question is not what somebody holds compared with the key's holder.
// It is whether the key is theirs, or they were told to read everybody's.
func TestAKeyIsReadByWhoseItIsAndByWhoeverIsToldTo(t *testing.T) {
	b, ctx := build(t)

	const (
		get   = app.ApiKeyService_Get_FullMethodName
		list  = app.ApiKeyService_List_FullMethodName
		erase = app.ApiKeyService_Erase_FullMethodName
	)

	// keyOf is a key of somebody's, as a row.
	keyOf := func(who pdid.Id, alias string) []byte {
		_, sum, err := keys.Mint(keys.PrefixTenant)
		require.NoError(t, err)
		v, err := b.Ungated.ApiKey().Add(ctx, app.ApiKeyAddRequest_builder{
			Holder: app.HolderRef_builder{Id: who.Bytes()}.Build(), Alias: alias, Secret: sum, Methods: []string{getHolder},
		}.Build())
		require.NoError(t, err)

		return v.GetId()
	}
	aliases := func(as context.Context, filters ...*app.ApiKeyFilter) ([]string, error) {
		vs, err := b.Walled.ApiKey().List(as, app.ApiKeyListRequest_builder{Filters: filters}.Build())
		if err != nil {
			return nil, err
		}

		var out []string
		for _, v := range vs.GetItems() {
			out = append(out, v.GetAlias())
		}

		return out, nil
	}
	of := func(who pdid.Id) *app.ApiKeyFilter {
		return app.ApiKeyFilter_builder{Holder: app.HolderRef_builder{Id: who.Bytes()}.Build()}.Build()
	}

	// The tenant's administrator.
	boss := b.holder(t, ctx, b.Contoso, "boss")
	b.as(ctx, boss, b.Contoso)
	bosss := keyOf(boss, "the-boss")

	// An auditor: the reads, and how anybody signs in, across the tenant --
	// and the erase, which the rule about a write still holds them to.
	auditor := b.holder(t, ctx, b.Contoso, "auditor")
	asAuditor := b.mayCall(t, ctx, auditor, "audits-keys", get, list, erase, app.HolderService_SignsIn_FullMethodName)
	keyOf(auditor, "the-auditor")

	// Somebody given the reads so as to see their own: across the tenant, as
	// a tenant gives them to everybody who mints their own keys, and not how
	// anybody else signs in.
	member := b.holder(t, ctx, b.Contoso, "member")
	asMember := b.mayCall(t, ctx, member, "own-keys", get, list)
	members := keyOf(member, "the-member")

	// And somebody who holds the same reads in one site, which is not being
	// told to read everybody's.
	east := b.site(t, ctx, b.Contoso, "east")
	role, err := b.Ungated.Role().Add(ctx, app.RoleAddRequest_builder{
		Tenant:  app.TenantRef_builder{Id: b.Contoso.Bytes()}.Build(),
		Site:    app.SiteRef_builder{Id: east.Bytes()}.Build(),
		Alias:   "east-keys",
		Methods: []string{get, list},
	}.Build())
	require.NoError(t, err)
	local := b.holder(t, ctx, b.Contoso, "local")
	_, err = b.Ungated.Binding().Add(ctx, app.BindingAddRequest_builder{
		Role:   app.RoleRef_builder{Id: role.GetId()}.Build(),
		Holder: app.HolderRef_builder{Id: local.Bytes()}.Build(),
		Site:   app.SiteRef_builder{Id: east.Bytes()}.Build(),
	}.Build())
	require.NoError(t, err)
	asLocal := b.asNobody(ctx, local, b.Contoso)
	locals := keyOf(local, "the-local")

	t.Run("somebody not told to reads their own, however they ask", func(t *testing.T) {
		x := require.New(t)

		got, err := aliases(asLocal)
		x.NoError(err)
		x.Equal([]string{"the-local"}, got, "a list that named nobody answered more than the caller's own")

		got, err = aliases(asLocal, of(local))
		x.NoError(err)
		x.Equal([]string{"the-local"}, got)

		_, err = b.Walled.ApiKey().Get(asLocal, app.ApiKeyGetRequest_builder{Ref: app.ApiKeyRef_builder{Id: locals}.Build()}.Build())
		x.NoError(err)
	})

	t.Run("nor does holding the read across the tenant make it everybody's", func(t *testing.T) {
		x := require.New(t)

		got, err := aliases(asMember)
		x.NoError(err)
		x.Equal([]string{"the-member"}, got, "everybody given their own keys to read was given everybody's")

		_, err = b.Walled.ApiKey().Get(asMember, app.ApiKeyGetRequest_builder{Ref: app.ApiKeyRef_builder{Id: members}.Build()}.Build())
		x.NoError(err)

		_, err = b.Walled.ApiKey().Get(asMember, app.ApiKeyGetRequest_builder{Ref: app.ApiKeyRef_builder{Id: bosss}.Build()}.Build())
		x.Equal(codes.PermissionDenied, status.Code(err), "%v", err)
	})

	t.Run("and nobody else's", func(t *testing.T) {
		x := require.New(t)

		// Somebody who is not of the site the read is held in is not there
		// for them at all (`cmd.People`).
		_, err := aliases(asLocal, of(boss))
		x.Equal(codes.NotFound, status.Code(err), "%v", err)
		_, err = b.Walled.ApiKey().Get(asLocal, app.ApiKeyGetRequest_builder{Ref: app.ApiKeyRef_builder{Id: bosss}.Build()}.Build())
		x.Equal(codes.NotFound, status.Code(err), "%v", err)

		// And somebody who is, is seen -- and their keys are still theirs.
		b.joins(t, ctx, boss, east)
		b.joins(t, ctx, auditor, east)

		_, err = aliases(asLocal, of(boss))
		x.Equal(codes.PermissionDenied, status.Code(err), "%v", err)

		// Nor somebody's who holds no more than they do: it is not a
		// comparison.
		_, err = aliases(asLocal, of(auditor))
		x.Equal(codes.PermissionDenied, status.Code(err), "%v", err)

		_, err = b.Walled.ApiKey().Get(asLocal, app.ApiKeyGetRequest_builder{Ref: app.ApiKeyRef_builder{Id: bosss}.Build()}.Build())
		x.Equal(codes.PermissionDenied, status.Code(err), "%v", err)
	})

	t.Run("whoever is told to reads everybody's, an administrator's among them", func(t *testing.T) {
		x := require.New(t)

		got, err := aliases(asAuditor)
		x.NoError(err)
		x.ElementsMatch([]string{"the-boss", "the-auditor", "the-local", "the-member"}, got)

		got, err = aliases(asAuditor, of(boss))
		x.NoError(err)
		x.Equal([]string{"the-boss"}, got)

		v, err := b.Walled.ApiKey().Get(asAuditor, app.ApiKeyGetRequest_builder{Ref: app.ApiKeyRef_builder{Id: bosss}.Build()}.Build())
		x.NoError(err, "an auditor was refused an administrator's key for holding less than they do")
		x.Empty(v.GetSecret())
	})

	// Reading is not ending. A key ended is a way in taken away, and that is
	// still asked what a write is.
	t.Run("and may not end one of somebody who holds more", func(t *testing.T) {
		x := require.New(t)

		_, err := b.Walled.ApiKey().Erase(asAuditor, app.ApiKeyRef_builder{Id: bosss}.Build())
		x.Equal(codes.PermissionDenied, status.Code(err), "%v", err)
	})
}
