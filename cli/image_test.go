package cli

import (
	"bytes"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/lesomnus/roster/cmd"
)

// imageStages is the Dockerfile's stages in the order it writes them, each
// with the `USER` it last says, which is what the image runs as.
func imageStages(t *testing.T) (names []string, users map[string]string, text string) {
	t.Helper()

	b, err := os.ReadFile("../Dockerfile")
	require.NoError(t, err)

	from := regexp.MustCompile(`(?i)^FROM\s+\S+(?:\s+\S+)*\s+AS\s+(\S+)\s*$`)
	user := regexp.MustCompile(`(?i)^USER\s+(\S+)\s*$`)

	users = map[string]string{}
	for _, line := range strings.Split(string(b), "\n") {
		if m := from.FindStringSubmatch(line); m != nil {
			names = append(names, m[1])
			continue
		}
		if m := user.FindStringSubmatch(line); m != nil && len(names) > 0 {
			users[names[len(names)-1]] = m[1]
		}
	}

	return names, users, string(b)
}

// TestAPlainBuildMakesTheImageADeploymentRuns: `docker build .` with no
// `--target` builds the Dockerfile's last stage, and that is `app`.
//
// `dev` was last, so a plain build made the compose image: Alpine, root, a
// shell, and an entrypoint that seeds an operator `admin` with the password
// `admin` where nothing else is said. Every file of this repository that builds
// names its stage; whoever builds by hand names none and gets the last. And
// `app` runs as 65532 by number: a kubelet told `runAsNonRoot` with no
// `runAsUser` cannot check that a name is not root, and refuses the pod.
func TestAPlainBuildMakesTheImageADeploymentRuns(t *testing.T) {
	x := require.New(t)

	names, users, _ := imageStages(t)
	x.NotEmpty(names, "no stage was read out of the Dockerfile")
	x.Equal("app", names[len(names)-1], "a build with no --target makes the last stage")
	x.Equal("65532:65532", users["app"], "the image a deployment runs names its user by number")
}

// TestAnImageSaysWhichCommitItIs: `roster version --short` in an image prints
// the commit it was built from.
//
// It printed an empty line in every image: the toolchain stamps the commit from
// `.git/`, which no build of the Dockerfile has, and the commit was the image's
// label alone. The build hands it in, as it hands in the version, and the
// binary says it the way a build at a desk does.
func TestAnImageSaysWhichCommitItIs(t *testing.T) {
	x := require.New(t)

	was := revision
	t.Cleanup(func() { revision = was })
	revision = "1cb95a33c650cec616f83ee93b907aabdd15511f"

	var out bytes.Buffer
	k := Cmd(&cmd.Config{})
	k.Writer = &out
	x.NoError(k.Run(t.Context(), []string{"version", "--short"}))
	x.Equal("1cb95a33c650\n", out.String(), "a binary told its commit at the build says it")

	_, _, text := imageStages(t)
	x.True(strings.Contains(text, "-X github.com/lesomnus/roster/cli.revision=${APP_REVISION}"),
		"the Dockerfile stamps the variable `version` reads")
}
