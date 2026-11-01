package commands

import (
	"testing"
	"time"

	"github.com/dkarlovi/mediator/transcode"
)

func TestStatusLine(t *testing.T) {
	got := statusLine(transcode.Status{
		Fraction: 0.5, Speed: 3.14, ReadRate: 12_400_000,
		Elapsed: 41 * time.Second, ETA: 3725 * time.Second,
	}, 2, 9)
	want := "[████████████░░░░░░░░░░░░]  50%  3.1x  12.4 MB/s  elapsed 0:41  ETA 1:02:05  (2/9)"
	if got != want {
		t.Errorf("got  %q\nwant %q", got, want)
	}
	// Nothing known yet: no speed, rate, ETA or counter for a single file.
	if got, want := statusLine(transcode.Status{}, 1, 1), "[░░░░░░░░░░░░░░░░░░░░░░░░]   0%  elapsed 0:00"; got != want {
		t.Errorf("got  %q\nwant %q", got, want)
	}
}
