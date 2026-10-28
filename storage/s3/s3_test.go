package s3

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/smithy-go"
)

func testStore(t *testing.T, endpoint string) *s3Storage {
	t.Helper()
	st, err := New(&Options{Endpoint: endpoint, Region: "us-east-1", Access: "access", Secret: "secret", AddressingStyle: "path", Timeout: 3 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	return st
}
func TestAddressingStyle(t *testing.T) {
	for _, tc := range []struct{ style, host, path string }{
		{"auto", "cache.example", "/bucket/object"}, {"path", "cache.example", "/bucket/object"}, {"virtual", "bucket.cache.example", "/object"},
	} {
		t.Run(tc.style, func(t *testing.T) {
			st, err := New(&Options{Endpoint: "cache.example", AddressingStyle: tc.style, Region: "us-east-1", Access: "access", Secret: "secret", UseSSL: true})
			if err != nil {
				t.Fatal(err)
			}
			signed, err := awss3.NewPresignClient(st.client).PresignGetObject(context.Background(), &awss3.GetObjectInput{Bucket: aws.String("bucket"), Key: aws.String("object")})
			if err != nil {
				t.Fatal(err)
			}
			u, err := url.Parse(signed.URL)
			if err != nil {
				t.Fatal(err)
			}
			if u.Host != tc.host || u.Path != tc.path {
				t.Fatalf("got %s", u)
			}
		})
	}
	for _, tc := range []struct{ endpoint, accelerate, host string }{
		{"s3.amazonaws.com", "", "bucket.s3.amazonaws.com"},
		{"s3.amazonaws.com", "s3-accelerate.amazonaws.com", "bucket.s3-accelerate.amazonaws.com"},
		{"storage.googleapis.com", "", "bucket.storage.googleapis.com"},
	} {
		st, err := New(&Options{Endpoint: tc.endpoint, AcceleratedEndpoint: tc.accelerate, Region: "us-east-1", Access: "access", Secret: "secret", UseSSL: true})
		if err != nil {
			t.Fatal(err)
		}
		signed, err := awss3.NewPresignClient(st.client).PresignGetObject(context.Background(), &awss3.GetObjectInput{Bucket: aws.String("bucket"), Key: aws.String("object")})
		if err != nil {
			t.Fatal(err)
		}
		u, _ := url.Parse(signed.URL)
		if u.Host != tc.host {
			t.Fatalf("got %s", u)
		}
	}
	for _, opts := range []Options{{AddressingStyle: "invalid"}, {Timeout: -time.Second}, {Access: "one-sided"}, {Access: "access", Secret: "secret", FileCredentials: "file"}} {
		if _, err := New(&opts); err == nil {
			t.Fatal("invalid options accepted")
		}
	}
}
func TestGetObjectErrors(t *testing.T) {
	for _, tc := range []struct {
		code   string
		status int
	}{{"NoSuchKey", 404}, {"NoSuchBucket", 404}, {"AccessDenied", 403}} {
		t.Run(tc.code, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "GET" || r.URL.Path != "/bucket/object" {
					t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
				}
				w.Header().Set("Content-Type", "application/xml")
				w.WriteHeader(tc.status)
				_, _ = fmt.Fprintf(w, "<Error><Code>%s</Code><Message>failure</Message></Error>", tc.code)
			}))
			defer server.Close()
			_, err := testStore(t, server.URL).Get(context.Background(), "bucket/object")
			var apiErr smithy.APIError
			if !errors.As(err, &apiErr) || apiErr.ErrorCode() != tc.code {
				t.Fatalf("got %v", err)
			}
		})
	}
}
func TestDownloadTimeoutCoversBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "100")
		w.WriteHeader(200)
		_, _ = fmt.Fprint(w, "x")
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	defer server.Close()
	st := testStore(t, server.URL)
	st.timeout = 50 * time.Millisecond
	body, err := st.Get(context.Background(), "bucket/object")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = body.Close() }()
	_, err = io.ReadAll(body)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("got %v", err)
	}
}
func TestDownloadCloseCancelsRequest(t *testing.T) {
	stopped := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "100")
		w.WriteHeader(200)
		w.(http.Flusher).Flush()
		<-r.Context().Done()
		close(stopped)
	}))
	defer server.Close()
	body, err := testStore(t, server.URL).Get(context.Background(), "bucket/object")
	if err != nil {
		t.Fatal(err)
	}
	if err = body.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("request still active")
	}
}

type zeroReader struct{}

func (zeroReader) Read(p []byte) (int, error) { clear(p); return len(p), nil }

type failedReader struct{ err error }

func (r failedReader) Read([]byte) (int, error) { return 0, r.err }

func TestMultipartFailureAborts(t *testing.T) {
	for _, scenario := range []string{"source", "part", "cancel"} {
		t.Run(scenario, func(t *testing.T) {
			var aborted, completed atomic.Int32
			partStarted := make(chan struct{}, 1)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				for k := range r.Header {
					if strings.Contains(strings.ToLower(k), "checksum") {
						t.Errorf("optional checksum unexpectedly sent: %s", k)
					}
				}
				w.Header().Set("Content-Type", "application/xml")
				switch {
				case r.Method == "POST" && r.URL.Query().Has("uploads"):
					_, _ = fmt.Fprint(w, "<InitiateMultipartUploadResult><Bucket>bucket</Bucket><Key>object</Key><UploadId>test-upload</UploadId></InitiateMultipartUploadResult>")
				case r.Method == "PUT" && r.URL.Query().Has("partNumber"):
					select {
					case partStarted <- struct{}{}:
					default:
					}
					if scenario == "cancel" {
						_, _ = io.Copy(io.Discard, r.Body)
						return
					}
					if scenario == "part" {
						w.WriteHeader(403)
						_, _ = fmt.Fprint(w, "<Error><Code>AccessDenied</Code><Message>part rejected</Message></Error>")
						return
					}
					_, _ = io.Copy(io.Discard, r.Body)
					w.Header().Set("ETag", `"part-etag"`)
				case r.Method == "DELETE" && r.URL.Query().Get("uploadId") == "test-upload":
					if r.Context().Err() != nil {
						t.Error("abort inherited canceled context")
					}
					aborted.Add(1)
					w.WriteHeader(204)
				case r.Method == "POST" && r.URL.Query().Has("uploadId"):
					completed.Add(1)
					_, _ = fmt.Fprint(w, "<CompleteMultipartUploadResult><ETag>complete</ETag></CompleteMultipartUploadResult>")
				default:
					t.Errorf("unexpected request: %s %s", r.Method, r.URL)
					w.WriteHeader(400)
				}
			}))
			defer server.Close()
			st := testStore(t, server.URL)
			sourceErr := errors.New("source failed")
			src := io.LimitReader(zeroReader{}, 20*1024*1024)
			if scenario == "source" {
				src = io.MultiReader(io.LimitReader(zeroReader{}, 20*1024*1024), failedReader{sourceErr})
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan error, 1)
			go func() { done <- st.Put(ctx, "bucket/object", src) }()
			if scenario == "cancel" {
				select {
				case <-partStarted:
					cancel()
				case <-time.After(2 * time.Second):
					t.Fatal("part upload never started")
				}
			}
			select {
			case err := <-done:
				if err == nil {
					t.Fatal("failed upload reported success")
				}
				if scenario == "source" && !errors.Is(err, sourceErr) {
					t.Fatalf("source error lost: %v", err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("multipart failure did not stop")
			}
			if aborted.Load() != 1 || completed.Load() != 0 {
				t.Fatalf("abort=%d complete=%d", aborted.Load(), completed.Load())
			}
		})
	}
}
func TestListPaginationAndDelete(t *testing.T) {
	var pages, deletes atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "DELETE" {
			deletes.Add(1)
			w.WriteHeader(204)
			return
		}
		if r.Method != "GET" || r.URL.Query().Get("prefix") != "prefix/" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL)
		}
		w.Header().Set("Content-Type", "application/xml")
		pages.Add(1)
		token := ""
		truncated := "false"
		if r.URL.Query().Get("continuation-token") == "" {
			token = "<NextContinuationToken>next</NextContinuationToken>"
			truncated = "true"
		}
		_, _ = fmt.Fprintf(w, "<ListBucketResult><IsTruncated>%s</IsTruncated>%s<Contents><Key>prefix/item</Key><Size>3</Size><LastModified>2026-10-28T00:00:00Z</LastModified></Contents></ListBucketResult>", truncated, token)
	}))
	defer server.Close()
	st := testStore(t, server.URL)
	items, err := st.List(context.Background(), "bucket/prefix/")
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 || pages.Load() != 2 || items[0].Path != "bucket/prefix/item" || items[0].Size != 3 {
		t.Fatalf("items %v pages %d", items, pages.Load())
	}
	if err = st.Delete(context.Background(), items[0].Path); err != nil {
		t.Fatal(err)
	}
	if deletes.Load() != 1 {
		t.Fatal("delete not called")
	}
}
func TestCredentialSources(t *testing.T) {
	t.Setenv("AWS_ACCESS_KEY_ID", "env-access")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "env-secret")
	t.Setenv("AWS_SESSION_TOKEN", "")
	for _, static := range []bool{false, true} {
		opts := Options{Endpoint: "https://cache.example", Region: "us-east-1"}
		if static {
			opts.Access = "explicit-access"
			opts.Secret = "explicit-secret"
			opts.Token = "session"
		}
		st, err := New(&opts)
		if err != nil {
			t.Fatal(err)
		}
		creds, err := st.client.Options().Credentials.Retrieve(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		want := "env-access"
		if static {
			want = "explicit-access"
		}
		if creds.AccessKeyID != want {
			t.Fatalf("got %s", creds.AccessKeyID)
		}
	}
	t.Setenv("AWS_ACCESS_KEY_ID", "")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "")
	file := filepath.Join(t.TempDir(), "credentials")
	if err := os.WriteFile(file, []byte("[cache]\naws_access_key_id=file-access\naws_secret_access_key=file-secret\n"), 0600); err != nil {
		t.Fatal(err)
	}
	st, err := New(&Options{Endpoint: "https://cache.example", FileCredentials: file, Profile: "cache"})
	if err != nil {
		t.Fatal(err)
	}
	creds, err := st.client.Options().Credentials.Retrieve(context.Background())
	if err != nil || creds.AccessKeyID != "file-access" {
		t.Fatalf("file credentials: %v", err)
	}
}

func TestUploadTimeoutUnblocksIdleArchivePipe(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { t.Error("upload sent a request without source bytes") }))
	defer server.Close()
	st := testStore(t, server.URL)
	st.timeout = 30 * time.Millisecond
	reader, writer := io.Pipe()
	defer func() { _ = reader.Close() }()
	defer func() { _ = writer.Close() }()
	done := make(chan error, 1)
	go func() { done <- st.Put(context.Background(), "bucket/object", reader) }()
	select {
	case err := <-done:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("got %v", err)
		}
	case <-time.After(300 * time.Millisecond):
		_ = reader.CloseWithError(context.DeadlineExceeded)
		if err := <-done; err == nil {
			t.Error("unblocked upload unexpectedly succeeded")
		}
		t.Fatal("timeout did not interrupt the SDK's source read")
	}
}
