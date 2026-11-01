package scan

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestFiles(t *testing.T) {
	dir := t.TempDir()
	for _, f := range []string{
		"S01/E01.mkv",
		"S01/E02.mp4",
		"S01/E02.srt",
		"S01/._E01.mkv",
		"S01/E03.mediator-tmp.mkv",
		"S02/E01.MKV",
		".hidden/x.mkv",
		"cover.jpg",
	} {
		p := filepath.Join(dir, f)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}

	got, err := Files([]string{dir}, true)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		filepath.Join(dir, "S01/E01.mkv"),
		filepath.Join(dir, "S01/E02.mp4"),
		filepath.Join(dir, "S02/E01.MKV"),
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v\nwant %v", got, want)
	}

	got, err = Files([]string{filepath.Join(dir, "S01")}, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Errorf("non-recursive: %v", got)
	}
}
