package plugin

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/drone/drone-cache-lib/archive/util"
)

type testStorage struct {
	get func(context.Context, string, io.Writer) error
	put func(context.Context, string, io.Reader) error
}

func (s testStorage) GetContext(ctx context.Context, p string, w io.Writer) error {
	return s.get(ctx, p, w)
}
func (s testStorage) PutContext(ctx context.Context, p string, r io.Reader) error {
	return s.put(ctx, p, r)
}

type testArchive struct {
	pack   func([]string, io.Writer) error
	unpack func(string, io.Reader) error
}

func (a testArchive) Pack(p []string, w io.Writer) error { return a.pack(p, w) }
func (a testArchive) Unpack(p string, r io.Reader) error { return a.unpack(p, r) }
func finishes(t *testing.T, f func() error) error {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- f() }()
	select {
	case err := <-done:
		return err
	case <-time.After(time.Second):
		t.Fatal("cache transfer did not stop")
		return nil
	}
}
func TestRebuildEarlyFailure(t *testing.T) {
	want := errors.New("upload rejected")
	s := testStorage{put: func(context.Context, string, io.Reader) error { return want }}
	a := testArchive{pack: func(_ []string, w io.Writer) error { _, err := w.Write(make([]byte, 65536)); return err }}
	if err := finishes(t, func() error { return rebuildCache(s, a, []string{"x"}, "key") }); !errors.Is(err, want) {
		t.Fatalf("got %v", err)
	}
}
func TestRebuildPackFailure(t *testing.T) {
	want := errors.New("cannot read source")
	s := testStorage{put: func(_ context.Context, _ string, r io.Reader) error { _, err := io.Copy(io.Discard, r); return err }}
	a := testArchive{pack: func([]string, io.Writer) error { return want }}
	if err := finishes(t, func() error { return rebuildCache(s, a, nil, "key") }); !errors.Is(err, want) {
		t.Fatalf("got %v", err)
	}
}
func TestRestoreUnpackFailureCancelsDownload(t *testing.T) {
	want := errors.New("invalid archive")
	stopped := make(chan struct{})
	s := testStorage{get: func(ctx context.Context, _ string, _ io.Writer) error { <-ctx.Done(); close(stopped); return ctx.Err() }}
	a := testArchive{unpack: func(string, io.Reader) error { return want }}
	if err := finishes(t, func() error { return restoreCache(s, a, "key") }); !errors.Is(err, want) {
		t.Fatalf("got %v", err)
	}
	select {
	case <-stopped:
	default:
		t.Fatal("download still running")
	}
}
func TestRestoreDownloadFailure(t *testing.T) {
	want := errors.New("connection lost")
	s := testStorage{get: func(_ context.Context, _ string, w io.Writer) error {
		_, err := w.Write([]byte("prefix"))
		if err != nil {
			return err
		}
		return want
	}}
	a := testArchive{unpack: func(_ string, r io.Reader) error { _, err := io.Copy(io.Discard, r); return err }}
	if err := finishes(t, func() error { return restoreCache(s, a, "key") }); !errors.Is(err, want) {
		t.Fatalf("got %v", err)
	}
}
func TestRestoreDrainsArchiveTail(t *testing.T) {
	for _, tailErr := range []error{nil, errors.New("tail transfer failed")} {
		s := testStorage{get: func(_ context.Context, _ string, w io.Writer) error {
			if _, err := w.Write([]byte("data and tail")); err != nil {
				return err
			}
			return tailErr
		}}
		a := testArchive{unpack: func(_ string, r io.Reader) error { _, err := io.ReadFull(r, make([]byte, 1)); return err }}
		if err := finishes(t, func() error { return restoreCache(s, a, "key") }); !errors.Is(err, tailErr) {
			t.Fatalf("got %v want %v", err, tailErr)
		}
	}
}
func inWorkspace(t *testing.T) string {
	t.Helper()
	old, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err = os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Chdir(old); err != nil {
			t.Error(err)
		}
	})
	return dir
}
func TestArchiveRoundTrip(t *testing.T) {
	inWorkspace(t)
	for _, filename := range []string{"archive.tar", "archive.tar.gz"} {
		t.Run(filename, func(t *testing.T) {
			if err := os.MkdirAll("cache/sub", 0755); err != nil {
				t.Fatal(err)
			}
			want := bytes.Repeat([]byte("cache data"), 10000)
			if err := os.WriteFile("cache/sub/value", want, 0644); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink("sub/value", "cache/link"); err != nil {
				t.Fatal(err)
			}
			var buf bytes.Buffer
			s := testStorage{put: func(_ context.Context, _ string, r io.Reader) error { _, err := io.Copy(&buf, r); return err }, get: func(_ context.Context, _ string, w io.Writer) error {
				_, err := io.Copy(w, bytes.NewReader(buf.Bytes()))
				return err
			}}
			a, err := util.FromFilename(filename)
			if err != nil {
				t.Fatal(err)
			}
			if err = rebuildCache(s, a, []string{"cache"}, "key"); err != nil {
				t.Fatal(err)
			}
			if err = os.RemoveAll("cache"); err != nil {
				t.Fatal(err)
			}
			if err = restoreCache(s, a, "key"); err != nil {
				t.Fatal(err)
			}
			got, err := os.ReadFile(filepath.Join("cache", "link"))
			if err != nil || !bytes.Equal(got, want) {
				t.Fatalf("roundtrip failed: %v", err)
			}
			if err = os.RemoveAll("cache"); err != nil {
				t.Fatal(err)
			}
		})
	}
}
