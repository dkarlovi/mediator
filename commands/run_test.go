package commands

import (
	"bytes"
	"testing"
	"time"

	"github.com/dkarlovi/mediator/transcode"
)

func TestStatusLine(t *testing.T) {
	for _, tt := range []struct {
		name string
		st   transcode.Status
		want string
	}{
		{
			name: "converting",
			st: transcode.Status{Phase: transcode.PhaseConvert, Fraction: 0.5, Speed: 3.14, Rate: 12_400_000,
				Elapsed: 41 * time.Second, ETA: 3725 * time.Second},
			want: "convert [██████████░░░░░░░░░░]  50%  3.1x  read 12.4 MB/s  elapsed 0:41  ETA 1:02:05  (2/9)",
		},
		{
			name: "uploading",
			st: transcode.Status{Phase: transcode.PhaseUpload, Fraction: 0.25, Rate: 18_000_000,
				Elapsed: 10 * time.Second, ETA: 30 * time.Second},
			want: "upload  [█████░░░░░░░░░░░░░░░]  25%  write 18.0 MB/s  elapsed 0:10  ETA 0:30  (2/9)",
		},
		{
			name: "phase finished",
			st: transcode.Status{Phase: transcode.PhaseConvert, Fraction: 1, Speed: 30.2, Rate: 95_100_000,
				Elapsed: 252 * time.Second, Done: true},
			want: "convert [████████████████████] 100%  30.2x  read 95.1 MB/s  took 4:12",
		},
		{
			name: "nothing known yet",
			st:   transcode.Status{Phase: transcode.PhaseConvert},
			want: "convert [░░░░░░░░░░░░░░░░░░░░]   0%  elapsed 0:00  (2/9)",
		},
	} {
		if got := statusLine(tt.st, 2, 9); got != tt.want {
			t.Errorf("%s:\n got  %q\n want %q", tt.name, got, tt.want)
		}
	}
	// A single file doesn't show a counter.
	if got := statusLine(transcode.Status{Phase: transcode.PhaseConvert}, 1, 1); got != "convert [░░░░░░░░░░░░░░░░░░░░]   0%  elapsed 0:00" {
		t.Errorf("single file: %q", got)
	}
}

func TestProgressPrinterOutsideTerminal(t *testing.T) {
	// Without a terminal only finished phases are printed, one line each.
	var buf bytes.Buffer
	p := progressPrinter(&buf, false, 1, 1)
	p(transcode.Status{Phase: transcode.PhaseConvert, Fraction: 0.5})
	p(transcode.Status{Phase: transcode.PhaseConvert, Fraction: 1, Done: true})
	p(transcode.Status{Phase: transcode.PhaseUpload, Fraction: 0.5})
	p(transcode.Status{Phase: transcode.PhaseUpload, Fraction: 1, Done: true})
	want := "    convert [████████████████████] 100%  took 0:00\n    upload  [████████████████████] 100%  took 0:00\n"
	if buf.String() != want {
		t.Errorf("got\n%q\nwant\n%q", buf.String(), want)
	}
}
