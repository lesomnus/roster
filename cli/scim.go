package cli

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/lesomnus/otx/log"
	"github.com/lesomnus/payday/access"
	"github.com/lesomnus/payday/pdid"
	"github.com/lesomnus/xli"
	"github.com/lesomnus/xli/cfg"
	"github.com/lesomnus/xli/flg"
	"github.com/lesomnus/z"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/lesomnus/roster/cmd"
	rstr "github.com/lesomnus/roster/rstr"
	"github.com/lesomnus/roster/scim"
	"github.com/lesomnus/roster/server/keys"
)

// NewCmdScim is `roster scim`: roster as a SCIM 2.0 endpoint, for a directory
// that provisions the people it signs in (`docs/scim.md`).
//
// `serve` is the endpoint as a process of its own, for `roster account`'s
// reason; `roster serve` opens the same thing when `scim:` names an address.
// `provision` is the operator's half: a tenant's directory, and the key it
// presents.
func NewCmdScim(l *cfg.Loader[cmd.Config], c *cmd.Config) *xli.Command {
	return &xli.Command{
		Name:     "scim",
		Brief:    "roster as a SCIM 2.0 endpoint, where a directory provisions its people",
		Commands: xli.Commands{newCmdScimServe(l, c), newCmdScimProvision(c), newCmdScimAdopt(c)},
	}
}

func newCmdScimServe(l *cfg.Loader[cmd.Config], c *cmd.Config) *xli.Command {
	return &xli.Command{
		Name:  "serve",
		Brief: "answer SCIM at /scim/v2, as the directory's tenant key",
		// Bound to `scim:`; see `cli/account.go`.
		Flags: flg.Flags{
			cfg.Bind(l, &c.Scim.Addr, &flg.String{Name: "listen", Brief: "where to speak HTTP; :8080 if empty"}),
			cfg.Bind(l, &c.Scim.Roster, &flg.String{Name: "roster", Brief: "roster's data plane, gRPC: host:port"}),
			cfg.Bind(l, &c.Scim.Insecure, &flg.Switch{Name: "insecure", Brief: "dial roster without TLS"}),
		},
		Handler: xli.OnRun(func(ctx context.Context, cl *xli.Command, next xli.Next) error {
			ctx, stop, err := telemetry(ctx, c, "roster-scim")
			if err != nil {
				return err
			}
			defer stop()

			// The block, with the flags bound above already in it.
			sc := c.Scim
			if sc.Roster == "" {
				return errors.New("--roster (or scim.roster): where roster speaks gRPC")
			}
			if sc.Addr == "" {
				sc.Addr = ":8080"
			}

			return serveScim(ctx, sc)
		}),
	}
}

// serveScim answers SCIM until ctx is done. Told a [cmd.ScimConfig] and nothing
// else, for the reason `serveAccount` is: two callers build it the same way.
func serveScim(ctx context.Context, sc cmd.ScimConfig) error {
	if err := sc.Meant(); err != nil {
		return err
	}
	s, err := scim.New(scim.Config{
		Roster:      sc.Roster,
		Insecure:    sc.Insecure,
		Concurrency: sc.Concurrency,
		Log:         log.From(ctx),
	})
	if err != nil {
		return err
	}
	defer s.Close()

	l, err := cmd.Listen(ctx, sc.Addr)
	if err != nil {
		return err
	}
	log.From(ctx).InfoContext(ctx, "scim", slog.String("addr", l.Addr().String()))

	// With the clocks the account app's and the Login App's listeners have:
	// how long a request's headers may take to arrive, how long a connection
	// may sit idle, and how much header is read. This listener faces a
	// directory's cloud, which is the internet, and had none -- a connection
	// left half way through a header was kept for ever. A body has a clock of
	// its own once its key is accepted (`scim.BodyTimeout`). Nothing bounds
	// the answer: a page of people is read from roster while it is written.
	srv := &http.Server{
		Handler:           s,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       2 * time.Minute,
		MaxHeaderBytes:    64 << 10,
	}
	go func() {
		<-ctx.Done()
		_ = srv.Close()
	}()
	if err := srv.Serve(l); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}

	return nil
}

// scimOf is `scim:` with the deployment's own data plane as its roster, for
// `roster serve` running it in the same process.
func scimOf(c *cmd.Config, l net.Listener) cmd.ScimConfig {
	sc := c.Scim
	if sc.Roster == "" {
		sc.Roster = l.Addr().String()
	}

	return sc
}

// ScimHolder is what a tenant's directory is called in it.
const ScimHolder = "scim"

func newCmdScimProvision(c *cmd.Config) *xli.Command {
	return &xli.Command{
		Name:  "provision",
		Brief: "let a tenant's directory provision its people through one connection, and mint the key it presents; printed once",
		Flags: flg.Flags{
			&flg.String{Name: "tenant", Brief: "the tenant, by alias"},
			&flg.String{Name: "connection", Brief: "the connection its people sign in through, by name; the directory links them there"},
		},
		Handler: xli.OnRun(func(ctx context.Context, cl *xli.Command, next xli.Next) error {
			tenant, _ := flg.Find[string](cl, "tenant")
			connection, _ := flg.Find[string](cl, "connection")
			if tenant == "" {
				return errors.New("--tenant: whose directory")
			}
			if connection == "" {
				return errors.New("--connection: which connection its people sign in through")
			}

			s, err := controlled(ctx, c)
			if err != nil {
				return err
			}
			defer s.Close()

			token, err := provisionScim(ctx, s, tenant, connection)
			if err != nil {
				return err
			}

			fmt.Fprintln(os.Stdout, token)
			fmt.Fprintf(os.Stderr, "roster: the key for %s's directory, allowing %d method(s). This is the only time it is shown; "+
				"running this again mints another and the one above stops working.\n", tenant, len(scim.Methods))

			return nil
		}),
	}
}

// provisionScim is [newCmdScimProvision] without the process.
//
// # What it writes
//
// The connection marked as the one the tenant provisions through, unless a
// file declares it -- then the file says so, or the next start would take it
// back. A holder for the directory, a role holding [scim.Bound], the binding,
// and a tenant key on that holder listing [scim.Methods]: replaced, never added
// to, so running it again is a rotation.
//
// A tenant key and not a deployment key: a directory is one tenant's, and a
// key that is one tenant by being minted in it cannot be pointed at another.
func provisionScim(ctx context.Context, s *cmd.Server, tenant, connection string) (string, error) {
	at := rstr.TenantRef_builder{Alias: z.Ptr(tenant)}.Build()
	t, err := s.Ungated.Tenant().Get(ctx, rstr.TenantGetRequest_builder{Ref: at}.Build())
	if err != nil {
		return "", fmt.Errorf("--tenant %s: %w", tenant, err)
	}
	at = rstr.TenantRef_builder{Id: t.GetId()}.Build()

	// Whose the rows of the directory's name are, before anything is written:
	// a person the tenant already calls `scim`, or a role of that name bound
	// to its people, is not the directory's to take. Nothing asked -- the
	// holder was found and handed the directory's role, and the key minted
	// below acted as them (#276). Refused here and not skipped, as the front
	// doors skip: this is one tenant, asked for by name at a terminal.
	found, err := holderFound(ctx, s, at, ScimHolder)
	if err != nil {
		return "", err
	}
	existing, err := roleNamed(ctx, s, at, ScimHolder)
	if err != nil {
		return "", err
	}
	why, err := somebodys(ctx, s, tenant, ScimHolder, found, existing)
	if err != nil {
		return "", err
	}
	if why != "" {
		return "", fmt.Errorf("--tenant %s: %s. rename them, or take what they hold away, and run this again", tenant, why)
	}

	conn, err := s.Ungated.Connection().Get(ctx, rstr.ConnectionGetRequest_builder{
		Ref: rstr.ConnectionRef_builder{At: rstr.ConnectionRefByAt_builder{
			Tenant: at, Name: z.Ptr(connection),
		}.Build()}.Build(),
		Select: rstr.ConnectionSelect_builder{All: z.Ptr(true)}.Build(),
	}.Build())
	if status.Code(err) == codes.NotFound {
		return "", fmt.Errorf("--connection %s: %s has no connection of that name", connection, tenant)
	}
	if err != nil {
		return "", err
	}
	if !conn.GetProvisions() {
		if conn.GetLabels()[cmd.Declared] != "" {
			return "", fmt.Errorf("--connection %s: a file declares it, so the file says it is the one the directory provisions "+
				"through -- `provisions: true` on it -- or the next start takes this back", connection)
		}
		if _, err := s.Ungated.Connection().Patch(ctx, rstr.ConnectionPatchRequest_builder{
			Ref:         rstr.ConnectionRef_builder{Id: conn.GetId()}.Build(),
			Provisions:  z.Ptr(true),
			DateUpdated: conn.GetDateUpdated(),
		}.Build()); err != nil {
			return "", fmt.Errorf("--connection %s: %w", connection, err)
		}
	}

	who, _, err := holderNamed(ctx, s, at, ScimHolder, nil)
	if err != nil {
		return "", err
	}
	// Not the writes that change a group: those the directory holds in each
	// group it makes, as its manager, and bound they would reach every group
	// of the tenant (`scim.InGroups`). The key below lists them all the same,
	// since a key's own list narrows what its holder holds.
	role, err := ensureRoleNamed(ctx, s, at, ScimHolder, scim.Bound(), nil)
	if err != nil {
		return "", err
	}
	if err := ensureBinding(ctx, s, role, who, nil); err != nil {
		return "", err
	}

	return mintNamed(ctx, s.Ungated, who, ScimHolder, scim.Methods, keys.PrefixTenant)
}

func newCmdScimAdopt(c *cmd.Config) *xli.Command {
	return &xli.Command{
		Name:  "adopt",
		Brief: "the people a tenant's directory matched and roster has not linked: listed, and linked with --confirm",
		Flags: flg.Flags{
			&flg.String{Name: "tenant", Brief: "the tenant, by alias"},
			&flg.String{Name: "confirm", Brief: "the tenant's alias, as it is, to say the list is believed; without it nothing is written"},
			&flg.String{Name: "of", Brief: "with --confirm, the digest `roster scim adopt` printed of the list that was read"},
		},
		Handler: xli.OnRun(func(ctx context.Context, cl *xli.Command, next xli.Next) error {
			tenant, _ := flg.Find[string](cl, "tenant")
			confirm, _ := flg.Find[string](cl, "confirm")
			of, _ := flg.Find[string](cl, "of")
			of = strings.TrimSpace(of)
			if tenant == "" {
				return errors.New("--tenant: whose directory")
			}
			// Which list is believed, before anything is read: a list read
			// again is one nobody has looked at yet.
			if confirm != "" && of == "" {
				return errors.New("--of: the digest `roster scim adopt` printed of the list that was read; nothing was written")
			}
			if of != "" && confirm == "" {
				return errors.New("--of: goes with --confirm")
			}
			if confirm != "" && confirm != tenant {
				return fmt.Errorf("--confirm %s: that is not %s; nothing was written", confirm, tenant)
			}

			s, err := controlled(ctx, c)
			if err != nil {
				return err
			}
			defer s.Close()

			by, err := atTheBox(ctx, c, s)
			if err != nil {
				return err
			}

			v, err := adoptScim(ctx, s, by, tenant, of, os.Stdout)
			if err != nil {
				return err
			}
			switch {
			case len(v.found) == 0:
				fmt.Fprintf(os.Stderr, "roster: %s's directory has matched nobody roster has not linked.\n", tenant)
			case of == "":
				fmt.Fprintf(os.Stderr, "roster: %d of %s's people are matched and not linked, so their directory cannot suspend them. "+
					"Nothing was written: each line is who roster has, their address, and what the directory calls them. "+
					"`--confirm %s --of %s` links them where you believe it.\n", len(v.found), tenant, tenant, v.digest)
			default:
				fmt.Fprintf(os.Stderr, "roster: linked %d of %d.\n", len(v.found)-v.failed, len(v.found))
			}
			if of == "" {
				// What `--of` names, as `roster trail rows` says its own.
				fmt.Fprintf(os.Stderr, "digest %s\n", v.digest)
			}
			if v.failed > 0 {
				return fmt.Errorf("%d could not be linked; each is said above", v.failed)
			}

			return nil
		}),
	}
}

// adopted is somebody a directory matched and roster has not linked.
type adopted struct {
	id      []byte
	alias   string
	address string
	subject string
}

// adoption is what one run of [adoptScim] found and did: the list, its
// digest, and how many of it could not be linked.
type adoption struct {
	found  []adopted
	digest string
	failed int
}

// adoptScim is [newCmdScimAdopt] without the process: everybody of the tenant
// whose directory said what it calls them (`Holder.directory_subject`) and who
// has no identity at the connection it provisions through, written to `out` a
// line each -- and, when `of` is the digest of that list, linked there.
//
// # Why this is a command at the box, and not something a directory does
//
// The link is an `Identity`: who a sign-in through that connection is. The
// directory's key is not given that write, because it would reach people who
// already exist (`HolderService.Provision`), so what the directory said was
// kept and believed by nothing (`HolderService.Tell`). Believing it is a
// decision about a way into somebody's account, and that is the deployment's
// to make, looking at the list: an address beside an id, where one that does
// not belong together is seen before it is written.
//
// # What is believed is the list that was read
//
// The list is read again by the run that writes, and a directory may have
// said something in between: somebody new on it, or an address or an id that
// moved. So the run that writes is told which list was read, by its digest
// ([listDigest]), and writes nothing when the list it reads now is another --
// it prints that one, and its digest, for somebody to read. The whole list or
// none of it: a line cannot be left out.
//
// Each link is the write `roster identity add` makes, through the same rules
// -- a subject somebody else signs in by is refused, and said -- and what was
// kept is taken away once it is how roster knows them.
//
// # On the access record, before anything is printed
//
// The list is each person's alias, their address and what their directory
// calls them, read past the servers and printed as text -- so it says whose it
// showed (`access.Saw`) in a call of `HolderService/List` made at the shell by
// `by`, which is the RPC it stands for, and prints once that row is written
// (#221, [cmd.ListKeys]). Each link, and each `Tell` that takes away what was
// kept, is a call of its own RPC at the shell by `by` too: the write's row
// `roster --as … identity add` leaves, naming the operator, where the bare
// context left none. Nothing is said of them with `access.Saw`, since a write
// shows nothing: the write's own row names what it wrote, and no read's row
// stands beside it.
func adoptScim(ctx context.Context, s *cmd.Server, by cmd.Operator, tenant string, of string, out io.Writer) (adoption, error) {
	at := rstr.TenantRef_builder{Alias: z.Ptr(tenant)}.Build()
	t, err := s.Ungated.Tenant().Get(ctx, rstr.TenantGetRequest_builder{Ref: at}.Build())
	if err != nil {
		return adoption{}, fmt.Errorf("--tenant %s: %w", tenant, err)
	}
	at = rstr.TenantRef_builder{Id: t.GetId()}.Build()

	provider := ""
	for after := ""; ; {
		vs, err := s.Ungated.Connection().List(ctx, rstr.ConnectionListRequest_builder{
			Filters: []*rstr.ConnectionFilter{rstr.ConnectionFilter_builder{Tenant: at}.Build()},
			After:   after,
		}.Build())
		if err != nil {
			return adoption{}, err
		}
		for _, v := range vs.GetItems() {
			if v.GetProvisions() {
				provider = v.GetName()
			}
		}
		if after = vs.GetNext(); after == "" {
			break
		}
	}
	if provider == "" {
		return adoption{}, fmt.Errorf("--tenant %s: no connection of it provisions its people, so no directory has matched anybody", tenant)
	}

	whose, err := pdid.From(t.GetId())
	if err != nil {
		return adoption{}, err
	}

	var found, apps []adopted
	if err := s.AtTheShell(ctx, by, rstr.HolderService_List_FullMethodName, func(ctx context.Context) error {
		var err error
		found, apps, err = matched(ctx, s, at, provider)
		if err != nil {
			return err
		}

		// The holders an app acts as are named below, so they are shown too.
		for _, v := range slices.Concat(found, apps) {
			who, err := pdid.From(v.id)
			if err != nil {
				return err
			}
			access.Saw(ctx, access.Subject{Id: who, Tenant: whose})
		}

		return nil
	}); err != nil {
		return adoption{}, err
	}

	r := adoption{found: found, digest: listDigest(t.GetId(), provider, found)}
	if of != "" && of != r.digest {
		for _, v := range found {
			fmt.Fprintf(out, "%s\t%s\t%s\n", v.alias, v.address, v.subject)
		}
		notPeople(apps)
		fmt.Fprintf(os.Stderr, "digest %s\n", r.digest)

		return r, fmt.Errorf("--of: the list read was %s, and it is now %s; nothing was written. "+
			"Read the list above, and `--confirm %s --of %s` links it where you believe it", of, r.digest, tenant, r.digest)
	}

	for _, v := range found {
		fmt.Fprintf(out, "%s\t%s\t%s\n", v.alias, v.address, v.subject)
		if of == "" {
			continue
		}

		// Each write a call of its RPC at the shell by `by`, as `roster --as …
		// identity add` is: a row of the access record naming who made it.
		ref := rstr.HolderRef_builder{Id: v.id}.Build()
		if err := s.AtTheShell(ctx, by, rstr.IdentityService_Add_FullMethodName, func(ctx context.Context) error {
			_, err := s.Ungated.Identity().Add(ctx, rstr.IdentityAddRequest_builder{
				Holder: ref, Provider: provider, Subject: v.subject,
			}.Build())

			return err
		}); err != nil {
			r.failed++
			fmt.Fprintf(os.Stderr, "roster: %s was not linked: %v\n", v.alias, err)

			continue
		}
		if err := s.AtTheShell(ctx, by, rstr.HolderService_Tell_FullMethodName, func(ctx context.Context) error {
			_, err := s.Ungated.Holder().Tell(ctx, rstr.HolderTellRequest_builder{
				Ref: ref, Subject: z.Ptr(""),
			}.Build())

			return err
		}); err != nil {
			return r, fmt.Errorf("%s is linked, and what the directory said of them was not taken away: %w", v.alias, err)
		}
	}
	notPeople(apps)

	return r, nil
}

// notPeople says, after the list, the holders an app acts as that a directory
// said an id for: left off it, since nobody signs in as them, and never
// linked. Said rather than dropped, since a directory that speaks of them has
// been told something an operator should know.
func notPeople(apps []adopted) {
	if len(apps) == 0 {
		return
	}

	aliases := make([]string, 0, len(apps))
	for _, v := range apps {
		aliases = append(aliases, v.alias)
	}
	fmt.Fprintf(os.Stderr, "roster: the directory said an id for %d holder(s) an app acts as, and nobody signs in as them, "+
		"so they are not on the list and are not linked: %s.\n", len(apps), strings.Join(aliases, ", "))
}

// listDigest is what names the list `adopt` printed of a tenant and the
// connection its directory provisions through: SHA-256, in hexadecimal, of the
// tenant's identifier, the connection's name, and of each line in the order
// printed the person's identifier, alias, address and what the directory
// calls them -- each followed by a zero byte. So a line added, taken away or
// moved, or an address or an id that changed, is another digest.
func listDigest(tenant []byte, provider string, found []adopted) string {
	h := sha256.New()
	field := func(b []byte) {
		h.Write(b)
		h.Write([]byte{0})
	}

	field(tenant)
	field([]byte(provider))
	for _, v := range found {
		field(v.id)
		field([]byte(v.alias))
		field([]byte(v.address))
		field([]byte(v.subject))
	}

	return hex.EncodeToString(h.Sum(nil))
}

// matched is everybody of the tenant `at` whose directory said what it calls
// them and who has no identity at `provider`, the connection it provisions
// through: the people, and apart from them the holders an app acts as.
//
// A holder a `Nomination` of the tenant names as the one an app acts as
// (`roster app install` writes it) is nobody who signs in, and an account at
// the connection would be a way in as the app. So it is not on the list,
// whatever the directory said of it, and is answered apart, to be said.
func matched(ctx context.Context, s *cmd.Server, at *rstr.TenantRef, provider string) ([]adopted, []adopted, error) {
	acts := map[string]bool{}
	for after := ""; ; {
		vs, err := s.Ungated.Nomination().List(ctx, rstr.NominationListRequest_builder{
			Filters: []*rstr.NominationFilter{rstr.NominationFilter_builder{Tenant: at}.Build()},
			After:   after,
		}.Build())
		if err != nil {
			return nil, nil, err
		}
		for _, v := range vs.GetItems() {
			acts[string(v.GetActsAs().GetId())] = true
		}
		if after = vs.GetNext(); after == "" {
			break
		}
	}

	var found, apps []adopted
	for after := ""; ; {
		vs, err := s.Ungated.Holder().List(ctx, rstr.HolderListRequest_builder{
			Filters: []*rstr.HolderFilter{rstr.HolderFilter_builder{Tenant: at}.Build()},
			After:   after,
		}.Build())
		if err != nil {
			return nil, nil, err
		}
		for _, h := range vs.GetItems() {
			if h.GetDirectorySubject() == "" {
				continue
			}
			if acts[string(h.GetId())] {
				apps = append(apps, adopted{id: h.GetId(), alias: h.GetAlias(), subject: h.GetDirectorySubject()})

				continue
			}
			ref := rstr.HolderRef_builder{Id: h.GetId()}.Build()

			ids, err := s.Ungated.Identity().List(ctx, rstr.IdentityListRequest_builder{
				Filters: []*rstr.IdentityFilter{rstr.IdentityFilter_builder{Holder: ref}.Build()},
			}.Build())
			if err != nil {
				return nil, nil, err
			}
			linked := false
			for _, v := range ids.GetItems() {
				linked = linked || v.GetProvider() == provider
			}
			if linked {
				// Linked since, and by another subject than the directory
				// said: a drift an operator reads in the admin console, and
				// not a link this would write over.
				continue
			}

			v := adopted{id: h.GetId(), alias: h.GetAlias(), subject: h.GetDirectorySubject()}
			es, err := s.Ungated.Email().List(ctx, rstr.EmailListRequest_builder{
				Filters: []*rstr.EmailFilter{rstr.EmailFilter_builder{Holder: ref}.Build()},
			}.Build())
			if err != nil {
				return nil, nil, err
			}
			if len(es.GetItems()) > 0 {
				v.address = es.GetItems()[0].GetAddress()
			}
			found = append(found, v)
		}
		if after = vs.GetNext(); after == "" {
			return found, apps, nil
		}
	}
}
