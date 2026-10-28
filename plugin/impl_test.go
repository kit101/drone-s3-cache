package plugin

import (
	"archive/tar"
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/drone-plugins/drone-plugin-lib/drone"
	"github.com/drone-plugins/drone-s3-cache/storage/s3"
	"github.com/urfave/cli/v2"
)

func TestLegacyDefaultBranchFallback(t *testing.T) {
	inWorkspace(t)
	var data bytes.Buffer
	tw := tar.NewWriter(&data)
	if err := tw.WriteHeader(&tar.Header{Name: "value", Size: 4, Mode: 0644}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write([]byte("warm")); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	paths := make(chan string, 4)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths <- r.URL.Path
		if strings.Contains(r.URL.Path, "/feature/") {
			w.Header().Set("Content-Type", "application/xml")
			w.WriteHeader(404)
			_, _ = fmt.Fprint(w, "<Error><Code>NoSuchKey</Code><Message>missing</Message></Error>")
			return
		}
		w.Header().Set("Content-Length", fmt.Sprint(data.Len()))
		_, _ = w.Write(data.Bytes())
	}))
	defer server.Close()
	p := &Plugin{settings: Settings{Restore: true, Root: "bucket", S3Options: s3.Options{Endpoint: server.URL, Region: "us-east-1", Access: "access", Secret: "secret", Timeout: time.Second}}, pipeline: drone.Pipeline{Repo: drone.Repo{Owner: "team", Name: "repo", Branch: "main"}}}
	p.pipeline.Commit.Branch = "feature"
	if err := p.Validate(); err != nil {
		t.Fatal(err)
	}
	if err := p.Execute(); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile("value")
	if err != nil || string(got) != "warm" {
		t.Fatalf("restore: %s %v", got, err)
	}
	close(paths)
	var seen []string
	for path := range paths {
		seen = append(seen, path)
	}
	if !reflect.DeepEqual(seen, []string{"/bucket/team/repo/feature/archive.tar", "/bucket/team/repo/main/archive.tar"}) {
		t.Fatalf("paths %v", seen)
	}
}
func TestLegacyRestoreRemainsSoftFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/xml")
		w.WriteHeader(403)
		_, _ = fmt.Fprint(w, "<Error><Code>AccessDenied</Code><Message>denied</Message></Error>")
	}))
	defer server.Close()
	p := &Plugin{settings: Settings{Mode: restoreMode, Root: "bucket", Path: "cache", FallbackPath: "default", S3Options: s3.Options{Endpoint: server.URL, Region: "us-east-1", Access: "access", Secret: "secret", Timeout: time.Second}}}
	if err := p.Validate(); err != nil {
		t.Fatal(err)
	}
	if err := p.Execute(); err != nil {
		t.Fatalf("legacy restore behavior changed: %v", err)
	}
}
func TestModeValidation(t *testing.T) {
	for _, s := range []Settings{{}, {Mode: "invalid"}, {Restore: true, Rebuild: true}, {Mode: restoreMode, Restore: true}, {Mode: rebuildMode}} {
		p := &Plugin{settings: s}
		if err := p.validateMode(); err == nil {
			t.Fatalf("accepted %#v", s)
		}
	}
	p := &Plugin{settings: Settings{Mode: rebuildMode, Mount: *cli.NewStringSlice("cache")}}
	if err := p.validateMode(); err != nil {
		t.Fatal(err)
	}
	if p.settings.Filename != "archive.tar" {
		t.Fatal("default archive format changed")
	}
}
