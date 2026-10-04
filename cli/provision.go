package cli

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"strings"

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
	n, err := nominateAs(ctx, s, accountProvisioned, methods, borrower, accountProvisioned, "config: account")
	if err != nil {
		return "", 0, err
	}

	return token, n, nil
}

// holderNamed is a holder called `alias` in the tenant, made if there is none,
// and whether this call made it.
//
// Whether it was made is the question the callers have to ask next, and it is
// why this no longer answers with the row alone. A holder that was already
// there is somebody's: a person a tenant called `account`, its administrator
// called `admin`, an app's holder from before. Handing it an app's role hands
// whoever signs in as it the app's methods, and answering the app's key as it
// hands the app whatever it already held -- so a holder found is looked at
// ([holdingOf]) before it is used, and a holder made needs nothing.
//
// `labels` are written on a holder it makes, and added to one it finds; nil
// writes none. A front door's own holder is declared by the configuration that
// turns the front door on, and carries [cmd.Declared] to say so.
func holderNamed(ctx context.Context, s *cmd.Server, at *rstr.TenantRef, alias string, labels map[string]string) ([]byte, bool, error) {
	v, err := s.Ungated.Holder().Add(ctx, rstr.HolderAddRequest_builder{Tenant: at, Alias: alias, Labels: labels}.Build())
	if err == nil {
		return v.GetId(), true, nil
	}
	if status.Code(err) != codes.AlreadyExists {
		return nil, false, err
	}

	got, err := s.Ungated.Holder().Get(ctx, rstr.HolderGetRequest_builder{
		Ref: rstr.HolderRef_builder{
			Slug: rstr.HolderRefBySlug_builder{Alias: z.Ptr(alias), Tenant: at}.Build(),
		}.Build(),
		Select: rstr.HolderSelect_builder{Labels: z.Ptr(true), DateUpdated: z.Ptr(true)}.Build(),
	}.Build())
	if err != nil {
		return nil, false, err
	}
	if next, ok := withLabels(got.GetLabels(), labels); ok {
		if _, err := s.Ungated.Holder().Patch(ctx, rstr.HolderPatchRequest_builder{
			Ref: rstr.HolderRef_builder{Id: got.GetId()}.Build(), Labels: next, DateUpdated: got.GetDateUpdated(),
		}.Build()); err != nil {
			return nil, false, err
		}
	}

	return got.GetId(), false, nil
}

// withLabels is `have` with `want` over it, and whether that differs from
// `have` -- so a row already carrying them is not written again.
func withLabels(have, want map[string]string) (map[string]string, bool) {
	changed := false
	next := make(map[string]string, len(have)+len(want))
	for k, v := range have {
		next[k] = v
	}
	for k, v := range want {
		if next[k] != v {
			next[k] = v
			changed = true
		}
	}

	return next, changed
}

// holding is what a holder already has: what an app answered as it would be
// answered with too, and the ways somebody signs in as it.
type holding struct {
	roles       []string
	credentials int
	identities  int
	emails      int
	keys        int
}

// wayIn is whether somebody signs in as this holder: a person, or a row people
// share. An app's role on it is that somebody's.
func (h holding) wayIn() bool { return h.credentials+h.identities+h.emails > 0 }

func (h holding) empty() bool { return len(h.roles) == 0 && !h.wayIn() && h.keys == 0 }

func (h holding) String() string {
	out := []string{}
	if len(h.roles) > 0 {
		out = append(out, "role "+strings.Join(h.roles, ", "))
	}
	for _, v := range []struct {
		n    int
		what string
	}{
		{h.credentials, "credential"}, {h.identities, "identity"}, {h.emails, "email"}, {h.keys, "key"},
	} {
		if v.n > 0 {
			out = append(out, fmt.Sprintf("%d %s(s)", v.n, v.what))
		}
	}
	if len(out) == 0 {
		return "nothing"
	}

	return strings.Join(out, "; ")
}

// holdingOf reads what `who` holds, leaving out bindings to `except`: the role
// being given, which a second run finds already bound and which is not news.
func holdingOf(ctx context.Context, s *cmd.Server, who []byte, except []byte) (holding, error) {
	ref := rstr.HolderRef_builder{Id: who}.Build()
	out := holding{}

	seen := map[string]bool{}
	after := ""
	for {
		vs, err := s.Ungated.Binding().List(ctx, rstr.BindingListRequest_builder{
			Filters: []*rstr.BindingFilter{rstr.BindingFilter_builder{Holder: ref}.Build()},
			Size:    100,
			After:   after,
		}.Build())
		if err != nil {
			return holding{}, err
		}
		for _, v := range vs.GetItems() {
			r := v.GetRole().GetId()
			if bytes.Equal(r, except) || seen[string(r)] {
				continue
			}
			seen[string(r)] = true

			got, err := s.Ungated.Role().Get(ctx, rstr.RoleGetRequest_builder{
				Ref:    rstr.RoleRef_builder{Id: r}.Build(),
				Select: rstr.RoleSelect_builder{Alias: z.Ptr(true)}.Build(),
			}.Build())
			if err != nil {
				return holding{}, err
			}
			out.roles = append(out.roles, got.GetAlias())
		}
		if after = vs.GetNext(); after == "" {
			break
		}
	}

	cs, err := s.Ungated.Credential().List(ctx, rstr.CredentialListRequest_builder{
		Filters: []*rstr.CredentialFilter{rstr.CredentialFilter_builder{Holder: ref}.Build()},
	}.Build())
	if err != nil {
		return holding{}, err
	}
	out.credentials = len(cs.GetItems())

	is, err := s.Ungated.Identity().List(ctx, rstr.IdentityListRequest_builder{
		Filters: []*rstr.IdentityFilter{rstr.IdentityFilter_builder{Holder: ref}.Build()},
	}.Build())
	if err != nil {
		return holding{}, err
	}
	out.identities = len(is.GetItems())

	es, err := s.Ungated.Email().List(ctx, rstr.EmailListRequest_builder{
		Filters: []*rstr.EmailFilter{rstr.EmailFilter_builder{Holder: ref}.Build()},
	}.Build())
	if err != nil {
		return holding{}, err
	}
	out.emails = len(es.GetItems())

	ks, err := s.Ungated.ApiKey().List(ctx, rstr.ApiKeyListRequest_builder{
		Filters: []*rstr.ApiKeyFilter{rstr.ApiKeyFilter_builder{Holder: ref}.Build()},
	}.Build())
	if err != nil {
		return holding{}, err
	}
	out.keys = len(ks.GetItems())

	return out, nil
}

// roleNamed is the role called `alias` in the tenant, or nil when there is none.
func roleNamed(ctx context.Context, s *cmd.Server, at *rstr.TenantRef, alias string) ([]byte, error) {
	v, err := s.Ungated.Role().Get(ctx, rstr.RoleGetRequest_builder{
		Ref: rstr.RoleRef_builder{
			Slug: rstr.RoleRefBySlug_builder{Alias: z.Ptr(alias), Tenant: at}.Build(),
		}.Build(),
		Select: rstr.RoleSelect_builder{}.Build(),
	}.Build())
	if status.Code(err) == codes.NotFound {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	return v.GetId(), nil
}

// boundToOthers is whether `role` is bound to anybody but `who`.
//
// The question [ensureRoleNamed] has to ask before it rewrites a role it finds
// by name: a tenant's own role of the same name, bound to its people, would be
// rewritten to an app's methods -- and every one of them would hold those.
func boundToOthers(ctx context.Context, s *cmd.Server, role, who []byte) (bool, error) {
	after := ""
	for {
		vs, err := s.Ungated.Binding().List(ctx, rstr.BindingListRequest_builder{
			Filters: []*rstr.BindingFilter{rstr.BindingFilter_builder{Role: rstr.RoleRef_builder{Id: role}.Build()}.Build()},
			Size:    100,
			After:   after,
		}.Build())
		if err != nil {
			return false, err
		}
		for _, v := range vs.GetItems() {
			if !bytes.Equal(v.GetHolder().GetId(), who) || len(v.GetGroup().GetId()) > 0 {
				return true, nil
			}
		}
		if after = vs.GetNext(); after == "" {
			return false, nil
		}
	}
}

// ensureRoleNamed is a role called `alias` holding exactly `methods`.
//
// Patched when it is already there rather than left alone: the list grows with
// the app, and a role written by an older version is an app that starts and
// then refuses one thing.
func ensureRoleNamed(ctx context.Context, s *cmd.Server, at *rstr.TenantRef, alias string, methods []string, labels map[string]string) ([]byte, error) {
	v, err := s.Ungated.Role().Add(ctx, rstr.RoleAddRequest_builder{
		Tenant: at, Alias: alias, Methods: methods, Labels: labels,
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
		Select: rstr.RoleSelect_builder{DateUpdated: z.Ptr(true), Labels: z.Ptr(true)}.Build(),
	}.Build())
	if err != nil {
		return nil, err
	}
	next, _ := withLabels(got.GetLabels(), labels)
	if _, err := s.Ungated.Role().Patch(ctx, rstr.RolePatchRequest_builder{
		Ref:         rstr.RoleRef_builder{Id: got.GetId()}.Build(),
		Methods:     methods,
		Labels:      next,
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
func ensureBinding(ctx context.Context, s *cmd.Server, role, who []byte, labels map[string]string) error {
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
		if len(v.GetSite().GetId()) != 0 {
			continue
		}
		if len(labels) == 0 {
			return nil
		}

		got, err := s.Ungated.Binding().Get(ctx, rstr.BindingGetRequest_builder{
			Ref:    rstr.BindingRef_builder{Id: v.GetId()}.Build(),
			Select: rstr.BindingSelect_builder{Labels: z.Ptr(true), DateUpdated: z.Ptr(true)}.Build(),
		}.Build())
		if err != nil {
			return err
		}
		if next, ok := withLabels(got.GetLabels(), labels); ok {
			_, err = s.Ungated.Binding().Patch(ctx, rstr.BindingPatchRequest_builder{
				Ref: rstr.BindingRef_builder{Id: v.GetId()}.Build(), Labels: next, DateUpdated: got.GetDateUpdated(),
			}.Build())
		}

		return err
	}

	_, err = s.Ungated.Binding().Add(ctx, rstr.BindingAddRequest_builder{
		Role:   rstr.RoleRef_builder{Id: role}.Build(),
		Holder: rstr.HolderRef_builder{Id: who}.Build(),
		Labels: labels,
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
	if err := eraseKeyNamed(ctx, at, who, alias); err != nil {
		return "", err
	}

	token, sum, err := keys.Mint(prefix)
	if err != nil {
		return "", err
	}
	add := func() error {
		_, err := at.ApiKey().Add(ctx, rstr.ApiKeyAddRequest_builder{
			Holder: rstr.HolderRef_builder{Id: who}.Build(), Alias: alias,
			Secret: sum, Methods: methods,
		}.Build())

		return err
	}
	if err := add(); status.Code(err) == codes.AlreadyExists {
		// Another run minted one between the erase and this -- two provisions
		// at once. The key to keep is this run's, which is the one about to be
		// written to a file; theirs is erased again, once.
		if err := eraseKeyNamed(ctx, at, who, alias); err != nil {
			return "", err
		}
		if err := add(); err != nil {
			return "", err
		}
	} else if err != nil {
		return "", err
	}

	return token, nil
}

// eraseKeyNamed erases the key called `alias` on a holder, if there is one.
func eraseKeyNamed(ctx context.Context, at rstr.Server, who []byte, alias string) error {
	v, err := at.ApiKey().Get(ctx, rstr.ApiKeyGetRequest_builder{
		Ref: rstr.ApiKeyRef_builder{
			Slug: rstr.ApiKeyRefBySlug_builder{Holder: rstr.HolderRef_builder{Id: who}.Build(), Alias: z.Ptr(alias)}.Build(),
		}.Build(),
		Select: rstr.ApiKeySelect_builder{}.Build(),
	}.Build())
	if status.Code(err) == codes.NotFound {
		return nil
	}
	if err != nil {
		return err
	}

	_, err = at.ApiKey().Erase(ctx, rstr.ApiKeyRef_builder{Id: v.GetId()}.Build())

	return err
}

// writeKey puts a token into a file that is either whole or not there.
//
// `0600` in a directory made at `0700`: what is written is a credential, and
// the only reader is the process beside it. Written beside and moved into
// place, because that reader waits on the file's existence (`docker/account.sh`)
// and a half-written key is a refused start with nothing saying why.
//
// The leftover of a run that died between the two is removed first: `WriteFile`
// keeps the mode of a file that is already there, so a `.tmp` somebody made
// readable would have handed its mode to the key written into it.
func writeKey(path, token string) error {
	tmp := path + ".tmp"
	if err := os.Remove(tmp); err != nil && !os.IsNotExist(err) {
		return err
	}

	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.WriteString(token + "\n"); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}

	return os.Rename(tmp, path)
}
