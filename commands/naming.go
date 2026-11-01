package commands

import (
	"path/filepath"
	"strings"

	"github.com/dkarlovi/mediator/config"
	"github.com/dkarlovi/mediator/plan"
)

// suffix marks the converted copy written beside a kept original:
// "Episode.mkv" -> "Episode-mediator.mkv".
const suffix = "-mediator"

// outputPath is where mediator puts the converted version of a planned file,
// according to the config's original policy.
func outputPath(p *plan.Plan, policy config.OriginalPolicy) string {
	stem := strings.TrimSuffix(p.Source, filepath.Ext(p.Source))
	// A file mediator produced itself is never an original worth keeping, so
	// it is converted in place instead of growing a second suffix.
	if policy == config.OriginalKeep && !strings.HasSuffix(stem, suffix) {
		stem += suffix
	}
	return stem + "." + p.Dst.Ext
}
