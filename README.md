# drone-s3-cache

Fork 自 [drone-plugins/drone-s3-cache](https://github.com/drone-plugins/drone-s3-cache)。

用于 Drone CI 的目录缓存插件，将构建工作区中的目录归档到兼容 S3 的对象存储，并在后续构建中恢复。

## 构建

需要 Go 1.24 或以上版本；CI 使用 Go 1.26。

```sh
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
  go build -o release/linux/amd64/drone-s3-cache .
docker build -f docker/Dockerfile.linux.amd64 -t drone-s3-cache:local .
```

发布镜像地址为 `docker.io/kit101z/drone-s3-cache`，支持 Linux amd64、arm64。

## 使用

以 Node.js 项目的 npm 下载缓存为例，构建前恢复，构建成功后回存。存储桶需要预先创建，凭据通过 Drone secrets 提供。

```yaml
kind: pipeline
type: docker
name: build

environment:
  CACHE_S3_ENDPOINT: https://s3.example.com
  CACHE_S3_REGION: us-east-1
  CACHE_S3_ACCESS_KEY:
    from_secret: cache_access_key
  CACHE_S3_SECRET_KEY:
    from_secret: cache_secret_key

steps:
  - name: cache-restore
    image: docker.io/kit101z/drone-s3-cache:latest
    settings:
      mode: restore
      root: ci-cache
      filename: npm.tar.gz

  - name: build
    image: node:24-alpine
    commands:
      - npm ci --cache .npm
      - npm run build

  - name: cache-rebuild
    image: docker.io/kit101z/drone-s3-cache:latest
    settings:
      mode: rebuild
      root: ci-cache
      filename: npm.tar.gz
      mount:
        - .npm
```

## 配置

| 配置 | 说明 |
| --- | --- |
| `mode` | `restore` 恢复、`rebuild` 回存、`flush` 清理过期缓存 |
| `root` | 存储桶名称 |
| `endpoint` / `region` | 对象存储入口和区域，也可通过全局环境变量 `CACHE_S3_ENDPOINT` / `CACHE_S3_REGION` 设置 |
| `access_key` / `secret_key` | 访问凭据，也可通过 `CACHE_S3_ACCESS_KEY` / `CACHE_S3_SECRET_KEY` 设置 |
| `filename` | 归档文件名，默认 `archive.tar`；使用 `.tar.gz` 后缀启用压缩 |
| `mount` | 回存的目录列表 |
| `path` | 桶内缓存路径，默认 `<仓库所有者>/<仓库名>/<分支>` |
| `fallback_path` | 恢复失败时尝试的路径，默认指向仓库默认分支 |
| `addressing_style` | `auto`（默认）、`path` 或 `virtual`；OBS 使用区域入口并设置 `virtual`，MinIO 可使用 `path` |
| `timeout` | 单次存储操作超时，默认 `10m` |
| `flush_path` / `flush_age` | 清理范围及过期天数，默认当前仓库路径及 30 天 |

`restore` 未能恢复缓存时输出警告并继续构建；`rebuild` 失败会使步骤失败。回存使用同一路径覆盖已有缓存，过期缓存需通过 `flush` 清理。

## 开发检查

```sh
go test -race -cover -timeout 30s ./...
golangci-lint run
```

GitHub Actions 自动运行测试、lint 和 Linux amd64/arm64、Windows amd64 构建，并验证双架构 Linux 镜像构建。

## 镜像发布

在仓库的 Actions secrets 中配置 `DOCKER_USERNAME` 和 `DOCKER_PASSWORD`（可使用有推送权限的 Docker Hub access token）。检查和构建通过后：

- 推送到 `master`：发布 `docker.io/kit101z/drone-s3-cache:latest`。
- 推送 `v*` tag：发布同名标签，例如 `docker.io/kit101z/drone-s3-cache:v1.0.0`。
- 手动运行 CI：在 `master` 或 `v*` tag 上发布相应标签，其他分支仅检查。
- PR：验证镜像构建，不登录或推送 Docker Hub。
