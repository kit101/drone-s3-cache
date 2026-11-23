package plugin

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func tarGzip(t *testing.T, headers []*tar.Header) []byte {
	t.Helper()
	var out bytes.Buffer
	gz := gzip.NewWriter(&out)
	tw := tar.NewWriter(gz)
	for _, h := range headers {
		if err := tw.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		if h.Typeflag == tar.TypeReg && h.Size > 0 {
			if _, err := tw.Write([]byte(strings.Repeat("x", int(h.Size)))); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

func TestUnsafeAndCorruptArchivesLeaveWorkspaceUnchanged(t *testing.T) {
	inWorkspace(t)
	writeFile(t, "cache/value", "original")
	cases := map[string][]byte{
		"empty":            tarGzip(t, nil),
		"directories only": tarGzip(t, []*tar.Header{{Name: "cache", Typeflag: tar.TypeDir, Mode: 0755}}),
		"traversal":        tarGzip(t, []*tar.Header{{Name: "cache/../../outside", Typeflag: tar.TypeReg, Mode: 0644, Size: 1}}),
		"absolute":         tarGzip(t, []*tar.Header{{Name: "/cache/outside", Typeflag: tar.TypeReg, Mode: 0644, Size: 1}}),
		"undeclared":       tarGzip(t, []*tar.Header{{Name: "source.go", Typeflag: tar.TypeReg, Mode: 0644, Size: 1}}),
		"symlink escape":   tarGzip(t, []*tar.Header{{Name: "cache/link", Typeflag: tar.TypeSymlink, Linkname: "../../outside"}}),
		"hardlink":         tarGzip(t, []*tar.Header{{Name: "cache/link", Typeflag: tar.TypeLink, Linkname: "/etc/passwd"}}),
		"duplicate":        tarGzip(t, []*tar.Header{{Name: "cache/x", Typeflag: tar.TypeReg, Mode: 0644, Size: 1}, {Name: "cache/x", Typeflag: tar.TypeReg, Mode: 0644, Size: 1}}),
		"file root":        tarGzip(t, []*tar.Header{{Name: "cache", Typeflag: tar.TypeReg, Mode: 0644, Size: 1}}),
	}
	corrupt := tarGzip(t, []*tar.Header{{Name: "cache/new", Typeflag: tar.TypeReg, Mode: 0644, Size: 4}})
	corrupt[len(corrupt)-1] ^= 0xff
	cases["gzip footer"] = corrupt
	for name, data := range cases {
		t.Run(name, func(t *testing.T) {
			p := validated(t, settingsFor(`{"key":"x","paths":["cache"],"fallback_keys":["other"]}`, restoreMode))
			s := &memoryStorage{objects: map[string][]byte{p.object("x"): data}}
			if err := p.execute(s); err != nil {
				t.Fatal(err)
			}
			got, err := os.ReadFile("cache/value")
			if err != nil || string(got) != "original" {
				t.Fatalf("existing cache lost: %s %v", got, err)
			}
			if _, err := os.Stat("cache/new"); !os.IsNotExist(err) {
				t.Fatal("partial extraction published")
			}
			if len(s.gets) != 1 {
				t.Fatal("archive failure incorrectly tried fallback")
			}
		})
	}
}

func TestPublishRollback(t *testing.T) {
	inWorkspace(t)
	workspace, _ := os.Getwd()
	writeFile(t, "cache/value", "original")
	writeFile(t, "blocked", "file")
	stage, err := os.MkdirTemp(workspace, ".test-stage-")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = removeCache(stage) }()
	writeFile(t, filepath.Join(stage, "cache/value"), "replacement")
	writeFile(t, filepath.Join(stage, "blocked/sub/value"), "bad")
	if err := publishCache(workspace, stage, []string{"cache", "blocked/sub"}); err == nil {
		t.Fatal("expected failure")
	}
	got, err := os.ReadFile("cache/value")
	if err != nil || string(got) != "original" {
		t.Fatalf("rollback failed: %s %v", got, err)
	}
	backups, _ := filepath.Glob(filepath.Join(workspace, ".cache-backup-*"))
	if len(backups) != 0 {
		t.Fatal("backup leaked after successful rollback")
	}
}

func TestPackingRejectsEscapingLinkWithoutOverwrite(t *testing.T) {
	inWorkspace(t)
	outside := filepath.Join(t.TempDir(), "private")
	writeFile(t, outside, "secret")
	writeFile(t, "cache/value", "warm")
	if err := os.Symlink(outside, "cache/link"); err != nil {
		t.Fatal(err)
	}
	p := validated(t, settingsFor(`{"key":"x","paths":["cache"]}`, rebuildMode))
	s := &memoryStorage{objects: map[string][]byte{p.object("x"): []byte("original object")}}
	if err := p.execute(s); err == nil {
		t.Fatal("escaping link accepted")
	}
	if string(s.objects[p.object("x")]) != "original object" || len(s.puts) > 0 {
		t.Fatal("failed source overwrote old object")
	}
}
