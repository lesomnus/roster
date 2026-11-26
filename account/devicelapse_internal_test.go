package account

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestACodeThatLapsedIsOverWhateverItWasAnswered: past its quarter of an
// hour a code is `expired_token` to the terminal and no code to the person,
// whether nobody answered it or roster refused the yes and the terminal never
// came back for the refusal. What is held is not told after the code's life,
// any more than a key is handed over after it.
func TestACodeThatLapsedIsOverWhateverItWasAnswered(t *testing.T) {
	for name, v := range map[string]*device{
		"unanswered": {},
		"refused":    {refused: "a terminal cannot be allowed more than you hold"},
	} {
		t.Run(name, func(t *testing.T) {
			x := require.New(t)

			d := held()
			v.tenant, v.expires = "contoso", time.Now().Add(-time.Second)
			ok, _ := d.put("dc", "BBBB-CCCC", v)
			x.True(ok)

			_, ok = d.claim("BBBB-CCCC", "contoso")
			x.False(ok, "a lapsed code was answered")

			token, why, said := d.polled("dc")
			x.Empty(token)
			x.Equal("expired_token", why)
			x.Empty(said, "a lapsed code told its refusal")
		})
	}
}
