package cmd_test

import (
	"bytes"
	"compress/gzip"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/lesomnus/flob"
	"github.com/lesomnus/payday/pdid"
	"github.com/lesomnus/payday/pdtest"
	"github.com/lesomnus/payday/trail"
	"github.com/stretchr/testify/require"

	"github.com/lesomnus/roster/cli"
	"github.com/lesomnus/roster/cmd"
	entcredential "github.com/lesomnus/roster/internal/ent/credential"
)

// exitStatusCase is one invocation and the status the process ends with.
type exitStatusCase struct {
	what string
	c    *cmd.Config
	in   string // stdin, for a command that reads a secret there
	args []string
	want int
	says string // what the refusal says, so a case fails for the reason it is about
}

// exitStatusOf runs one invocation as `main` does, and answers the status
// the process would end with.
func exitStatusOf(t *testing.T, v exitStatusCase) int {
	t.Helper()

	_, _, err := pipedErr(t, v.in, cli.Cmd(v.c), v.args...)
	require.ErrorContains(t, err, v.says, "%v", v.args)

	return cli.Status(err)
}

// exitStatusPlanted is a deployment whose archive holds a chunk nobody wrote:
// what `trail verify` looks for.
func exitStatusPlanted(t *testing.T) *cmd.Config {
	t.Helper()
	x := require.New(t)

	dir := t.TempDir()
	var c cmd.Config
	b, ctx := build(t, func(cc *cmd.Config) {
		cc.Audit.Archive = dir
		cc.Audit.Retain = time.Nanosecond
		cc.Audit.Checkpoints.Key = pdtest.Key(t)
		c = *cc
	})
	b.holder(t, ctx, b.Contoso, "another")
	x.NoError(cli.Cmd(&c).Run(ctx, []string{"trail", "prune"}))

	a := flob.NewOsStores(dir)
	cs, err := trail.Chunks(ctx, a)
	x.NoError(err)
	i := slices.IndexFunc(cs, func(v trail.Chunk) bool { return v.Namespace == b.Contoso.String() })
	x.GreaterOrEqual(i, 0, "contoso has no chunk of its own")
	info, err := a.Use(cs[i].Namespace).Stat(ctx, cs[i].Digest)
	x.NoError(err)
	l, err := info.Labels(ctx)
	x.NoError(err)

	var buf bytes.Buffer
	z := gzip.NewWriter(&buf)
	_, err = z.Write([]byte(`{"id":"planted"}` + "\n"))
	x.NoError(err)
	x.NoError(z.Close())
	_, err = a.Use(cs[i].Namespace).Add(ctx, flob.Meta{Labels: l}, &buf)
	x.NoError(err)

	return &c
}

// exitStatusChanged is a deployment with a row changed past its servers: what
// `trail rows` looks for. And `without` is the same deployment with no key to
// check a row against, where the check cannot run.
func exitStatusChanged(t *testing.T) (with, without *cmd.Config) {
	t.Helper()

	var c cmd.Config
	b, ctx := build(t, func(cc *cmd.Config) {
		cc.Audit.Digest.Key = "base64:" + base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{7}, 32))
		c = *cc
	})
	h := b.holder(t, ctx, b.Contoso, "another")
	require.NoError(t, b.Ent.Holder.UpdateOneId(h.Uuid()).SetDateDisabled(time.Now()).Exec(ctx))

	none := c
	none.Audit.Digest.Key = ""

	return &c, &none
}

// exitStatusHydra is as much of a Hydra as `login doctor` reads, with one
// client this stack cannot sign anybody in through: it names no way to send
// its secret.
func exitStatusHydra(t *testing.T) string {
	t.Helper()

	client := map[string]any{
		"client_id":      "product",
		"grant_types":    []string{"authorization_code"},
		"response_types": []string{"code"},
		"scope":          "openid",
		"redirect_uris":  []string{"https://product.example/callback"},
	}
	m := http.NewServeMux()
	m.HandleFunc("GET /admin/clients", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("content-type", "application/json")
		_ = json.NewEncoder(w).Encode([]any{client})
	})
	m.HandleFunc("GET /admin/clients/product", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("content-type", "application/json")
		_ = json.NewEncoder(w).Encode(client)
	})
	hydra := httptest.NewServer(m)
	t.Cleanup(hydra.Close)

	return hydra.URL
}

// TestAnExitStatusSaysWhichKindOfFailure is the status a process ends with,
// by what went wrong.
//
// Every failure was 1: a mistyped flag, a refusal, roster not there, and a
// check that ran and found something. A cron job running `roster trail
// verify` could not tell a finding from a database that was not there, nor a
// script a wrong flag from a refusal, but by gRPC's words on standard error
// (#284). Right is 2 for an invocation refused by reading the command line
// alone, 3 for a check that ran to its end and did not say yes, and 1 for everything
// else, which is what `docs/usage/cli.md` § *Exit statuses* says.
func TestAnExitStatusSaysWhichKindOfFailure(t *testing.T) {
	const (
		verify = "/roster.VouchService/Verify"
		get    = "/roster.HolderService/Get"
	)
	b := cliUp(t, verify, get)
	unnamed := b.Local
	unnamed.Client.As = ""
	planted := exitStatusPlanted(t)
	changed, keyless := exitStatusChanged(t)
	doctored := b.Local
	doctored.Login.Hydra.Admin = exitStatusHydra(t)
	away := cmd.Config{Client: cmd.ClientConfig{Addr: "127.0.0.1:1", Insecure: true}}
	nodb := cmd.Config{}

	// Bob's password, locked: a verification of it answers *locked until*.
	sets(t, b, b.Bob.GetId(), "correct horse battery staple")
	bob, err := pdid.From(b.Bob.GetId())
	require.NoError(t, err)
	require.NoError(t, b.Server.Ent.Credential.Update().
		Where(entcredential.HolderIdEQ(bob.Uuid())).
		SetDateLocked(time.Now().Add(time.Hour)).Exec(t.Context()))

	// The operator's authenticator, enrolled and not yet proved.
	_, said, err := pipedErr(t, "", cli.Cmd(&b.Local), "control", "vouch", "enrol", "@admin")
	require.NoError(t, err, said)

	for _, v := range []exitStatusCase{
		// The invocation is wrong.
		{what: "an unknown command", c: &b.Local, args: []string{"nosuch"}, want: 2, says: "unknown subcommand"},
		{what: "an unknown flag", c: &b.Local, args: []string{"tenant", "list", "--nosuch"}, want: 2, says: "unknown flag"},
		{what: "an argument missing, by the parser", c: &b.Local, args: []string{"control", "key", "add", "--allow", get}, want: 2, says: "required argument not given"},
		{what: "an argument missing, by the command", c: &b.Local, args: []string{"restore"}, want: 2, says: "REF"},
		{what: "an argument too many", c: &b.Local, args: []string{"key", "list", "extra"}, want: 2, says: "too many arguments"},
		{what: "a flag's value that does not parse", c: &b.Local, args: []string{"key", "add", "--tenant", "newco", "--holder", "alice", "--allow", get, "--expires", "soon"}, want: 2, says: "--expires"},
		{what: "a method that is not one", c: &b.Local, args: []string{"key", "add", "--tenant", "newco", "--holder", "alice", "--allow", "HolderService.Get"}, want: 2, says: "not a method"},
		{what: "an -o that is none of its words", c: &b.Local, args: []string{"tenant", "list", "-o", "yaml"}, want: 2, says: "not a format"},
		{what: "two flags that do not go together", c: &b.Local, args: []string{"trail", "prune", "--older-than", "1h", "--discard", "--to", filepath.Join(t.TempDir(), "x")}, want: 2, says: "--discard and --to"},
		{what: "a group with nothing after it", c: &b.Local, args: []string{"key"}, want: 2, says: "subcommand is required"},
		{what: "no --as at a deployment with a control plane", c: &unnamed, args: []string{"holder", "list"}, want: 2, says: "--as: who is at this shell"},

		// A check ran to its end and found what it looks for.
		{what: "trail verify with a finding", c: planted, args: []string{"trail", "verify"}, want: 3, says: "finding(s)"},
		{what: "trail rows with a finding", c: changed, args: []string{"trail", "rows"}, want: 3, says: "finding(s)"},
		{what: "login doctor with a finding", c: &doctored, args: []string{"login", "doctor"}, want: 3, says: "cannot sign anybody in"},
		{what: "vouch verify answered locked", c: &b.Hers, in: "correct horse battery staple\n", args: []string{"vouch", "verify", "--secret-stdin", "@newco/bob"}, want: 3, says: "locked until"},
		{what: "control vouch prove answered no", c: &b.Local, in: "000000\n", args: []string{"control", "vouch", "prove", "--code-stdin", "@admin"}, want: 3, says: "no: that is not a code"},
		{what: "vouch verify answered no", c: &b.Hers, in: "not the password\n", args: []string{"vouch", "verify", "--secret-stdin", "@newco/alice"}, want: 3, says: "one no for every reason"},

		// Everything else.
		{what: "a refusal", c: &b.Local, args: []string{"holder", "get", "@newco/ghost"}, want: 1, says: "NotFound"},
		{what: "a name at --as that is nobody's", c: &b.Local, args: []string{"--as", "ghost", "holder", "list"}, want: 1, says: "no operator is called that"},
		{what: "roster not reachable", c: &away, args: []string{"me", "get"}, want: 1, says: "Unavailable"},
		{what: "a file that cannot be read", c: &b.Local, args: []string{"--config", filepath.Join(t.TempDir(), "none.yaml"), "tenant", "list"}, want: 1, says: "none.yaml"},
		{what: "a configuration that names no database", c: &nodb, args: []string{"key", "list"}, want: 1, says: "db.driver"},
		{what: "a check that could not run", c: keyless, args: []string{"trail", "rows"}, want: 1, says: "audit.digest.key"},
		{what: "a password on stdin that is not there", c: &b.Hers, args: []string{"vouch", "verify", "--secret-stdin", "@newco/alice"}, want: 1, says: "stdin was empty"},
	} {
		t.Run(v.what, func(t *testing.T) {
			require.Equal(t, v.want, exitStatusOf(t, v), "%v", v.args)
		})
	}

	// And nothing that went right is a failure.
	require.Equal(t, 0, cli.Status(nil))
}
