package arrives

import (
	"bytes"
	"errors"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/lesomnus/payday/pdid"
)

// TestTheLogOfSomebodyErasedArrivingNamesNoSubject: somebody erased who
// arrives at a front door is refused, and the door says so in its log for
// whoever runs it: which tenant, which holder, which provider, and which of
// the two ways they were found. It said their subject at the provider too. A
// subject is declared personal (`Identity.subject`), and a log is kept by
// nothing roster reaches: once the person is forgotten, that line is the one
// place left that ties the identifier their row keeps to who they were at a
// provider. Right is the line without it.
//
// It costs an administrator nothing. What they do next -- bring the person
// back, or let go of what they hold -- names the holder, and the identifier
// is the name that still finds them: their alias came free with the erase.
func TestTheLogOfSomebodyErasedArrivingNamesNoSubject(t *testing.T) {
	x := require.New(t)

	said := &bytes.Buffer{}
	was := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(said, nil)))
	t.Cleanup(func() { slog.SetDefault(was) })

	holder := pdid.New(1)
	err := erased(t.Context(), Caller{
		Tenant: pdid.New(2), TenantAlias: "contoso", Provider: "entra", Subject: "subject-4417", Email: "hana.kim@corp.example", Name: "Hana Kim",
	}, holder, "the account they had")
	x.True(errors.Is(err, ErrErased))

	line := said.String()
	x.Contains(line, holder.String(), "the holder is not named, so there is nobody to bring back or let go: %s", line)
	x.Contains(line, "contoso", "%s", line)
	x.Contains(line, "entra", "%s", line)
	x.Contains(line, "the account they had", "which way they were found is not said: %s", line)

	x.NotContains(line, "subject-4417", "the log ties a holder's identifier to their subject at a provider, and outlives their being forgotten: %s", line)
	x.NotContains(line, "hana.kim@corp.example", "%s", line)
	x.NotContains(line, "Hana Kim", "%s", line)
}
