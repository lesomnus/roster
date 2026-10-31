package arrives

import (
	"net/url"
	"slices"
	"strings"
)

// Google's sign-in, and what it says about when it asked somebody.
//
// # Only when asked by name, and only where it was let
//
// Deleting an account reads when the provider last authenticated the person
// (`auth_time`, [Caller.Authenticated]). Google sends that only to an app that
// asks for it by name -- the `claims` parameter, with `auth_time` essential in
// the ID token -- and only where the app's Google Auth Platform project is
// published, verified and has *Session age claims* turned on, which it is not
// by default. So every round trip to Google's issuer asks ([asking]): a project
// with the setting answers with the time, and one without answers with none,
// which is a sign-in that cannot say.
//
// # And it does not ask again when asked
//
// Google lists the parameters its authorization endpoint takes, and `max_age`
// is not one of them; its `prompt` takes `none`, `consent` and
// `select_account`, and its own guide says it does not support Google Account
// reauthentication requests. So a round trip that wants a sign-in a moment ago
// ([Providers.Begin] with `again`) sends Google nothing more than any other
// round trip to it, and what it answers is when it last asked -- which is a
// moment ago when the browser was not signed in to Google.

// googleHosts is where Google signs people in and issues their ID tokens.
var googleHosts = []string{"accounts.google.com"}

// authTimeClaims asks for `auth_time` by name in the ID token, essential.
// Google uses it on every trip; Microsoft uses it when asking again without
// `max_age`, which otherwise makes the claim mandatory.
const authTimeClaims = `{"id_token":{"auth_time":{"essential":true}}}`

// google is whether an issuer is Google's sign-in, which decides what its
// authorization request asks for ([asking]).
//
// By the host and over https, as [Providers.microsoft] reads Microsoft's: the
// fact is the host, and a name or a path is the operator's to choose.
func (p *Providers) google(issuer string) bool {
	u, err := url.Parse(issuer)
	if err != nil || u.Host == "" {
		return false
	}
	if p.Google != "" {
		at, err := url.Parse(p.Google)

		return err == nil && at.Scheme == u.Scheme && strings.EqualFold(at.Host, u.Host)
	}

	return u.Scheme == "https" && slices.ContainsFunc(googleHosts, func(h string) bool { return strings.EqualFold(h, u.Host) })
}
