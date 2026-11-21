package cmd

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/goccy/go-yaml"
	"github.com/goccy/go-yaml/ast"
	"github.com/goccy/go-yaml/parser"
	"github.com/goccy/go-yaml/token"
	"github.com/lesomnus/otx/log"
	"github.com/lesomnus/protobuf-patch/patch"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"

	"github.com/lesomnus/payday/frame"
	"github.com/lesomnus/payday/pdid"

	app "github.com/lesomnus/roster/rstr"
	"github.com/lesomnus/roster/server/core"
)

// Declaring a deployment's rows in a file, and applying them at start.
//
// # What this is for
//
// A `Connection` is a customer's directory: an issuer, a client id, the scopes,
// and the reference to a secret roster stores and never reads. It is
// configuration by every test one can put to it -- it is written once, it is
// the same on every replica, and somebody who loses it rebuilds it from what
// they wrote down. What it was until now is a row somebody typed into a
// console, which is a deployment that cannot be stood up twice the same way.
//
// So the rows that are configuration are declared beside the configuration:
// `resources:` in `roster.yaml` names files, this reads them, and `serve`
// applies them before it serves. A file in a ConfigMap makes that GitOps
// without roster knowing what Kubernetes is -- editing it changes the hash,
// which replaces the pod, which applies it.
//
// # Three rules, and each of them is a decision
//
// **It adds and it updates. It never erases.** A resource dropped from a file
// leaves its row where it is. That is not laziness: `Connection.Update` exists
// because erase-and-add on a provider is a gap in service and, under a
// mistyped name, every identity through it orphaned silently. A reconciler that
// deleted what it no longer saw would do that on a bad merge. Removing a row is
// a person's act, with the admin console or the terminal.
//
// **It writes as somebody.** Every row carries `Audit.actor_id`, and a
// provisioner that wrote as nobody would leave a trail that says a row appeared.
// So there is a `provisioner` holder in the control plane -- made here, on
// first run -- and every write below is framed as them. It has no password and
// no key: it is not something that signs in, it is something the trail can
// name.
//
// **What it writes, it owns.** A row this applied carries [Declared], and
// `server/core` refuses a write to it from anybody else. The alternative is a
// console edit that survives until the next restart and then vanishes, which is
// worse than being told no -- see `server/core/declared.go`.
//
// # What is not here, and why
//
// `Holder`, `Credential`, `Identity` and `Email` are people and the ways into
// their accounts. A file that made those is a file that grants access to
// whoever can write it, and `server/core/escalate.go` is a whole document about
// not doing that by accident.
//
// `Role` and `Binding` are the same argument one step further out -- *a grant
// is any write that changes what the gate will answer for somebody* -- and they
// are a better idea than they look, because RBAC in git is reviewed RBAC. They
// are left out **for now** rather than refused: it is a decision to take on
// purpose and not one to arrive at because a provisioner happened to support
// them.
//
// `Tenant.config` **is** here, since #67: the two settings that decide how a
// tenant signs in are configuration by the same test a `Connection` passes,
// and a deployment that could declare the directory and not the switch that
// says whether passwords are a way in could not be stood up twice the same
// way. [Settings] is the one rule that makes it safe to declare.

// Declared is the label a provisioned row carries, and its value is the file
// that declared it.
//
// A label rather than a field, because it is true of every entity this touches
// and adding a column to four of them for it would be four migrations for one
// fact. The cost is `Holder.Profile`'s -- labels are where an app puts what the
// schema does not name, and this spends one of those names.
const Declared = "roster.declared"

// Provisioner is the control-plane holder every write below is framed as.
const Provisioner = "provisioner"

// Resource is one declared row: what kind, and its fields.
//
// Shaped like a Kubernetes object because that is what somebody writing one
// expects, and not because roster knows anything about Kubernetes. `kind` picks
// the entity and the rest is that entity's own vocabulary -- the same names its
// proto uses, so a person reading `docs/entity.md` can write one.
type Resource struct {
	Kind string `yaml:"kind"`

	// Tenant is the customer this hangs off, by alias. Every kind below but
	// `Tenant` needs one.
	Tenant string `yaml:"tenant"`

	// Alias is a `Tenant`'s; Name is what every other kind is called.
	Alias string `yaml:"alias"`
	Name  string `yaml:"name"`
	Desc  string `yaml:"desc"`

	// Connection.
	Issuer    string   `yaml:"issuer"`
	ClientId  string   `yaml:"client_id"`
	Scopes    []string `yaml:"scopes"`
	SecretRef string   `yaml:"secret_ref"`

	// Connection: which claim of the ID token is a person's subject, `sub`
	// when left out. `oid` for Entra; `Connection.subject_claim` says why.
	SubjectClaim string `yaml:"subject_claim"`

	// Connection: whether the tenant's directory provisions its people
	// through it (`docs/scim.md`). One connection a tenant at most.
	Provisions bool `yaml:"provisions"`

	// MailDomain: which of the tenant's connections an address at this domain
	// is routed to. Empty routes nowhere, as on `Add`.
	Routes string `yaml:"routes"`

	// Tenant: what it has decided about itself. See [Settings] for the one
	// rule that makes it declarable.
	Config *Settings `yaml:"config"`

	// From is the file this came out of, which is what the row's label says
	// so that a refusal can name it.
	From string `yaml:"-"`

	// Where in From it is, for a refusal to name.
	line, col int

	// nothing is a file that declares no row, handed on as one so that
	// [ApplyResources] says so: it is no row.
	nothing bool
}

// Settings is `Tenant.config` as a file declares it, and every field is a
// pointer for one reason: **a key absent from the file leaves the setting as
// it is.** `password` has presence in the schema on purpose -- *unset is yes*
// -- and a file that read an absent key as `false` would lock a tenant out of
// the one credential roster holds itself by not mentioning it. So the file is
// applied field by field over what the row already says, and `config:` left
// out altogether touches nothing.
//
// The two together are how a tenant signs in, and they are configuration by
// every test the kinds above pass: the front door is an origin the deployment
// runs and routes, and whether a password is a way in is one bool that
// `Vouch.Verify` enforces -- the security-relevant half, and the one worth
// reading in a diff.
type Settings struct {
	Password  *bool            `yaml:"password"`
	FrontDoor *string          `yaml:"front_door"`
	Profile   *ProfileSettings `yaml:"profile"`

	// Leave is whether the tenant's people may delete their own account
	// (`TenantConfig.leave`). Off unless a file says so, and a file that does
	// not mention it leaves it as it is.
	Leave *bool `yaml:"leave"`
}

// ProfileSettings is `config.profile`: whether a sign-in fills what a profile
// lacks, and whether from a Slack workspace rather than the directory. Field by
// field, like the rest of [Settings].
//
// The file is the one place a tenant's Slack reference is written from outside
// the deployment's own hands, and that is why it is here: it names one of the
// deployment's secrets, which the deployment mounts, and `server/core` refuses
// a new one from a tenant's own administrator (`tenant.ext.proto`).
type ProfileSettings struct {
	Fill  *bool   `yaml:"fill"`
	Slack *string `yaml:"slack"`
}

// Resources is a file of them.
type Resources struct {
	Resources []Resource `yaml:"resources"`
}

// Applied is what a run did, for a caller that wants to say so.
type Applied struct {
	Added   []string
	Changed []string
	Same    []string

	// Left is what a file declares of a tenant that is closed or destroyed,
	// or under one: not written, and not looked at. See [ApplyResources].
	Left []string

	// Nothing is the files that declare no row.
	Nothing []string
}

// ReadResources reads the files named, in order, and refuses what nothing
// reads.
//
// **Strictly, as the configuration file is read**: a key the file's top level
// does not hold, and a key the entry's kind does not read -- one another kind
// reads among them -- is refused by the file, where it is in it and its name.
// It was a plain `yaml.Unmarshal` into one struct for every kind, so a file
// whose one key was `resource:` declared nothing and said nothing, `clientId`
// was passed over and `client_id` then refused as empty, and `routes` on a
// `Host` was taken and dropped. A file is what a deployment is stood up from,
// and a word in it that is read as nothing is a row somebody will look for.
//
// And what a run would stop at half way is refused here, before anything is
// opened: a `kind` this does not apply ([checkResources]).
//
// A file that declares nothing is not refused -- one may be emptied on
// purpose -- and is handed on as one, for [ApplyResources] to say.
func ReadResources(paths []string) ([]Resource, error) {
	out := []Resource{}
	for _, p := range paths {
		rs, err := readResourceFile(p)
		if err != nil {
			return nil, fmt.Errorf("resources: %w", err)
		}
		if len(rs) == 0 {
			out = append(out, Resource{From: p, nothing: true})
		}
		out = append(out, rs...)
	}
	if err := checkResources(out); err != nil {
		return nil, err
	}

	return out, nil
}

// kindsRead is the keys each kind reads, as the file writes them; a key whose
// value is not nil is a block, and these are the keys it reads.
//
// [Resource] is one struct for the four, so what decodes into it cannot say
// which kind a key is of. This does, and is the one list of it: a key a kind
// is given here and not read by its apply would be one more word taken and
// dropped.
var kindsRead = map[string]keysRead{
	"Tenant": {
		"kind": nil, "alias": nil, "name": nil, "desc": nil,
		"config": {
			"password": nil, "front_door": nil, "leave": nil,
			"profile": {"fill": nil, "slack": nil},
		},
	},
	"Connection": {
		"kind": nil, "tenant": nil, "name": nil, "desc": nil,
		"issuer": nil, "client_id": nil, "scopes": nil, "secret_ref": nil, "subject_claim": nil, "provisions": nil,
	},
	"Host": {
		"kind": nil, "tenant": nil, "name": nil, "desc": nil,
	},
	"MailDomain": {
		"kind": nil, "tenant": nil, "name": nil, "desc": nil, "routes": nil,
	},
}

// kindsApplied is the kinds in the order a refusal lists them.
var kindsApplied = []string{"Tenant", "Connection", "Host", "MailDomain"}

type keysRead map[string]keysRead

func (k keysRead) String() string {
	return strings.Join(slices.Sorted(maps.Keys(k)), ", ")
}

// readResourceFile is one file's rows, each knowing where in the file it is.
func readResourceFile(p string) ([]Resource, error) {
	b, err := os.ReadFile(p)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", p, err)
	}
	f, err := parser.ParseBytes(b, 0)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", p, err)
	}

	at := func(n ast.Node, key string, why string, args ...any) error {
		pos := posOf(n)
		return fmt.Errorf("%s:%d:%d: %s: %s", p, pos.Line, pos.Column, key, fmt.Sprintf(why, args...))
	}

	var body ast.Node
	for i, d := range f.Docs {
		if d.Body == nil || isComment(d.Body) {
			continue
		}
		if body != nil {
			return nil, at(d.Body, fmt.Sprintf("document %d", i+1), "a resources file is one document; put its rows under the first one's resources")
		}
		body = d.Body
	}
	if body == nil {
		return nil, nil
	}

	top, ok := pairsOf(body)
	if !ok {
		return nil, at(body, "resources", "want a block whose one key is resources, not %s", body.Type())
	}
	var list ast.Node
	for _, kv := range top {
		k := keyOf(kv)
		if k != "resources" {
			return nil, at(kv.Key, k, "nothing reads this key; a resources file holds one, resources")
		}
		list = kv.Value
	}
	if list == nil || list.Type() == ast.NullType || isComment(list) {
		return nil, nil
	}
	seq, ok := list.(*ast.SequenceNode)
	if !ok {
		return nil, at(list, "resources", "want a list of rows, not %s", list.Type())
	}

	out := make([]Resource, 0, len(seq.Values))
	for i, n := range seq.Values {
		entry := fmt.Sprintf("resources[%d]", i)
		kvs, ok := pairsOf(n)
		if !ok {
			return nil, at(n, entry, "want a row -- kind and its keys -- not %s", n.Type())
		}

		kind := ""
		var kindAt ast.Node
		for _, kv := range kvs {
			if keyOf(kv) == "kind" {
				kind, kindAt = strings.TrimSpace(kv.Value.String()), kv.Value
			}
		}
		if kindAt == nil || kind == "" {
			return nil, at(n, entry, "says no kind; one of %s", strings.Join(kindsApplied, ", "))
		}
		read, ok := kindsRead[kind]
		if !ok {
			// Named rather than ignored. A `kind` this does not know is a typo
			// or a thing somebody expected to work, and either way a silent
			// skip is the worst answer -- and refused here, it is refused
			// before anything is written.
			return nil, at(kindAt, entry+".kind", "%q is not a kind this applies; one of %s", kind, strings.Join(kindsApplied, ", "))
		}
		if err := readOnly(at, kvs, entry, "a "+kind, read); err != nil {
			return nil, err
		}

		r := Resource{}
		if err := yaml.NodeToValue(n, &r); err != nil {
			return nil, at(n, entry, "%s", yaml.FormatError(err, false, false))
		}

		// Where the file is read, with its name, before anything is opened:
		// the server refuses it too, without the file.
		if err := core.Referenced(r.SecretRef); err != nil {
			for _, kv := range kvs {
				if keyOf(kv) == "secret_ref" {
					return nil, at(kv.Key, entry+".secret_ref", "%s", err.Error())
				}
			}
		}
		pos := posOf(n)
		r.From, r.line, r.col = p, pos.Line, pos.Column
		out = append(out, r)
	}

	return out, nil
}

// readOnly refuses a key `read` does not hold, under `key`, and looks into each
// block it does.
func readOnly(at func(ast.Node, string, string, ...any) error, kvs []*ast.MappingValueNode, key, whose string, read keysRead) error {
	for _, kv := range kvs {
		k := keyOf(kv)
		sub, ok := read[k]
		if !ok {
			return at(kv.Key, key+"."+k, "%s does not read this key; it reads %s", whose, read)
		}
		if sub == nil || kv.Value == nil || kv.Value.Type() == ast.NullType {
			continue
		}
		inner, ok := pairsOf(kv.Value)
		if !ok {
			return at(kv.Value, key+"."+k, "want a block of %s, not %s", sub, kv.Value.Type())
		}
		if err := readOnly(at, inner, key+"."+k, whose+"'s "+k, sub); err != nil {
			return err
		}
	}

	return nil
}

// pairsOf is a mapping's keys and values, whether it holds one or several: the
// parser hands a mapping of one key over as that one pair.
func pairsOf(n ast.Node) ([]*ast.MappingValueNode, bool) {
	switch v := n.(type) {
	case *ast.MappingNode:
		return v.Values, true
	case *ast.MappingValueNode:
		return []*ast.MappingValueNode{v}, true
	}

	return nil, false
}

// posOf is where a node begins as somebody reading the file sees it: a
// mapping at its first key, where the parser's own token is the colon after it.
func posOf(n ast.Node) *token.Position {
	if kvs, ok := pairsOf(n); ok && len(kvs) > 0 {
		return kvs[0].Key.GetToken().Position
	}

	return n.GetToken().Position
}

func keyOf(kv *ast.MappingValueNode) string {
	if v, ok := kv.Key.(*ast.StringNode); ok {
		return v.Value
	}

	return strings.TrimSpace(kv.Key.String())
}

func isComment(n ast.Node) bool {
	_, ok := n.(*ast.CommentGroupNode)
	return ok || n.Type() == ast.CommentType
}

// checkResources refuses, before anything is written, what a run would stop
// at half way or apply twice: a kind this does not apply, and two entries
// that declare one row. A file has been refused a kind already as it was
// read, with the line; this is for a caller that hands rows in too.
//
// Two entries of one row were applied in turn, the second over the first: `+`
// and then `~`, and `~` twice at every start after, with a row that is
// whichever came last. Refused naming both, since which is meant is the
// person's to say.
func checkResources(rs []Resource) error {
	seen := map[string]Resource{}
	for _, r := range rs {
		if r.nothing {
			continue
		}
		if _, ok := kindsRead[r.Kind]; !ok {
			return fmt.Errorf("resources: %s: %q is not a kind this applies; one of %s",
				r.where(), r.Kind, strings.Join(kindsApplied, ", "))
		}

		id := r.Kind + " " + rowOf(r)
		if rowOf(r) == "" {
			// No row to be the same as: the kind's own refusal says what
			// is missing.
			continue
		}
		if was, ok := seen[id]; ok {
			return fmt.Errorf("resources: %s: %s %s: %s declares it too, and two entries declare one row; "+
				"take one of them out", r.where(), r.Kind, named(r), was.where())
		}
		seen[id] = r
	}

	return nil
}

// rowOf is what makes a row one row, by kind: the key its apply finds it by.
// A `Host`'s name is one row across the deployment, and every other kind but
// a `Tenant` is one within its tenant.
func rowOf(r Resource) string {
	switch r.Kind {
	case "Tenant":
		return r.Alias
	case "Host":
		return r.Name
	}
	if r.Tenant == "" || r.Name == "" {
		return ""
	}

	return r.Tenant + "/" + r.Name
}

// refused is a refusal of `r`, by where it is declared and what it is called
// -- not by an index across every file read, which is no line of any of them.
func (r Resource) refused(err error) error {
	return fmt.Errorf("resources: %s: %s %s: %w", r.where(), r.Kind, named(r), err)
}

// where is where a row is declared, for a refusal to name: its file and line,
// or its kind where a caller handed it in without a file.
func (r Resource) where() string {
	switch {
	case r.From == "":
		return r.Kind
	case r.line == 0:
		return r.From
	}

	return fmt.Sprintf("%s:%d:%d", r.From, r.line, r.col)
}

// ApplyResources writes what the files declared, as the provisioner.
//
// Idempotent by construction rather than by checking: each kind is `Add`, and
// on `AlreadyExists` a read for the version and a `Patch` over it. So a first
// run creates, a second with the same file changes nothing anybody can see, and
// one with a field edited writes that field.
//
// `dry` answers what it would do and writes nothing, which is what a person
// wants before a deployment does it for them.
//
// # A tenant that is leaving is left as it is
//
// A file finds its tenant by alias, and looked at nothing else of it. So a
// file that still declared a tenant somebody had closed and destroyed wrote it
// back at the next start -- its name, its settings, a connection and a host,
// onto a row with nobody in it that no sweep visits again -- and once the
// alias was given back, made a second tenant of that name and stopped at the
// host the first one held, which stops `serve` (#279). And a tenant closed at
// the box went on being edited by its file while everybody of it was shut
// out.
//
// A file does not close a tenant and does not bring one back: those are an
// operator's acts, at the box. So what a file declares of a tenant that is
// closed or destroyed -- the tenant, and every row it names that tenant for --
// is not written and not compared, and is answered as [Applied.Left]. It is
// said in the log, as a warning, each time the files are applied, with the
// files to take it out of: a start is not stopped by it, and nobody is left
// to find out a year on that the file still says so. A closed tenant that is
// restored is the file's again at the next start.
func ApplyResources(ctx context.Context, s *Server, rs []Resource, dry bool) (Applied, error) {
	out := Applied{}
	if err := checkResources(rs); err != nil {
		return out, err
	}

	// A file that declares nothing is said, once a run, which for `serve` is
	// once a start: it is not wrong -- a file may be emptied on purpose --
	// but one somebody expected a customer of is otherwise found out when the
	// customer is not there. It was said by nothing: `serve` logged its counts
	// only where a file declared something.
	declared := []Resource{}
	for _, r := range rs {
		if !r.nothing {
			declared = append(declared, r)
			continue
		}
		out.Nothing = append(out.Nothing, r.From)
		log.From(ctx).InfoContext(ctx, "resources: a file declares nothing", slog.String("file", r.From))
	}
	rs = declared
	if len(rs) == 0 {
		return out, nil
	}

	as, err := asProvisioner(ctx, s)
	if err != nil {
		return out, err
	}

	// The stack the **admin console** writes a customer through, and for its
	// reason: the provisioner is a holder of the control plane, so a layer
	// whose judgements read the data plane finds it holding nothing --
	// `Granted` looks for its bindings where they are not. That did not matter
	// while a declared tenant was one row; it does now that `Tenant.Add` writes
	// the role that administers it, and granting is refused for anything the
	// granter does not hold.
	at, err := Admin(s)
	if err != nil {
		return out, err
	}

	// Said however the run ends: a file that stops a start further down is
	// read by somebody who should be told this as well.
	leaving := leavers{}
	defer leaving.said(ctx)

	for _, r := range rs {
		// Every kind is one of these: [checkResources] refused any other
		// before anything was written.
		apply := map[string]func(context.Context, app.Server, Resource, bool) (string, string, error){
			"Tenant":     applyTenant,
			"Connection": applyConnection,
			"Host":       applyHost,
			"MailDomain": applyMailDomain,
		}[r.Kind]

		// Whose it is, before anything of it is read or written: a tenant
		// that is leaving is not the file's.
		left, err := leaving.left(as, at, r)
		if err != nil {
			return out, r.refused(err)
		}
		if left {
			out.Left = append(out.Left, named(r))

			continue
		}

		what, was, err := apply(as, at, r, dry)
		if err != nil {
			return out, r.refused(err)
		}

		switch was {
		case "added":
			out.Added = append(out.Added, what)
		case "changed":
			out.Changed = append(out.Changed, what)
		default:
			out.Same = append(out.Same, what)
		}
	}

	return out, nil
}

// named is what a resource is called in what a run answers, as each kind's
// own apply names it: a tenant by its alias, a connection by its tenant's and
// its own name, and a host or a mail domain by its name.
func named(r Resource) string {
	switch r.Kind {
	case "Tenant":
		return "@" + r.Alias
	case "Connection":
		return "@" + r.Tenant + "/" + r.Name
	}

	return r.Name
}

// leavers is the tenants a run found closed or destroyed, by alias, and what
// the files still declare of each: see [ApplyResources].
type leavers map[string]*leaver

type leaver struct {
	// state is `closed` or `destroyed`, and empty for a tenant that is
	// neither: open, or not there yet.
	state string
	since time.Time

	// What of it the files declare, and which files.
	left  []string
	files []string
}

// left is whether `r` is, or is under, a tenant that is closed or destroyed.
// Asked once of each tenant, through the stack the rest is written through.
func (l leavers) left(ctx context.Context, s app.Server, r Resource) (bool, error) {
	alias := r.Tenant
	if r.Kind == "Tenant" {
		alias = r.Alias
	}
	if alias == "" {
		// Nothing to ask about: the kind's own refusal says what is missing.
		return false, nil
	}

	v, ok := l[alias]
	if !ok {
		v = &leaver{}
		l[alias] = v

		got, err := s.Tenant().Get(ctx, app.TenantGetRequest_builder{
			Ref:    app.TenantRef_builder{Alias: proto.String(alias)}.Build(),
			Select: app.TenantSelect_builder{DateClosed: proto.Bool(true), DateDestroyed: proto.Bool(true)}.Build(),
		}.Build())
		switch {
		case status.Code(err) == codes.NotFound:
			// Nobody of that alias: a tenant the file makes, or a row whose
			// own kind says its tenant is not there.
		case err != nil:
			return false, err
		case got.GetDateDestroyed() != nil:
			v.state, v.since = "destroyed", got.GetDateDestroyed().AsTime()
		case got.GetDateClosed() != nil:
			v.state, v.since = "closed", got.GetDateClosed().AsTime()
		}
	}
	if v.state == "" {
		return false, nil
	}

	v.left = append(v.left, named(r))
	if from := r.From; from != "" && !slices.Contains(v.files, from) {
		v.files = append(v.files, from)
	}

	return true, nil
}

// said tells the log about each tenant the files still declare and that is
// leaving, in the order of their aliases: once a run, which for `serve` is
// once a start.
func (l leavers) said(ctx context.Context) {
	for _, alias := range slices.Sorted(maps.Keys(l)) {
		v := l[alias]
		if v.state == "" {
			continue
		}

		what := "resources: a file still declares a tenant that was destroyed. Nothing of it or under it is written, " +
			"and nothing brings it back: take it, and what is declared under it, out of the file"
		if v.state == "closed" {
			what = "resources: a file declares a tenant that is closed. It and what is declared under it are left as they are " +
				"until it is restored (`roster restore`); if it is to go, take them out of the file"
		}
		log.From(ctx).WarnContext(ctx, what,
			slog.String("tenant", "@"+alias),
			slog.String(v.state, v.since.Format(time.RFC3339)),
			slog.Any("files", v.files),
			slog.Any("left", v.left))
	}
}

// asProvisioner is the context every write goes out on: the control plane's
// `provisioner` holder, framed as the actor.
//
// Made here on first run rather than by `init`, because a deployment that has
// already been initialised -- which is every one this is useful to -- would
// otherwise need a second visit to get one.
//
// `frame.Everything` because this writes across every tenant and the wall is
// what it would otherwise be narrowed by. It is `Ungated` either way; what the
// frame buys is the **trail**, which now says which rows a file wrote.
func asProvisioner(ctx context.Context, s *Server) (context.Context, error) {
	if s.Control == nil {
		return nil, errors.New("resources: no control plane, and the provisioner is a holder in it")
	}

	// The same call `roster key add` makes for a service, which is what this
	// is: a row in the control plane's one tenant, made if it is not there.
	who, err := HolderNamed(ctx, s.Control, Provisioner)
	if err != nil {
		return nil, err
	}

	// And holding what it hands out, which is new since `Tenant.Add` began
	// writing the role that administers the tenant it makes: declaring a
	// customer is now granting `/roster.*/*` inside it, and `mayGrant` reads
	// what the **caller** holds. A provisioner bound to nothing could make no
	// tenant at all -- refused at the role, with the tenant rolled back.
	//
	// The control plane's own `everything`, which is the one `roster init`
	// binds its operator to, whichever of the two came first ([allow]). One
	// role and not a second of the provisioner's own, so it holds what an
	// operator holds and no more.
	if err := allowProvisioner(ctx, s.Control, who); err != nil {
		return nil, err
	}

	// `frame.Everything` because this writes across every tenant, and the wall
	// is what would otherwise narrow it. The server is `Ungated` either way;
	// what the frame buys is the **trail**, which now names which rows a file
	// wrote rather than saying they appeared -- and, since the tenant's settings
	// go through `Update`, the scope is also what `mayWriteDeclared` reads to
	// tell the file's own writer from a caller at a port. This said
	// `frame.Everything` for as long as it existed and passed no scope at all,
	// which nothing noticed while every kind was written with `Patch`.
	return frame.Into(ctx, frame.New(who, pdid.Nil, frame.Whole()).WithScope(frame.Everything)), nil
}

// labelsOf is what a declared row carries: the file it came from, so that a
// refusal from a port can say where the row is written. A row made by a Go
// call rather than a file -- a test, the sandbox -- carries its kind.
func labelsOf(r Resource) map[string]string {
	if r.From == "" {
		return map[string]string{Declared: r.Kind}
	}

	return map[string]string{Declared: r.From}
}

// The four kinds. Each is the same shape and the shape is the point: `Add`,
// and on `AlreadyExists` a read for the version and a `Patch` over it. So the
// first run creates, the second changes nothing, and one with a field edited
// writes that field.
//
// What none of them writes twice is the **name**. A `Connection.name` is what
// `Identity.provider` points at, a `Host.name` is what a tenant is resolved
// through and a `MailDomain.name` is what an address is routed by -- so a
// renamed one is a **different resource**, and the old row stays because this
// erases nothing. `Update` on each of these refuses the name for the same
// reason (`proto/ext/app/host_svc.ext.proto` and its neighbours); this is that
// rule arrived at from the other side.

func applyTenant(ctx context.Context, s app.Server, r Resource, dry bool) (string, string, error) {
	what := "@" + r.Alias
	if r.Alias == "" {
		return what, "", errors.New("alias")
	}

	got, err := s.Tenant().Get(ctx, app.TenantGetRequest_builder{
		Ref:    app.TenantRef_builder{Alias: proto.String(r.Alias)}.Build(),
		Select: app.TenantSelect_builder{All: proto.Bool(true)}.Build(),
	}.Build())
	if status.Code(err) == codes.NotFound {
		if dry {
			return what, "added", nil
		}
		add := app.TenantAddRequest_builder{
			Alias: r.Alias, Name: r.Name, Desc: r.Desc, Labels: labelsOf(r),
		}
		if r.Config != nil {
			add.Config = settle(nil, r.Config)
		}
		_, err = s.Tenant().Add(ctx, add.Build())

		return what, "added", err
	}
	if err != nil {
		return what, "", err
	}

	// The settings as they will be: what the row says, with the fields the
	// file mentions written over it. Compared against the row's own so that a
	// file mentioning nothing, or nothing new, is "same".
	next := settle(got.GetConfig(), r.Config)
	if got.GetName() == r.Name && got.GetDesc() == r.Desc && declared(got.GetLabels()) &&
		proto.Equal(next, settle(got.GetConfig(), nil)) {
		return what, "same", nil
	}
	if dry {
		return what, "changed", nil
	}

	// `Update` and not `Patch`, which the three below use: it is the verb the
	// consoles call, so the one rule about a front door -- an origin and
	// nothing more -- meets a file exactly as it meets a form.
	up := app.TenantUpdateRequest_builder{
		Ref:         app.TenantRef_builder{Id: got.GetId()}.Build(),
		Name:        proto.String(r.Name),
		Desc:        proto.String(r.Desc),
		Labels:      labelsOf(r),
		DateUpdated: got.GetDateUpdated(),
	}
	if r.Config != nil {
		up.Config = next
	}
	_, err = s.Tenant().Update(ctx, up.Build())

	return what, "changed", err
}

// settle is a tenant's settings with what a file said written over them,
// field by field, as a message of its own -- `Update` replaces the settings
// whole, so what it is handed has to carry the fields the file did not mention
// as they already are.
func settle(cur *app.TenantConfig, said *Settings) *app.TenantConfig {
	out := app.TenantConfig_builder{}.Build()
	if cur.HasPassword() {
		out.SetPassword(cur.GetPassword())
	}
	out.SetFrontDoor(cur.GetFrontDoor())
	out.SetLeave(cur.GetLeave())
	profile := app.TenantProfile_builder{
		Fill:           cur.GetProfile().GetFill(),
		SlackSecretRef: cur.GetProfile().GetSlackSecretRef(),
	}.Build()

	if said != nil {
		if said.Password != nil {
			out.SetPassword(*said.Password)
		}
		if said.FrontDoor != nil {
			out.SetFrontDoor(*said.FrontDoor)
		}
		if said.Leave != nil {
			out.SetLeave(*said.Leave)
		}
		if p := said.Profile; p != nil {
			if p.Fill != nil {
				profile.SetFill(*p.Fill)
			}
			if p.Slack != nil {
				profile.SetSlackSecretRef(*p.Slack)
			}
		}
	}

	// Absent rather than empty when there is nothing in it, so a tenant that
	// never mentioned a profile reads as one that never did.
	if profile.GetFill() || profile.GetSlackSecretRef() != "" {
		out.SetProfile(profile)
	}

	return out
}

func applyConnection(ctx context.Context, s app.Server, r Resource, dry bool) (string, string, error) {
	what := "@" + r.Tenant + "/" + r.Name
	at, err := tenantRef(ctx, s, r)
	if err != nil {
		return what, "", err
	}

	got, err := s.Connection().Get(ctx, app.ConnectionGetRequest_builder{
		Ref: app.ConnectionRef_builder{
			At: app.ConnectionRefByAt_builder{Tenant: at, Name: proto.String(r.Name)}.Build(),
		}.Build(),
		Select: app.ConnectionSelect_builder{All: proto.Bool(true)}.Build(),
	}.Build())
	if status.Code(err) == codes.NotFound {
		if dry {
			return what, "added", nil
		}
		_, err = s.Connection().Add(ctx, app.ConnectionAddRequest_builder{
			Tenant: at, Name: r.Name, Desc: r.Desc,
			Issuer: r.Issuer, ClientId: r.ClientId, Scopes: r.Scopes, SecretRef: r.SecretRef,
			SubjectClaim: r.SubjectClaim,
			Provisions:   r.Provisions,
			Labels:       labelsOf(r),
		}.Build())

		return what, "added", err
	}
	if err != nil {
		return what, "", err
	}
	if got.GetIssuer() == r.Issuer && got.GetClientId() == r.ClientId &&
		got.GetSecretRef() == r.SecretRef && got.GetDesc() == r.Desc &&
		got.GetSubjectClaim() == r.SubjectClaim && got.GetProvisions() == r.Provisions &&
		same(got.GetScopes(), r.Scopes) && declared(got.GetLabels()) {
		return what, "same", nil
	}
	if dry {
		return what, "changed", nil
	}

	v, err := s.Connection().Patch(ctx, app.ConnectionPatchRequest_builder{
		Ref:          app.ConnectionRef_builder{Id: got.GetId()}.Build(),
		Desc:         proto.String(r.Desc),
		Issuer:       proto.String(r.Issuer),
		ClientId:     proto.String(r.ClientId),
		Scopes:       r.Scopes,
		SecretRef:    proto.String(r.SecretRef),
		SubjectClaim: proto.String(r.SubjectClaim),
		Provisions:   proto.Bool(r.Provisions),
		Labels:       labelsOf(r),
		DateUpdated:  got.GetDateUpdated(),
	}.Build())
	if err != nil || len(r.Scopes) > 0 || len(got.GetScopes()) == 0 {
		return what, "changed", err
	}

	// Scopes left out are none, as every other key left out is empty. A list
	// has no presence in `Patch` -- empty there is *not said* -- so the row
	// kept the scopes a file once asked for, was found changed, and was
	// written again at every start. Emptied here, against the version the
	// patch left.
	doc, err := patch.New(string((&app.Connection{}).ProtoReflect().Descriptor().FullName()),
		patch.Target(patch.Name("date_updated")).Test(patch.Msg(
			patch.F(patch.Name("seconds"), patch.Int64(v.GetDateUpdated().GetSeconds())),
			patch.F(patch.Name("nanos"), patch.Int32(v.GetDateUpdated().GetNanos())),
		)),
		patch.Target(patch.Name("scopes")).Assign(patch.List()),
	)
	if err != nil {
		return what, "", err
	}
	_, err = s.Connection().Apply(ctx, app.ConnectionApplyRequest_builder{
		Ref:   app.ConnectionRef_builder{Id: got.GetId()}.Build(),
		Patch: doc,
	}.Build())

	return what, "changed", err
}

func applyHost(ctx context.Context, s app.Server, r Resource, dry bool) (string, string, error) {
	what := r.Name
	at, err := tenantRef(ctx, s, r)
	if err != nil {
		return what, "", err
	}

	got, err := s.Host().Get(ctx, app.HostGetRequest_builder{
		Ref:    app.HostRef_builder{Name: proto.String(r.Name)}.Build(),
		Select: app.HostSelect_builder{All: proto.Bool(true)}.Build(),
	}.Build())
	if status.Code(err) == codes.NotFound {
		if dry {
			return what, "added", nil
		}
		_, err = s.Host().Add(ctx, app.HostAddRequest_builder{
			Tenant: at, Name: r.Name, Desc: r.Desc, Labels: labelsOf(r),
		}.Build())

		return what, "added", err
	}
	if err != nil {
		return what, "", err
	}

	// Whose row it found, which it did not look at: a name is one row across
	// the deployment, so the row of that name may be another tenant's. A file
	// that said a name is contoso's, where it was fabrikam's, applied *changed*
	// with the name still fabrikam's -- and marked fabrikam's row as the
	// file's, which its own tenant then could not erase (#277). A file does not
	// take a name: it is refused, before a dry run says otherwise.
	theirs, err := s.Tenant().Get(ctx, app.TenantGetRequest_builder{Ref: at, Select: app.TenantSelect_builder{}.Build()}.Build())
	if err != nil {
		return what, "", err
	}
	if !bytes.Equal(got.GetTenant().GetId(), theirs.GetId()) {
		return what, "", fmt.Errorf("%s is held by another tenant, and a file does not take a name: "+
			"declared for %s here, and their row would have to be erased first", r.Name, r.Tenant)
	}

	if got.GetDesc() == r.Desc && declared(got.GetLabels()) {
		return what, "same", nil
	}
	if dry {
		return what, "changed", nil
	}

	_, err = s.Host().Patch(ctx, app.HostPatchRequest_builder{
		Ref:         app.HostRef_builder{Id: got.GetId()}.Build(),
		Desc:        proto.String(r.Desc),
		Labels:      labelsOf(r),
		DateUpdated: got.GetDateUpdated(),
	}.Build())

	return what, "changed", err
}

func applyMailDomain(ctx context.Context, s app.Server, r Resource, dry bool) (string, string, error) {
	what := r.Name
	at, err := tenantRef(ctx, s, r)
	if err != nil {
		return what, "", err
	}

	// Where an address at this domain is routed: one of the tenant's own
	// connections, **by name**, and empty routes nowhere -- which is what `Add`
	// already means by an empty one.
	ref := app.MailDomainRef_builder{
		At: app.MailDomainRefByAt_builder{Tenant: at, Name: proto.String(r.Name)}.Build(),
	}.Build()

	got, err := s.MailDomain().Get(ctx, app.MailDomainGetRequest_builder{
		Ref:    ref,
		Select: app.MailDomainSelect_builder{All: proto.Bool(true)}.Build(),
	}.Build())
	if status.Code(err) == codes.NotFound {
		if dry {
			return what, "added", nil
		}
		_, err = s.MailDomain().Add(ctx, app.MailDomainAddRequest_builder{
			Tenant: at, Name: r.Name, Desc: r.Desc, Provider: r.Routes, Labels: labelsOf(r),
		}.Build())

		return what, "added", err
	}
	if err != nil {
		return what, "", err
	}
	if got.GetProvider() == r.Routes && got.GetDesc() == r.Desc && declared(got.GetLabels()) {
		return what, "same", nil
	}
	if dry {
		return what, "changed", nil
	}

	_, err = s.MailDomain().Patch(ctx, app.MailDomainPatchRequest_builder{
		Ref:         app.MailDomainRef_builder{Id: got.GetId()}.Build(),
		Desc:        proto.String(r.Desc),
		Provider:    proto.String(r.Routes),
		Labels:      labelsOf(r),
		DateUpdated: got.GetDateUpdated(),
	}.Build())

	return what, "changed", err
}

// tenantRef is the customer a resource hangs off, and a refusal when it names
// none: everything but a `Tenant` is inside one, and a resource that forgot to
// say which would otherwise land wherever the first query looked.
func tenantRef(ctx context.Context, s app.Server, r Resource) (*app.TenantRef, error) {
	if r.Tenant == "" {
		return nil, errors.New("tenant")
	}
	if r.Name == "" {
		return nil, errors.New("name")
	}

	return app.TenantRef_builder{Alias: proto.String(r.Tenant)}.Build(), nil
}

// declared is whether a row already carries the label, so that a row somebody
// made by hand and then declared is **changed** rather than reported the same.
func declared(labels map[string]string) bool {
	_, ok := labels[Declared]

	return ok
}

func same(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}

	return true
}

// allowProvisioner has the provisioner hold the control plane's `everything`,
// and is a no-op on the second run.
//
// The role is [allow]'s to find or write, and it writes it on a control plane
// nobody has run `roster init` on -- which is not only a test or the sandbox:
// `deploy/` starts `serve` first, and its operator comes after (#268).
func allowProvisioner(ctx context.Context, s *Server, who pdid.Id) error {
	t, err := s.Ent.Tenant.Query().First(ctx)
	if err != nil {
		return fmt.Errorf("resources: the control plane has no owner: %w", err)
	}
	if err := allow(ctx, s.Ungated, pdid.Id(t.Id), who); err != nil {
		return fmt.Errorf("resources: what the provisioner holds: %w", err)
	}

	return nil
}
