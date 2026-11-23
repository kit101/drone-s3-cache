package main

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"time"

	git "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing/object"
	"strings"
	"testing"

	"github.com/drone-plugins/drone-s3-cache/plugin"
	"github.com/urfave/cli/v2"
)

func TestCLIInputsAndDefaults(t *testing.T) {
	t.Setenv("PLUGIN_CACHE", `{"key":"x","paths":["cache"]}`)
	t.Setenv("DRONE_COMMIT_SHA", strings.Repeat("a", 40))
	for _, tc := range []struct {
		args    []string
		fromGit bool
	}{
		{[]string{"cache"}, true}, {[]string{"cache", "--key-from-git=false"}, false},
	} {
		s := &plugin.Settings{}
		app := &cli.App{Flags: settingsFlags(s), Action: func(*cli.Context) error { return nil }}
		if err := app.Run(tc.args); err != nil {
			t.Fatal(err)
		}
		if s.KeyFromGit != tc.fromGit || s.CommitSHA != strings.Repeat("a", 40) || !strings.HasPrefix(s.Cache, "{") || s.FlushAge != 30 {
			t.Fatalf("incorrect input: %+v", s)
		}
	}
	for _, legacy := range []string{"root", "filename", "mount", "path", "fallback-path", "restore", "rebuild", "flush", "transport.skip-verify"} {
		app := newApp()
		app.Writer = io.Discard
		app.ErrWriter = io.Discard
		if err := app.Run([]string{"cache", "--" + legacy, "x"}); err == nil {
			t.Errorf("legacy flag %s accepted", legacy)
		}
	}
}

func TestCLIHelpAndDisabledCache(t *testing.T) {
	app := newApp()
	var out bytes.Buffer
	app.Writer = &out
	if err := app.Run([]string{"cache", "--help"}); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"--cache", "--bucket", "--key-from-git", "--commit-sha"} {
		if !strings.Contains(out.String(), name) {
			t.Fatal("missing help", name)
		}
	}
	if len(app.Flags) != 22 {
		t.Fatalf("help flags: %d", len(app.Flags))
	}
	if err := newApp().Run([]string{"cache", "--mode", "restore", "--cache", "[]"}); err != nil {
		t.Fatal(err)
	}
	if err := newApp().Run([]string{"cache", "--mode", "restore", "--cache", "[]", "--log-level", "bad"}); err == nil {
		t.Fatal("invalid log level accepted")
	}
}

func TestCLIEnvironmentJSONAndGitRoundTrip(t *testing.T) {
	t.Chdir(t.TempDir())
	if err := os.WriteFile("pom.xml", []byte("source pom"), 0644); err != nil {
		t.Fatal(err)
	}
	repo, err := git.PlainInit(".", false)
	if err != nil {
		t.Fatal(err)
	}
	wt, err := repo.Worktree()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := wt.Add("pom.xml"); err != nil {
		t.Fatal(err)
	}
	sha, err := wt.Commit("source", &git.CommitOptions{Author: &object.Signature{Name: "test", Email: "test@example.com", When: time.Now()}})
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	objects := map[string][]byte{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch r.Method {
		case "PUT":
			data, err := io.ReadAll(r.Body)
			if err != nil {
				t.Error(err)
				w.WriteHeader(500)
				return
			}
			objects[r.URL.Path] = data
			w.Header().Set("ETag", `"object"`)
		case "GET":
			data, ok := objects[r.URL.Path]
			if !ok {
				t.Errorf("key drifted after worktree changes: %s", r.URL.Path)
				w.WriteHeader(404)
				return
			}
			w.Header().Set("Content-Length", fmt.Sprint(len(data)))
			_, _ = w.Write(data)
		default:
			t.Errorf("unexpected method %s", r.Method)
			w.WriteHeader(400)
		}
	}))
	defer server.Close()
	for key, value := range map[string]string{"CACHE_S3_ENDPOINT": server.URL, "CACHE_S3_REGION": "us-east-1", "CACHE_S3_ACCESS_KEY": "access", "CACHE_S3_SECRET_KEY": "secret", "DRONE_REPO_OWNER": "team", "DRONE_REPO_NAME": "repo", "DRONE_COMMIT_SHA": sha.String(), "PLUGIN_CACHE": `{"key":{"prefix":"maven-v1","files":["./**/pom.xml"]},"paths":["cache"]}`} {
		t.Setenv(key, value)
	}
	if err := os.Mkdir("cache", 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile("cache/value", []byte("cached"), 0644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", "")
	if err := newApp().Run([]string{"cache", "--mode", "rebuild", "--bucket", "bucket"}); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll("cache"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile("pom.xml", []byte("changed"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir("target", 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile("target/pom.xml", []byte("generated"), 0644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PLUGIN_CACHE", `[{"key":{"prefix":"maven-v1","files":["./**/pom.xml"]},"paths":["cache"]}]`)
	if err := newApp().Run([]string{"cache", "--mode", "restore", "--bucket", "bucket"}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile("cache/value")
	if err != nil || string(data) != "cached" {
		t.Fatalf("CLI roundtrip: %s %v", data, err)
	}
}
