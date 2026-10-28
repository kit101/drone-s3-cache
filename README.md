# drone-s3-cache

[![Build Status](http://cloud.drone.io/api/badges/drone-plugins/drone-s3-cache/status.svg)](http://cloud.drone.io/drone-plugins/drone-s3-cache)
[![Gitter chat](https://badges.gitter.im/drone/drone.png)](https://gitter.im/drone/drone)
[![Join the discussion at https://discourse.drone.io](https://img.shields.io/badge/discourse-forum-orange.svg)](https://discourse.drone.io)
[![Drone questions at https://stackoverflow.com](https://img.shields.io/badge/drone-stackoverflow-orange.svg)](https://stackoverflow.com/questions/tagged/drone.io)
[![](https://images.microbadger.com/badges/image/plugins/s3-cache.svg)](https://microbadger.com/images/plugins/s3-cache "Get your own image badge on microbadger.com")
[![Go Doc](https://godoc.org/github.com/drone-plugins/drone-s3-cache?status.svg)](http://godoc.org/github.com/drone-plugins/drone-s3-cache)
[![Go Report](https://goreportcard.com/badge/github.com/drone-plugins/drone-s3-cache)](https://goreportcard.com/report/github.com/drone-plugins/drone-s3-cache)

Drone plugin that allows you to cache directories within the build workspace, this plugin is backed by S3 compatible storages. For the usage information and a listing of the available options please take a look at [the docs](http://plugins.drone.io/drone-plugins/drone-s3-cache/).

## Build

Build the binary with the following command:

```console
export GOOS=linux
export GOARCH=amd64
export CGO_ENABLED=0
export GO111MODULE=on

go build -v -a -tags netgo -o release/linux/amd64/drone-s3-cache
```

## Docker

Build the Docker image with the following command:

```console
docker build \
  --label org.label-schema.build-date=$(date -u +"%Y-%m-%dT%H:%M:%SZ") \
  --label org.label-schema.vcs-ref=$(git rev-parse --short HEAD) \
  --file docker/Dockerfile.linux.amd64 --tag plugins/s3-cache .
```

## Usage

```console
docker run --rm \
  -e PLUGIN_FLUSH=true \
  -e PLUGIN_ENDPOINT="http://minio.company.com" \
  -e PLUGIN_ACCESS_KEY="myaccesskey" \
  -e PLUGIN_SECRET_KEY="mysecretKey" \
  -v $(pwd):$(pwd) \
  -w $(pwd) \
  plugins/s3-cache

docker run --rm \
  -e PLUGIN_RESTORE=true \
  -e PLUGIN_ENDPOINT="http://minio.company.com" \
  -e PLUGIN_ACCESS_KEY="myaccesskey" \
  -e PLUGIN_SECRET_KEY="mysecretKey" \
  -e DRONE_REPO_OWNER="foo" \
  -e DRONE_REPO_NAME="bar" \
  -e DRONE_COMMIT_BRANCH="test" \
  -v $(pwd):$(pwd) \
  -w $(pwd) \
  plugins/s3-cache

docker run -it --rm \
  -v $(pwd):$(pwd) \
  -w $(pwd) \
  alpine:latest sh -c "mkdir -p cache && echo 'testing cache' >> cache/test && cat cache/test"

docker run --rm \
  -e PLUGIN_REBUILD=true \
  -e PLUGIN_MOUNT=".bundler" \
  -e PLUGIN_ENDPOINT="http://minio.company.com" \
  -e PLUGIN_ACCESS_KEY="myaccesskey" \
  -e PLUGIN_SECRET_KEY="mysecretKey" \
  -e DRONE_REPO_OWNER="foo" \
  -e DRONE_REPO_NAME="bar" \
  -e DRONE_COMMIT_BRANCH="test" \
  -v $(pwd):$(pwd) \
  -w $(pwd) \
  plugins/s3-cache
```

## AWS SDK v2 storage

This fork uses AWS SDK for Go v2 and `feature/s3/transfermanager` for uploads.
Go 1.24 or newer is required; the Drone build images use Go 1.26. Dependency
versions are pinned in `go.mod`.

The existing `root`, `path`, `fallback_path`, `filename`, `mount`, credentials and
mode settings remain. Normal restore still tries the current branch followed by
the default branch and reports a warning without failing the build if neither
can be restored. Rebuild errors fail the step. Buckets must already exist.

```yaml
settings:
  mode: restore
  endpoint: https://your-s3-endpoint.example
  region: your-region
  root: your-bucket
  addressing_style: virtual
  timeout: 10m
  filename: archive.tar.gz
```

| Setting | Environment | Meaning |
| --- | --- | --- |
| `addressing_style` | `PLUGIN_ADDRESSING_STYLE`, `CACHE_S3_ADDRESSING_STYLE` | `auto` (default), `path`, or `virtual` |
| `timeout` | `PLUGIN_TIMEOUT`, `CACHE_S3_TIMEOUT` | Go duration, default `10m`; zero selects the default |

Use the regional service endpoint and put the bucket in `root`. `auto` retains
path-style for custom non-AWS/non-Google endpoints; `virtual` uses the AWS
resolver's bucket-host addressing, subject to bucket DNS/TLS compatibility.
Use `virtual` for OBS and `path` or `auto` for a local MinIO endpoint.

Explicit access/secret/session credentials take precedence over the SDK default
credential chain. The existing credentials-file and profile options remain.
Without explicit credentials, the SDK loads its normal environment, shared
configuration and role providers. An omitted region uses the SDK configuration,
falling back to `us-east-1`; specify the actual bucket region for other regions.
S3 Transfer Acceleration retains its existing explicit endpoint option for AWS.

Downloads return a response stream directly to the unpacker. Closing the stream
releases the connection and cancels its timeout; successful unpacking also checks
the remaining response bytes and close errors. Uploads stream the archive through
a pipe. Failures close the pipe before waiting for the producer, preserve source
errors and capture tar/gzip final-write errors discarded by the old archive
library. Timeouts also close the archive pipe to interrupt the SDK's initial
source read, before any HTTP request exists. Transfer Manager aborts failed multipart uploads; cancellation cleanup
has a separate timeout of at most 10 seconds. Cleanup failures are reported and
may require storage-side cleanup.

For S3-compatible services, both the S3 client and Transfer Manager explicitly
use checksum calculation `when_required`; response validation also uses
`when_required`. This avoids optional checksum/trailer incompatibility while
retaining checksums required by the operation.

Archive formatting/extraction otherwise retains cache-lib's implementation.
Archives must come from trusted writers; restoration is not an atomic directory
replacement or an untrusted-archive sandbox.

See the [migration and validation record](docs/aws-sdk-migration.zh-CN.md).
