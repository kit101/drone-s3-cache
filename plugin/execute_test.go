package plugin

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/drone-plugins/drone-s3-cache/storage/s3"
	"github.com/urfave/cli/v2"
)

func TestExecuteRestoreErrorCompatibility(t *testing.T) {
	for _, keyMode := range []bool{false, true} {
		t.Run(fmt.Sprint(keyMode), func(t *testing.T) {
			paths := make(chan string, 8)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				paths <- r.URL.Path
				w.Header().Set("Content-Type", "application/xml")
				w.WriteHeader(403)
				fmt.Fprint(w, "<Error><Code>AccessDenied</Code><Message>denied</Message></Error>")
			}))
			defer server.Close()
			p := keyPlugin(restoreMode)
			if !keyMode {
				p.settings.Key = ""
			}
			p.settings.S3Options = s3.Options{Endpoint: server.URL, Region: "us-east-1", Access: "test", Secret: "test", AddressingStyle: "path", Timeout: time.Second}
			if err := p.Validate(); err != nil {
				t.Fatal(err)
			}
			err := finishes(t, p.Execute)
			if (err != nil) != keyMode {
				t.Fatalf("keyMode=%v err=%v", keyMode, err)
			}
			if keyMode {
				if len(paths) != 1 {
					t.Fatal("key mode fell back on permission error")
				}
			} else {
				if len(paths) != 2 {
					t.Fatal("legacy mode did not try default branch")
				}
			}
		})
	}
}
func TestRebuildAlwaysTargetsPrimaryKey(t *testing.T) {
	inWorkspace(t)
	if err := os.WriteFile("data", []byte("payload"), 0644); err != nil {
		t.Fatal(err)
	}
	paths := make(chan string, 8)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths <- r.URL.Path
		w.Header().Set("Content-Type", "application/xml")
		w.WriteHeader(403)
		fmt.Fprint(w, "<Error><Code>AccessDenied</Code><Message>denied</Message></Error>")
	}))
	defer server.Close()
	p := keyPlugin(rebuildMode)
	p.settings.FallbackKeys = *cli.NewStringSlice("main")
	p.settings.Mount = *cli.NewStringSlice("data")
	p.settings.S3Options = s3.Options{Endpoint: server.URL, Region: "us-east-1", Access: "test", Secret: "test", AddressingStyle: "path", Timeout: time.Second}
	if err := p.Validate(); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := finishes(t, p.Execute); err == nil {
			t.Fatal("upload rejection was swallowed")
		}
	}
	if len(paths) != 2 {
		t.Fatalf("unexpected request count %d", len(paths))
	}
	close(paths)
	for path := range paths {
		if !strings.HasSuffix(path, "/keys/deps-v1/archive.tar") {
			t.Fatalf("wrote fallback or wrong path: %s", path)
		}
	}
}
