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

## Explicit cache keys

The fork adds `key`, `key_files` and `fallback_keys`. See the
[approved design](docs/cache-key-design.zh-CN.md) for the full contract and
[research](docs/cache-key-research.zh-CN.md) for the alternatives considered.

```yaml
steps:
  - name: restore-dependencies
    image: your-registry/drone-s3-cache:your-tested-version
    environment: &credentials
      CACHE_S3_ACCESS_KEY:
        from_secret: cache_access_key
      CACHE_S3_SECRET_KEY:
        from_secret: cache_secret_key
    settings: &dependencies
      mode: restore
      endpoint: https://your-s3-endpoint.example
      region: your-region
      root: your-bucket/ci-cache
      addressing_style: virtual
      timeout: 10m
      filename: archive.tar.gz
      key: deps-v1-node24-go1.26-linux-amd64
      key_files:
        - frontend/package-lock.json
        - go.mod
        - go.sum
      mount:
        - .cache/npm
        - .cache/go-mod

  # Run the dependency install and build steps here.

  - name: rebuild-dependencies
    image: your-registry/drone-s3-cache:your-tested-version
    environment: *credentials
    settings:
      <<: *dependencies
      mode: rebuild
```

The image reference above is a placeholder for an image you build from this fork;
no new plugin image is published by this change. Gate the rebuild step using your
pipeline's normal dependencies and event conditions.

| Setting | CLI flag / environment | Meaning |
| --- | --- | --- |
| `key` | `--key` / `PLUGIN_KEY` | Complete key, or prefix when `key_files` is set |
| `key_files` | `--key-files` / `PLUGIN_KEY_FILES` | Explicit workspace-relative files; no glob expansion |
| `fallback_keys` | `--fallback-keys` / `PLUGIN_FALLBACK_KEYS` | Ordered complete keys to restore on an object miss |
| `addressing_style` | `--addressing-style` / `PLUGIN_ADDRESSING_STYLE`, `CACHE_S3_ADDRESSING_STYLE` | `auto` (default), `path`, or `virtual` |
| `timeout` | `--timeout` / `PLUGIN_TIMEOUT`, `CACHE_S3_TIMEOUT` | Per-S3-operation timeout, default `10m`; zero selects the default |

CLI list flags can be repeated. Environment lists are comma-separated, as with
`PLUGIN_MOUNT`. Credentials retain their existing environment aliases. `root`
is the bucket and optional prefix (`PLUGIN_ROOT`), separate from `endpoint`.

### Key calculation and scope

Without `key_files`, the key is used unchanged. With files, the result is
`<key>-<SHA-256 hex>`. The v1 digest uses a fixed domain marker and sorted,
deduplicated normalized relative paths, each framed with its path length, path,
file length and contents. Configuration order and mtime do not affect it; both
file paths and contents do. A golden test freezes this format.

Files must exist and be regular files inside the workspace. Absolute paths,
parent traversal out of the workspace and symlinks escaping it fail validation.
Prepare the files before invoking the plugin and do not change them between
restore and rebuild. The key does not require Git history.

Objects are stored at:

```text
<root>/<repo owner>/<repo name>/keys/<resolved key>/<filename>
```

The owner may contain nested groups. Namespaces cannot contain empty, `.` or `..`
segments. Keys and filenames must be single path segments without control
characters or either slash; keys are limited to 512 bytes including the digest.
Keys do not implicitly include branch, tag, event, OS or architecture. Add the
compatibility dimensions your cache needs to the readable prefix. Different
archives should use different keys or filenames.

Without `key`, existing path defaults, single default-branch fallback and
restore soft-failure behavior remain. `key_files` and `fallback_keys` require
`key`; key mode cannot be mixed with `path`, `fallback_path` or `flush_path`.
Flush continues to use the existing `flush_path` interface, not new key options.

### Fallbacks, writes and errors

```yaml
key: deps-v1-node24-go1.26-linux-amd64
key_files: [frontend/package-lock.json, go.mod, go.sum]
fallback_keys:
  - deps-v1-node24-go1.26-linux-amd64-main
```

The plugin tries the resolved primary key and then each exact fallback in order,
stopping after the first success. Duplicate candidates are skipped. There is no
prefix search, automatic branch fallback, or archive merging in key mode. File
hashes are appended only to the primary key. A fallback object needs a separate
writer using that complete key **without** `key_files`; no aliases are created.

Rebuild always writes the primary key and may overwrite an existing object. It
never skips an upload based on a matching key and never writes the fallbacks.
This supports accumulating compiler caches without changing dependency locks.
Schedule refreshes through Drone conditions; use storage permissions to enforce
read-only consumers. Concurrent writers retain S3's object overwrite behavior.
Use bucket lifecycle rules to expire historical keys.

Only `NoSuchKey`/`NoSuchObject` is a normal miss in key mode. If all candidates are
absent, restore succeeds so the build can proceed cold. Bucket, permission,
network and archive errors fail the step and do not trigger fallback. Restore
logs `resolved_key`, `matched_key` (when applicable), and
`cache_result=exact_hit|fallback_hit|miss|error`.

### S3 compatibility

For OBS endpoints requiring bucket hostnames, select `addressing_style: virtual`.
For MinIO endpoints using bucket paths, select `path`, or retain `auto` for the
SDK's existing selection. The endpoint should be the regional service address;
put the bucket in `root`. Buckets must already exist; the plugin does not create
them or need a bucket HEAD request for normal upload/download.

Failed transfers close their pipes and cancel the opposite operation before
waiting for its goroutine. Network operations have a finite timeout. Downloads
close their objects and consume the transport tail even if the archive parser
finishes early. Tar/tgz handling otherwise retains the existing cache-lib
implementation: cache archives must come from trusted writers, and extraction
is not an untrusted-archive sandbox or an atomic directory replacement.

## Validation

```sh
go test -race -cover ./...
go vet ./...
```

On the macOS Go 1.22.12 environment used for development, test binaries need
`go test -ldflags=-linkmode=external -race -cover ./...` to avoid a local dyld
`LC_UUID` failure. No Go module dependencies were added or upgraded.
