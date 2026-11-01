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

func TestCoverArt(t *testing.T) {
	attached := Stream{CodecType: "video", CodecName: "mjpeg", Disposition: map[string]int{"attached_pic": 1},
		Tags: map[string]string{"filename": "cover.jpg", "mimetype": "image/jpeg"}}
	// What ffmpeg makes of a poster copied into an MKV: a track that kept the
	// attachment's tags (now upper case) but lost the flag.
	lost := Stream{CodecType: "video", CodecName: "mjpeg", Tags: map[string]string{"FILENAME": "cover.jpg", "MIMETYPE": "image/jpeg"}}
	// Motion-JPEG video from an old camera: a real video, no attachment tags.
	camera := Stream{CodecType: "video", CodecName: "mjpeg"}
	movie := Stream{CodecType: "video", CodecName: "h264", Tags: map[string]string{"FILENAME": "x"}}

	for _, tt := range []struct {
		name        string
		s           Stream
		cover, lost bool
	}{
		{"attached", attached, true, false},
		{"lost", lost, true, true},
		{"camera mjpeg", camera, false, false},
		{"movie with odd tags", movie, false, false},
	} {
		if tt.s.IsCoverArt() != tt.cover || tt.s.CoverLost() != tt.lost {
			t.Errorf("%s: cover=%v lost=%v", tt.name, tt.s.IsCoverArt(), tt.s.CoverLost())
		}
	}
}
