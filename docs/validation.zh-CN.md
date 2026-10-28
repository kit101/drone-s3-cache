# Cache key 与 S3 修复验证记录

日期：2026-10-28。范围：本地 fork 的插件实现，不是 WeKnora 正式流水线或新镜像发布验收。

## 本地检查

- go test -ldflags=-linkmode=external -race -cover ./... 通过。
- go vet ./... 通过。
- Linux amd64、Linux arm64、Windows amd64 的 CGO_ENABLED=0 构建通过；交叉编译不代表这些平台运行验收。
- 文件摘要有固定预期值测试，覆盖排序/去重/mtime 稳定性及内容变化。
- key 参数冲突、工作区越界、目录与符号链接越界、非法名称、最终长度限制通过定向测试。
- 故障测试覆盖上传提前拒绝、归档失败、解包提前退出取消网络、下载尾部失败与真实 tar/tgz 符号链接往返。
- HTTP 模拟验证寻址 URL、请求取消/超时、NoSuchKey 与 NoSuchBucket/AccessDenied 区别；Execute 级测试验证旧路径软失败、新 key 严格失败及 rebuild 只访问主 key。

## 真实 OBS

本地编译插件 CLI，以 virtual 寻址和已有 from-secret 对应凭据，在随机诊断前缀 plugin-validation/20261028-1bbc4da1ffbb 完成：

1. 16 MiB 随机数据的 tar.gz 分片上传与文件摘要 key 恢复；源与恢复文件 SHA-256 均为 676b493124b15c17bae8e4c6ac5de34023fc3e467772500b973cc605b1605ed5。
2. 同 key 回存不同内容，清空本地目录后恢复，确认新内容覆盖旧对象。
3. 修改 lockfile 后主 key 改变；跳过不存在的备用 key，从下一备用 key 成功恢复，并输出 fallback_hit。
4. 全部缺失输出 miss，退出成功且不生成缓存目录。
5. 旧 path 参数下 tar 归档上传与恢复。

仅清理本次随机前缀；再次列举确认对象数为 0。未触碰既有依赖或 Docker 缓存前缀，也未将凭据写入本仓库。测试使用本机网络，不等同 Kubernetes Runner 的 DNS、网络或镜像拉取验证。

## 真实 MinIO

使用本机已有 minio/minio:RELEASE.2023-12-09T18-17-51Z 启动随机名称临时容器、随机测试密码及临时桶，不挂载宿主数据。通过 S3 API 创建桶后，用当前插件 CLI 在 path 与默认 auto 两种寻址模式分别完成 1 MiB 随机内容的 tar.gz 分片上传与恢复比较。测试完成即移除容器及临时数据。

## 边界

未发布插件容器镜像、未运行 Drone Runner 流水线、未修改 WeKnora 当前缓存脚本或正式发布配置。现有 tar/tgz 解包实现保持原样，归档必须来自可信写入者；当前测试不构成恶意归档安全审计。较大生产缓存的吞吐与资源占用尚未基准测试。
