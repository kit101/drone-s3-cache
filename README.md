# drone-s3-cache

Fork 自 [drone-plugins/drone-s3-cache](https://github.com/drone-plugins/drone-s3-cache)。

用于 Drone CI 的目录缓存插件，将构建工作区中的目录归档到兼容 S3 的对象存储，并在后续构建中恢复。

## 构建

需要 Go 1.26 或以上版本。

```sh
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
  go build -o release/linux/amd64/drone-s3-cache .
docker build -f docker/Dockerfile.linux.amd64 -t drone-s3-cache:local .
```

发布目标为 `docker.io/kit101z/drone-s3-cache`，GitHub Actions 构建 Linux amd64、arm64 镜像。

## 使用

新接口使用 `bucket` 和 JSON `cache`（单对象或对象数组），不兼容旧 `root`、`filename`、`mount` 或隐式默认分支 fallback。当前分支的接口尚未发布到 Docker Hub；以下本地镜像须先构建，并在 Drone Runner 所用的 Docker 环境中可用，正式使用时替换为已构建、可拉取的镜像标签。

```yaml
kind: pipeline
type: docker
name: build
trigger:
  event: [push]
steps:
  - name: cache-restore
    image: drone-s3-cache:local
    settings: &npm-cache
      mode: restore
      bucket: ci-cache
      endpoint: https://s3.example.com
      region: us-east-1
      access_key:
        from_secret: cache_access_key
      secret_key:
        from_secret: cache_secret_key
      cache:
        key:
          prefix: "npm-node24-{{ .OS }}-{{ .ARCH }}-v1"
          files: [package-lock.json]
        paths: [.npm]
  - name: build
    image: node:24-alpine
    commands:
      - npm ci --cache .npm
      - npm run build
  - name: cache-save
    image: drone-s3-cache:local
    settings:
      <<: *npm-cache
      mode: rebuild
    when:
      status: [success]
```

`key_from_git` 默认开启，通过纯 Go 库从 `DRONE_COMMIT_SHA` 的提交树匹配 key.files 并读取文件内容，不依赖系统 git，也不受工作区生成物影响。关闭后才按工作区计算；本地 CLI 的 Git 模式需提供 `--commit-sha` 或对应环境变量，并在待缓存项目的仓库根目录执行。缓存目录始终来自当前工作区。

对象位置为 `s3://<bucket>/<owner>/<repo>/<最终key>.tar.gz`。恢复依次尝试主 key 与显式 fallback_keys；回存只写主 key，同 key 可覆盖。稳定 main 缓存需要 main 成功构建单独回存，不会自动更新。归档格式固定为 gzip。

| 配置 | 说明 |
| --- | --- |
| `mode` | 必填：restore、rebuild、flush，一次只执行一种操作 |
| `bucket` | 桶名，需预先创建，不能包含对象路径 |
| `cache` | 单个缓存对象或对象数组；恢复/回存必填，`[]` 明确跳过 |
| `cache.key` | 固定字符串，或包含 prefix、files 的对象 |
| `cache.key.files` | 明确文件名或 `*`、`**` glob；按匹配文件路径与内容计算 SHA-256 |
| `cache.paths` | 实际归档的明确相对目录，不支持 glob，不允许重叠 |
| `cache.fallback_keys` | 按顺序精确匹配的完整 key；不会自动回存 |
| `key_from_git` / `commit_sha` | 默认按固定提交计算；SHA 默认取 DRONE_COMMIT_SHA，可显式指定 |
| `endpoint` / `region` | 存储入口与区域，也可由 CACHE_S3_ENDPOINT / CACHE_S3_REGION 提供 |
| `access_key` / `secret_key` | Drone secrets 或 CACHE_S3_ACCESS_KEY / CACHE_S3_SECRET_KEY；也支持 SDK 凭据链 |
| `addressing_style` | auto、path、virtual；OBS 建议显式 virtual，MinIO 可用 path |
| `timeout` | 单次存储操作超时，默认 10m，必须大于 0 |
| `flush_path` / `flush_age` | 仅用于 flush；默认仓库前缀和 30 天，不允许扩大到其他仓库 |

key、prefix 和 fallback 支持 `{{ .OS }}`、`{{ .ARCH }}`、`{{ .Env.VAR_NAME }}`。未知或空变量、缺失文件、任一 glob 无匹配、重复 key、未知 JSON 字段均在传输前报错。

S3 客户端初始化成功后，下载或解压故障警告后继续冷构建；配置、客户端初始化、回存和清理失败使步骤失败。只有对象不存在（NoSuchKey/NotFound）才继续 fallback，桶不存在或权限错误不会。恢复先隔离解压、校验 gzip 和路径后再替换命中的缓存目录，不合并旧文件；空目录不会覆盖远端对象。归档支持普通文件、目录和同一缓存目录内可解析的相对符号链接，拒绝越界链接、悬空链接、硬链接归档条目和特殊文件。

完整参数、CLI 与 10 类 Drone 场景见 [使用说明](docs/usage.zh-CN.md)，契约及验收项见 [设计说明](docs/cache-usage-proposal.zh-CN.md)。

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

当前 fork 的发布流程以 [GitHub Actions 配置](.github/workflows/ci.yml)为准，仍监听 `master`，不会因创建 `main` 或功能分支自动改变。仓库保留的 `.drone.yml` 是上游 VM 流水线，仍指向 `plugins/s3-cache`，不用于上述 fork 镜像发布。
