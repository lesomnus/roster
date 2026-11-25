package cmd_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
	"google.golang.org/protobuf/types/known/emptypb"

	"github.com/lesomnus/payday/config"
	"github.com/lesomnus/payday/pdid"
	"github.com/lesomnus/payday/pdtest"
	"github.com/lesomnus/payday/web"

	"github.com/lesomnus/roster/cmd"
	app "github.com/lesomnus/roster/rstr"
	"github.com/lesomnus/roster/server/prove"
)

// failureRow is one row of the manual's table of failures: the codes it says,
// in the order it says them, and the HTTP status of each.
type failureRow struct {
	failure  string
	codes    []codes.Code
	statuses []int
}

// failureCodeNamed is a gRPC code by the name the page writes it in.
func failureCodeNamed(name string) (codes.Code, bool) {
	for c := codes.OK; c <= codes.Unauthenticated; c++ {
		if c.String() == name {
			return c, true
		}
	}

	return 0, false
}

// failuresOnThePage reads the table of failures out of `docs/usage/rules.md`,
// and the row of it about an `Erase` apart: that row says which services
// answer which code, by name.
func failuresOnThePage(t *testing.T) ([]failureRow, map[string]codes.Code) {
	t.Helper()
	x := require.New(t)

	doc, err := os.ReadFile(filepath.Join("..", "docs", "usage", "rules.md"))
	x.NoError(err)

	_, section, ok := strings.Cut(string(doc), "\n## What a failure is answered with\n")
	x.True(ok, "docs/usage/rules.md has no section `What a failure is answered with`")
	section, _, _ = strings.Cut(section, "\n## ")

	ticked := regexp.MustCompile("`([^`]+)`")
	number := regexp.MustCompile(`\b[1-5]\d\d\b`)

	var rows []failureRow
	erase := map[string]codes.Code{}
	for _, line := range strings.Split(section, "\n") {
		if !strings.HasPrefix(line, "| ") || strings.HasPrefix(line, "| ---") || strings.HasPrefix(line, "| the failure ") {
			continue
		}
		cells := strings.Split(strings.Trim(line, "|"), " | ")
		x.Len(cells, 4, "a row of the table of failures is not four cells: %s", line)
		failure := strings.NewReplacer("*", "", "`", "").Replace(strings.TrimSpace(cells[0]))

		if strings.HasPrefix(failure, "an Erase of a row that is not there") {
			for _, part := range strings.Split(cells[1], ";") {
				var code codes.Code
				for i, m := range ticked.FindAllStringSubmatch(part, -1) {
					if i == 0 {
						c, ok := failureCodeNamed(m[1])
						x.True(ok, "the Erase row says %q where a code is", m[1])
						code = c
						continue
					}
					_, twice := erase[m[1]]
					x.False(twice, "the Erase row names %s twice", m[1])
					erase[m[1]] = code
				}
			}
			continue
		}

		row := failureRow{failure: failure}
		for _, m := range ticked.FindAllStringSubmatch(cells[1], -1) {
			if c, ok := failureCodeNamed(m[1]); ok {
				row.codes = append(row.codes, c)
			}
		}
		for _, m := range number.FindAllString(cells[2], -1) {
			n, err := strconv.Atoi(m)
			x.NoError(err)
			row.statuses = append(row.statuses, n)
		}
		x.NotEmpty(row.codes, "the row %q says no code", failure)
		x.Len(row.statuses, len(row.codes), "the row %q says %d codes and %d statuses", failure, len(row.codes), len(row.statuses))
		rows = append(rows, row)
	}
	x.NotEmpty(rows, "the table of failures has no rows, which is this test reading nothing")
	x.NotEmpty(erase, "the table of failures has no row about an Erase")

	return rows, erase
}

// failureCall is one wrong call, sent as somebody, and the row of the page it
// is an instance of.
type failureCall struct {
	row    string // the start of the row's first cell
	nth    int    // which of the row's codes, in the order the row says them
	as     pdid.Id
	method string
	req    string // the request, as JSON; the same bytes go over HTTP
}

// failureAt is a deployment served twice: over gRPC, and through the
// transcoder a page calls.
type failureAt struct {
	conn *grpc.ClientConn
	url  string
}

func failureServe(t *testing.T, b *built, c cmd.Config) failureAt {
	t.Helper()

	g, err := b.Grpc(t.Context(), c)
	require.NoError(t, err)
	h, err := web.New(config.HttpConfig{AllowWeb: true}, g)
	require.NoError(t, err)
	srv := httptest.NewServer(cmd.Arrived(h))
	t.Cleanup(srv.Close)

	return failureAt{conn: pdtest.Serve(t, g), url: srv.URL}
}

// failureMessages is the request and the answer of `method`, the request read
// from `req`; for a method no descriptor has, two empty messages.
func failureMessages(t *testing.T, method, req string) (proto.Message, proto.Message) {
	t.Helper()

	service, name, _ := strings.Cut(strings.TrimPrefix(method, "/"), "/")
	d, err := protoregistry.GlobalFiles.FindDescriptorByName(protoreflect.FullName(service))
	if err != nil {
		return &emptypb.Empty{}, &emptypb.Empty{}
	}
	m := d.(protoreflect.ServiceDescriptor).Methods().ByName(protoreflect.Name(name))
	if m == nil {
		return &emptypb.Empty{}, &emptypb.Empty{}
	}

	in, err := protoregistry.GlobalTypes.FindMessageByName(m.Input().FullName())
	require.NoError(t, err)
	out, err := protoregistry.GlobalTypes.FindMessageByName(m.Output().FullName())
	require.NoError(t, err)

	v := in.New().Interface()
	require.NoError(t, protojson.Unmarshal([]byte(req), v), "%s: %s", method, req)

	return v, out.New().Interface()
}

func (d failureAt) code(t *testing.T, c failureCall) codes.Code {
	t.Helper()

	ctx := t.Context()
	if c.as != pdid.Nil {
		ctx = asOverTheWire(ctx, c.as)
	}
	in, out := failureMessages(t, c.method, c.req)

	return status.Code(d.conn.Invoke(ctx, c.method, in, out))
}

func (d failureAt) status(t *testing.T, c failureCall) int {
	t.Helper()

	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, d.url+c.method, strings.NewReader(c.req))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Connect-Protocol-Version", "1")
	if c.as != pdid.Nil {
		req.Header.Set("Authorization", "Plain "+c.as.String())
	}

	res, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	res.Body.Close()

	return res.StatusCode
}

// failureRef is a reference by identifier, as JSON.
func failureRef(id pdid.Id) string {
	return fmt.Sprintf(`{"id":%q}`, base64.StdEncoding.EncodeToString(id.Bytes()))
}

// failureRows is one of each row the calls below name, in `tenant`: a person
// of `site`, their address, and a group, a role and a team in `site`.
type failureRows struct {
	site, holder, email, group, role, team pdid.Id
}

func failureRowsIn(t *testing.T, ctx context.Context, b *built, tenant pdid.Id, sfx string) failureRows {
	t.Helper()
	x := require.New(t)

	at := app.TenantRef_builder{Id: tenant.Bytes()}.Build()
	r := failureRows{site: b.site(t, ctx, tenant, "site-"+sfx), holder: b.holder(t, ctx, tenant, "h-"+sfx)}
	in := app.SiteRef_builder{Id: r.site.Bytes()}.Build()
	who := app.HolderRef_builder{Id: r.holder.Bytes()}.Build()

	b.joins(t, ctx, r.holder, r.site)

	e, err := b.Ungated.Email().Add(ctx, app.EmailAddRequest_builder{Holder: who, Address: sfx + "@failures.example.com"}.Build())
	x.NoError(err)
	r.email = mustId(t, e.GetId())

	g, err := b.Ungated.Group().Add(ctx, app.GroupAddRequest_builder{Tenant: at, Site: in, Alias: "group-" + sfx}.Build())
	x.NoError(err)
	r.group = mustId(t, g.GetId())

	l, err := b.Ungated.Role().Add(ctx, app.RoleAddRequest_builder{Tenant: at, Site: in, Alias: "role-" + sfx, Methods: []string{"/roster.HolderService/Get"}}.Build())
	x.NoError(err)
	r.role = mustId(t, l.GetId())

	m, err := b.Ungated.Team().Add(ctx, app.TeamAddRequest_builder{Tenant: at, Site: in, Alias: "team-" + sfx}.Build())
	x.NoError(err)
	r.team = mustId(t, m.GetId())

	return r
}

// failureReads are the reads and the writes of six services that name one row
// of `r`, each by its reference: what *nobody*, *another tenant's*, *erased*
// and *out of reach* are each asked of.
func failureReads(row string, as pdid.Id, r failureRows) []failureCall {
	return []failureCall{
		{row, 0, as, "/roster.HolderService/Get", `{"ref":` + failureRef(r.holder) + `}`},
		{row, 0, as, "/roster.EmailService/Get", `{"ref":` + failureRef(r.email) + `}`},
		{row, 0, as, "/roster.GroupService/Get", `{"ref":` + failureRef(r.group) + `}`},
		{row, 0, as, "/roster.RoleService/Get", `{"ref":` + failureRef(r.role) + `}`},
		{row, 0, as, "/roster.TeamService/Get", `{"ref":` + failureRef(r.team) + `}`},
		{row, 0, as, "/roster.HolderService/Disable", `{"ref":` + failureRef(r.holder) + `}`},
		{row, 0, as, "/roster.RoleService/Widen", `{"ref":` + failureRef(r.role) + `,"methods":["/roster.GroupService/Get"]}`},
	}
}

// TestTheManualsTableOfFailuresIsWhatIsAnswered holds the table of failures in
// `docs/usage/rules.md` to what roster answers, over gRPC and through the
// transcoder, so that the page and the server cannot part.
//
// The page had four lines about it: the wall is `NotFound`, the gate and the
// rules `PermissionDenied`, or `FailedPrecondition` for the state a write
// would leave. It did not say `Unauthenticated`, `InvalidArgument`,
// `AlreadyExists`, `Unimplemented`, a version that moved, or that `Unknown`
// and `Internal` are never roster refusing, and *a refusal at the wall is
// `NotFound`* was not so of an `Erase`, which answers `OK` in fourteen
// services. A client's author was left to learn the codes by sending wrong
// calls.
//
// Each row that a test can make is made here, for a representative verb of
// five or six services, and the code is the one the row says, in the order the
// row says them; the HTTP status is the row's too. A row a test cannot make
// (roster's own store away, a deadline) is named below
// with the reason, so that a row added to the page is a row this test is told
// about.
func TestTheManualsTableOfFailuresIsWhatIsAnswered(t *testing.T) {
	x := require.New(t)
	rows, erase := failuresOnThePage(t)

	b, ctx := build(t)
	at := failureServe(t, b, cmd.Config{})

	admin := b.holder(t, ctx, b.Contoso, "failures-admin")
	b.mayAnything(admin, b.Contoso)
	norole := b.holder(t, ctx, b.Contoso, "failures-norole")

	seoul := failureRowsIn(t, ctx, b, b.Contoso, "seoul")
	frankfurt := failureRowsIn(t, ctx, b, b.Contoso, "frankfurt")
	theirs := failureRowsIn(t, ctx, b, b.Fabrikam, "theirs")

	// Somebody who holds every method in Seoul, and nothing in Frankfurt.
	sited := b.holder(t, ctx, b.Contoso, "failures-sited")
	b.joins(t, ctx, sited, seoul.site)
	b.binds(t, sited, b.role(t, ctx, "failures-everything-in-seoul", "/roster.*/*"), &seoul.site)

	// A set erased, the rows that hang off others first.
	gone := failureRowsIn(t, ctx, b, b.Contoso, "gone")
	{
		_, err := b.Ungated.Team().Erase(ctx, app.TeamRef_builder{Id: gone.team.Bytes()}.Build())
		x.NoError(err)
		_, err = b.Ungated.Group().Erase(ctx, app.GroupRef_builder{Id: gone.group.Bytes()}.Build())
		x.NoError(err)
		_, err = b.Ungated.Role().Erase(ctx, app.RoleRef_builder{Id: gone.role.Bytes()}.Build())
		x.NoError(err)
		_, err = b.Ungated.Email().Erase(ctx, app.EmailRef_builder{Id: gone.email.Bytes()}.Build())
		x.NoError(err)
		_, err = b.Ungated.Holder().Erase(ctx, app.HolderRef_builder{Id: gone.holder.Bytes()}.Build())
		x.NoError(err)
	}

	nobody := failureRows{
		site: pdid.New(2), holder: pdid.New(2), email: pdid.New(2), group: pdid.New(2), role: pdid.New(2), team: pdid.New(2),
	}

	// Somebody stopped, whose credential then names nobody who may call.
	stopped := b.holder(t, ctx, b.Contoso, "failures-stopped")
	b.mayAnything(stopped, b.Contoso)
	_, err := b.Ungated.Holder().Disable(ctx, app.HolderDisableRequest_builder{Ref: app.HolderRef_builder{Id: stopped.Bytes()}.Build()}.Build())
	x.NoError(err)

	// A name taken already, and an account at a provider already held.
	b.site(t, ctx, b.Contoso, "failures-twice")
	b.identity(t, ctx, seoul.holder, "okta", "first")

	stale := `"dateUpdated":"2001-01-01T00:00:00Z"`
	contoso := failureRef(b.Contoso)

	var calls []failureCall
	calls = append(calls, failureReads("nobody by that reference", admin, nobody)...)
	calls = append(calls, failureReads("somebody of another tenant", admin, theirs)...)
	calls = append(calls, failureReads("somebody erased", admin, gone)...)
	calls = append(calls, failureReads("somebody out of the caller's reach in a site", sited, frankfurt)...)
	calls = append(calls,
		failureCall{"somebody out of the caller's reach in a site", 1, sited, "/roster.TenantService/Update", `{"ref":` + contoso + `,"desc":"said"}`},
		failureCall{"somebody out of the caller's reach in a site", 1, sited, "/roster.SiteService/Add", `{"tenant":` + contoso + `,"alias":"failures-sited-site"}`},
		failureCall{"somebody out of the caller's reach in a site", 1, sited, "/roster.GroupService/Update", `{"ref":` + failureRef(frankfurt.group) + `,"name":"named",` + stale + `}`},
		failureCall{"somebody out of the caller's reach in a site", 1, sited, "/roster.GroupExposureService/Add", `{"group":` + failureRef(seoul.group) + `,"client":"kube"}`},
		failureCall{"somebody out of the caller's reach in a site", 1, sited, "/roster.GroupExposureService/Erase", failureRef(pdid.New(2))},
		failureCall{"somebody out of the caller's reach in a site", 1, sited, "/roster.SiteMembershipService/Add", `{"holder":` + failureRef(seoul.holder) + `,"site":` + failureRef(seoul.site) + `}`},
		failureCall{"somebody out of the caller's reach in a site", 1, sited, "/roster.SiteMembershipService/Erase", failureRef(pdid.New(2))},
	)
	calls = append(calls, failureReads("no credential", pdid.Nil, seoul)...)
	calls = append(calls, failureReads("a credential that is none", pdid.New(2), seoul)...)
	calls = append(calls, failureReads("a credential that is none", stopped, seoul)...)
	calls = append(calls,
		failureCall{"a method the caller lacks", 0, norole, "/roster.GroupService/Get", `{"ref":` + failureRef(seoul.group) + `}`},
		failureCall{"a method the caller lacks", 0, norole, "/roster.RoleService/Get", `{"ref":` + failureRef(seoul.role) + `}`},
		failureCall{"a method the caller lacks", 0, norole, "/roster.SiteService/Get", `{"ref":` + failureRef(seoul.site) + `}`},
		failureCall{"a method the caller lacks", 0, norole, "/roster.TeamService/Get", `{"ref":` + failureRef(seoul.team) + `}`},
		failureCall{"a method the caller lacks", 0, norole, "/roster.HolderService/Disable", `{"ref":` + failureRef(seoul.holder) + `}`},
		failureCall{"a method the caller lacks", 0, norole, "/roster.TenantService/Update", `{"ref":` + contoso + `,"desc":"said"}`},
		failureCall{"a method the caller lacks", 1, norole, "/roster.HolderService/Get", `{"ref":` + failureRef(seoul.holder) + `}`},
		failureCall{"a method the caller lacks", 1, norole, "/roster.EmailService/Get", `{"ref":` + failureRef(seoul.email) + `}`},

		failureCall{"a row that exists already", 0, admin, "/roster.SiteService/Add", `{"tenant":` + contoso + `,"alias":"failures-twice"}`},
		failureCall{"a row that exists already", 0, admin, "/roster.HolderService/Add", `{"tenant":` + contoso + `,"alias":"h-seoul"}`},
		failureCall{"a row that exists already", 0, admin, "/roster.RoleService/Add", `{"tenant":` + contoso + `,"site":` + failureRef(seoul.site) + `,"alias":"role-seoul","methods":["/roster.HolderService/Get"]}`},
		failureCall{"a row that exists already", 0, admin, "/roster.IdentityService/Add", `{"holder":` + failureRef(seoul.holder) + `,"provider":"okta","subject":"second"}`},

		failureCall{"a request that cannot be taken as it is written", 0, admin, "/roster.RoleService/Widen", `{"ref":` + failureRef(seoul.role) + `}`},
		failureCall{"a request that cannot be taken as it is written", 0, admin, "/roster.HolderService/Update", `{"ref":` + failureRef(seoul.holder) + `,"profile":{}}`},
		failureCall{"a request that cannot be taken as it is written", 0, admin, "/roster.GroupService/Get", `{}`},

		failureCall{"one rule's refusal", 0, admin, "/roster.SiteService/Erase", failureRef(frankfurt.site)},
		failureCall{"one rule's refusal", 1, admin, "/roster.CredentialService/Set", `{"holder":` + failureRef(seoul.holder) + `,"kind":"password","secret":"` + base64.StdEncoding.EncodeToString([]byte("correct horse battery staple")) + `"}`},

		failureCall{"a version that moved", 0, admin, "/roster.HolderService/Update", `{"ref":` + failureRef(seoul.holder) + `,"profile":{},` + stale + `}`},
		failureCall{"a version that moved", 0, admin, "/roster.GroupService/Update", `{"ref":` + failureRef(seoul.group) + `,"name":"named",` + stale + `}`},
		failureCall{"a version that moved", 0, admin, "/roster.TenantService/Update", `{"ref":` + contoso + `,"desc":"said",` + stale + `}`},
		failureCall{"a version that moved", 0, admin, "/roster.HolderService/Disable", `{"ref":` + failureRef(seoul.holder) + `,` + stale + `}`},

		failureCall{"a general write", 0, admin, "/roster.HolderService/Patch", `{"ref":` + failureRef(seoul.holder) + `}`},
		failureCall{"a general write", 0, admin, "/roster.RoleService/Apply", `{}`},
		failureCall{"a general write", 1, norole, "/roster.HolderService/Patch", `{"ref":` + failureRef(seoul.holder) + `}`},

		failureCall{"a method nobody serves", 0, admin, "/roster.HolderService/Nope", `{}`},
		failureCall{"a method nobody serves", 0, admin, "/roster.NopeService/Get", `{}`},
		failureCall{"a method nobody serves", 0, pdid.Nil, "/roster.HolderService/Nope", `{}`},
	)

	made := map[string]bool{}
	for _, c := range calls {
		row := failureRowStarting(t, rows, c.row)
		made[row.failure] = true
		x.Less(c.nth, len(row.codes), "%q says %d codes, and a call is held to its %dth", row.failure, len(row.codes), c.nth+1)

		x.Equal(row.codes[c.nth].String(), at.code(t, c).String(), "%s, %s: the page says %s", row.failure, c.method, row.codes[c.nth])
		x.Equal(row.statuses[c.nth], at.status(t, c), "%s, %s over HTTP: the page says %d", row.failure, c.method, row.statuses[c.nth])
	}

	// A deployment that holds no key to wrap a seed with cannot hold a second
	// factor, and one that cannot ask DNS cannot check a name is a tenant's;
	// each says so as `Unimplemented`. (A name claimed where DNS is asked, with
	// no claim made first, is a rule's refusal about a row, and not this row.)
	{
		b, ctx := build(t, withoutKeyring, func(c *cmd.Config) { c.Host.Resolver = prove.None })
		at := failureServe(t, b, cmd.Config{})
		admin := b.holder(t, ctx, b.Contoso, "failures-admin")
		b.mayAnything(admin, b.Contoso)

		for _, c := range []failureCall{
			{"the deployment is not set up for it", 0, admin, "/roster.CredentialService/Enrol", `{"holder":` + failureRef(admin) + `,"kind":"totp","name":"phone"}`},
			{"the deployment is not set up for it", 0, admin, "/roster.HostService/Add", `{"tenant":` + failureRef(b.Contoso) + `,"name":"failures.example.com"}`},
		} {
			row := failureRowStarting(t, rows, c.row)
			made[row.failure] = true
			x.Equal(row.codes[c.nth].String(), at.code(t, c).String(), "%s, %s", row.failure, c.method)
			x.Equal(row.statuses[c.nth], at.status(t, c), "%s, %s over HTTP", row.failure, c.method)
		}
	}

	// An `Erase` of a row that is not there, of every service whose `Erase`
	// is served: the code of the part of the row that names the service.
	served := 0
	for svc, info := range b.grpc(t).GetServiceInfo() {
		entity, ok := strings.CutPrefix(svc, "roster.")
		if !ok {
			continue
		}
		entity = strings.TrimSuffix(entity, "Service")
		for _, m := range info.Methods {
			if m.Name != "Erase" {
				continue
			}
			c := failureCall{"an Erase", 0, admin, "/" + svc + "/Erase", failureRef(pdid.New(2))}
			got := at.code(t, c)
			if got == codes.Unimplemented {
				// Shut, or refused by a layer: an `Erase` no caller is
				// served, which is not this row's.
				_, named := erase[entity]
				x.False(named, "the Erase row names %s, whose Erase is not served", entity)
				continue
			}
			served++

			want, named := erase[entity]
			x.True(named, "the Erase row does not name %s, whose Erase answers %s", entity, got)
			x.Equal(want.String(), got.String(), "%s: an Erase of a row that is not there", c.method)
		}
	}
	x.Len(erase, served, "the Erase row names a service no port serves an Erase of")

	// A deployment that allows one call and then one every quarter of an hour.
	{
		at := failureServe(t, b, cmd.Config{Server: cmd.ServerConfig{ServerConfig: config.ServerConfig{Limit: config.LimitConfig{Rate: 0.001, Burst: 1}}}})
		c := failureCall{"too many calls", 0, admin, "/roster.GroupService/Get", `{"ref":` + failureRef(seoul.group) + `}`}
		x.Equal(codes.OK.String(), at.code(t, c).String(), "the one call the burst allows")

		row := failureRowStarting(t, rows, c.row)
		made[row.failure] = true
		x.Equal(row.codes[c.nth].String(), at.code(t, c).String(), "%s, %s", row.failure, c.method)
		x.Equal(row.statuses[c.nth], at.status(t, c), "%s, %s over HTTP", row.failure, c.method)
	}

	// What the transcoder refuses before any server is asked, which has no
	// gRPC call to make: the status, and the code in the error's JSON. And a
	// method nobody serves, whose 404 was a line of text.
	for _, c := range []struct {
		row         string
		verb, path  string
		contentType string
		body        string
	}{
		{"a body that cannot be read as the request", http.MethodPost, "/roster.GroupService/Get", "application/json", "not json"},
		{"a body that cannot be read as the request", http.MethodPost, "/roster.GroupService/Get", "application/json", `{"ref":"a group"}`},
		// A protobuf body that does not parse, which grpc-go answered 500
		// `internal` in a sentence of its own that payday no longer reads
		// (#531): a field whose length runs past the end.
		{"a body that cannot be read as the request", http.MethodPost, "/roster.GroupService/Get", "application/proto", "\x0a\xff"},
		{"a body of a type the call does not take", http.MethodPost, "/roster.GroupService/Get", "text/plain", "{}"},
		{"a body larger than the server takes", http.MethodPost, "/roster.GroupService/Get", "application/json", `{"ref":{"id":"` + strings.Repeat("A", 8<<20) + `"}}`},
		{"an HTTP method a call does not take", http.MethodPut, "/roster.GroupService/Get", "application/json", "{}"},
		{"a method nobody serves", http.MethodPost, "/roster.HolderService/Nope", "application/json", "{}"},
		{"a method nobody serves", http.MethodPost, "/nothing", "application/json", "{}"},
	} {
		row := failureRowStarting(t, rows, c.row)
		made[row.failure] = true

		req, err := http.NewRequestWithContext(t.Context(), c.verb, at.url+c.path, strings.NewReader(c.body))
		x.NoError(err)
		req.Header.Set("Content-Type", c.contentType)
		req.Header.Set("Connect-Protocol-Version", "1")
		req.Header.Set("Authorization", "Plain "+admin.String())
		res, err := http.DefaultClient.Do(req)
		x.NoError(err)
		var said struct{ Code, Message string }
		x.NoError(json.NewDecoder(res.Body).Decode(&said), "%s %s: the answer is not the error's JSON", c.verb, c.path)
		res.Body.Close()

		x.Equal(row.statuses[0], res.StatusCode, "%s, %s %s over HTTP: the page says %d: %s %s", row.failure, c.verb, c.path, row.statuses[0], said.Code, said.Message)
		x.Equal(connectCodeOf(row.codes[0]), said.Code, "%s, %s %s: %s", row.failure, c.verb, c.path, said.Message)
		x.NotContains(said.Message, "grpc:", "%s: gRPC's own words", row.failure)
	}

	// A deployment's hook refusing a write, and not there to answer one
	// (`cmd/hook.go`).
	{
		_, refusing := hookListening(t, func(context.Context, *app.WriteReview) (*app.WriteVerdict, error) {
			return app.WriteVerdict_builder{Refused: true, Why: "no"}.Build(), nil
		})
		closed, err := net.Listen("tcp", "127.0.0.1:0")
		x.NoError(err)
		nobody := closed.Addr().String()
		x.NoError(closed.Close())

		for row, addr := range map[string]string{
			"a deployment's hook refusing":      refusing,
			"a deployment's hook not answering": nobody,
		} {
			b, ctx := build(t, hookAt(addr, app.GroupMembershipService_Add_FullMethodName))
			at := failureServe(t, b, cmd.Config{})
			admin := b.holder(t, ctx, b.Contoso, "failures-admin")
			b.mayAnything(admin, b.Contoso)
			group := hookGroup(t, ctx, b.Server, b.Contoso, "failures")

			c := failureCall{row, 0, admin, app.GroupMembershipService_Add_FullMethodName,
				`{"group":` + failureRef(group) + `,"holder":` + failureRef(admin) + `}`}
			r := failureRowStarting(t, rows, c.row)
			made[r.failure] = true
			x.Equal(r.codes[c.nth].String(), at.code(t, c).String(), "%s, %s", r.failure, c.method)
			x.Equal(r.statuses[c.nth], at.status(t, c), "%s, %s over HTTP", r.failure, c.method)
		}
	}

	// What a test does not make, and why. Each is a row of the page all the
	// same, and a row the page adds is one this list is told about.
	notMade := map[string]string{
		"a deadline":            "a test does not time anything",
		"roster itself failing": "the store taken away under a running server, which is not a refusal of anybody's",
		"an Erase of a row another row still names": "no Erase a caller is served deletes a row, so none meets a foreign key; " +
			"the database refusing one is TestAnAppsRowsKeepTheHolderItIsFrom",
	}
	for _, row := range rows {
		if made[row.failure] {
			continue
		}
		found := false
		for prefix := range notMade {
			if strings.HasPrefix(row.failure, prefix) {
				found = true
			}
		}
		x.True(found, "the page's row %q is neither made here nor said why not", row.failure)
	}
}

// connectCodeOf is `c` as the error's JSON names it: `invalid_argument`.
func connectCodeOf(c codes.Code) string {
	var b strings.Builder
	for i, r := range c.String() {
		if i > 0 && r >= 'A' && r <= 'Z' {
			b.WriteByte('_')
		}
		b.WriteRune(r)
	}

	return strings.ToLower(b.String())
}

// failureRowStarting is the row of the page whose first cell begins with
// `prefix`; there is one.
func failureRowStarting(t *testing.T, rows []failureRow, prefix string) failureRow {
	t.Helper()

	var found []failureRow
	for _, row := range rows {
		if strings.HasPrefix(row.failure, prefix) {
			found = append(found, row)
		}
	}
	require.Len(t, found, 1, "the page has %d rows beginning %q", len(found), prefix)

	return found[0]
}
