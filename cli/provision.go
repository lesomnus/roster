package cli

import (
	"context"
	"os"

	"github.com/lesomnus/z"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/lesomnus/roster/account"
	"github.com/lesomnus/roster/cmd"
	rstr "github.com/lesomnus/roster/rstr"
	"github.com/lesomnus/roster/server/keys"
)

// What a front door needs before it can be one, made where it is used.
//
// One deployment key on the control plane, allowed only what the app asks
// before a request names a tenant; and in each tenant it fronts a holder of its
// own, a role holding exactly what it calls as itself, the binding between them,
// and the nomination that answers the key as that holder there. `roster login
// provision` has written those for the Login App since #36 and #73; this is the
// same for the account app since #76, and the helpers the two share. Neither
// makes a tenant: a customer is the tenant's, and a command that made one by
// mentioning it would be a way to write rows into somebody else's by typo.
//
// # Which tenants
//
// The ones with a `Host` row. A tenant that registered a name is a tenant a
// front door fronts (#42), so *who do we front* is a query and not a list. A
// tenant with no name yet is unreachable from a browser whatever key the app
// holds, so there is nothing to nominate in, and one that registers a name after
// this ran is nominated in on the next run -- which the running app then finds
// for itself, by its own nominations, without a restart.
//
// # Where the key goes
//
// Into a file when `out` names a directory -- `roster account provision`, run
// beside a process of its own -- and into memory when it does not, which is
// what `roster serve` asks for when `account.key` and `account.keys` say nothing: a key made at
// start, which is one replica, exactly as `account.seal` reads an empty value.
// Either way the row is a key like any other. It is in the trail, it can be
// revoked, and a restart is a rotation -- so what a deployment gives up by
// writing nothing down is only what a rotation costs, which is that the
// account page asks everybody to sign in again.

// accountProvisioned is what the account app's rows are called, so that a
// later run finds them and a person reading a list can tell them from
// somebody's. `docker/customer.sh` has minted a holder and a key of this name
// by hand for as long as it existed, which is what makes this the replacement
// for a runbook step rather than a second name for one.
const accountProvisioned = "account"

// provisionAccount ensures the account app's rows in every tenant that has a
// name, mints one key per tenant, and answers with the keys by alias.
func provisionAccount(ctx context.Context, s *cmd.Server, enrol string, out string) (string, int, error) {
	methods := account.Calls
	if enrol == "enrolling" {
		methods = append(append([]string{}, account.Calls...), rstr.HolderService_Add_FullMethodName)
	}

	if out != "" {
		if err := os.MkdirAll(out, 0o700); err != nil {
			return "", 0, err
		}
	}

	// One key, the account app's own on the control plane, allowed only what it
	// asks before a request names a tenant; and in each tenant with a name, the
	// holder it is answered as there. The Login App's arrangement, by the same
	// two functions (#76).
	token, borrower, err := provisionDeploymentKey(ctx, s, accountProvisioned, account.Resolving, out)
	if err != nil {
		return "", 0, err
	}
	n, err := nominateAs(ctx, s, accountProvisioned, methods, borrower)
	if err != nil {
		return "", 0, err
	}

	return token, n, nil
}

// ensureHolderNamed is a holder called `alias` in the tenant, made if there is
// none.
func ensureHolderNamed(ctx context.Context, s *cmd.Server, at *rstr.TenantRef, alias string) ([]byte, error) {
	v, err := s.Ungated.Holder().Add(ctx, rstr.HolderAddRequest_builder{Tenant: at, Alias: alias}.Build())
	if err == nil {
		return v.GetId(), nil
	}
	if status.Code(err) != codes.AlreadyExists {
		return nil, err
	}

	got, err := s.Ungated.Holder().Get(ctx, rstr.HolderGetRequest_builder{
		Ref: rstr.HolderRef_builder{
			Slug: rstr.HolderRefBySlug_builder{Alias: z.Ptr(alias), Tenant: at}.Build(),
		}.Build(),
		Select: rstr.HolderSelect_builder{}.Build(),
	}.Build())
	if err != nil {
		return nil, err
	}

	return got.GetId(), nil
}

// ensureRoleNamed is a role called `alias` holding exactly `methods`.
//
// Patched when it is already there rather than left alone: the list grows with
// the app, and a role written by an older version is an app that starts and
// then refuses one thing.
func ensureRoleNamed(ctx context.Context, s *cmd.Server, at *rstr.TenantRef, alias string, methods []string) ([]byte, error) {
	v, err := s.Ungated.Role().Add(ctx, rstr.RoleAddRequest_builder{
		Tenant: at, Alias: alias, Methods: methods,
	}.Build())
	if err == nil {
		return v.GetId(), nil
	}
	if status.Code(err) != codes.AlreadyExists {
		return nil, err
	}

	got, err := s.Ungated.Role().Get(ctx, rstr.RoleGetRequest_builder{
		Ref: rstr.RoleRef_builder{
			Slug: rstr.RoleRefBySlug_builder{Alias: z.Ptr(alias), Tenant: at}.Build(),
		}.Build(),
		// `date_updated` because a patch is refused without the version it is
		// against -- which is the rule keeping two writers from each thinking
		// they wrote last.
		Select: rstr.RoleSelect_builder{DateUpdated: z.Ptr(true)}.Build(),
	}.Build())
	if err != nil {
		return nil, err
	}
	if _, err := s.Ungated.Role().Patch(ctx, rstr.RolePatchRequest_builder{
		Ref:         rstr.RoleRef_builder{Id: got.GetId()}.Build(),
		Methods:     methods,
		DateUpdated: got.GetDateUpdated(),
	}.Build()); err != nil {
		return nil, err
	}

	return got.GetId(), nil
}

// ensureBinding binds the role to the holder across the tenant, once.
//
// # Looked for first, because nothing refuses a second
//
// This used to `Add` and treat `AlreadyExists` as done -- and `Binding` has no
// unique index for it to come back with, since the same role may rightly be
// bound to the same holder at two sites. So every start added one more: a
// deployment found with sixty-three identical `login-app` bindings on one
// holder, one per name per restart. They grant nothing extra and are noise in
// every screen and every audit of who holds what.
//
// The tenant-wide one is what this writes, so a binding of the same pair at a
// site is not it and does not count.
func ensureBinding(ctx context.Context, s *cmd.Server, role, who []byte) error {
	vs, err := s.Ungated.Binding().List(ctx, rstr.BindingListRequest_builder{
		Filters: []*rstr.BindingFilter{rstr.BindingFilter_builder{
			Role:   rstr.RoleRef_builder{Id: role}.Build(),
			Holder: rstr.HolderRef_builder{Id: who}.Build(),
		}.Build()},
	}.Build())
	if err != nil {
		return err
	}
	for _, v := range vs.GetItems() {
		if len(v.GetSite().GetId()) == 0 {
			return nil
		}
	}

	_, err = s.Ungated.Binding().Add(ctx, rstr.BindingAddRequest_builder{
		Role:   rstr.RoleRef_builder{Id: role}.Build(),
		Holder: rstr.HolderRef_builder{Id: who}.Build(),
	}.Build())
	if err != nil && status.Code(err) != codes.AlreadyExists {
		return err
	}

	return nil
}

// mintNamed replaces the key called `alias` on a holder, and answers with the
// token once.
//
// Replaced rather than added to: a key's alias is unique per holder and a key
// cannot be read back, so the row from the last run is of no use to anybody
// and is one more thing that would answer if it leaked. A restart is a
// rotation, and nothing accumulates.
func mintNamed(ctx context.Context, at rstr.Server, who []byte, alias string, methods []string, prefix string) (string, error) {
	if v, err := at.ApiKey().Get(ctx, rstr.ApiKeyGetRequest_builder{
		Ref: rstr.ApiKeyRef_builder{
			Slug: rstr.ApiKeyRefBySlug_builder{Holder: rstr.HolderRef_builder{Id: who}.Build(), Alias: z.Ptr(alias)}.Build(),
		}.Build(),
		Select: rstr.ApiKeySelect_builder{}.Build(),
	}.Build()); err == nil {
		if _, err := at.ApiKey().Erase(ctx, rstr.ApiKeyRef_builder{Id: v.GetId()}.Build()); err != nil {
			return "", err
		}
	} else if status.Code(err) != codes.NotFound {
		return "", err
	}

	token, sum, err := keys.Mint(prefix)
	if err != nil {
		return "", err
	}
	if _, err := at.ApiKey().Add(ctx, rstr.ApiKeyAddRequest_builder{
		Holder: rstr.HolderRef_builder{Id: who}.Build(), Alias: alias,
		Secret: sum, Methods: methods,
	}.Build()); err != nil {
		return "", err
	}

	return token, nil
}

// writeKey puts a token into a file that is either whole or not there.
//
// `0600` in a directory made at `0700`: what is written is a credential, and
// the only reader is the process beside it. Written beside and moved into
// place, because that reader waits on the file's existence (`docker/account.sh`)
// and a half-written key is a refused start with nothing saying why.
func writeKey(path, token string) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(token+"\n"), 0o600); err != nil {
		return err
	}

	return os.Rename(tmp, path)
}
