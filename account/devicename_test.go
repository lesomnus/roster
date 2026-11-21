package account_test

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/lesomnus/roster/account"
)

// TestATerminalIsCalledWhatItsKeyWillBe: what a terminal asks to be called is
// a name somebody typed, or a machine's own, and the key made for it is named
// in small letters as every key is. So the name is folded where the terminal
// says it, and the page somebody approves it on shows the name the key will
// have.
//
// roster folded it at the write, without saying: a terminal that asked to be
// `Erins-Laptop` was shown to her as that, and the key she then found on her
// account was `erins-laptop`. A write that says an alias in another spelling
// is refused now (`TestAnAliasIsWrittenAsItWillBeRead`), which here would be
// an approval answered *the key could not be made*; a front door says the
// spelling it means.
func TestATerminalIsCalledWhatItsKeyWillBe(t *testing.T) {
	x := require.New(t)

	d := serve(t, account.Invited(), terminal)
	mints(t, d)
	term := d.browser(t, "contoso.test")
	her := d.browser(t, "contoso.test")

	code, body := her.do(t, http.MethodPost, "/session", `{"alias":"erin","password":"correct horse battery staple"}`,
		func(r *http.Request) { r.Header.Set("Content-Type", "application/json") })
	x.Equal(http.StatusNoContent, code, body)

	code, body = term.do(t, http.MethodPost, "/device/begin",
		`{"name":" Erins-Laptop ","allow":["/roster.MeService/Get"]}`, nil)
	x.Equal(http.StatusOK, code, body)
	var began struct {
		DeviceCode string `json:"device_code"`
		UserCode   string `json:"user_code"`
	}
	x.NoError(json.Unmarshal([]byte(body), &began))

	code, body = her.do(t, http.MethodGet, "/device/pending?user_code="+began.UserCode, "", nil)
	x.Equal(http.StatusOK, code, body)
	x.Contains(body, `"erins-laptop"`, "the page says what the key will be called")
	x.NotContains(body, "Erins-Laptop")

	code, body = her.do(t, http.MethodPost, "/device/approve", `{"user_code":"`+began.UserCode+`"}`, nil)
	x.Equal(http.StatusOK, code, body)

	code, body = asked(t, term, began.DeviceCode)
	x.Equal(http.StatusOK, code, body)

	code, body = her.rpc(t, "/roster.MeService/Get", `{}`)
	x.Equal(http.StatusOK, code, body)
	x.Contains(body, `"erins-laptop"`, "and it is: %s", body)
}
