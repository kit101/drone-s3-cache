# Cache key 实施计划

目标：保留旧配置，修复 OBS 寻址及管道失败阻塞，落实已确认的文件摘要 key 与有序精确回退。

采用 Go 标准库、现有 MinIO SDK 和 cache-lib 的归档/存储接口。默认独立执行；用户无需重复批准已确认设计。

- [ ] 修复提交：main.go 增加 addressing-style、timeout；storage/s3/s3.go 增加 context 传输及 miss 分类；plugin/transfer.go 接管管道编排。测试在对应 *_test.go 覆盖立即失败、取消、尾部错误、寻址与超时，先重现再修复。
- [ ] 特性提交：main.go、plugin/impl.go 接入 key/key-files/fallback-keys；plugin/key.go 负责文件摘要与对象路径，plugin/restore.go 负责精确回退。测试真实文件、配置冲突、日志、恢复优先级与只写主 key，补 CLI 环境变量测试。
- [ ] 说明与交付：README.md 写配置、摘要/错误/回退/刷新语义；本地使用 go test -ldflags=-linkmode=external -race ./...、go vet ./...，再交叉构建 Linux amd64/arm64 与 Windows amd64。独立前缀 OBS 小数据往返及清理，以实际结果记录边界；不更改正式流水线。
- [ ] 自审 diff，提交并推送 fork，创建面向 fork master 的 PR，说明修复、接口、兼容与验证范围。

macOS Go 1.22.12 默认链接器的 LC_UUID 问题采用 external 链接规避，不改用户系统 Go 安装。研究阶段已证实原测试覆盖率为 0%，不能将原测试通过视作行为验收。
