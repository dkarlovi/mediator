package media

import "testing"

func TestStreamDuration(t *testing.T) {
	for _, tt := range []struct {
		s    Stream
		want float64
	}{
		{Stream{RawDuration: "6112.600000"}, 6112.6},                                    // MP4
		{Stream{Tags: map[string]string{"DURATION": "01:41:52.600000000"}}, 6112.6},     // Matroska
		{Stream{Tags: map[string]string{"DURATION-eng": "00:00:20.001000000"}}, 20.001}, // mkvmerge, per language
		{Stream{RawDuration: "N/A", Tags: map[string]string{"DURATION": "garbage"}}, 0},
		{Stream{}, 0},
	} {
		if got := tt.s.Duration(); got < tt.want-1e-6 || got > tt.want+1e-6 {
			t.Errorf("%+v: got %v, want %v", tt.s, got, tt.want)
		}
	}
}
