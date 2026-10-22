package cli

// The witnesses `audit.checkpoints.witnesses` may name: where the newest
// checkpoint of the trail's chain is heard outside the database and the
// archive (payday's `trail.Witness`, payday#115), so that whoever can write
// both cannot cut the chain short at its head and wind the count back without
// something they do not hold saying so. See `docs/operating.md`, "Who heard
// the newest checkpoint".
//
// Blank-imported here, beside the drivers and for their reason: a process
// hears checkpoints, and the sandbox, which imports `cmd` and not this, has no
// archive to hear. Both are linked, because a configuration naming a scheme
// this binary does not open is refused where the process comes up -- and
// refused in a sentence about an import, which is the right sentence for a
// build and the wrong one for somebody who only wrote a line of YAML.
//
// `log:` writes a line of the log and the gauge `payday.trail.checkpoint` for
// every checkpoint, and is a witness only with the alerts on it that the
// operating guide gives. `store:` and a directory keeps what it hears on a
// disk of its own: mounted with credentials neither the database's nor the
// archive's, and best one that refuses deletion.
import (
	_ "github.com/lesomnus/payday/config/witnesslog"
	_ "github.com/lesomnus/payday/config/witnessstore"
)
