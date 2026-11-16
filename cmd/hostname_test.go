package cmd_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strconv"
	"strings"
	"testing"

	"github.com/lesomnus/otx/log"
	"github.com/lesomnus/z"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"github.com/lesomnus/roster/cmd"
	app "github.com/lesomnus/roster/rstr"
	"github.com/lesomnus/roster/server/front"
	"github.com/lesomnus/roster/server/keys"
	"github.com/lesomnus/roster/server/prove"
)

// TestATrailingDotIsTheSameName: `name.` is `name`, to whatever writes one and
// to whatever reads one.
//
// # What it was
//
// `front.Hostname` lowered a name and took its port off, and left the dot a
// fully qualified name ends in. So `shared.example.com.` was already what it
// would be compared as, and `Host.Add` stored it beside `shared.example.com`
// as a second row.
//
// DNS reads the two as one name, and the lookup trims the dot before it asks.
// So one TXT record proved both, two tenants each held the name, and which of
// them a browser reached was decided by whether what it sent ended in a dot
// (#277).
func TestATrailingDotIsTheSameName(t *testing.T) {
	const name, dotted = "shared.example.com", "shared.example.com."

	x := require.New(t)
	published := map[string][]string{}
	b, ctx := build(t, zone(published))
	b.mayAnything(b.ContosoUser, b.Contoso)

	theirs := b.holder(t, ctx, b.Fabrikam, "someone")
	b.mayAnything(theirs, b.Fabrikam)
	fabrikam := app.TenantRef_builder{Id: b.Fabrikam.Bytes()}.Build()

	// contoso holds the name, and proved it.
	c := claiming(t, b, b.ContosoUser, b.Contoso, name)
	published[prove.Record+"."+name] = []string{c.GetToken()}
	_, err := adding(t, b, b.ContosoUser, b.Contoso, name)
	x.NoError(err)

	// Refused with the name it is, as every other spelling of one is: quoted,
	// because the plain form is a substring of the dotted one.
	refused := func(t *testing.T, err error) {
		t.Helper()

		require.Equal(t, codes.InvalidArgument, status.Code(err), "%v", err)
		require.Contains(t, status.Convert(err).Message(), strconv.Quote(name),
			"the refusal did not name the form it should have been written as")
	}

	t.Run("so it is not a second name to claim", func(t *testing.T) {
		_, err := b.Walled.HostProof().Add(b.as(ctx, theirs, b.Fabrikam), app.HostProofAddRequest_builder{
			Tenant: fabrikam, Name: dotted,
		}.Build())
		refused(t, err)
	})

	t.Run("nor a second row, by the tenant's road or the operator's", func(t *testing.T) {
		_, err := adding(t, b, theirs, b.Fabrikam, dotted)
		refused(t, err)

		_, err = b.Ungated.Host().Add(ctx, app.HostAddRequest_builder{Tenant: fabrikam, Name: dotted}.Build())
		refused(t, err)
	})

	t.Run("nor what a row is renamed to", func(t *testing.T) {
		x := require.New(t)

		v, err := b.Ungated.Host().Add(ctx, app.HostAddRequest_builder{
			Tenant: fabrikam, Name: "fabrikam.example.com",
		}.Build())
		x.NoError(err)

		_, err = b.Ungated.Host().Patch(ctx, app.HostPatchRequest_builder{
			Ref:              v.Ref(),
			Name:             z.Ptr(dotted),
			DateUpdatedForce: z.Ptr(true),
		}.Build())
		refused(t, err)
	})

	t.Run("and whatever reads a name reads the two as one", func(t *testing.T) {
		x := require.New(t)

		// A front door asking whose name a browser arrived at.
		for _, arrived := range []string{dotted, "Shared.Example.com.:443"} {
			res, err := front.New(b.Ungated).WhoseHost(ctx, app.FrontWhoseHostRequest_builder{Host: arrived}.Build())
			x.NoError(err, arrived)
			x.Equal(b.Contoso.Bytes(), res.GetTenant(), arrived)
		}

		// A request that arrived at roster's own door, and one that declares
		// where it arrived.
		x.Equal(name, cmd.ArrivedAt(metadata.NewIncomingContext(ctx, metadata.Pairs("x-forwarded-host", dotted))))
		x.Equal(name, cmd.ArrivedAt(metadata.NewIncomingContext(ctx, metadata.Pairs("host", dotted+":8443"))))

		at, ok := keys.ArrivedAt(metadata.Pairs(keys.HeaderAt, dotted))
		x.True(ok)
		x.Equal(name, at)
	})
}

// TestANameThatIsNotAHostnameIsRefused: a `Host` is named as DNS could hold
// the name, by every road that writes one.
//
// # What it was
//
// A name was held to one thing: being what `front.Hostname` answers for it,
// which lowers, trims and takes a port off. Whatever that leaves alone was a
// name. So a row could be called `*.example.com`, `a b`,
// `contoso.example.com/x` -- names no request arrives at, listed in a console
// as though one could -- and `@fabrikam`, which is how `roster-at` spells a
// tenant: `front.TenantMark` says a hostname cannot hold that character, and
// nothing held one to it (#277).
func TestANameThatIsNotAHostnameIsRefused(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t, zone(map[string][]string{}))
	b.mayAnything(b.ContosoUser, b.Contoso)

	at := app.TenantRef_builder{Id: b.Contoso.Bytes()}.Build()
	as := b.as(ctx, b.ContosoUser, b.Contoso)

	// A row to rename, which is the fourth road.
	held, err := b.Ungated.Host().Add(ctx, app.HostAddRequest_builder{Tenant: at, Name: "contoso.example.com"}.Build())
	x.NoError(err)

	roads := []struct {
		desc  string
		write func(name string) error
	}{
		{"an operator writing the row", func(name string) error {
			_, err := b.Ungated.Host().Add(ctx, app.HostAddRequest_builder{Tenant: at, Name: name}.Build())

			return err
		}},
		{"a tenant claiming it", func(name string) error {
			_, err := b.Walled.HostProof().Add(as, app.HostProofAddRequest_builder{Tenant: at, Name: name}.Build())

			return err
		}},
		{"a tenant taking it", func(name string) error {
			_, err := b.Walled.Host().Add(as, app.HostAddRequest_builder{Tenant: at, Name: name}.Build())

			return err
		}},
		{"a rename", func(name string) error {
			_, err := b.Ungated.Host().Patch(ctx, app.HostPatchRequest_builder{
				Ref:              held.Ref(),
				Name:             z.Ptr(name),
				DateUpdatedForce: z.Ptr(true),
			}.Build())

			return err
		}},
	}

	label := strings.Repeat("a", 63)

	for _, tc := range []struct{ desc, name, says string }{
		{"a wildcard", "*.example.com", "a letter, a digit or a hyphen"},
		{"a space", "a b.example.com", "a letter, a digit or a hyphen"},
		{"a path", "contoso.example.com/x", "a letter, a digit or a hyphen"},
		{"how `roster-at` spells a tenant", "@fabrikam", "a letter, a digit or a hyphen"},
		{"an underscore, which names a record and not a host", "_dmarc.example.com", "a letter, a digit or a hyphen"},
		{"a label that begins with a hyphen", "-contoso.example.com", "neither begins nor ends with a hyphen"},
		{"a label that ends with one", "contoso-.example.com", "neither begins nor ends with a hyphen"},
		{"a label with nothing in it", "contoso..example.com", "empty"},
		{"a dot in front", ".example.com", "empty"},
		{"two dots behind, which is one more than a whole name has", "contoso.example.com..", "empty"},
		{"a label of 64 bytes", label + "a.example.com", "63"},
		{"a name of 254", strings.Join([]string{label, label, label, label[:62]}, "."), "253"},

		// Not a different name so much as a different spelling: DNS holds the
		// A-label, a browser sends the A-label, and a row in the other form is
		// a row no request arrives as. The refusal says which to write.
		{"a name in letters DNS does not hold", "bücher.example", "A-label"},
	} {
		t.Run(tc.desc, func(t *testing.T) {
			for _, road := range roads {
				err := road.write(tc.name)
				require.Equal(t, codes.InvalidArgument, status.Code(err), "%s: %v", road.desc, err)

				msg := status.Convert(err).Message()
				require.Contains(t, msg, strconv.Quote(tc.name), road.desc)
				require.Contains(t, msg, "is not a hostname", road.desc)
				require.Contains(t, msg, tc.says, road.desc)
			}
		})
	}

	t.Run("and none of the refusals wrote anything", func(t *testing.T) {
		x := require.New(t)

		vs, err := b.Ungated.Host().List(ctx, app.HostListRequest_builder{
			Filters: []*app.HostFilter{app.HostFilter_builder{Tenant: at}.Build()},
		}.Build())
		x.NoError(err)
		x.Len(vs.GetItems(), 1, "a row was written under a name that cannot be one")
		x.Equal("contoso.example.com", vs.GetItems()[0].GetName(), "a row was renamed to one")

		cs, err := b.Walled.HostProof().List(as, app.HostProofListRequest_builder{}.Build())
		x.NoError(err)
		x.Empty(cs.GetItems(), "a claim was written for a name that cannot be one")
	})

	// And what was a name still is. Nothing here asks for a dot:
	// `scripts/e2e.sh` is reached at `localhost` and at an address, and a
	// deployment on one machine is a deployment.
	t.Run("and a hostname is not", func(t *testing.T) {
		for _, name := range []string{
			"localhost",
			"127.0.0.1",
			"::1",
			"2001:db8::7",
			"shop.contoso.example.com",
			"a.b-c.d0.example",
			"3d.example",

			// An A-label is letters, digits and hyphens like any other.
			"xn--bcher-kva.example",

			// The two limits, at them.
			label + ".example.com",
			strings.Join([]string{label, label, label, label[:61]}, "."),
		} {
			v, err := b.Ungated.Host().Add(ctx, app.HostAddRequest_builder{Tenant: at, Name: name}.Build())
			require.NoError(t, err, name)
			require.Equal(t, name, v.GetName())
		}
	})
}

// saying is a resolver that could not ask, in whichever words a test gives it.
type saying struct{ err error }

func (s *saying) TXT(context.Context, string) ([]string, error) { return nil, s.err }

// TestALookupThatFailedSaysNothingOfTheResolver: a claim whose lookup could
// not be made is refused in one sentence about the claimed name, and what the
// resolver said is in the log.
//
// # What it was
//
// `Host.Add` answered a lookup that failed with the error it had been handed,
// and that error is the resolver's own words: `lookup ns1.corp.internal on
// 127.0.0.11:53: no such host`. Which servers are asked is what the claimed
// name's zone lists, and a tenant writes its own zone. So a tenant that listed
// a name inside the deployment's network was told the resolver's address and
// whether that name exists there, one question to a claim (#277).
func TestALookupThatFailedSaysNothingOfTheResolver(t *testing.T) {
	const name = "inside.example.com"

	x := require.New(t)
	asks := &saying{}
	b, ctx := build(t, func(c *cmd.Config) { c.Host.Asks = asks })
	b.mayAnything(b.ContosoUser, b.Contoso)

	claiming(t, b, b.ContosoUser, b.Contoso, name)

	// The three ways a lookup fails, as `server/prove` words them: a zone's
	// server that is not there, one that did not answer, and a name no zone
	// could be found for.
	answers := map[string]bool{}
	for _, failure := range []string{
		"lookup ns1.corp.internal on 127.0.0.11:53: no such host",
		"lookup " + prove.Record + "." + name + " on 127.0.0.11:53: read udp 127.0.0.1:41708->10.0.0.53:53: i/o timeout",
		prove.Record + "." + name + ": nothing says which nameservers hold this name",
	} {
		asks.err = errors.New(failure)

		said := &bytes.Buffer{}
		logged := log.Into(ctx, slog.New(slog.NewJSONHandler(said, nil)))

		_, err := b.Walled.Host().Add(b.as(logged, b.ContosoUser, b.Contoso), app.HostAddRequest_builder{
			Tenant: app.TenantRef_builder{Id: b.Contoso.Bytes()}.Build(),
			Name:   name,
		}.Build())

		// Still *could not ask* and not *your record is wrong*, which is the
		// distinction the code is for.
		x.Equal(codes.Unavailable, status.Code(err), "%v", err)

		answer := status.Convert(err).Message()
		x.Contains(answer, name)
		for _, word := range []string{
			"127.0.0.11", "10.0.0.53", "corp.internal", "no such host", "i/o timeout", "nameservers",
		} {
			x.NotContains(answer, word, "the caller was told what the resolver said")
		}
		answers[answer] = true

		// And an operator can still read why.
		line := map[string]any{}
		x.NoError(json.Unmarshal(said.Bytes(), &line), "the log: %q", said.String())
		x.Equal("WARN", line["level"])
		x.Equal(name, line["name"])
		x.Equal(failure, line["err"])
	}

	x.Len(answers, 1, "what the caller is told differs with what failed, so it still says which: %v", answers)
}
