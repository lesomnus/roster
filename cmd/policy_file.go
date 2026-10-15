package cmd

import (
	"context"
	"fmt"
	"strings"
	"time"

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
func policySubjects(db *ent.Client) func(rbac.Rules) error {
	return func(r rbac.Rules) error {
		// Reload outlives Build's context; bound each database validation instead.
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
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
		for name := range names {
			t, h, _ := strings.Cut(strings.TrimPrefix(name, "@"), "/")
			tenant, _ := pdid.Parse(t)
			who, _ := pdid.Parse(h)
			exists, err := db.Holder.Query().Where(holder.IdEQ(who.Uuid()), holder.TenantIdEQ(tenant.Uuid()), holder.DateErasedIsNil()).Exist(ctx)
			if err != nil {
				return fmt.Errorf("policy holder %s: %w", name, err)
			}
			if !exists {
				return fmt.Errorf("policy holder %s: no live holder in this tenant", name)
			}
		}
		return nil
	}
}
