package plugin

import (
	"bytes"
	"context"
	"errors"
	"io"
	"reflect"
	"strings"
	"testing"

	"github.com/drone-plugins/drone-s3-cache/storage/s3"
	"github.com/sirupsen/logrus"
	"github.com/urfave/cli/v2"
)

func TestRestoreKeyOrderAndErrors(t *testing.T) {
	for _, tc := range []struct {
		name      string
		found     string
		failure   error
		wantCalls []string
		result    string
	}{
		{"primary", "deps-v1", nil, []string{"deps-v1"}, "exact_hit"},
		{"second fallback", "main", nil, []string{"deps-v1", "branch", "main"}, "fallback_hit"},
		{"all missing", "", nil, []string{"deps-v1", "branch", "main"}, "miss"},
		{"permission error", "", errors.New("access denied"), []string{"deps-v1"}, "error"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := keyPlugin(restoreMode)
			p.settings.FallbackKeys = *cli.NewStringSlice("deps-v1", "branch", "branch", "main")
			if err := p.validateMode(); err != nil {
				t.Fatal(err)
			}
			var calls []string
			st := testStorage{get: func(_ context.Context, path string, w io.Writer) error {
				key := strings.TrimSuffix(strings.TrimPrefix(path, "bucket/prefix/team/subgroup/repo/keys/"), "/archive.tar")
				calls = append(calls, key)
				if tc.failure != nil {
					return tc.failure
				}
				if key != tc.found {
					return s3.ErrCacheMiss
				}
				_, err := w.Write([]byte("contents"))
				return err
			}}
			a := testArchive{unpack: func(_ string, r io.Reader) error { _, err := io.Copy(io.Discard, r); return err }}
			var logs bytes.Buffer
			logger := logrus.StandardLogger()
			old := logger.Out
			logger.SetOutput(&logs)
			defer logger.SetOutput(old)
			err := p.restoreByKey(st, a)
			if !errors.Is(err, tc.failure) {
				t.Fatalf("got %v want %v", err, tc.failure)
			}
			if !reflect.DeepEqual(calls, tc.wantCalls) {
				t.Fatalf("calls %v", calls)
			}
			if !strings.Contains(logs.String(), "cache_result="+tc.result) || !strings.Contains(logs.String(), "resolved_key=deps-v1") {
				t.Fatalf("logs: %s", logs.String())
			}
		})
	}
}
func TestCorruptCacheDoesNotFallBack(t *testing.T) {
	p := keyPlugin(restoreMode)
	p.settings.FallbackKeys = *cli.NewStringSlice("main")
	if err := p.validateMode(); err != nil {
		t.Fatal(err)
	}
	calls := 0
	st := testStorage{get: func(_ context.Context, _ string, w io.Writer) error {
		calls++
		_, err := w.Write(make([]byte, 65536))
		return err
	}}
	want := errors.New("corrupt archive")
	a := testArchive{unpack: func(string, io.Reader) error { return want }}
	if err := finishes(t, func() error { return p.restoreByKey(st, a) }); !errors.Is(err, want) {
		t.Fatalf("got %v", err)
	}
	if calls != 1 {
		t.Fatalf("unexpected fallback: %d", calls)
	}
}
