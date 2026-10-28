package plugin

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/drone/drone-cache-lib/archive/util"
)

type testStorage struct {
	get func(context.Context, string) (io.ReadCloser, error)
	put func(context.Context, string, io.Reader) error
}

func (s testStorage) Get(ctx context.Context, p string) (io.ReadCloser, error) { return s.get(ctx, p) }
func (s testStorage) Put(ctx context.Context, p string, r io.Reader) error     { return s.put(ctx, p, r) }

type testBody struct {
	io.Reader
	close func() error
}

func (b *testBody) Close() error {
	if b.close != nil {
		return b.close()
	}
	return nil
}

type errorReader struct{ err error }

func (r errorReader) Read([]byte) (int, error) { return 0, r.err }

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
func TestRestoreUnpackFailureClosesBody(t *testing.T) {
	want := errors.New("invalid archive")
	closed := false
	var downloadCtx context.Context
	s := testStorage{get: func(ctx context.Context, _ string) (io.ReadCloser, error) {
		downloadCtx = ctx
		return &testBody{Reader: strings.NewReader("data"), close: func() error { closed = true; return nil }}, nil
	}}
	a := testArchive{unpack: func(string, io.Reader) error { return want }}
	if err := restoreCache(s, a, "path"); !errors.Is(err, want) {
		t.Fatalf("got %v", err)
	}
	if !closed || downloadCtx.Err() != context.Canceled {
		t.Fatal("download was not closed and canceled")
	}
}
func TestRestoreDownloadFailure(t *testing.T) {
	want := errors.New("connection lost")
	s := testStorage{get: func(context.Context, string) (io.ReadCloser, error) { return nil, want }}
	if err := restoreCache(s, testArchive{}, "path"); !errors.Is(err, want) {
		t.Fatalf("got %v", err)
	}
}
func TestRestoreDrainsArchiveTail(t *testing.T) {
	for _, tailErr := range []error{nil, errors.New("tail transfer failed")} {
		s := testStorage{get: func(context.Context, string) (io.ReadCloser, error) {
			return io.NopCloser(io.MultiReader(strings.NewReader("data and tail"), errorReader{tailErr})), nil
		}}
		// A successful reader must return EOF rather than repeatedly returning 0,nil.
		if tailErr == nil {
			s.get = func(context.Context, string) (io.ReadCloser, error) {
				return io.NopCloser(strings.NewReader("data and tail")), nil
			}
		}
		a := testArchive{unpack: func(_ string, r io.Reader) error { _, err := io.ReadFull(r, make([]byte, 1)); return err }}
		if err := restoreCache(s, a, "path"); !errors.Is(err, tailErr) {
			t.Fatalf("got %v want %v", err, tailErr)
		}
	}
}
func TestRestoreCloseError(t *testing.T) {
	want := errors.New("close failed")
	s := testStorage{get: func(context.Context, string) (io.ReadCloser, error) {
		return &testBody{Reader: strings.NewReader(""), close: func() error { return want }}, nil
	}}
	a := testArchive{unpack: func(string, io.Reader) error { return nil }}
	if err := restoreCache(s, a, "path"); !errors.Is(err, want) {
		t.Fatalf("got %v", err)
	}
}
func TestRebuildDiscardedFooterError(t *testing.T) {
	s := testStorage{put: func(context.Context, string, io.Reader) error { return nil }}
	// The old tar/tgz Pack methods discard errors from their deferred Close writes.
	a := testArchive{pack: func(_ []string, w io.Writer) error { _, _ = w.Write([]byte("footer")); return nil }}
	if err := finishes(t, func() error { return rebuildCache(s, a, nil, "path") }); !errors.Is(err, io.ErrClosedPipe) {
		t.Fatalf("footer error lost: %v", err)
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
			s := testStorage{put: func(_ context.Context, _ string, r io.Reader) error { _, err := io.Copy(&buf, r); return err }, get: func(context.Context, string) (io.ReadCloser, error) {
				return io.NopCloser(bytes.NewReader(buf.Bytes())), nil
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

func TestRebuildPreservesSourceAndCleanupErrors(t *testing.T) {
	sourceErr := errors.New("source failed")
	cleanupErr := errors.New("multipart cleanup failed")
	s := testStorage{put: func(_ context.Context, _ string, r io.Reader) error {
		_, _ = io.Copy(io.Discard, r)
		return cleanupErr
	}}
	a := testArchive{pack: func([]string, io.Writer) error { return sourceErr }}
	err := rebuildCache(s, a, nil, "path")
	if !errors.Is(err, sourceErr) || !errors.Is(err, cleanupErr) {
		t.Fatalf("lost source or cleanup error: %v", err)
	}
}
