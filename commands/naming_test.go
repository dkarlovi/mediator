package commands

import (
	"testing"

	"github.com/dkarlovi/mediator/config"
	"github.com/dkarlovi/mediator/media"
	"github.com/dkarlovi/mediator/plan"
)

func TestOutputPath(t *testing.T) {
	mkv, _ := media.LookupContainer("mkv")
	for _, tt := range []struct {
		src    string
		policy config.OriginalPolicy
		want   string
	}{
		{"/s/E01.mkv", config.OriginalKeep, "/s/E01-mediator.mkv"},
		{"/s/E01.avi", config.OriginalKeep, "/s/E01-mediator.mkv"},
		{"/s/E01-mediator.mkv", config.OriginalKeep, "/s/E01-mediator.mkv"}, // our own output: in place
		{"/s/E01.mkv", config.OriginalDelete, "/s/E01.mkv"},
		{"/s/E01.avi", config.OriginalDelete, "/s/E01.mkv"},
	} {
		p := &plan.Plan{Source: tt.src, Dst: mkv}
		if got := outputPath(p, tt.policy); got != tt.want {
			t.Errorf("outputPath(%s, %s) = %s, want %s", tt.src, tt.policy, got, tt.want)
		}
	}
}
