package s3

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/feature/s3/transfermanager"
	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/drone/drone-cache-lib/storage"
	"github.com/sirupsen/logrus"
)

// Options contains configuration for the S3 connection.
type Options struct {
	Endpoint            string
	AcceleratedEndpoint string
	Secret              string
	Access              string
	Token               string
	FileCredentials     string
	Profile             string
	AddressingStyle     string
	Timeout             time.Duration
	Region              string
	UseSSL              bool
}

type s3Storage struct {
	client   *awss3.Client
	uploader *transfermanager.Client
	timeout  time.Duration
}

func New(opts *Options) (*s3Storage, error) {
	style := opts.AddressingStyle
	if style != "" && style != "auto" && style != "path" && style != "virtual" {
		return nil, fmt.Errorf("invalid addressing style %q", style)
	}
	timeout := opts.Timeout
	if timeout < 0 {
		return nil, fmt.Errorf("timeout must be positive")
	}
	if timeout == 0 {
		timeout = 10 * time.Minute
	}
	if (opts.Access == "") != (opts.Secret == "") {
		return nil, fmt.Errorf("both access-key and secret-key are required")
	}
	if opts.Access != "" && opts.FileCredentials != "" {
		return nil, fmt.Errorf("static credentials and credentials file cannot be combined")
	}
	load := []func(*config.LoadOptions) error{}
	if opts.Region != "" {
		load = append(load, config.WithRegion(opts.Region))
	}
	if opts.Access != "" {
		load = append(load, config.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(opts.Access, opts.Secret, opts.Token)))
	}
	if opts.FileCredentials != "" {
		load = append(load, config.WithSharedCredentialsFiles([]string{opts.FileCredentials}))
	}
	if opts.Profile != "" {
		load = append(load, config.WithSharedConfigProfile(opts.Profile))
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cfg, err := config.LoadDefaultConfig(ctx, load...)
	if err != nil {
		return nil, fmt.Errorf("load AWS configuration: %w", err)
	}
	if cfg.Region == "" {
		cfg.Region = "us-east-1"
	}
	endpoint := opts.Endpoint
	if !strings.HasPrefix(endpoint, "http://") && !strings.HasPrefix(endpoint, "https://") {
		scheme := "http://"
		if opts.UseSSL {
			scheme = "https://"
		}
		endpoint = scheme + endpoint
	}
	endpointURL, err := url.Parse(endpoint)
	if err != nil || endpointURL.Host == "" {
		return nil, fmt.Errorf("invalid S3 endpoint %q", endpoint)
	}
	client := awss3.NewFromConfig(cfg, func(o *awss3.Options) {
		o.BaseEndpoint = aws.String(endpoint)
		// Preserve MinIO's previous auto choice for custom endpoints.
		awsHost := endpointURL.Hostname() == "s3.amazonaws.com" || strings.HasSuffix(endpointURL.Hostname(), ".amazonaws.com")
		googleHost := endpointURL.Hostname() == "storage.googleapis.com"
		o.UsePathStyle = style == "path" || ((style == "" || style == "auto") && !awsHost && !googleHost)
		if awsHost && opts.AcceleratedEndpoint != "" {
			o.BaseEndpoint = aws.String(endpointURL.Scheme + "://" + opts.AcceleratedEndpoint)
			o.UsePathStyle = false
		}
		o.RequestChecksumCalculation = aws.RequestChecksumCalculationWhenRequired
		o.ResponseChecksumValidation = aws.ResponseChecksumValidationWhenRequired
	})
	uploader := transfermanager.New(client, func(o *transfermanager.Options) {
		o.RequestChecksumCalculation = aws.RequestChecksumCalculationWhenRequired
		o.FailTimeout = min(timeout, 10*time.Second)
	})
	return &s3Storage{client: client, uploader: uploader, timeout: timeout}, nil
}

type responseBody struct {
	io.ReadCloser
	cancel context.CancelFunc
}

func (b *responseBody) Close() error { defer b.cancel(); return b.ReadCloser.Close() }

func (s *s3Storage) Get(parent context.Context, p string) (io.ReadCloser, error) {
	bucket, key := splitBucket(p)
	if bucket == "" || key == "" {
		return nil, fmt.Errorf("invalid path %s", p)
	}
	ctx, cancel := context.WithTimeout(parent, s.timeout)
	out, err := s.client.GetObject(ctx, &awss3.GetObjectInput{Bucket: aws.String(bucket), Key: aws.String(key)})
	if err != nil {
		cancel()
		return nil, err
	}
	logrus.WithField("path", p).Info("downloading file")
	return &responseBody{ReadCloser: out.Body, cancel: cancel}, nil
}
func (s *s3Storage) Put(parent context.Context, p string, src io.Reader) error {
	bucket, key := splitBucket(p)
	if bucket == "" || key == "" {
		return fmt.Errorf("invalid path %s", p)
	}
	ctx, cancel := context.WithTimeout(parent, s.timeout)
	defer cancel()
	// Transfer Manager may block on the archive before making any HTTP request.
	if pipe, ok := src.(*io.PipeReader); ok {
		stop := context.AfterFunc(ctx, func() { _ = pipe.CloseWithError(ctx.Err()) })
		defer stop()
	}
	logrus.WithField("path", p).Info("uploading file")
	out, err := s.uploader.UploadObject(ctx, &transfermanager.UploadObjectInput{Bucket: aws.String(bucket), Key: aws.String(key), Body: src, ContentType: aws.String("application/tar")})
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil && !errors.Is(err, ctxErr) {
			err = errors.Join(err, ctxErr)
		}
		return fmt.Errorf("upload %s: %w", p, err)
	}
	logrus.WithFields(logrus.Fields{"path": p, "size": out.ContentLength}).Info("file uploaded")
	return nil
}
func (s *s3Storage) List(parent context.Context, p string) ([]storage.FileEntry, error) {
	bucket, key := splitBucket(p)
	if bucket == "" || key == "" {
		return nil, fmt.Errorf("invalid path %s", p)
	}
	ctx, cancel := context.WithTimeout(parent, s.timeout)
	defer cancel()
	pages := awss3.NewListObjectsV2Paginator(s.client, &awss3.ListObjectsV2Input{Bucket: aws.String(bucket), Prefix: aws.String(key)})
	var files []storage.FileEntry
	for pages.HasMorePages() {
		page, err := pages.NextPage(ctx)
		if err != nil {
			return nil, err
		}
		for _, item := range page.Contents {
			files = append(files, storage.FileEntry{Path: bucket + "/" + aws.ToString(item.Key), Size: aws.ToInt64(item.Size), LastModified: aws.ToTime(item.LastModified)})
		}
	}
	logrus.WithFields(logrus.Fields{"path": p, "count": len(files)}).Info("found objects")
	return files, nil
}
func (s *s3Storage) Delete(parent context.Context, p string) error {
	bucket, key := splitBucket(p)
	if bucket == "" || key == "" {
		return fmt.Errorf("invalid path %s", p)
	}
	ctx, cancel := context.WithTimeout(parent, s.timeout)
	defer cancel()
	_, err := s.client.DeleteObject(ctx, &awss3.DeleteObjectInput{Bucket: aws.String(bucket), Key: aws.String(key)})
	return err
}
func splitBucket(p string) (string, string) {
	full := strings.TrimPrefix(p, "/")
	bucket, key, ok := strings.Cut(full, "/")
	if !ok || bucket == "" || key == "" {
		return "", ""
	}
	return strings.ToLower(bucket), key
}
