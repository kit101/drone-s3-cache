# AWS SDK v2 迁移记录

最后核对：2026-10-30。本文区分 SDK 迁移时的历史验证与当前缓存接口；当前参数及示例以[使用说明](usage.zh-CN.md)为准。历史 OBS 成功不表示新 cache 接口已在 OBS 或真实 Drone Runner 验收。

## 实现范围

- 采用 AWS SDK for Go v2 的配置、凭据链、S3 client 与 feature/s3/transfermanager；当前 go.mod 最低要求 Go 1.26.0（包含 go-git 文件 key 实现），CI 使用 Go 1.26。
- 当前使用 endpoint、bucket、region、静态凭据/凭据文件/profile、transfer acceleration，以及 auto/path/virtual 与操作超时选项。auto 对自定义非 AWS/Google 入口保留 path-style；virtual 交给 AWS resolver，桶名称须符合虚拟主机寻址要求。
- Get 返回 io.ReadCloser，超时覆盖响应体读取，关闭响应体同时取消请求；解包后检查尾部读取与关闭错误，不再通过下载管道和后台协程复制。
- 上传仍通过管道衔接归档器和 Transfer Manager，错误关闭管道，保留源错误并等待协程释放。保留尾部 Write 错误检测，当前内置 tar/gzip 归档器也传播 Close 错误，不允许归档失败被当成成功；分片失败由官方工具清理，取消时使用独立有界的清理 context。SDK 在首个请求前读取归档流也可能阻塞，因此上传 timeout/context 取消时主动关闭输入管道，不能仅依赖 HTTP 超时。
- OBS 兼容设置在 S3 client 与 Transfer Manager 两层均指定 when_required，仍验证服务要求的校验和。删除过期缓存继续由插件判断，适配层只负责 List/Delete。

## 验证范围

定向测试覆盖提前上传失败、打包及尾部写入失败、流式下载及资源释放、操作超时、分片源错误和网络拒绝后的 AbortMultipartUpload、配置校验与列表分页。迁移时的旧接口 OBS/MinIO 往返在独立诊断空间执行，结果保留在下节；旧配置、隐式默认分支 fallback 和普通 tar 选项已被新接口删除，不再作为当前验收要求。

## SDK 迁移阶段的历史验证结果

以下来自此前 SDK 迁移阶段，未在本次文档核对中重跑；当时的 root、默认分支 fallback、普通 tar 等行为不能用于配置当前接口。

- Go 1.26.0 下 go test -race -cover -timeout 30s ./...、go vet ./... 和 golangci-lint v2.15.0 通过。
- HTTP 协议测试确认：源读取失败、分片被拒和请求取消均不会 CompleteMultipartUpload，并触发 AbortMultipartUpload；取消后清理请求使用有效的独立 context。另有先复现再修复的空闲输入管道超时测试，验证 SDK 首次读流也能被打断。
- 真实 OBS 使用隔离前缀 plugin-validation/aws-df163e4384b4，20 MiB 随机文件 tar.gz 分片往返的 SHA-256 为 64cfaaa98f32a3de174be896b8c0cd700ab8ed9d68c408a2ba28d891ad3e4871。默认分支回退与普通 tar 往返均通过。
- 在已成功缓存的同一路径上，故意于超过分片阈值后让打包失败：CLI 返回错误，重新恢复仍是之前的成功内容；S3 ListMultipartUploads 确认该诊断前缀下没有未完成分片。只清理本次前缀，第二次列举确认对象数为 0。
- 临时 MinIO 容器以随机测试密码分别验证 path/auto，覆盖 1 MiB 单次上传及 20 MiB 分片上传的 tar.gz 往返；无宿主数据挂载，容器结束后删除。
- Linux amd64/arm64、Windows amd64 的无 CGO 构建通过；这些构建不等同跨平台运行验收。

## 当前缓存接口与验证边界

- 对外使用 `bucket`、显式 `mode` 和 JSON `cache`，归档固定为 tar.gz，上传 Content-Type 为 application/gzip；不接受旧 root、filename、mount 等参数。
- 传输适配层保留配置、凭据链、超时、分片失败清理和 when_required 校验设置。当前插件要求 endpoint 为不携带桶路径的 HTTP/HTTPS URL，timeout 必须大于 0；适配层内部的默认处理不是额外的 CLI 兼容入口。
- 恢复只在 NoSuchKey/NotFound 时尝试下一个 fallback；NoSuchBucket、403 或损坏归档停止该条目的尝试。客户端初始化失败使步骤失败；成功初始化后的恢复故障才允许警告后冷构建。
- 新接口已在上一轮实现验证中通过本地 MinIO 的 20 MiB tar.gz 分片上传、Git 快照精确命中、fallback、失败上传保留旧对象和零残留分片检查。该结果不覆盖远端 OBS、真实 Drone Runner 或镜像发布。
- 本次文档核对重新运行本地测试并比对实际 CLI 帮助，不重复上述容器与远端测试。
