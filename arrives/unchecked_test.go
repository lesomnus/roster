package arrives

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/lesomnus/payday/pdid"
	"github.com/lesomnus/payday/slug"

	rstr "github.com/lesomnus/roster/rstr"
)

// TestAnAddressNobodyCheckedNamesNobody: an address is read for who somebody
// is -- what they are called here, and whose name and photograph Slack has --
// only when the provider says it is verified.
//
// # What it was
//
// An address a provider does not say is verified is one the person typed into
// their own profile. Linking has always known it: an invitation is found by
// address only under `email_verified`, or Microsoft's `xms_edov`. Two other
// readers of the same address did not ask.
//
// `Enrolling` named a new person by the local part of whatever address the
// token carried, so the first stranger to type `ceo@contoso.example` at a
// directory that asks no proof was `ceo` in that tenant: in every list its
// administrators read and in every token its products are handed. And a
// tenant that fills its profiles from Slack had Slack asked who it has under
// that address, so the same stranger's account was given the real name and
// the photograph of whoever the address belongs to (#277).
func TestAnAddressNobodyCheckedNamesNobody(t *testing.T) {
	ctx := t.Context()

	// Somebody a directory signed in, with an address it carried and did not
	// check -- and then the same, checked.
	typed := Caller{
		Tenant: pdid.New(1), TenantAlias: "contoso", Provider: "example", Subject: "stranger-at-the-directory",
		Email: "ceo@contoso.example", Name: "A Stranger",
	}
	checked := typed
	checked.Verified = true

	t.Run("what they are called", func(t *testing.T) {
		x := require.New(t)
		enrolled := func(who Caller) string {
			r := &roster{}
			_, err := Enrolling()(ctx, r, who)
			x.NoError(err)
			x.Len(r.added, 1, "they were not enrolled, which is the policy's to decide and not the address's")

			return r.added[0].GetAlias()
		}

		got := enrolled(typed)
		x.NotEqual("ceo", got, "somebody was named by an address nobody checked")
		x.NoError(slug.Validate(got))
		x.Len(got, 7, "the name is not the one somebody with no address is given")

		x.Equal("ceo", enrolled(checked), "an address the provider checked no longer names them")
	})

	t.Run("whose name and photograph Slack has", func(t *testing.T) {
		x := require.New(t)

		// A workspace that has the address's owner, counting what it is asked.
		var lookups atomic.Int32
		var slack *httptest.Server
		m := http.NewServeMux()
		m.HandleFunc("/users.lookupByEmail", func(w http.ResponseWriter, r *http.Request) {
			lookups.Add(1)
			w.Header().Set("content-type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "user": map[string]any{"profile": map[string]any{
				"real_name": "The Chief", "image_512": slack.URL + "/chief.png", "is_custom_image": true,
			}}})
		})
		img := photo(t)
		m.HandleFunc("/chief.png", func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(img) })
		slack = httptest.NewServer(m)
		t.Cleanup(slack.Close)

		filled := func(who Caller) *roster {
			r := &roster{profile: rstr.TenantProfile_builder{Fill: true, SlackSecretRef: "env:CONTOSO_SLACK"}.Build()}
			p := onThisMachine(t, r)
			p.secret = func(string) (string, error) { return "xoxb-test", nil }
			p.SlackAPI = slack.URL
			x.NoError(p.Fill(ctx, pdid.New(2), who))

			return r
		}

		r := filled(typed)
		x.Zero(lookups.Load(), "Slack was asked who it has under an address nobody checked")
		x.Empty(r.name, "they were given the name of whoever the address belongs to")
		x.False(r.picture, "they were given the photograph of whoever the address belongs to")

		r = filled(checked)
		x.EqualValues(1, lookups.Load())
		x.Equal("The Chief", r.name, "an address the provider checked no longer finds them")
		x.True(r.picture)
	})
}
