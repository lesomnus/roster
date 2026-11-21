package cmd

import (
	"fmt"
	"log/slog"
	"testing"

	"github.com/protobuf-orm/ent/dialect"
	"github.com/stretchr/testify/require"

	"github.com/lesomnus/payday/access"
	"github.com/lesomnus/payday/config"
	"github.com/lesomnus/payday/pdid"
	"github.com/lesomnus/payday/pdtest"

	"github.com/lesomnus/roster/internal/ent"
	entmigrate "github.com/lesomnus/roster/internal/ent/migrate"
	app "github.com/lesomnus/roster/rstr"
	"github.com/lesomnus/roster/server/pd"
)

// TestWhoseTenantAPageIsInIsOneLookup: where a message does not say which
// tenant its rows are in, the access record asks the database once for each
// kind of row it names, however many of them (#280).
//
// A row is filed under the tenant of whoever was read, and where the message
// leaves the tenant out that is read off the row ([filed]). Read a row at a
// time, a request that named a hundred people would be a hundred statements in
// front of its answer, on every call: it is one. And a message that said every
// tenant, which is what a read of whole rows answers, asks nothing.
//
// Written against the function with a client that counts its statements,
// since a served call makes statements of its own and what is asked here is
// how many this adds.
func TestWhoseTenantAPageIsInIsOneLookup(t *testing.T) {
	x := require.New(t)
	ctx := t.Context()

	drv, dsn := pdtest.DB(t)
	s, err := Build(ctx, Config{
		Db:    config.DbConfig{Driver: drv, Dsn: dsn},
		Watch: config.WatchConfig{Broker: config.BrokerMemory},
	})
	x.NoError(err)
	t.Cleanup(func() { s.Close() })
	x.NoError(entmigrate.NewSchema(s.Drv).Create(ctx))

	tenant, err := s.Ungated.Tenant().Add(ctx, app.TenantAddRequest_builder{Alias: "contoso"}.Build())
	x.NoError(err)

	const many = 100
	named := app.HolderListRequest_builder{}
	whole := app.HolderListResponse_builder{}
	var address *app.Email
	for i := range many {
		v, err := s.Ungated.Holder().Add(ctx, app.HolderAddRequest_builder{
			Tenant: app.TenantRef_builder{Id: tenant.GetId()}.Build(),
			Alias:  fmt.Sprintf("person-%03d", i),
		}.Build())
		x.NoError(err)

		named.Filters = append(named.Filters, app.HolderFilter_builder{Ref: app.HolderRef_builder{Id: v.GetId()}.Build()}.Build())
		whole.Items = append(whole.Items, v)
		if address == nil {
			address, err = s.Ungated.Email().Add(ctx, app.EmailAddRequest_builder{
				Holder: app.HolderRef_builder{Id: v.GetId()}.Build(), Address: "first@contoso.example",
			}.Build())
			x.NoError(err)
		}
	}

	asked := 0
	db := ent.NewClient(ent.Driver(dialect.Debug(s.Drv, func(...any) { asked++ })))
	quiet := slog.New(slog.DiscardHandler)

	t.Run("a hundred people named with no tenant", func(t *testing.T) {
		x := require.New(t)
		asked = 0

		vs := filed(db, quiet, pd.SubjectRefs)(named.Build())
		x.Len(vs, many)
		for _, v := range vs {
			x.Equal(tenant.GetId(), v.Tenant.Bytes())
		}
		x.Equal(1, asked, "a statement for each of them")
	})

	t.Run("and a statement for each kind of row among them", func(t *testing.T) {
		x := require.New(t)
		asked = 0

		// Somebody and an address, each named by its key and neither saying
		// its tenant, as a request does.
		vs := filed(db, quiet, pd.SubjectRefs)(app.EmailListRequest_builder{
			Filters: []*app.EmailFilter{
				app.EmailFilter_builder{Ref: app.EmailRef_builder{Id: address.GetId()}.Build()}.Build(),
				app.EmailFilter_builder{Holder: named.Filters[0].GetRef()}.Build(),
				app.EmailFilter_builder{Holder: named.Filters[1].GetRef()}.Build(),
			},
		}.Build())
		x.Len(vs, 3)
		for _, v := range vs {
			x.Equal(tenant.GetId(), v.Tenant.Bytes())
		}
		x.Equal(2, asked)
	})

	t.Run("and none where the message said every tenant", func(t *testing.T) {
		x := require.New(t)
		asked = 0

		vs := filed(db, quiet, pd.Subjects)(whole.Build())
		x.Len(vs, many)
		x.Zero(asked)
	})

	t.Run("and no more than a row names, of a request that names more", func(t *testing.T) {
		x := require.New(t)
		asked = 0

		// A request is read as it was sent, and one that is refused names
		// whatever its caller liked: the first thousand are looked for, five
		// hundred a statement, and the rest stay the caller's.
		much := app.HolderListRequest_builder{}
		for i := range 3 * access.MaxSubjects {
			who := named.Filters[i%many].GetRef()
			if i >= many {
				who = app.HolderRef_builder{Id: pdid.New(pd.HolderDomain).Bytes()}.Build()
			}
			much.Filters = append(much.Filters, app.HolderFilter_builder{Ref: who}.Build())
		}

		vs := filed(db, quiet, pd.SubjectRefs)(much.Build())
		x.Len(vs, 3*access.MaxSubjects)
		x.Equal(2, asked)
		for _, v := range vs[:many] {
			x.Equal(tenant.GetId(), v.Tenant.Bytes())
		}
	})

	t.Run("and one that names nobody stays as it was", func(t *testing.T) {
		x := require.New(t)

		nobody := pdid.New(pd.HolderDomain)
		vs := filed(db, quiet, pd.SubjectRefs)(app.HolderGetRequest_builder{
			Ref: app.HolderRef_builder{Id: nobody.Bytes()}.Build(),
		}.Build())
		x.Len(vs, 1)
		x.True(vs[0].Tenant.IsZero())
	})
}
