package plugin

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aws/smithy-go"
	"github.com/drone-plugins/drone-s3-cache/storage/s3"
)

func settingsFor(raw, mode string) Settings {
	return Settings{Mode: mode, Cache: raw, Bucket: "bucket", Owner: "team", Repo: "repo", FlushAge: 30, S3Options: s3.Options{Endpoint: "http://localhost", Access: "access", Secret: "secret", Timeout: time.Second}}
}

func validated(t *testing.T, s Settings) *Plugin {
	t.Helper()
	p := New(s)
	if err := p.Validate(); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestCacheValidation(t *testing.T) {
	inWorkspace(t)
	for _, raw := range []string{"", `null`, `"[]"`, `true`, `{}`, `[null]`, `{"key":"x","paths":["cache"],"policy":"pull"}`, `{"key":{"prefix":"x","files":["pom.xml"],"unknown":true},"paths":["cache"]}`, `{"key":"x","paths":["cache"]} {}`, `{"key":"x","paths":["../cache"]}`, `{"key":"x","paths":["cache/*"]}`, `[{"key":"x","paths":["a"]},{"key":"x","paths":["b"]}]`, `[{"key":"a","paths":["a"]},{"key":"b","paths":["a/b"]}]`, `{"key":"x","paths":[".git"]}`} {
		if err := New(settingsFor(raw, restoreMode)).Validate(); err == nil {
			t.Errorf("accepted %s", raw)
		}
	}
	object := validated(t, settingsFor(`{"key":"x","paths":["cache"]}`, restoreMode))
	array := validated(t, settingsFor(`[{"key":"x","paths":["cache"]}]`, restoreMode))
	if object.entries[0].resolved != array.entries[0].resolved {
		t.Fatal("object and array differ")
	}
	disabled := New(Settings{Mode: restoreMode, Cache: "[]", KeyFromGit: true})
	if err := disabled.Validate(); err != nil {
		t.Fatal(err)
	}
	if err := disabled.Execute(); err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"", "unknown"} {
		s := settingsFor("[]", mode)
		if err := New(s).Validate(); err == nil {
			t.Fatal("invalid mode accepted")
		}
	}
	flush := settingsFor("", flushMode)
	flush.CacheSet = true
	if err := New(flush).Validate(); err == nil {
		t.Fatal("flush accepted an explicitly empty cache parameter")
	}
	for _, mutate := range []func(*Settings){
		func(s *Settings) { s.Bucket = "bucket/prefix" }, func(s *Settings) { s.Owner = "../team" }, func(s *Settings) { s.Repo = "repo/other" },
		func(s *Settings) { s.S3Options.Endpoint = "s3://bucket" }, func(s *Settings) { s.S3Options.Endpoint = "https://cache.example/bucket" },
		func(s *Settings) { s.S3Options.Timeout = 0 }, func(s *Settings) { s.S3Options.Secret = "" },
		func(s *Settings) { s.FlushAgeSet = true }, func(s *Settings) { s.S3Options.AddressingStyle = "bad" },
	} {
		s := settingsFor(`{"key":"x","paths":["cache"]}`, restoreMode)
		mutate(&s)
		if err := New(s).Validate(); err == nil {
			t.Fatal("invalid configuration accepted")
		}
	}
	outside := t.TempDir()
	if err := os.Symlink(outside, "cache"); err != nil {
		t.Fatal(err)
	}
	if err := New(settingsFor(`{"key":"x","paths":["cache/sub"]}`, restoreMode)).Validate(); err == nil {
		t.Fatal("symlink cache ancestor accepted")
	}
}

type memoryStorage struct {
	objects             map[string][]byte
	gets, puts, deleted []string
	getErr              error
	files               []s3.FileEntry
}

func (s *memoryStorage) Get(_ context.Context, key string) (io.ReadCloser, error) {
	s.gets = append(s.gets, key)
	if s.getErr != nil {
		return nil, s.getErr
	}
	data, ok := s.objects[key]
	if !ok {
		return nil, &smithy.GenericAPIError{Code: "NoSuchKey", Message: "missing"}
	}
	return io.NopCloser(bytes.NewReader(data)), nil
}
func (s *memoryStorage) Put(_ context.Context, key string, r io.Reader) error {
	data, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	s.objects[key] = data
	s.puts = append(s.puts, key)
	return nil
}
func (s *memoryStorage) List(context.Context, string) ([]s3.FileEntry, error) { return s.files, nil }
func (s *memoryStorage) Delete(_ context.Context, key string) error {
	s.deleted = append(s.deleted, key)
	return nil
}

func TestRestoreFallbackAndOverwrite(t *testing.T) {
	inWorkspace(t)
	writeFile(t, "cache/value", "warm")
	raw := `{"key":"main","paths":["cache"]}`
	s := &memoryStorage{objects: map[string][]byte{}}
	save := validated(t, settingsFor(raw, rebuildMode))
	if err := save.execute(s); err != nil {
		t.Fatal(err)
	}
	writeFile(t, "cache/value", "old")
	restore := validated(t, settingsFor(`{"key":"new","paths":["cache"],"fallback_keys":["missing","main"]}`, restoreMode))
	if err := restore.execute(s); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile("cache/value")
	if string(got) != "warm" {
		t.Fatalf("got %s", got)
	}
	if strings.Join(s.gets, ",") != "bucket/team/repo/new.tar.gz,bucket/team/repo/missing.tar.gz,bucket/team/repo/main.tar.gz" {
		t.Fatal(s.gets)
	}
	writeFile(t, "cache/value", "fresh")
	if err := save.execute(s); err != nil {
		t.Fatal(err)
	}
	if len(s.puts) != 2 {
		t.Fatal("exact key not refreshed")
	}
	for _, code := range []string{"AccessDenied", "NoSuchBucket"} {
		s.gets = nil
		s.getErr = &smithy.GenericAPIError{Code: code, Message: "failed"}
		if err := restore.execute(s); err != nil {
			t.Fatal(err)
		}
		if len(s.gets) != 1 {
			t.Fatal("remote failure incorrectly tried fallback")
		}
	}
}

func TestEmptyUploadAndCleanupBoundary(t *testing.T) {
	inWorkspace(t)
	if err := os.Mkdir("cache", 0755); err != nil {
		t.Fatal(err)
	}
	s := &memoryStorage{objects: map[string][]byte{}}
	p := validated(t, settingsFor(`{"key":"x","paths":["cache","missing"]}`, rebuildMode))
	if err := p.execute(s); err != nil {
		t.Fatal(err)
	}
	if len(s.puts) != 0 {
		t.Fatal("empty archive uploaded")
	}
	for _, prefix := range []string{"team/repo2/", "team/", "team/repo/../repo2/", "/team/repo/"} {
		cfg := settingsFor("", flushMode)
		cfg.FlushPath = prefix
		if err := New(cfg).Validate(); err == nil {
			t.Errorf("accepted cleanup prefix %s", prefix)
		}
	}
	cfg := settingsFor("", flushMode)
	p = validated(t, cfg)
	s.files = []s3.FileEntry{{Path: "bucket/team/repo2/old", LastModified: time.Now().AddDate(0, 0, -60)}}
	if err := p.execute(s); err == nil || len(s.deleted) != 0 {
		t.Fatal("cleanup escaped repository")
	}
	s.files = []s3.FileEntry{{Path: "bucket/team/repo/old", LastModified: time.Now().AddDate(0, 0, -31)}, {Path: "bucket/team/repo/new", LastModified: time.Now()}}
	if err := p.execute(s); err != nil {
		t.Fatal(err)
	}
	if len(s.deleted) != 1 || s.deleted[0] != "bucket/team/repo/old" {
		t.Fatal(s.deleted)
	}
}

func TestS3PluginRoundTrip(t *testing.T) {
	inWorkspace(t)
	var mu sync.Mutex
	objects := map[string][]byte{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch r.Method {
		case "PUT":
			if r.Header.Get("Content-Type") != "application/gzip" {
				t.Error("archive content type is not gzip")
			}
			data, err := io.ReadAll(r.Body)
			if err != nil {
				t.Error(err)
				w.WriteHeader(500)
				return
			}
			objects[r.URL.Path] = data
			w.Header().Set("ETag", `"test"`)
		case "GET":
			if data, ok := objects[r.URL.Path]; ok {
				w.Header().Set("Content-Length", fmt.Sprint(len(data)))
				_, _ = w.Write(data)
			} else {
				w.Header().Set("Content-Type", "application/xml")
				w.WriteHeader(404)
				_, _ = fmt.Fprint(w, "<Error><Code>NoSuchKey</Code><Message>missing</Message></Error>")
			}
		default:
			t.Errorf("unexpected method %s", r.Method)
			w.WriteHeader(400)
		}
	}))
	defer server.Close()
	writeFile(t, "cache/value", "payload")
	cfg := settingsFor(`{"key":"primary","paths":["cache"]}`, rebuildMode)
	cfg.S3Options.Endpoint = server.URL
	if err := validated(t, cfg).Execute(); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll("cache"); err != nil {
		t.Fatal(err)
	}
	cfg.Mode = restoreMode
	cfg.Cache = `{"key":"other","paths":["cache"],"fallback_keys":["primary"]}`
	if err := validated(t, cfg).Execute(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join("cache", "value"))
	if err != nil || string(data) != "payload" {
		t.Fatalf("%s %v", data, err)
	}
}
