package cli_test

import (
	"regexp"
	"testing"

	"github.com/stretchr/testify/require"
)

// fetches is a step that installs or downloads what nobody here controls: an
// action that checks out, sets a toolchain up, downloads an artifact or logs
// in to a registry, or a command that fetches packages, a browser, a binary
// or an image.
var fetches = regexp.MustCompile(`actions/checkout@|actions/setup-|actions/download-artifact@|docker/login-action@|\bnpm ci\b|playwright install|\bcurl\b|docker pull|buildx bake`)

// TestAJobsLimitIsRostersOwnTime holds `ci.yml` and `release.yml` to what a
// job's limit is for: catching roster hanging.
//
// `e2e` had nine minutes for the whole job, and on 2026-11-24 the browser's
// installation took seven of them, so `./scripts/e2e.sh` was cut off with
// nothing of roster's wrong and `main`'s run was cancelled. A step that
// fetches what nobody here controls has a limit of its own and spends none
// of roster's: in a job with a limit, every step has one of its own, a step
// that fetches is not a step of roster's scripts, and the job's limit is
// the sum of its steps'.
func TestAJobsLimitIsRostersOwnTime(t *testing.T) {
	root := repoRoot(t)

	for _, file := range []string{"ci.yml", "release.yml"} {
		_, w := readWorkflow(t, root, file)
		for name, j := range w.Jobs {
			if j.TimeoutMinutes == 0 {
				continue
			}
			t.Run(file+"/"+name, func(t *testing.T) {
				x := require.New(t)

				sum := 0
				for i, s := range j.Steps {
					step := s.Name + s.ID + s.Uses + s.Run
					x.NotZero(s.TimeoutMinutes, "step %d (%.60s) has no limit of its own, and spends the job's", i, step)
					sum += s.TimeoutMinutes

					if fetches.MatchString(s.Uses + "\n" + s.Run) {
						x.NotRegexp(`\./scripts/`, s.Run, "step %d (%.60s) fetches and runs roster's script in one limit", i, step)
					}
				}
				x.Equal(sum, j.TimeoutMinutes, "the job's limit is not the sum of its steps'")
			})
		}
	}
}
