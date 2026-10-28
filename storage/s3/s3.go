package s3

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/drone/drone-cache-lib/storage"
	"github.com/dustin/go-humanize"
	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
	"github.com/sirupsen/logrus"
)

// Options contains configuration for the S3 connection.
type Options struct {
	Endpoint            string
	AcceleratedEndpoint string
	Key                 string
	Secret              string
	Access              string
	Token               string
	FileCredentials     string
	Profile             string
	AddressingStyle     string
	Timeout             time.Duration

	// us-east-1
	// us-west-1
	// us-west-2
	// eu-west-1
	// ap-southeast-1
	// ap-southeast-2
	// ap-northeast-1
	// sa-east-1
	Region string

	UseSSL bool
}

type s3Storage struct {
	client *minio.Client
	opts   *Options
	ctx    context.Context
}

// ErrCacheMiss means the object is absent, not that a bucket or permission is missing.
var ErrCacheMiss = errors.New("cache object not found")

// New creates an S3 backend with cancellable object transfers.
func New(opts *Options) (*s3Storage, error) {
	lookup := minio.BucketLookupAuto
	switch opts.AddressingStyle {
	case "", "auto":
	case "path":
		lookup = minio.BucketLookupPath
	case "virtual":
		lookup = minio.BucketLookupDNS
	default:
		return nil, fmt.Errorf("invalid addressing style %q", opts.AddressingStyle)
	}
	if opts.Timeout < 0 {
		return nil, fmt.Errorf("timeout must be positive")
	}
	copyOpts := *opts
	opts = &copyOpts
	if opts.Timeout == 0 {
		opts.Timeout = 10 * time.Minute
	}
	var creds *credentials.Credentials
	if len(opts.Access) != 0 && len(opts.Secret) != 0 {
		creds = credentials.NewStaticV4(opts.Access, opts.Secret, opts.Token)
	} else if len(opts.FileCredentials) != 0 {
		creds = credentials.NewFileAWSCredentials(opts.FileCredentials, opts.Profile)
	} else {
		creds = credentials.NewIAM("")

		// See if the IAM role can be retrieved
		_, err := creds.Get()
		if err != nil {
			return nil, fmt.Errorf("could not connect to %s using IAM role: %w", opts.Endpoint, err)
		}
	}
	client, err := minio.New(opts.Endpoint, &minio.Options{
		Creds:        creds,
		Secure:       opts.UseSSL,
		Region:       opts.Region,
		BucketLookup: lookup,
	})

	if err != nil {
		return nil, fmt.Errorf("could not connect to %s: %w", opts.Endpoint, err)
	}

	if opts.AcceleratedEndpoint != "" {
		client.SetS3TransferAccelerate(opts.AcceleratedEndpoint)
	}

	return &s3Storage{
		client: client,
		opts:   opts,
		ctx:    context.Background(),
	}, nil
}

func (s *s3Storage) Get(p string, dst io.Writer) error {
	return s.GetContext(s.ctx, p, dst)
}

func (s *s3Storage) GetContext(ctx context.Context, p string, dst io.Writer) error {
	ctx, cancel := context.WithTimeout(ctx, s.opts.Timeout)
	defer cancel()
	bucket, key := splitBucket(p)

	if len(bucket) == 0 || len(key) == 0 {
		return fmt.Errorf("invalid path %s", p)
	}

	logrus.WithFields(logrus.Fields{
		"bucket": bucket,
		"key":    key,
	}).Info("downloading file")

	object, err := s.client.GetObject(ctx, bucket, key, minio.GetObjectOptions{})
	if err != nil {
		return cacheReadError(err)
	}
	defer object.Close()

	numBytes, err := io.Copy(dst, object)
	if err != nil {
		return cacheReadError(err)
	}

	logrus.WithFields(logrus.Fields{
		"bucket": bucket,
		"key":    key,
		"size":   humanize.Bytes(uint64(numBytes)),
	}).Info("file downloaded", bucket, key)
	return nil
}

func (s *s3Storage) Put(p string, src io.Reader) error {
	return s.PutContext(s.ctx, p, src)
}

func (s *s3Storage) PutContext(ctx context.Context, p string, src io.Reader) error {
	ctx, cancel := context.WithTimeout(ctx, s.opts.Timeout)
	defer cancel()
	bucket, key := splitBucket(p)

	if len(bucket) == 0 || len(key) == 0 {
		return fmt.Errorf("invalid path %s", p)
	}

	logrus.WithFields(logrus.Fields{
		"bucket": bucket,
		"key":    key,
	}).Info("uploading file")

	uploadInfo, err := s.client.PutObject(ctx, bucket, key, src, -1, minio.PutObjectOptions{ContentType: "application/tar"})
	if err != nil {
		return fmt.Errorf("could not put file in bucket %s at %s: %w", bucket, key, err)
	}

	logrus.WithFields(logrus.Fields{
		"bucket": bucket,
		"key":    key,
		"size":   uploadInfo,
	}).Info("file uploaded")
	return nil
}

func (s *s3Storage) List(p string) ([]storage.FileEntry, error) {
	ctx, cancel := context.WithTimeout(s.ctx, s.opts.Timeout)
	defer cancel()
	bucket, key := splitBucket(p)

	if len(bucket) == 0 || len(key) == 0 {
		return nil, fmt.Errorf("invalid path %s", p)
	}

	logrus.WithFields(logrus.Fields{
		"bucket": bucket,
		"key":    key,
	}).Info("finding objects")

	exists, err := s.client.BucketExists(ctx, bucket)
	if err != nil {
		return nil, fmt.Errorf("error when accessing bucket %s: %w", bucket, err)
	} else if !exists {
		return nil, fmt.Errorf("bucket %s does not exist", bucket)
	}

	var objects []storage.FileEntry
	opts := minio.ListObjectsOptions{
		Recursive: true,
		Prefix:    key,
	}

	for object := range s.client.ListObjects(ctx, bucket, opts) {
		if object.Err != nil {
			return nil, fmt.Errorf("could not get file in bucket %s at %s: %w", bucket, object.Key, object.Err)
		}

		path := bucket + "/" + object.Key
		objects = append(objects, storage.FileEntry{
			Path:         path,
			Size:         object.Size,
			LastModified: object.LastModified,
		})
		logrus.WithFields(logrus.Fields{
			"bucket":        bucket,
			"key":           object.Key,
			"size":          humanize.Bytes(uint64(object.Size)),
			"last-modified": object.LastModified,
		}).Debug("found object")
	}

	logrus.WithFields(logrus.Fields{
		"bucket": bucket,
		"key":    key,
		"count":  len(objects),
	}).Info("found objects")
	return objects, nil
}

func (s *s3Storage) Delete(p string) error {
	ctx, cancel := context.WithTimeout(s.ctx, s.opts.Timeout)
	defer cancel()
	bucket, key := splitBucket(p)

	if len(bucket) == 0 || len(key) == 0 {
		return fmt.Errorf("invalid path %s", p)
	}

	logrus.WithFields(logrus.Fields{
		"bucket": bucket,
		"key":    key,
	}).Info("deleting object")

	exists, err := s.client.BucketExists(ctx, bucket)
	if err != nil {
		return fmt.Errorf("error when accessing bucket %s: %w", bucket, err)
	} else if !exists {
		return fmt.Errorf("bucket %s does not exist", bucket)
	}

	err = s.client.RemoveObject(ctx, bucket, key, minio.RemoveObjectOptions{})
	if err != nil {
		return fmt.Errorf("could not delete file in %s at %s: %w", bucket, key, err)
	}
	return err
}

func splitBucket(p string) (string, string) {
	// Remove initial forward slash
	full := strings.TrimPrefix(p, "/")

	// Get first index
	i := strings.Index(full, "/")

	if i != -1 && len(full) != i+1 {
		// Bucket names need to be all lower case for the key it doesnt matter
		return strings.ToLower(full[0:i]), full[i+1:]
	}

	return "", ""
}

func cacheReadError(err error) error {
	code := minio.ToErrorResponse(err).Code
	if code == "NoSuchKey" || code == "NoSuchObject" {
		return fmt.Errorf("%w: %v", ErrCacheMiss, err)
	}
	return err
}
