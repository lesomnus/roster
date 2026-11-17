package cmd

import (
	"context"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/lesomnus/otx/log"
	"github.com/lesomnus/payday/pdid"
	"github.com/lesomnus/payday/rbac"

	"github.com/lesomnus/roster/internal/ent"
	"github.com/lesomnus/roster/internal/ent/holder"
)

// An alias is user-editable. The file authority binds durable identities so
// Holder.Realias, tenant renames and alias reuse cannot acquire its grants.
// Roles and groups retain readable names inside the immutable tenant namespace.
func validatePolicySubjects(r rbac.Rules) error {
	checkTenant := func(name string) error {
		t, err := rbac.Name(name)
		if err != nil {
			return err
		}
		id, err := pdid.Parse(t)
		if err != nil || id.IsZero() || id.String() != t {
			return fmt.Errorf("policy name %q: tenant must be its immutable UUID", name)
		}
		return nil
	}
	checkHolder := func(name string) error {
		if err := checkTenant(name); err != nil {
			return err
		}
		_, h, _ := strings.Cut(name, "/")
		id, err := pdid.Parse(h)
		if err != nil || id.IsZero() || id.String() != h {
			return fmt.Errorf("policy holder %q: holder must be its immutable UUID", name)
		}
		return nil
	}
	for name := range r.Roles {
		if err := checkTenant(name); err != nil {
			return err
		}
	}
	for name, group := range r.Groups {
		if err := checkTenant(name); err != nil {
			return err
		}
		for _, member := range group.Members {
			if err := checkHolder(member); err != nil {
				return err
			}
		}
	}
	for _, b := range r.Bindings {
		if b.Holder != "" {
			if err := checkHolder(b.Holder); err != nil {
				return err
			}
		}
	}
	return nil
}

// Resolving every subject at acceptance prevents a caller from creating a row
// with a predeclared ID to acquire a grant. The file must name existing holders.
//
// Existing, and not living. The check asked for a holder who is not erased,
// and a file that fails it is one a starting server does not start on -- so
// erasing somebody a file names, which their own tenant's administrator may
// do and which they may do themselves by leaving, decided whether every
// tenant was served after the next restart (#279). An erased holder's row
// stays: erased, forgotten, or forgotten with their tenant destroyed, it is
// the same row under the same identifier, so nobody can be made under it and
// the reason above holds without refusing them. They are granted nothing
// (`configuredGrants` reads who is there now, each time it is asked), and
// said in the log, so that whoever keeps `file` takes them out. Restored
// while their grace lasts, they hold what the file says again, since the
// file never stopped saying it.
//
// # Said when it becomes so
//
// The file is read every `policy.refresh`, five seconds unless said, and
// this is asked at every read. Nothing about it changes from one read to the
// next until somebody edits the file or somebody else is erased, so a line at
// each is a log nobody reads by the afternoon. It is said once at a start --
// the first read -- and again whenever who the file names that is erased
// changes: somebody else erased, somebody restored. One line, naming all of
// them and the file. When nobody is left there is nothing to name, and
// nothing is said.
//
// A subject no row has is still refused: that one is a file that is wrong.
func policySubjects(ctx context.Context, db *ent.Client, file string) func(rbac.Rules) error {
	// Reload outlives Build's context, so what is kept of it is what it
	// carries -- the log -- and not when it ends.
	base := context.WithoutCancel(ctx)

	// Who was last said, for the next read to tell a change from more of the
	// same. Under a lock of its own: a read is the refresh's, and a test's.
	var (
		mu   sync.Mutex
		said string
	)

	return func(r rbac.Rules) error {
		// Bound each database validation instead.
		ctx, cancel := context.WithTimeout(base, 5*time.Second)
		defer cancel()
		if err := validatePolicySubjects(r); err != nil {
			return err
		}
		names := map[string]bool{}
		for _, group := range r.Groups {
			for _, member := range group.Members {
				names[member] = true
			}
		}
		for _, b := range r.Bindings {
			if b.Holder != "" {
				names[b.Holder] = true
			}
		}
		var erased []string
		for name := range names {
			t, h, _ := strings.Cut(strings.TrimPrefix(name, "@"), "/")
			tenant, _ := pdid.Parse(t)
			who, _ := pdid.Parse(h)
			v, err := db.Holder.Query().Where(holder.IdEQ(who.Uuid()), holder.TenantIdEQ(tenant.Uuid())).Only(ctx)
			if ent.IsNotFound(err) {
				return fmt.Errorf("policy holder %s: no holder in this tenant has this identifier, and a file names somebody who is already there", name)
			}
			if err != nil {
				return fmt.Errorf("policy holder %s: %w", name, err)
			}
			if v.DateErased != nil {
				erased = append(erased, name)
			}
		}
		slices.Sort(erased)
		now := strings.Join(erased, " ")

		mu.Lock()
		changed := now != said
		said = now
		mu.Unlock()

		if changed && len(erased) > 0 {
			// A warning and not an error: nothing is wrong with the server,
			// and the file goes on naming somebody until it is edited.
			log.From(ctx).WarnContext(ctx, "policy: the file names somebody who has been erased, and grants them nothing; take them out of it",
				slog.String("file", file), slog.Any("holders", erased))
		}
		return nil
	}
}
