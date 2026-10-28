package s3

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestAddressingStyle(t *testing.T) {
	for _, tc := range []struct{ style, host, path string }{
		{"auto", "cache.example", "/bucket/object"},
		{"path", "cache.example", "/bucket/object"},
		{"virtual", "bucket.cache.example", "/object"},
	} {
		t.Run(tc.style, func(t *testing.T) {
			st, err := New(&Options{Endpoint: "cache.example", AddressingStyle: tc.style, Region: "us-east-1", Access: "access", Secret: "secret", UseSSL: true})
			if err != nil {
				t.Fatal(err)
			}
			u, err := st.client.PresignedGetObject(context.Background(), "bucket", "object", time.Minute, nil)
			if err != nil {
				t.Fatal(err)
			}
			if u.Host != tc.host || u.Path != tc.path {
				t.Fatalf("got %s", u)
			}
		})
	}
	if _, err := New(&Options{AddressingStyle: "invalid"}); err == nil {
		t.Fatal("invalid style accepted")
	}
	if _, err := New(&Options{Timeout: -time.Second}); err == nil {
		t.Fatal("negative timeout accepted")
	}
}
func TestGetObjectErrorClassification(t *testing.T) {
	for _, tc := range []struct {
		code   string
		status int
		miss   bool
	}{
		{"NoSuchKey", 404, true}, {"NoSuchBucket", 404, false}, {"AccessDenied", 403, false},
	} {
		t.Run(tc.code, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "GET" || r.URL.Path != "/bucket/object" {
					t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
				}
				w.Header().Set("Content-Type", "application/xml")
				w.WriteHeader(tc.status)
				fmt.Fprintf(w, "<Error><Code>%s</Code><Message>failure</Message></Error>", tc.code)
			}))
			defer server.Close()
			st, err := New(&Options{Endpoint: strings.TrimPrefix(server.URL, "http://"), Region: "us-east-1", Access: "access", Secret: "secret", AddressingStyle: "path"})
			if err != nil {
				t.Fatal(err)
			}
			err = st.Get("bucket/object", io.Discard)
			if err == nil || errors.Is(err, ErrCacheMiss) != tc.miss {
				t.Fatalf("got %v miss=%v", err, tc.miss)
			}
		})
	}
}
func TestGetTimeoutAndCancellation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done() }))
	defer server.Close()
	st, err := New(&Options{Endpoint: strings.TrimPrefix(server.URL, "http://"), Region: "us-east-1", Access: "access", Secret: "secret", AddressingStyle: "path", Timeout: 30 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	if err = st.Get("bucket/object", io.Discard); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("got %v", err)
	}
	if time.Since(started) > time.Second {
		t.Fatal("deadline not enforced")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err = st.GetContext(ctx, "bucket/object", io.Discard); !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v", err)
	}
}
