package cmd_test

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/goccy/go-yaml"
	"github.com/stretchr/testify/require"
)

// TestTheQuickStartPublishesToThisMachineAlone: every port `compose.yaml`
// publishes is bound to `${BIND:-127.0.0.1}`, so the quick start is reached
// from this machine unless whoever runs it says `BIND` otherwise.
//
// Each was published with no address in front, which Docker binds on every
// interface: the admin console with the operator `admin` / `admin`, the
// control plane, Hydra's admin API, the directory, the SCIM endpoint and the
// mailbox each answered another machine on the network, with credentials that
// are the same in every checkout. The file's head said `localhost` only, which
// was a sentence about a browser's cookie.
func TestTheQuickStartPublishesToThisMachineAlone(t *testing.T) {
	x := require.New(t)

	src, err := os.ReadFile(filepath.Join("..", "compose.yaml"))
	x.NoError(err)

	var file struct {
		Services map[string]struct {
			Ports []any `yaml:"ports"`
		} `yaml:"services"`
	}
	x.NoError(yaml.Unmarshal(src, &file))

	names := make([]string, 0, len(file.Services))
	for name := range file.Services {
		names = append(names, name)
	}
	sort.Strings(names)

	published := 0
	for _, name := range names {
		for _, p := range file.Services[name].Ports {
			published++
			v, ok := p.(string)
			x.True(ok, "%s publishes a port in the long form, which this test does not read: %v", name, p)
			x.True(strings.HasPrefix(v, "${BIND:-127.0.0.1}:"), "%s publishes %q on every interface", name, v)
		}
	}
	x.NotZero(published, "compose.yaml publishes nothing, or this test no longer reads it")
}
