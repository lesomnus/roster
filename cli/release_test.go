package cli_test

import (
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/goccy/go-yaml"
	"github.com/stretchr/testify/require"
)

// workflowFile is what a test here holds of a workflow: its triggers, its
// permissions, and each job's runner, permissions and steps.
type workflowFile struct {
	Name        string            `yaml:"name"`
	On          map[string]any    `yaml:"on"`
	Permissions map[string]string `yaml:"permissions"`
	Jobs        map[string]struct {
		RunsOn      string            `yaml:"runs-on"`
		If          string            `yaml:"if"`
		Permissions map[string]string `yaml:"permissions"`
		Steps       []struct {
			Name string `yaml:"name"`
			ID   string `yaml:"id"`
			If   string `yaml:"if"`
			Uses string `yaml:"uses"`
			Run  string `yaml:"run"`
		} `yaml:"steps"`
	} `yaml:"jobs"`
}

// readWorkflow reads `.github/workflows/<name>`, as text and as YAML.
func readWorkflow(t *testing.T, root, name string) (string, workflowFile) {
	t.Helper()

	src, err := os.ReadFile(filepath.Join(root, ".github", "workflows", name))
	require.NoError(t, err)

	var w workflowFile
	require.NoError(t, yaml.Unmarshal(src, &w), "%s is not YAML", name)

	return string(src), w
}

// TestATagNamesTheImageMainPushedAndBuildsNothing holds `release.yml` to what
// a release is: a `v*` tag names the image `ci` already pushed from `main` for
// the tagged commit, and nothing is built for it.
//
// There was no such workflow. A tag of this repository named a commit and no
// image, and an operator went from a commit to a digest by its `ci` run's id.
// The image a release names is right only because it is one `push` wrote, and
// `push` writes only after every gate of that run: so no step here builds or
// pushes an image of its own, the one write is `imagetools create` of a name
// for a digest already there, and it is written only for a tag that was
// pushed -- a run started by hand finds the image and names nothing. And the
// token it holds can list runs and write a package, and nothing else: no
// `id-token`, since nothing is signed again.
//
// What it cannot hold is that the road works, which is a run's to show: the
// workflow runs only once it is on `main`.
func TestATagNamesTheImageMainPushedAndBuildsNothing(t *testing.T) {
	x := require.New(t)
	root := repoRoot(t)

	src, w := readWorkflow(t, root, "release.yml")
	_, ci := readWorkflow(t, root, "ci.yml")

	x.Equal("release", w.Name)

	triggers := make([]string, 0, len(w.On))
	for k := range w.On {
		triggers = append(triggers, k)
	}
	slices.Sort(triggers)
	x.Equal([]string{"push", "workflow_dispatch"}, triggers, "a release starts on a tag pushed, or by hand, and on nothing else")
	x.Equal(map[string]any{"tags": []any{"v*"}}, w.On["push"], "the push it starts on is a `v*` tag's, and no branch's")

	x.Equal(map[string]string{"contents": "read"}, w.Permissions, "the workflow's default is to read the checkout")

	var runners []string
	for _, j := range ci.Jobs {
		runners = append(runners, j.RunsOn)
	}

	writes := 0
	for name, j := range w.Jobs {
		x.Equal(map[string]string{"contents": "read", "actions": "read", "packages": "write"}, j.Permissions,
			"job %s: to list `ci`'s runs and write a name, and nothing more", name)
		x.Contains(runners, j.RunsOn, "job %s runs on an image ci.yml does not", name)

		for _, s := range j.Steps {
			step := s.Name + s.ID + s.Uses
			// The bake file is read for the image's name, and is the one
			// `bake` that may be said.
			said := strings.ReplaceAll(s.Uses+"\n"+s.Run, "docker-bake.hcl", "")
			x.NotRegexp(`\bbake\b|\bbuild\b|--push|docker push|build-push`, said,
				"job %s, step %s: nothing is built or pushed for a tag", name, step)

			if !strings.Contains(s.Run, "imagetools create") {
				continue
			}
			writes++
			x.Contains(s.If, "github.event_name == 'push'", "job %s, step %s writes a name on more than a tag's push", name, step)
			x.Contains(s.If, "startsWith(github.ref, 'refs/tags/v')", "job %s, step %s writes a name for more than a `v*` tag", name, step)
			x.Contains(s.Run, "${TAG}", "job %s, step %s: the name written is the tag's own", name, step)
		}
	}
	x.NotZero(writes, "no step writes the tag's name with `imagetools create`")

	// The image's name is the bake file's `REPO`, read from it at the tagged
	// commit, and not spelt here a second time.
	x.Contains(src, "docker-bake.hcl", "the image's name is read from docker-bake.hcl")
	x.NotContains(src, "ghcr.io/", "the image's name is spelt here as well as in docker-bake.hcl")
}

// TestEveryWorkflowNamesWhatItRunsOnByCommit holds every workflow to what
// `ci.yml`'s head says of itself: an action is named by a commit with its
// version after it, one action is one commit wherever it is named, and a job
// runs on a runner image named here and not on GitHub's `-latest`.
//
// Nothing held `ci.yml` to it: a tag written there by hand, or a second
// workflow pinning `actions/checkout` at another commit, was green. With a
// workflow that writes to the registry beside `ci`'s `push`, the rule is
// about two files that hold a token to write a package, so it is the tree
// that is asked.
func TestEveryWorkflowNamesWhatItRunsOnByCommit(t *testing.T) {
	x := require.New(t)
	root := repoRoot(t)

	files, err := filepath.Glob(filepath.Join(root, ".github", "workflows", "*.yml"))
	x.NoError(err)
	x.NotEmpty(files, "no workflow was read")

	uses := regexp.MustCompile(`(?m)^\s*(?:-\s+)?uses:\s*(.*)$`)
	pinned := regexp.MustCompile(`^([\w.-]+/[\w./-]+)@([0-9a-f]{40}) # v\d+(?:\.\d+)*$`)

	commits := map[string]map[string][]string{}
	runners := map[string][]string{}
	for _, f := range files {
		name := filepath.Base(f)
		src, w := readWorkflow(t, root, name)

		for _, m := range uses.FindAllStringSubmatch(src, -1) {
			p := pinned.FindStringSubmatch(strings.TrimSpace(m[1]))
			x.NotNil(p, "%s: `uses: %s` is not an action named by its commit with its version after it", name, m[1])
			if commits[p[1]] == nil {
				commits[p[1]] = map[string][]string{}
			}
			commits[p[1]][p[2]] = append(commits[p[1]][p[2]], name)
		}

		for job, j := range w.Jobs {
			x.NotEmpty(j.RunsOn, "%s: job %s names no runner", name, job)
			x.NotContains(j.RunsOn, "latest", "%s: job %s runs on whatever GitHub's -latest is that day", name, job)
			runners[j.RunsOn] = append(runners[j.RunsOn], name+" "+job)
		}
	}
	x.NotEmpty(commits, "no action is named anywhere, which is this test reading nothing")

	for action, at := range commits {
		x.Len(at, 1, "%s is named at more than one commit: %v", action, at)
	}
	x.Len(runners, 1, "the jobs run on more than one runner image: %v", runners)
}
