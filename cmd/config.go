// Package cmd is this app's own wiring, and it is short on purpose.
//
// Everything that does not change from one app to the next is in payday. What
// is left is here, and it is deliberately **not** hidden behind a
// `payday.Serve(cfg)`: the stack, the order of the interceptors and which
// server the wall is on are the decisions a reader of an app most needs to be
// able to see, and a framework that hid them would be hiding the only part
// worth reading.
package cmd

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"maps"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"time"

	"github.com/lesomnus/payday/auth"
	"github.com/lesomnus/payday/config"
	"github.com/lesomnus/payday/pdid"
	"github.com/lesomnus/payday/rbac/rbacfile"

	"github.com/lesomnus/roster/server/core"
	"github.com/lesomnus/roster/server/pd"
	"github.com/lesomnus/roster/server/prove"
	// The two engines this app runs on, blank-imported here rather than by
	// payday so that an app does not carry one it never opens.
	//
	// Both, because both are used: `compose.yaml` runs it on PostgreSQL and a
	// test runs it on SQLite. Linking only the second made `docker compose up`
	// -- the quickstart `docs/operating.md` gives -- fail at
	// `unknown driver "pgx"`, which is a sentence about a name rather than
	// about a missing import and reads as a typo in the configuration.
	// And the broker that rides the first of them, so that
	// `watch.broker: postgres` is a name this binary has.
	//
	// The one thing that stopped roster running more than one replica was that
	// a client watching against one never heard about a write that landed on
	// another -- and the answer for a deployment already on PostgreSQL needs no
	// second piece of infrastructure. See `docs/operating.md`, "Running more
	// than one".
	//
	// Linked whatever this deployment runs on, like the drivers above: what it
	// costs is a `LISTEN` client in the binary, and what the other arrangement
	// costs is `watch.broker: postgres` reading as a typo.
	"github.com/lesomnus/payday/trail"
)

// Name is what this app is called, and it is the only place it is written.
// The environment prefix and the names of the configuration files are derived
// from it -- ROSTER_DB_DSN, roster.yaml -- by the loader `cli.Cmd` makes with
// it, so there is nothing to keep in step.
const Name = "roster"

// Config is what this app is configured with.
//
// The framework cannot own this struct, since what an app is configured with is
// the app's. What it owns is the pieces: each of these is a payday type, and
// what is written here is only which of them this app has. `server` holds one,
// payday's listener, with the page it serves beside it, as `admin` does.
type Config struct {
	Server ServerConfig       `yaml:"server"`
	Db     config.DbConfig    `yaml:"db"`
	Otel   config.OtelConfig  `yaml:"otel"`
	Watch  config.WatchConfig `yaml:"watch"`

	// Spin is which process of a deployment runs each background loop -- the
	// sweeps, the trail's retention, the outbox drain -- and it is named for
	// the reason `watch.broker` is: `local` runs every loop in this process,
	// which is right for one replica and silently wrong for two, and nothing
	// reports it. `postgres` holds each loop on an advisory lock on the
	// database the rows are already in (payday#54), for both planes.
	//
	// Read by `serve` and nothing else, so a command that only builds the
	// server has nothing to name.
	Spin config.SpinConfig `yaml:"spin"`

	// Resources are files declaring rows that are configuration: a customer,
	// the names it is reached at, the directories its people arrive through.
	// `serve` applies them before it serves.
	//
	// Empty is none, which is every deployment that has not asked for this and
	// leaves the admin console the only way those rows are made. See
	// `cmd/resources.go` for the three rules -- it adds and updates and never
	// erases, it writes as a `provisioner` the trail can name, and what it
	// writes it owns.
	Resources []string `yaml:"resources"`

	// Policy adds read-only, tenant-wide grants from a file. Its groups and
	// memberships belong to the file; database groups do not modify them.
	Policy rbacfile.Config `yaml:"policy"`

	// Control is who may call this deployment, and it is roster again -- a
	// second instance, in this process, on its own database. See
	// docs/position.md, 'Two planes, one schema'.
	//
	// Nothing written down is a deployment that believes its callers, which
	// `auth.Plain` announces once in the log. It is the default because an app
	// that cannot be run until a control plane exists is an app nobody runs. So
	// it is easy, and it is loud.
	Control ControlConfig `yaml:"control"`

	// Account, Ldap and Login are the three consumers, when this deployment runs
	// them itself. Named is a listener and empty is nowhere, the shape `control` and
	// `admin` have -- and a deployment that wants them in processes of their
	// own leaves both out and runs `roster account serve` and `roster ldap
	// serve` -- or `roster login serve` -- which read the same blocks and take
	// flags over them.
	//
	// They reach roster over the wire either way, which is what `cmd/consumers.go`
	// is about and what `scripts/test.sh` holds them to.
	Account AccountConfig `yaml:"account"`
	Ldap    LdapConfig    `yaml:"ldap"`
	Login   LoginConfig   `yaml:"login"`

	// Scim is the fourth: where a directory pushes the people it provisions.
	Scim ScimConfig `yaml:"scim"`

	// Stream is how often a stream that is open asks again who opened it. See
	// [StreamConfig].
	Stream StreamConfig `yaml:"stream"`

	// SignIn is whether the **data plane** answers `AuthService.SignIn`, so a
	// roster user can be a caller of roster itself rather than only of an app
	// in front of it.
	//
	// **Off unless said**, and the reason is the one `Public` gives about the
	// method it makes public: anybody who reaches the port may guess passwords,
	// and this port is the reachable one. What makes guessing expensive is the
	// lockout in `server/vouch`; what makes it *narrow* is that a sign-in here
	// is about one tenant, resolved from the name the browser arrived at.
	SignIn SignInConfig `yaml:"sign_in"`

	// Admin is where an operator administers **customers**: the data plane,
	// with no wall, behind a session. Empty is nowhere.
	//
	// A third listener because it can be nothing else. The data plane's port is
	// walled and an operator has no tenant there, so it shows them nothing; and
	// the control plane's port already registers `roster.HolderService` over its
	// own rows, so this cannot join it under the same name.
	//
	// Bind it where only a console reaches. It answers with no wall at all.
	Admin AdminConfig `yaml:"admin"`

	// Client is where the entity commands -- `tenant ls`, `holder get` -- send
	// their calls. **Empty is this process**, which is the default.
	//
	// It is the app's and not payday's for the reason `pdcmd` takes a
	// connector rather than a connection: where to connect, as whom, and what
	// that credential may do are the three decisions that make an admin command
	// safe or unsafe, and a framework making them would make them the same way
	// for every app.
	Client ClientConfig `yaml:"client"`

	// Vouch is what this deployment needs to hold a secret it must read back.
	//
	// Only one thing so far and it is the first of its kind: a TOTP seed is not
	// a verifier -- computing the code somebody is about to type means holding
	// the seed -- so the row **is** the secret, and it is wrapped with a key
	// this deployment keeps somewhere the database is not.
	//
	// Empty is a deployment that holds no second factor, and asking it to is
	// refused rather than answered with a seed in the clear.
	Vouch VouchConfig `yaml:"vouch"`

	// Audit is how long the trail is kept, and where what leaves it goes.
	//
	// payday's, like every block above it: the `Audit` entity is payday's, the
	// recorder that fills it is payday's, and *this table grows forever* is
	// every payday app's problem rather than roster's. What is roster's is
	// which of payday's pieces it has, which is this line. See
	// `config.AuditConfig` and payday's `trail`.
	//
	// It is the data plane's alone. The nested build that raises the control
	// plane is handed a config with no `audit` -- see [Build] -- so nothing
	// sweeps the trail of the deployment's own operations, which is a table
	// that grows by the key and is the last one anybody wants a clock deleting
	// from.
	Audit config.AuditConfig `yaml:"audit"`

	// Host is what this deployment needs to check that a hostname is a tenant's.
	//
	// One setting, and `docs/operating.md` § "A tenant registers its own
	// hostname" is what it decides: with a resolver, a tenant claims a name and
	// proves it through DNS; with `none`, they are told a roster operator writes
	// the row, which is what an air gap already does.
	Host prove.Config `yaml:"host"`

	// Holder is what happens to somebody after they leave.
	//
	// roster's and not payday's, and that is where the line falls: payday holds
	// the trail and offers the half of forgetting that has no judgement in it,
	// and *which rows are a person's* is a fact about this app's schema that no
	// framework can read. See `server/forget`, and `docs/operating.md` §
	// "Destroying somebody, which an erase does not" for the operator's half.
	Holder HolderConfig `yaml:"holder"`

	// Consent is how long what somebody consented to is kept once they are
	// gone (`proto/app/consent.proto`).
	//
	// roster's, beside `holder` and for its reason: which rows are kept past
	// a person as proof, and for how long, is this app's to say.
	Consent ConsentConfig `yaml:"consent"`

	// Tenant is what happens to a tenant after it closes (#98): how long it
	// may still be restored, and how long its alias is held once it is
	// destroyed.
	//
	// roster's, beside `holder` and for that block's reason one level up:
	// payday has a `Tenant` and no opinion about a customer leaving. See
	// `server/forget`, and `docs/operating.md` § "A tenant leaving".
	Tenant TenantLeaveConfig `yaml:"tenant"`

	// Delegation is how long a delegation somebody hands an app may live
	// (`DelegationService/Issue`). See [DelegationConfig].
	Delegation DelegationConfig `yaml:"delegation"`

	// Hook is a service this deployment names to be asked about the writes
	// roster's rules let through, which may refuse them and do nothing else
	// (`cmd/hook.go`, #305). Empty is none, and then nothing of it is
	// installed. See [HookConfig].
	Hook HookConfig `yaml:"hook"`

	// Admission is the same for an app that runs roster in its own process:
	// a policy in Go, where a file names a service. Not in the file, as
	// [ClientConfig.Local] is not. See [Admission].
	Admission Admission `yaml:"-"`

	// box is [Config.AtTheBox]: a command at a shell, which asks no hook.
	box bool
}

// DelegationConfig bounds the delegations people hand apps.
//
// An app writes for a person through a delegation that person approved: it
// presents its own key and the delegation beside it, and is answered as the
// person (`server/keys`, `acting.go`). What a front door is handed at a
// sign-in lives minutes; this one is approved once and lives for as long as
// the app is to act for them, so it is the deployment's to bound. Signing
// out everywhere, a suspension, an erasure and the person ending it stop it
// sooner, at the next call.
type DelegationConfig struct {
	// Life is how long a delegation somebody hands an app may live: no
	// `date_expired` later than this from now is taken, and none is this.
	// Empty is [core.DelegationLife], a day.
	Life time.Duration `yaml:"life"`
}

// Lives is [DelegationConfig.Life] with the default filled in: what roster
// holds a delegation to, and what a front door is handed to tell the person
// approving one.
func (c DelegationConfig) Lives() time.Duration {
	if c.Life > 0 {
		return c.Life
	}

	return core.DelegationLife
}

// TenantLeaveConfig is a closed tenant's grace, and its alias's after it is
// destroyed.
//
// # Two windows, with opposite defaults
//
// `RestoreWithin` is `holder.forget_after` one level up, and empty is
// **never**, for that one's reason: with nothing said, destroying a customer
// is an operator's act (`roster forget @tenant`) rather than a clock a version
// upgrade starts. A close can be restored until somebody does it.
//
// `AliasHold` is the other way round. Zero would be the unsafe default -- the
// name free the moment the rows are gone, for anybody to put up and sign the
// old organisation's people in -- so empty is a year. A deployment that wants
// less says so; an operator frees one name early on purpose with
// `roster forget --reuse @tenant`, and the trail records that they did.
type TenantLeaveConfig struct {
	// RestoreWithin is how long after a close the tenant is destroyed, and
	// until when `Restore` may undo it. Empty is never.
	RestoreWithin time.Duration `yaml:"restore_within"`

	// AliasHold is how long a destroyed tenant's alias stays taken. Empty is
	// [DefaultAliasHold].
	AliasHold time.Duration `yaml:"alias_hold"`

	// Every is how often both are checked. Empty is a day, as `holder.every`.
	Every time.Duration `yaml:"every"`
}

// DefaultAliasHold is a year: long enough for the bookmarks, the mail and the
// sign-in habits of a tenant's former people to have stopped pointing at it.
const DefaultAliasHold = 365 * 24 * time.Hour

// Hold is [TenantLeaveConfig.AliasHold], with the default filled in.
func (c TenantLeaveConfig) Hold() time.Duration {
	if c.AliasHold > 0 {
		return c.AliasHold
	}

	return DefaultAliasHold
}

// HolderConfig is how long an erased person is kept before what is held about
// them is destroyed.
//
// # Two clocks again, and only one of them is here
//
// `Holder.Erase` makes somebody unreachable and destroys nothing. This is the
// window between that and destruction, and it is **operational rather than
// legal**: a mistaken deletion, a compromised account deleting things, a
// billing dispute. Thirty days is the ordinary answer and it fits inside GDPR's
// month; a deployment under 개인정보보호법's five days for an explicit request
// wants it shorter, or uses `roster forget <who>`, which has no window at all
// because the person asked.
//
// The other clock -- an explicit request -- is not configuration. It is a
// command, because it is somebody exercising a right rather than a policy
// running.
//
// Empty is **never**, which is the same default the trail's retention has and
// for the same reason: a version upgrade is not the right thing to start
// destroying a deployment's data.
type HolderConfig struct {
	// ForgetAfter is how long after an erase somebody is destroyed. Empty is
	// never.
	ForgetAfter time.Duration `yaml:"forget_after"`

	// Every is how often that is checked. Empty is a day, which is what
	// `trail.Swept` is and for the same reason: what this period decides is
	// only how far past the window somebody may sit.
	Every time.Duration `yaml:"every"`
}

// StreamConfig is how a stream is held to who opened it, once it is open.
//
// A call carries its credential every time, so somebody stopped is refused at
// their next call. A stream carries one once, when it opens, and a `Watch` is
// then served until somebody hangs up -- so it is asked again, on a clock
// ([AskedAgain]).
type StreamConfig struct {
	// Recheck is how often. Empty is [DefaultRecheck]; a stream is never left
	// unasked, so there is no value that turns this off.
	//
	// What it decides is how long after somebody is stopped -- suspended,
	// erased, their key ended, their role taken away -- a stream they had open
	// goes on being served: at most this. Shorter is more reads, a few a
	// stream each time; it is not a setting about anything else.
	Recheck time.Duration `yaml:"recheck"`
}

// DefaultRecheck is [StreamConfig.Recheck] where nothing says.
const DefaultRecheck = 5 * time.Minute

// ConsentConfig is how long an account's consents outlive it.
//
// A consent is what shows afterwards that somebody agreed, so erasing the
// account leaves its consents, and so does forgetting the person: the holder
// stays, blank, for them to point at. Keep is how long after the erase they
// are kept, and then the deployment destroys them; an app sweeps nothing.
//
// Empty is **for good**, which is the default the trail's retention and
// `holder.forget_after` have, for their reason -- a version upgrade is not the
// right thing to start destroying a deployment's proof -- and what Loft's own
// `app.consent.keep` said before its consents moved here.
type ConsentConfig struct {
	// Keep is how long after its account was erased a consent is kept. Empty
	// is for good.
	Keep time.Duration `yaml:"keep"`

	// Every is how often that is checked. Empty is a day, `holder.every`'s
	// default and for its reason.
	Every time.Duration `yaml:"every"`
}

// Trail is the trail's policy, which forgetting answers to as well as the
// retention sweep: its archive, which forgetting has to reach as well as the
// database, and the legal holds that keep a row of it as it was.
//
// Read off `audit:` rather than repeated here, because there is one archive and
// two things that write to it. A deployment that kept them apart would have a
// person destroyed in one copy and not the other, which is the failure this
// whole act exists to prevent. It is [TrailPolicy], lease and all.
func (c HolderConfig) Trail(ctx context.Context, from Config) (trail.Policy, error) {
	return TrailPolicy(ctx, from)
}

// TrailPolicy is `audit:` as the policy every act of this process that moves
// or erases the trail's rows applies -- a pass, a person forgotten, a tenant
// destroyed, an archive purged by hand -- holding the deployment's lease for the
// turns those acts take (`trail.Policy.Lease`, payday#115), so that none undoes
// another: a pass writing back into the archive what an erasure had just taken
// out, in this process or another.
//
// Its own lease, and not the one the loops are run under: a loop's lease is
// which process runs it, and the trail's turn is a lock any process takes for a
// moment. A deployment that runs no loops (`spin.lease: none`) has nothing to
// take turns on, and its policy says so where it is logged.
func TrailPolicy(ctx context.Context, c Config) (trail.Policy, error) {
	p, err := AuditPolicy(c.Audit)
	if err != nil {
		return trail.Policy{}, err
	}
	if c.Spin.Lease != "" && c.Spin.Lease != config.LeaseNone {
		if p.Lease, err = c.Spin.Build(ctx, c.Db); err != nil {
			return trail.Policy{}, fmt.Errorf("spin: %w", err)
		}
	}

	return p, nil
}

// AuditPolicy is `audit:` as roster reads it: refused where it cannot be
// meant ([AuditMeant]), then payday's policy, with one thing a block says
// read as what it says.
//
// # A kind's block that names no window keeps the deployment's
//
// payday reads a kind's block as a window of its own unless the block says a
// floor (`config.AuditKeepConfig.window`), and a window with nothing in it is
// *for ever*. So `by: {holder: {content: fields}}` beside `retain: 2160h`
// kept every line about a person for good while the rest left, and the policy
// said `holder: forever` (#280) -- where the same block with a `min:` beside
// its content kept the deployment's window. What such a block says is how
// much of a write a line keeps, which is [config.AuditConfig.Contents]'s to
// read and payday's own comment calls best said by kind; it says nothing of
// how long. The kind is left to the deployment's window here, and
// `profile: forever` is how a block says for ever.
//
// Every reading of the policy in this binary goes through this, so that a
// pass, a check of the rows and a command at the box cannot disagree about
// one kind's window.
func AuditPolicy(a config.AuditConfig) (trail.Policy, error) {
	if err := AuditMeant(a); err != nil {
		return trail.Policy{}, err
	}

	p, err := a.Policy()
	if err != nil {
		return trail.Policy{}, err
	}
	for name, v := range a.By {
		if windowed(v) {
			continue
		}
		if d, err := trail.DomainOf(name); err == nil {
			delete(p.By, d)
		}
	}

	return p, nil
}

// windowed answers whether a kind's block says anything of how long: a
// profile, or either clock, or that no copy is kept.
func windowed(v config.AuditKeepConfig) bool {
	return v.Profile != "" || v.Retain != 0 || v.Discard || v.Destroy != 0
}

// trailsOwn is the kinds the schema registers for the trail's own tables: the
// trail, the outbox, the archive's account, the erasures owed and a row's
// seal. No line of the trail is about a row of one -- nothing records a write
// to them -- so a block under `audit.by` for one is read by nothing.
//
// Written down, and held to the generated recorder by
// `TestWhatAKindsBlockMaySayFollowsFromWhatTheTrailHoldsOfIt`, which fails
// for a kind the schema adds that the trail holds no lines about.
var trailsOwn = []pdid.Domain{pd.AuditDomain, pd.OutboxDomain, pd.ArchivedDomain, pd.OwedDomain, pd.RowSealDomain}

// AuditMeant refuses an `audit:` block that cannot be meant.
//
// # Why roster refuses what payday reads
//
// The block is payday's, and so is what reads it (`config.AuditConfig`,
// `trail.Policy`): what each of these comes to is decided there, and each is
// payday's to mend. What is roster's is the file a deployment starts from.
// Every block refused here started, ran, and did something other than what
// it says, with nothing anywhere to say so -- the shape a `control:` block
// with no database had (#334), and a file that said `allow_general_writes`
// (#351). It is refused for their reason, where they are: before anything is
// opened, in a sentence that names the key and says what to write instead.
//
// # Two sorts
//
// **Nothing reads it.** The setting is taken and changes nothing, so the file
// believes something the deployment does not do: `discard` with no `retain`,
// a kind's block that says nothing, `every` with no window, `hear_within`
// with no witness, a time that is less than none, and a block for one of the
// trail's own tables.
//
// **Two things that cannot both be meant.** Each is read, and what comes of
// the pair is what neither says: `destroy` with no `retain`, which keeps a
// row in the database for ever; a discard beside a digest key, which makes a
// finding of every row nobody has written since; and a `store:` witness
// inside the archive it exists to be out of reach of.
//
// # What it leaves, which looked the same and is read
//
// `checkpoints.key`, `.dir` and `.trust` with no archive: `--archive` of
// `trail prune`, `purge`, `verify` and `accept` names the archive of one run
// for a deployment that names none, and that archive is signed and
// checkpointed by what these say. `min`: roster's tenants answer holds and
// no windows, so a floor raises nobody's answer, but it is still what the
// deployment's own windows are held to where it comes up, and a window under
// it is refused there. `every` with no window, where a witness is named and
// `hear_within` is not: twice `every` is how long that witness may go
// without hearing.
//
// What payday refuses already is left to it, and a kind this app does not
// have is passed over here so that its refusal, which lists the kinds, is the
// one that is read.
func AuditMeant(a config.AuditConfig) error {
	type block struct {
		config.AuditKeepConfig

		at    string
		kind  pdid.Domain
		named bool
	}

	blocks := []block{{at: "audit", AuditKeepConfig: config.AuditKeepConfig{
		Profile: a.Profile, Retain: a.Retain, Discard: a.Discard, Destroy: a.Destroy, Min: a.Min, Content: a.Content,
	}}}
	for _, name := range slices.Sorted(maps.Keys(a.By)) {
		d, err := trail.DomainOf(name)
		if err != nil {
			continue
		}

		blocks = append(blocks, block{at: "audit.by." + name, AuditKeepConfig: a.By[name], kind: d, named: true})
	}

	// A time that is less than none. payday asks each clock whether it is
	// more than nothing, so a negative one is read as one nobody wrote.
	negative := func(key string, v time.Duration, read string) error {
		if v >= 0 {
			return nil
		}

		return fmt.Errorf("%s: %s is less than no time, and is read as if nothing were written: %s. "+
			"write how long, or take it out", key, v, read)
	}
	for _, b := range blocks {
		for _, v := range []struct {
			key  string
			v    time.Duration
			read string
		}{
			{b.at + ".retain", b.Retain, "its rows stay in the database for ever"},
			{b.at + ".destroy", b.Destroy, "its rows are kept for ever"},
			{b.at + ".min.retain", b.Min.Retain, "there is no floor"},
			{b.at + ".min.destroy", b.Min.Destroy, "there is no floor"},
		} {
			if err := negative(v.key, v.v, v.read); err != nil {
				return err
			}
		}
	}
	if err := negative("audit.every", a.Every, "the policy is applied once a day"); err != nil {
		return err
	}
	if err := negative("audit.checkpoints.hear_within", a.Checkpoints.HearWithin,
		"a checkpoint may go unheard for twice audit.every"); err != nil {
		return err
	}

	digests := strings.TrimSpace(a.Digest.Key) != ""
	on := false
	for _, b := range blocks {
		if b.named {
			if slices.Contains(trailsOwn, b.kind) {
				return fmt.Errorf("%s: no line of the trail is about a row of this kind, which is one of the trail's own tables, "+
					"so nothing reads a window, a floor or a content for it. take the block out: what the trail itself keeps is "+
					"audit.retain and audit.destroy, and the access record is audit.by.access", b.at)
			}
			if b.AuditKeepConfig == (config.AuditKeepConfig{}) {
				// payday reads this as the kind kept for ever.
				return fmt.Errorf("%s: the block says nothing, and a kind is named to say how its window, its floor or its "+
					"content differs. write one of them (profile: forever keeps the kind for ever), or take the block out", b.at)
			}
		}

		// The two clocks as payday reads them, with what the profile fills
		// in. A profile it does not have is its own to refuse.
		k := trail.Keep{Retain: b.Retain, Destroy: b.Destroy}
		if b.Profile != "" {
			v, err := trail.NamedProfile(b.Profile)
			if err != nil {
				on = true

				continue
			}

			k = v.Over(k)
		}
		on = on || k.Retain > 0

		// A kind's block is the whole of its window, which is the half of
		// these two that is easy to write by mistake: `by.holder.discard`
		// under a deployment's `retain` reads as that kind discarded on the
		// deployment's clock, and is that kind kept for ever.
		whole := ""
		if b.named {
			whole = " A kind's block is the whole of its window, and does not take audit.retain."
		}
		if b.Discard && k.Retain <= 0 {
			return fmt.Errorf("%[1]s.discard: says a row leaves the database with no copy kept, and %[1]s.retain names no "+
				"window for it to leave after -- so nothing leaves, and nothing reads discard.%[2]s "+
				"write %[1]s.retain, or take discard out", b.at, whole)
		}
		if k.Destroy > 0 && k.Retain <= 0 {
			return fmt.Errorf("%[1]s.destroy: says a row exists no longer than this, and %[1]s.retain names no window, which "+
				"keeps a row in the database for ever: the two cannot both be meant. What comes of them is the second -- "+
				"nothing leaves the database, and only what is in the archive already, moved there by hand or under an "+
				"earlier window, is destroyed.%[2]s "+
				"write %[1]s.retain, how long a row stays in the database before the archive has it, or take destroy out", b.at, whole)
		}

		// A discard beside a digest key. A pass carries a digest forward out
		// of a chunk of the archive it is about to destroy, and out of
		// nothing else (`trail.pass.carry`), so a line discarded from the
		// database takes with it the one thing a check of the rows reads. The
		// access record's lines keep no digest, and are no part of this.
		if b.Discard && digests && !(b.named && b.kind == pd.AccessDomain) {
			return fmt.Errorf("%[1]s.discard with audit.digest.key: a pass that discards a line of the trail carries its digest "+
				"nowhere, so each row nobody wrote since its window would be found not-on-trail by `roster trail rows` -- "+
				"what a row added past the servers looks like, and a finding for every row is one nobody reads. "+
				"keep the lines, with audit.archive and %[1]s.destroy in place of discard, or take audit.digest.key out", b.at)
		}
	}

	heard := len(a.Checkpoints.Witnesses) > 0
	if a.Every > 0 && !on && !(heard && a.Checkpoints.HearWithin == 0) {
		return errors.New("audit.every: says how often the policy is applied, and nothing names a window -- no audit.retain, " +
			"no audit.profile that has one, none under audit.by -- so no pass runs and nothing reads every. " +
			"write a window, or take every out")
	}
	if a.Checkpoints.HearWithin > 0 && !heard {
		return errors.New("audit.checkpoints.hear_within: says how long the newest checkpoint may go unheard, and " +
			"audit.checkpoints.witnesses names nobody to hear it, so nothing reads it. name a witness, or take hear_within out")
	}

	// A `store:` witness in the archive's own directory. The manual says a
	// witness on the archive's disk is none; this is the one case of that a
	// path can show.
	if a.Archive != "" {
		for _, w := range a.Checkpoints.Witnesses {
			dir, ok := strings.CutPrefix(strings.TrimSpace(w), "store:")
			if !ok || dir == "" {
				continue
			}
			if within(a.Archive, dir) {
				return fmt.Errorf("audit.checkpoints.witnesses: %s is inside audit.archive (%s). A witness hears each checkpoint "+
					"somewhere the archive's credentials do not reach, and this one goes wherever the archive goes: the two "+
					"cannot both be meant. name a directory of its own, on another volume, or `log:`", strings.TrimSpace(w), a.Archive)
			}
		}
	}

	return nil
}

// Meant refuses a file a deployment starts from whose settings cannot be
// meant, as [AuditMeant] does an `audit:` block and for its reason: each of
// these started, and did something other than what the file says, with
// nothing to say so. Asked by [Build] before anything is opened, so a command
// at the box is refused the same file `serve` is; each refusal names the key
// as the file writes it, and says what would be right.
func (c Config) Meant() error {
	for _, v := range []func(Config) error{
		plainBesideCertificate,
		certificates,
		answersNothing,
		lessThanNone,
		formed,
		noEffect,
		severalOverSqlite,
		hookMeant,
	} {
		if err := v(c); err != nil {
			return err
		}
	}

	return nil
}

// listeners is the three listener blocks a file may say, by the name it
// writes each with: the data plane's, the control plane's and the admin
// listener.
func (c Config) listeners() []listenerBlock {
	return []listenerBlock{
		{"server", c.Server.ServerConfig},
		{"control", c.Control.ServerConfig},
		{"admin", c.Admin.ServerConfig},
	}
}

type listenerBlock struct {
	key string
	config.ServerConfig
}

// plainBesideCertificate refuses a block whose `tls:` asks every caller for a
// certificate and that opens an `http.addr` beside it.
//
// A block's `tls:` is its gRPC listener's alone: the `http` listener beside it
// is a plain socket and reads nothing of the block (`Server.http`). So the
// second listener answers, transcoded, the calls of whoever the first refuses
// at the handshake. Where the certificate is not asked of the caller the
// `http` listener is plain on purpose, for a proxy in front that holds one,
// and `serve` says so ([Server.plain]).
func plainBesideCertificate(c Config) error {
	for _, b := range c.listeners() {
		if b.Tls.ClientCA.IsZero() || b.Http.Addr == "" {
			continue
		}

		return fmt.Errorf("%[1]s.tls.client_ca: asks every caller of %[1]s.addr for a certificate, and %[1]s.http.addr "+
			"beside it is plain -- tls: is the gRPC listener's alone -- so it would answer whoever the first keeps out. "+
			"take %[1]s.http.addr out, or put a proxy that asks for the certificate in front of it and take client_ca out", b.key)
	}

	return nil
}

// certificates refuses a listener block's certificate that cannot be read,
// naming the key: both `cert` and `key` or neither, each a value that can be
// read and the two a certificate and its key, and `client_ca` one that holds a
// certificate.
//
// payday reads them where the block's gRPC server is made, which `serve` does
// after the resources are applied, the port is bound and the loops have
// started, and says `tls: both cert and key must be set` with nothing to say
// which of three blocks. So they are read here first, as payday will read
// them: a `${file:}` is the file as it is now. No refusal repeats what a key
// holds, since one of them is a private key.
func certificates(c Config) error {
	for _, b := range c.listeners() {
		t := b.Tls
		if !t.Active() {
			continue
		}
		switch {
		case t.Cert.IsZero() && t.Key.IsZero():
			return fmt.Errorf("%[1]s.tls.cert: %[1]s.tls says TLS, and names neither cert nor key. "+
				"name both, or take the block out", b.key)
		case t.Cert.IsZero():
			return fmt.Errorf("%[1]s.tls.cert: %[1]s.tls.key is named, and no certificate beside it. name both, or neither", b.key)
		case t.Key.IsZero():
			return fmt.Errorf("%[1]s.tls.key: %[1]s.tls.cert is named, and no key beside it. name both, or neither", b.key)
		}

		cert, err := t.Cert.Value()
		if err != nil {
			return fmt.Errorf("%s.tls.cert: %w", b.key, err)
		}
		key, err := t.Key.Value()
		if err != nil {
			return fmt.Errorf("%s.tls.key: %w", b.key, err)
		}
		if _, err := tls.X509KeyPair([]byte(cert), []byte(key)); err != nil {
			return fmt.Errorf("%[1]s.tls.cert, %[1]s.tls.key: not a certificate and its key: %[2]w", b.key, err)
		}
		if !t.ClientCA.IsZero() {
			ca, err := t.ClientCA.Value()
			if err != nil {
				return fmt.Errorf("%s.tls.client_ca: %w", b.key, err)
			}
			if !x509.NewCertPool().AppendCertsFromPEM([]byte(ca)) {
				return fmt.Errorf("%s.tls.client_ca: holds no certificate", b.key)
			}
		}
	}

	return nil
}

// answersNothing refuses an `http.addr` that would answer nothing it is there
// for.
//
// The transcoder is mounted only where `http.allow_web` says so, and payday
// cannot refuse an address without it, since it does not know an app's
// routes. roster knows its own. `control.http` has no page and no route of
// its own, so with neither `allow_web` nor `allow_pprof` it answered `404` to
// everything while the log said it was up; an `admin.http` or a `server.http`
// that serves a console without `allow_web` served the page and none of the
// calls the page makes.
func answersNothing(c Config) error {
	if c.Control.Serves() && c.Control.Http.Addr != "" && !c.Control.Http.AllowWeb && !c.Control.Http.AllowPprof {
		return errors.New("control.http.addr: the listener has no page and no route of its own, and with neither " +
			"control.http.allow_web nor control.http.allow_pprof it answers 404 to everything. turn allow_web on for " +
			"the control plane's calls over HTTP, or take control.http.addr out: a shell asks control.addr")
	}
	for _, v := range []struct {
		key, page string
		http      config.HttpConfig
		dir       string
	}{
		{"admin", "admin.console.dir", c.Admin.Http, c.Admin.Console.Dir},
		{"server", "server.console.dir", c.Server.Http, c.Server.Console.Dir},
	} {
		if v.http.Addr == "" || v.dir == "" || v.http.AllowWeb {
			continue
		}

		return fmt.Errorf("%[1]s.http.addr: serves the page %[2]s names, and %[1]s.http.allow_web is off, so none of "+
			"the calls the page makes is answered. turn %[1]s.http.allow_web on, or take %[2]s out", v.key, v.page)
	}

	return nil
}

// lessThanNone refuses a duration or a count below nought, wherever the file
// holds one.
//
// Each is read as if nothing were written: payday and roster both ask a clock
// whether it is more than nothing, so `holder.forget_after: -720h` was never,
// `consent.keep` negative for good, `stream.recheck` negative five minutes,
// and a negative `server.keepalive.time` gave back gRPC's two hours. [AuditMeant]
// refuses the same of `audit:` (#398) and [ScimConfig.Meant] of
// `scim.concurrency` (#429), and this is the rest of the file, found by
// walking it as the loader does so that the next key added is held too. What
// nought means is still what each key's comment says.
//
// [clocksLeft] is what it leaves, with why.
func lessThanNone(c Config) error {
	var walk func(v reflect.Value, at string) error
	walk = func(v reflect.Value, at string) error {
		if _, ok := clocksLeft[at]; ok {
			return nil
		}

		switch v.Kind() {
		case reflect.Struct:
			t := v.Type()
			for i := range t.NumField() {
				f := t.Field(i)
				if !f.IsExported() {
					continue
				}
				name, opts, _ := strings.Cut(f.Tag.Get("yaml"), ",")
				if name == "-" {
					continue
				}
				key := at
				if opts != "inline" {
					if name == "" {
						name = strings.ToLower(f.Name)
					}
					key = strings.TrimPrefix(at+"."+name, ".")
				}
				if err := walk(v.Field(i), key); err != nil {
					return err
				}
			}

		case reflect.Map:
			keys := v.MapKeys()
			slices.SortFunc(keys, func(a, b reflect.Value) int { return strings.Compare(a.String(), b.String()) })
			for _, k := range keys {
				if err := walk(v.MapIndex(k), at+"."+k.String()); err != nil {
					return err
				}
			}

		case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
			if v.Int() >= 0 {
				return nil
			}
			if v.Type() == reflect.TypeFor[time.Duration]() {
				return fmt.Errorf("%s: %s is less than no time, and is read as if nothing were written. "+
					"write how long, or take it out", at, time.Duration(v.Int()))
			}

			return fmt.Errorf("%s: %d is less than none, and is read as if nothing were written. "+
				"write how many, or take it out", at, v.Int())

		case reflect.Float32, reflect.Float64:
			if v.Float() >= 0 {
				return nil
			}

			return fmt.Errorf("%s: %v is less than none, and is read as if nothing were written. "+
				"write how many, or take it out", at, v.Float())
		}

		return nil
	}

	return walk(reflect.ValueOf(c), "")
}

// clocksLeft is what [lessThanNone] does not walk, and why.
var clocksLeft = map[string]string{
	// payday reads it, and gives less than none a meaning: a call that came
	// with no deadline is given none. The manual says so.
	"server.timeout":  "payday's: no deadline at all",
	"control.timeout": "payday's: no deadline at all",
	"admin.timeout":   "payday's: no deadline at all",

	// What reads it is the telemetry library, which holds its own block to
	// its own rules.
	"otel": "the telemetry library's",
}

// formed refuses a value that has a form and is not of it, each by its key.
// Each started, and was found at the first request that read it or never.
func formed(c Config) error {
	if err := c.Host.Meant(); err != nil {
		return err
	}
	if err := dirThere("server.console.dir", c.Server.Console.Dir); err != nil {
		return err
	}
	if err := dirThere("admin.console.dir", c.Admin.Console.Dir); err != nil {
		return err
	}

	// payday matches an origin against what a browser sends, which is a
	// scheme and a host and nothing else, so one written with a path or with
	// no scheme matches no browser's.
	for _, b := range c.listeners() {
		for _, o := range b.Http.Origins {
			if o == "*" {
				continue
			}
			u, err := url.Parse(o)
			if err == nil && u.Scheme != "" && u.Host != "" && u.Path == "" && u.User == nil && u.RawQuery == "" && u.Fragment == "" {
				continue
			}

			return fmt.Errorf("%s.http.origins: %q is no origin, which is a scheme and a host and nothing after them, "+
				"as http://localhost:5173; a browser sends nothing else, so it would match none", b.key, o)
		}
	}

	// Two keys of one name: the second replaced the first under the name new
	// seeds are sealed with, so a seed sealed with the first could not be
	// opened. The name is said and neither value.
	names := map[string]bool{}
	for _, v := range c.Vouch.Keys {
		name, _, ok := strings.Cut(v, ":")
		if !ok || name == "" {
			// [vouch.NewKeyring] refuses it, in words of its own.
			continue
		}
		if names[name] {
			return fmt.Errorf("vouch.keys: two keys are named %q, and a seed names the key it is sealed with. "+
				"name each key once", name)
		}
		names[name] = true
	}

	// A PIN shorter than four digits is a thousand guesses, and the lock keeps
	// a guess slow only where there are enough of them to be slow through; one
	// longer than twelve is past what anybody types at a station. Zero is
	// unsaid, which is four, and less than none is [lessThanNone]'s.
	if n := c.Vouch.Badge.Pin.MinLength; n > 0 && (n < core.MinPinLength || n > core.MaxPinLength) {
		return fmt.Errorf("vouch.badge.pin.min_length: %d, and a PIN is at least %d digits and at most %d. "+
			"write a number from %d to %d, or take it out for %d", n, core.MinPinLength, core.MaxPinLength,
			core.MinPinLength, core.MaxPinLength, core.MinPinLength)
	}

	for _, v := range []struct {
		serves bool
		meant  func() error
	}{
		{c.Account.Serves(), c.Account.Meant},
		{c.Ldap.Serves(), c.Ldap.Meant},
		{c.Login.Serves(), c.Login.Meant},
	} {
		if !v.serves {
			continue
		}
		if err := v.meant(); err != nil {
			return err
		}
	}

	return nil
}

// noEffect refuses a setting that is written down and cannot take effect, for
// the reason [Build] refuses `control:` with no database and
// `server.console.dir` with no sign-in: an address written down and not served
// is worse than one that is refused, because it is a port somebody believes is
// there. Each names both keys.
//
// A consumer's block said with no address is not among them: `roster ldap
// serve` and the others read the same block.
func noEffect(c Config) error {
	if c.Admin.Console.Dir != "" && !c.Control.Serves() {
		return errors.New("admin.console.dir: the admin console signs an operator in against the control plane, and " +
			"control.db.driver names no database, so no listener serves it. name a control plane, or take the page out")
	}
	if c.Admin.Console.Dir != "" && c.Admin.Http.Addr == "" {
		return errors.New("admin.console.dir: the page is served by admin.http.addr, which names no listener, so " +
			"nothing serves it. name admin.http.addr, or take the page out")
	}
	if c.Server.Console.Dir != "" && c.SignIn.Enabled && c.Server.Http.Addr == "" {
		return errors.New("server.console.dir: the page is served by server.http.addr, which names no listener, so " +
			"nothing serves it. name server.http.addr, or take the page out")
	}
	for _, b := range c.listeners() {
		if b.Http.Addr == "" {
			if b.Http.AllowWeb {
				return fmt.Errorf("%[1]s.http.allow_web: says what %[1]s.http.addr answers, and it names no listener. "+
					"name %[1]s.http.addr, or take allow_web out", b.key)
			}
			if len(b.Http.Origins) > 0 {
				return fmt.Errorf("%[1]s.http.origins: says which pages may call %[1]s.http.addr, and it names no listener. "+
					"name %[1]s.http.addr, or take origins out", b.key)
			}
		}
		if b.Limit.Burst != 0 && !b.Limit.Limits() {
			return fmt.Errorf("%[1]s.limit.burst: says how many calls may arrive at once before %[1]s.limit.rate holds them, "+
				"and no rate is said, so nothing is limited. write %[1]s.limit.rate, or take burst out", b.key)
		}
	}
	if c.Policy.Refresh != 0 && c.Policy.File == "" {
		return errors.New("policy.refresh: says how often policy.file is read again, and no file is named. " +
			"name policy.file, or take refresh out")
	}
	planes := []struct {
		key   string
		watch config.WatchConfig
	}{{"watch", c.Watch}}
	if c.Control.Serves() {
		planes = append(planes, struct {
			key   string
			watch config.WatchConfig
		}{"control.watch", c.Control.watch(c.Watch)})
	}
	for _, v := range planes {
		if v.watch.Dsn == "" || (v.watch.Broker != config.BrokerMemory && v.watch.Broker != config.BrokerNone) {
			continue
		}
		broker := v.key + ".broker"
		if v.key == "control.watch" && c.Control.Watch.Broker == "" {
			broker = "watch.broker, which control.watch.broker took as it is empty,"
		}

		return fmt.Errorf("%[1]s.dsn: says where the broker connects, and %[2]s is %[3]q, which connects nowhere. "+
			"take %[1]s.dsn out, or name a broker that reads it (postgres)", v.key, broker, v.watch.Broker)
	}

	return nil
}

// severalOverSqlite refuses a lease of several processes over a plane whose
// database is SQLite.
//
// SQLite is a file on one machine, so several processes on it are several
// deployments, each with operators, keys and sessions of its own. payday asks
// the data plane's database of a lease only where `spin.dsn` names none, and
// never asks the control plane's, so a lease of several stood over a SQLite
// control plane always and over a SQLite data plane whose lease was kept
// elsewhere.
func severalOverSqlite(c Config) error {
	switch c.Spin.Lease {
	case "", config.LeaseLocal, config.LeaseNone:
		return nil
	}

	for _, v := range []struct {
		key string
		db  config.DbConfig
	}{
		{"db", c.Db},
		{"control.db", c.Control.Db},
	} {
		if v.db.Dialect != config.DialectSQLite && !strings.HasPrefix(v.db.Driver, "sqlite") {
			continue
		}

		return fmt.Errorf("%[1]s.driver: %[2]q is a database on one machine, and spin.lease is %[3]q, which shares the "+
			"loops among several processes: each would be a deployment of its own, with its own operators, keys and "+
			"sessions. run one process (spin.lease: local), or put %[1]s on a database every process reaches", v.key, v.db.Driver, c.Spin.Lease)
	}

	return nil
}

// webOrigin refuses a URL a browser is sent to, or that is dialled, that has
// no scheme of the web's or no host: `hydra:4445` was taken, logged, and
// failed at the first call. Empty is left to whatever reads it.
//
// And one that carries a credential between `://` and `@`. An address is
// printed by `roster config` as the file writes it and logged where an app
// starts, so it is no place for one; a credential for Hydra's admin API goes
// in `login.hydra.header`, whose values are never printed. The address is not
// said back.
func webOrigin(key, v string) error {
	if v == "" {
		return nil
	}
	authority := v
	if _, rest, ok := strings.Cut(v, "://"); ok {
		authority = rest
	}
	if i := strings.IndexAny(authority, "/?#"); i >= 0 {
		authority = authority[:i]
	}
	if strings.Contains(authority, "@") {
		return fmt.Errorf("%s: the address carries a credential before its host, and an address is printed and logged "+
			"as it is written. write it without one; a credential for Hydra's admin API goes in login.hydra.header, "+
			"whose values are never printed", key)
	}
	u, err := url.Parse(v)
	if err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != "" {
		return nil
	}

	return fmt.Errorf("%s: %q is no URL with a scheme of http or https and a host, as https://login.example.com", key, v)
}

// dirThere refuses a page's directory that is not there: it started, and
// served nothing. Empty is no page, which is what each key says.
func dirThere(key, dir string) error {
	if dir == "" {
		return nil
	}
	st, err := os.Stat(dir)
	if err == nil && st.IsDir() {
		return nil
	}
	if err == nil {
		return fmt.Errorf("%s: %s is not a directory. name the directory the page was built into, or take it out for no page", key, dir)
	}

	return fmt.Errorf("%s: %w. name the directory the page was built into, or take it out for no page", key, err)
}

// within answers whether `dir` is `root` or under it, as far as their paths
// say.
func within(root, dir string) bool {
	x, err := filepath.Abs(root)
	if err != nil {
		return false
	}
	y, err := filepath.Abs(dir)
	if err != nil {
		return false
	}

	rel, err := filepath.Rel(x, y)
	if err != nil {
		return false
	}

	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))
}

// VouchConfig is what checking secrets needs beyond the rows.
type VouchConfig struct {
	// Breached is a file of leaked password hashes, and empty is a deployment
	// that does not check.
	//
	// The format the well-known corpus is published in: SHA-1, uppercase hex,
	// one per line, **sorted**. A file rather than a service because the
	// deployment this app is most careful about has no network at all, and the
	// search is a binary one over the file -- nothing loaded, nothing indexed,
	// and `sort -u` is enough to make one.
	//
	// It is read once at startup to check the order, because a file that is not
	// sorted answers *no* to things that are in it -- the direction that fails
	// quietly, in the one feature whose whole job is to say yes.
	Breached string `yaml:"breached"`

	// Keys wrap the seeds, written `name:base64` with the current one first.
	//
	// A list rather than one key because rotation is the only reason a
	// ciphertext carries a name at all: new rows take the first, and old rows
	// go on reading with whichever made them. A deployment with one key writes
	// one line and never thinks about it again.
	//
	// Thirty-two bytes each, base64. `server/vouch/seed.go` says what the key
	// buys -- protection against a copy of the rows, and not against a
	// compromised process -- and what a lost one costs.
	Keys []string `yaml:"keys"`

	// Lockout is how many wrong answers in a row close an account and for how
	// long: ten and fifteen minutes unless said. See `vouch.Lockout` for why
	// there is no off.
	Lockout LockoutConfig `yaml:"lockout"`

	// Password is what a password somebody chooses has to be: a minimum
	// length (eight unless said), and, off unless said, how many of their
	// former passwords are refused. See `core.Password` for what is
	// deliberately not here.
	Password PasswordConfig `yaml:"password"`

	// Badge is what a station's badge and PIN are held to, apart from a
	// password's: how long a PIN is, and what locks a badge and for how
	// long. See `core.Badge`.
	Badge BadgeConfig `yaml:"badge"`
}

// BadgeConfig is `core.Badge` as a file says it.
type BadgeConfig struct {
	Pin     BadgePinConfig     `yaml:"pin"`
	Lockout BadgeLockoutConfig `yaml:"lockout"`
}

// BadgePinConfig is how many digits a PIN is at least: four unless said,
// never fewer and never more than twelve.
type BadgePinConfig struct {
	MinLength int `yaml:"min_length"`
}

// BadgeLockoutConfig is how many wrong PINs in a row lock a badge, five
// unless said, and for how long: unset is until somebody unlocks it.
type BadgeLockoutConfig struct {
	Failures int32         `yaml:"failures"`
	For      time.Duration `yaml:"for"`
}

// LockoutConfig is `vouch.Lockout` as a file says it.
type LockoutConfig struct {
	Failures int32         `yaml:"failures"`
	For      time.Duration `yaml:"for"`
}

// PasswordConfig is `core.Password` as a file says it.
type PasswordConfig struct {
	MinLength int `yaml:"min_length"`
	NoReuse   int `yaml:"no_reuse"`
}

// ClientConfig is how a command reaches this deployment, when it is not this
// process.
//
// # Empty is local, and that is the default
//
// The opposite of what `oas` does, and the reason is the commands beside these:
// `roster init` and `roster key` have no remote form at all, since what they
// write is not served. A binary whose commands disagree about where they run is
// worse than one that is consistently the awkward way round.
//
// So a deployment that wants the wire says so, and one that does not gets a
// line in the log every time saying what it is doing.
type ClientConfig struct {
	// Addr is what to dial, and empty is this process. It is a gRPC target:
	// "dns:///roster.internal:8080".
	//
	// Which port to name is a decision with an answer that is not obvious; see
	// the note on `cli/entity.go`'s `remote`. In short: `server.addr` is the
	// data plane and is walled, so what comes back is what the credential's
	// tenant holds -- not every tenant.
	Addr string `yaml:"addr"`

	// Insecure sends without TLS, for a deployment reached over a network only
	// it is on. It is written down rather than inferred from the address,
	// because inferring it is how a production address gets served in plaintext
	// by a default nobody read.
	Insecure bool `yaml:"insecure"`

	// Auth is how a call says who is making it.
	Auth ClientAuthConfig `yaml:"auth"`

	// Local is `--HAL`: read the database directly whatever the rest of this
	// says.
	//
	// Not in the file, and it is the one field here that is not. What it is for
	// is the moment somebody with a shell on the box wants to look at the rows
	// under a deployment that is configured to go over the wire -- and editing
	// the configuration to do that is a change that outlives the look.
	//
	// It is a flag alone, where `oas` requires a flag **and** a setting
	// allowing it. The difference is which way round the default is: there,
	// remote is the default and reading the database is the privileged path, so
	// it is locked twice. Here local is already what a file with no `client`
	// block does, and anybody who can pass this flag is already holding the
	// file the `db` block is in. A second lock would guard nothing.
	Local bool `yaml:"-"`

	// As is `--as`: who is at this shell, an operator of the control plane by
	// alias, for the access record to name as the one who read or wrote what a
	// local command did (#221). See [OperatorNamed].
	//
	// Not in the file and not in the environment, for `Local`'s reason and one
	// of its own: a deployment's file and its environment are everybody's who
	// opens a shell on it, so a name written there would be one person named
	// for whoever ran the command. It is said by the person, each time.
	As string `yaml:"-"`
}

// ClientAuthConfig is the credential a command presents, and how.
//
// The scheme is a setting because roster serves more than one and which it
// serves depends on the rest of this file: with a control plane the data plane
// reads `Bearer` and checks an API key, and without one it reads `Plain` and
// believes whatever the caller writes. A command that could only send one of
// them would work against half the deployments this app supports.
type ClientAuthConfig struct {
	// Scheme is `bearer`, `plain`, or `none`, and it is the word that goes
	// before the credential in `authorization`.
	//
	//	bearer  an API key, checked against the control plane. What a
	//	        deployment serving anybody uses.
	//	plain   the caller says who it is and is believed, so the credential is
	//	        a slug: "@contoso/admin". It is what this app serves with **no
	//	        control plane configured**, which is a sandbox and not something
	//	        to serve where anyone can reach it.
	//	none    send nothing. For a port that authenticates at the transport --
	//	        a client certificate -- or one that is open.
	//
	// Empty with a credential given is `bearer`, which is the production
	// scheme. Defaulted rather than refused because the wrong answer here is
	// loud: a scheme the server does not read comes back `Unauthenticated` on
	// the first call, and there is nothing to notice weeks later.
	//
	// A name that is none of the three is refused rather than ignored.
	Scheme string `yaml:"scheme"`

	// Credential is what goes after the scheme -- a key for `bearer`, a slug
	// for `plain` -- as a [Token]: the thing itself, or where a deployment
	// keeps it, `${file:/run/secrets/roster-key}` for a mounted secret. The
	// whitespace around it goes, because a file ends in a newline.
	Credential Token `yaml:"credential"`
}

// IsSet reports whether this says anything at all.
//
// It is what makes `client.auth` with no `client.addr` a refusal rather than a
// credential that is quietly never sent; see `cli/entity.go`.
func (c ClientAuthConfig) IsSet() bool {
	return c.Scheme != "" || !c.Credential.IsZero()
}

// Provider is how a command says who it is, or nil when it says nothing.
//
// It is worked out here rather than where the connection is made, so that a
// configuration this cannot be built from is refused before anything is dialed
// and with the name of the field that is wrong.
func (c ClientAuthConfig) Provider() (auth.Provider, error) {
	v, err := c.value()
	if err != nil {
		return nil, err
	}

	scheme := strings.ToLower(c.Scheme)
	if scheme == "" && v != "" {
		scheme = "bearer"
	}

	switch scheme {
	case "", "none":
		if v != "" {
			return nil, fmt.Errorf("client.auth: a credential is given and the scheme is %q, which sends none", scheme)
		}

		return nil, nil

	case "bearer":
		if v == "" {
			return nil, fmt.Errorf("client.auth.scheme: bearer, and no credential to send")
		}

		return auth.BearerProvider(v), nil

	case "plain":
		if v == "" {
			return nil, fmt.Errorf("client.auth.scheme: plain, and nobody to say this call is from")
		}

		// Believed by whoever reads it, which is a deployment with no control
		// plane. Nothing here can tell whether that is what is at the other
		// end, so this is not refused -- but it is the one scheme worth
		// noticing in a file somebody reviews.
		return auth.PlainProvider(v), nil

	default:
		return nil, fmt.Errorf("client.auth.scheme: %q is not one of bearer, plain, none", c.Scheme)
	}
}

// value is the credential, read.
//
// A file that is named and not there is an error rather than an empty
// credential: a deployment that mounted a secret and got the mount path wrong
// must not fall through to calling as nobody, which reads as a permission
// problem three layers away.
func (c ClientAuthConfig) value() (string, error) {
	v, err := c.Credential.Value()
	if err != nil {
		return "", fmt.Errorf("client.auth.credential: %w", err)
	}

	return v, nil
}

// ControlConfig is the second roster: the one holding keys rather than people.
//
// # Why a database of its own
//
// A key must not live in the tables it protects. Separate, there is no query
// from the data plane to the keys at all, so a fault in the wall cannot reach
// one -- which is worth more than the migration it costs.
//
// # Why in this process
//
// Because the auth interceptor asks it on **every** request. A control plane
// somewhere else would need a credential to reach, and that credential would
// need checking somewhere, which is the same question one hop further out. Here
// the innermost lookup is a Go call against a server with no wall on it, and
// the recursion ends there.
type ControlConfig struct {
	Db config.DbConfig `yaml:"db"`

	// Where **this plane's** changes are published, which is its own question
	// and not the data plane's.
	//
	// It was `memory`, written into the code rather than read from anywhere,
	// and that made the admin console the one screen a second replica silently broke:
	// a key issued on one process would never reach an operator watching on
	// another, on a stream that stayed open and looked healthy.
	//
	// Separate from `watch` above for the reason the databases are separate.
	// A control plane publishing into the data plane's broker would make a key
	// changing look like a person changing, to every client watching -- so they
	// cannot be one setting, and the one that was implicit is the one nobody
	// could have found.
	//
	// Empty takes the data plane's **broker name**, and nothing else about it:
	// a deployment that named one broker for its people almost always means the
	// same kind for its keys, and having to write `memory` twice is a way of
	// getting it right once and wrong later.
	Watch config.WatchConfig `yaml:"watch"`

	// Where the control plane answers, and **empty is nowhere**.
	//
	// The rows are reachable in this process whatever this says -- that is what
	// the auth interceptor asks on every request. What an address adds is a way
	// for a console to manage them: which services exist, what keys they hold,
	// who the operators are.
	//
	// Nothing is opened unless it is written down, and it should be written
	// down as an interface a console can reach and nothing else can. A port
	// that is not open is a control nothing has to get right, which is the same
	// argument `AllowPprof` makes about a listener that is.
	//
	// Inlined, so this reads `control.addr` and `control.http` the way `admin`
	// reads `admin.addr` and `admin.http`. It was `control.server.addr` for one
	// commit, which put the two listeners' settings at different depths for no
	// reason anybody could have named.
	//
	// Its own settings rather than the data plane's, because they answer
	// different callers about different rows: a limit tuned for a product app's
	// traffic is not one for a console, and one `http` block cannot open two
	// ports.
	config.ServerConfig `yaml:",inline"`
}

// AdminConfig is the listener a **roster operator** administers customers
// from, and the page they do it on.
//
// The page is here rather than on `control.http` because a browser's session
// cookie is `__Host-` prefixed and host-only: a page and the listener it calls
// must be the same host, and the tenants screen calls this one. What is left
// on the control listener is the RPCs a shell makes of the deployment's own
// rows -- `roster holder list` with `client.addr` naming it -- and no page at
// all.
type AdminConfig struct {
	config.ServerConfig `yaml:",inline"`

	// Console is the built page, served by this listener at `/`. Empty serves
	// none, which is what a deployment with no browser-facing operator wants
	// and what `npm run dev` wants -- it serves the page itself and is told
	// `origins:` instead.
	Console ConsoleConfig `yaml:"console"`
}

// ServerConfig is the data plane's listener, the one a product app and a
// **roster user** call, and the user console's page, which it serves.
type ServerConfig struct {
	config.ServerConfig `yaml:",inline"`

	// Console is the page a **roster user** opens: their own tenant, and
	// nothing of anybody else's. Served by `server.http` at `/`, which is the
	// listener `sign_in` puts the door on. Empty serves none.
	//
	// Two reasons it is this listener and not a fourth. A session is a
	// `__Host-` cookie, host-only, so a page and the listener it signs in at
	// have to be one host -- the same thing that moved the admin console onto
	// `admin.http` (#27). And which tenant a sign-in is about is the name the
	// browser arrived at (`Hosted`), so the page is served at the tenant's own
	// name and the tenant is whoever claims it -- which a listener of its own
	// would have to be told instead.
	//
	// It is **not** the admin console with a filter. The caller is the tenant's
	// own holder, narrowed by the wall; the admin console's is a roster
	// operator, and `admin.addr` waives two rules from the port (#34, #32).
	//
	// There are two pages, and each is the `console` of the listener that
	// serves it: `admin.console` is the admin console and `server.console` the
	// user console. The key says where a page is served because that is the
	// fact that pins it: a `__Host-` cookie is host-only, and the name a
	// request arrives at decides the tenant.
	Console ConsoleConfig `yaml:"console"`
}

// SignInConfig is the data plane's own door for a roster user.
//
// A block rather than a boolean because what a deployment will want to say next
// is about the same door -- which names it answers at, how hard a wrong answer
// is made -- and a `sign_in: true` that grows a sibling is a key that meant a
// block all along.
type SignInConfig struct {
	// Enabled is whether `AuthService` is registered on the data plane at all.
	//
	// Not *whether it refuses*: a method that is served and says no is a method
	// somebody can count answers from. Off, it is not on the wire.
	Enabled bool `yaml:"enabled"`
}

// ConsoleConfig is a built page, as the listener that names it serves it.
//
// Two of them now -- `admin.console` is the admin console and `server.console`
// is the user console -- and the type is one because what a listener needs to
// know about a page is where the files are. Which page it is, and which caller
// it is for, is whichever field holds this.
//
// One field, and the second one is gone with the arrangement that needed it.
// `admin` named another origin for the page to call, because the page was
// served by `control.http` and the tenants screen calls `admin.http` -- and a
// browser will not send a `__Host-` session cookie to a second host, so that
// arrangement could not work at all (#27). A page is served by the listener it
// calls now, and there is nothing to tell it.
type ConsoleConfig struct {
	// Dir is `ts/dist/console` or `ts/dist/user`, or wherever the build was
	// put.
	Dir string `yaml:"dir"`
}

// Serves reports whether this deployment checks who is calling.
func (c ControlConfig) Serves() bool { return c.Db.Driver != "" }

// Answers reports whether the control plane is reachable over the network.
func (c ControlConfig) Answers() bool { return c.Serves() && c.Addr != "" }

// Said reports whether this block says anything beyond which kind of database
// it is on.
//
// It is the other half of [ControlConfig.Serves], and it exists because that
// one reads `control.db.driver` **and nothing else**. That is the right field
// to read -- a control plane is a database, and an address is only how it is
// reached -- and what it leaves possible is a block with every field but that
// one, which builds a deployment that is not the one somebody wrote down. See
// [Build].
//
// Whatever the block said rather than its address alone, because
// `control.http.addr`, `control.watch.broker` and every limit under it are as
// inert without a database as `control.addr` is. Compared against the zero
// value with the driver taken out, so that the next field added here does not
// have to be remembered.
//
// The **driver** and not the whole `db` block, which is what was taken out.
// [ControlConfig.Serves] reads the one field, so the rest of `db` is as inert
// without it as an address is -- and is what somebody writes first: a block
// that says where the database is (`db.dsn`) with `driver:` misspelled, or
// left to a default it has not got, said nothing by this reading and started
// a deployment that believed every caller (#276).
func (c ControlConfig) Said() bool {
	c.Db.Driver = ""

	return !reflect.DeepEqual(c, ControlConfig{})
}

// said reports whether a listener block says anything at all.
//
// A function rather than a method because [config.ServerConfig] is payday's,
// and `admin:` is the one place an app has to ask this of it: every other
// listener answers the question with its own address, and this one is opened
// or not by something else entirely. See [Build].
func said(c config.ServerConfig) bool {
	return !reflect.DeepEqual(c, config.ServerConfig{})
}

// watch is which broker this plane publishes to, falling back to the kind the
// data plane named.
//
// The **name** and not the whole block: an outbox is about durability of one
// database's writes and the two planes have two databases, so inheriting it
// would turn one decision into two rows in a table nobody asked for.
//
// It is the one field that falls back, and it took a rewrite to be: this used
// to answer with a fresh `WatchConfig` carrying the data plane's broker
// whenever `control.watch.broker` was empty -- which threw away everything else
// the control block had said. A deployment that wrote `control.watch.outbox:
// true` and left the broker to be inherited, exactly as the field above invites
// it to, got no recorder and no drain: the setting was loaded, listed by
// `roster config env`, and dropped on the floor, so a crash between the commit
// and the publish still lost the key change it had paid a row per write to
// keep. Nothing said so, and nothing could have -- both arrangements build a
// working server.
//
// Filling the one field in rather than choosing between two blocks is also what
// keeps the next field added to `WatchConfig` from having to be remembered
// here.
func (c ControlConfig) watch(data config.WatchConfig) config.WatchConfig {
	if c.Watch.Broker == "" {
		c.Watch.Broker = data.Broker
	}

	return c.Watch
}

// Queued is this configuration as a command at a shell builds the deployment
// with: `watch.outbox` on, on each plane that publishes anywhere (#214).
//
// # Why a command writes to the outbox rather than publishing
//
// A write reaches a stream in two halves (`payday/watch`): the recorder notes
// it inside its transaction, and the interceptor of the call that made it
// publishes it once the call has answered -- in this process, to this
// process's broker. A command beside a serving deployment is a process of its
// own, and from there neither half reaches anybody. Its broker is its own:
// under `memory`, which is what one replica runs, nobody is subscribed to it,
// and under `postgres` it publishes from a queue nothing waits for before the
// process ends. And most of what the commands write is not a call at all --
// `app install`, `key add`, `provision`, `restore @tenant` call the server as
// Go, where there is no interceptor.
//
// The outbox is the half that outlives a process -- *a write that has to be
// published even if this process does not live long enough to publish it*,
// `outbox.proto` says -- and its recorder is told of every write the servers
// make, however they were called, in that write's own transaction. So a
// command's writes go there, and the process that serves publishes them: its
// drain runs wherever there is a broker ([Build]). That is how `roster holder
// disable` at a shell reaches an app following the stream, within
// `watch.drain_every` of being said.
//
// Not on a plane that publishes nowhere: that would be a queue nothing drains,
// which [Build] refuses, and a plane with no stream has nobody to tell.
func (c Config) Queued() Config {
	if c.Watch.Broker != config.BrokerNone {
		c.Watch.Outbox = true
	}
	// Only where there is a control plane: a `control:` block saying anything
	// but its database is refused, and this would be such a block.
	if c.Control.Serves() && c.Control.watch(c.Watch).Broker != config.BrokerNone {
		c.Control.Watch.Outbox = true
	}

	return c
}
