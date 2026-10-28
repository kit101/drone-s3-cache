# S3 Cache 修复与 cache key 策略调研

调研日期：2026-10-28。状态：调研记录；接口设计已于本次讨论确认，现行约定见 [设计](cache-key-design.zh-CN.md)。

## 项目与证据

- Fork：https://github.com/kit101/drone-s3-cache；上游 drone-plugins/drone-s3-cache。
- 源码基线：aa0b81e5ccba0225ab62f3f709bd96090cf4ee09。
- 默认对象路径为 root/owner/repo/commit-branch/archive.tar；restore 已有一个默认分支 fallback，不是分支未命中就完全不能复用缓存。
- path、fallback_path、filename 可以手工控制对象位置；没有文件摘要 key，也没有多个有序 fallback key。
- 上游 [Issue #69](https://github.com/drone-plugins/drone-s3-cache/issues/69) 请求 cache_key 与避免重复上传；[#72](https://github.com/drone-plugins/drone-s3-cache/issues/72) 请求多路径恢复。后者是多份归档聚合，不能与 fallback 的择一恢复混为一谈。

## 待修复问题

1. OBS virtual-host 兼容：storage/s3/s3.go 没有暴露 MinIO SDK 的 BucketLookup；当前 SDK 对普通自定义域名使用 path-style。建议新增 addressing_style: auto|path|virtual，默认 auto 保持兼容，不把 OBS 行为硬编码给所有 S3 服务。
2. 管道生命周期：依赖 drone-cache-lib 的 rebuildCache 在 Put 提前返回后，先等待归档协程，再 defer 关闭 PipeReader；归档协程阻塞写管道，形成互等。restoreCache 在 Unpack 提前退出时也可能等待仍阻塞下载写入的协程。需要主动关闭管道并传播双方错误，网络传输也需要有界取消与超时。
3. 回归覆盖缺失：当前 plugin 的主要测试被 Skip，storage/s3 没有实际测试。修复应覆盖提前失败、损坏归档、上传读取中断与正常往返，不仅断言配置值。

2026-10-28 在当前依赖版本上，以 64 KiB 内存流及立即失败的模拟 Storage/Archive 验证：rebuild 和 restore 均超过 2 秒未退出。诊断程序放在本地临时目录，不是插件实现。未使用真实 OBS 凭据，也没有更改现有缓存对象。

死锁代码位于依赖库，不仅在本插件。修复需在插件内接管这两段小型传输编排，或另行 fork 依赖库；不能只改 endpoint 或在 Put 外层加日志。倾向先在本插件内修复，保留归档和存储接口，避免维护第二个 fork；具体落点需在实现设计中确定。

## CI 策略对比

| 工具 | key 来源 | 恢复方式 | 同 key 回写 | 借鉴点 |
| --- | --- | --- | --- | --- |
| GitLab CI | 字符串，或 prefix + files 内容摘要；也支持 files_commits | 主 key 后按顺序尝试 fallback_keys | 可覆盖；pull/push/pull-push 决定读写 | 文件摘要、明确回退、读写分离 |
| GitHub Actions | 表达式组合变量及 hashFiles | 精确匹配、前缀匹配、restore-keys，并受分支可见范围限制 | 不可修改现有缓存 | hit 类型、兼容环境维度、缓存版本 |
| CircleCI | 模板 checksum、arch、分支等 | 有序 keys，可前缀匹配 | 不可变，同 key 已存在则复用 | 手工版本前缀，避免每次构建生成新 key |
| Drone Meltwater Cache | Go template，可用 checksum、os、arch 等 | 文档提供自定义 cache_key 模板 | 需按所选实现核对，本文不假定 | 已有 Drone 模板先例；其文档 checksum 是 MD5，本项目建议 SHA-256 |

GitLab 的 files 按内容生成 key，跨分支内容相同可相同；files_commits 随文件最近提交变化，即使内容相同也可能产生新 key。前者更适合依赖缓存，后者依赖 Git 历史，不适合作为首版必需能力。GitLab 将不存在的输入文件忽略、全部不存在时退到 default；本项目可选择更严格的缺失文件报错，避免拼错路径仍共用缓存，但必须明确记录这一语义差异。

这些是在线文档的机制比较，不代表用户现有 GitLab 13.12 已具备所有新版本特性；也不要求 Drone 直接接受 GitLab 的嵌套 YAML 语法。前缀查找依赖 List 权限、排序与并发选择规则；精确 fallback 列表可只读取指定对象，行为更容易预测。

## 可选方案

A. 结构化 key（建议首版）：key 字符串 + key_files 文件列表 + fallback_keys 精确 key 列表。Drone settings 用扁平参数，避免嵌套对象到环境变量的额外编解码。覆盖主要依赖缓存需求，配置可检查。

B. 模板 key：类似 Meltwater/CircleCI，提供 checksum、变量与平台模板。组合更自由，但表达式错误、展开时机、模板函数和摘要兼容性都会成为接口，维护成本更高。

C. 多缓存声明：一次调用中定义多组 key、paths、fallback、policy。能减少步骤，但需要多份归档的失败策略与配置解析。属于独立扩展，不建议与首轮问题修复绑定。

## 建议需求边界（待确认）

- 未设置新 key 参数时，保留原有路径、单 fallback 与覆盖行为；新旧配置冲突应报错，不能静默优先其中一个。
- 新 key 默认按仓库隔离，不自动拼接分支或 tag；需要分支隔离时由 key 中的变量表达。这样同一依赖摘要可被 MR、main、tag 复用。平台、工具链和缓存格式版本由可读前缀明确编码。
- key_files 对工作区内指定文件计算 SHA-256；建议对路径排序、去重，以路径和内容的无歧义编码计算组合摘要。仅文件输入变化才变 key；文件顺序和 mtime 不改变 key。首版先支持明确路径，glob 是否必要待确认。
- key 本身不依赖 Git 历史或网络；启用 key_files 时必须有工作区文件，不能在未准备输入文件的 no-clone 步骤中直接计算。
- 有序精确 fallback 只用于恢复，成功一份就停止，不叠加解包；回存始终使用主 key。fallback 对象必须另有明确写入方，不能假定自动生成 main/latest 别名。
- 默认同 key 可覆盖，支持 nightly 刷新 Go build / BuildKit mounts 的增量积累。文件摘要只决定对象身份，不能被解释为自动跳过上传。可选不可变或 skip-existing 策略另行确认，不把简单 HEAD 检查当作原子并发保护。
- 区分 exact hit、fallback hit、miss、网络/权限错误，输出解析后的 key 和命中来源。仅对象不存在可视为正常 miss；权限和损坏不能伪装为 miss。保留旧模式的软失败兼容边界，新行为的失败策略需在实现前写清。
- MR 共享 main 缓存的写权限由 Drone secret 和存储权限约束；仅 key 中包含 protected/main 等字样不构成安全隔离。建议非可信构建只读，nightly/main 负责写入，不在插件内硬编码特定事件调度。
- 内容寻址会增加历史对象数；桶 lifecycle 清理旧对象，缓存目录自身仍由包管理器/编译器管理。key 变了不等于旧数据会被删除。

## 与现有 WeKnora 策略的对应

依赖归档仍可合并 npm、Go 模块及固定组件：例如前缀 deps-v1-node24-go1.26-linux-amd64，摘要输入包括 frontend/package-lock.json、go.mod、go.sum，以及实际控制固定组件版本的清单（实现前核对文件）。不必自动按分支切分。

Docker mounts 单独归档。它是持续积累的下载与编译缓存，同一个 key 应允许刷新；不能只因 Dockerfile 或 lockfile 未改就跳过回写。Go 构建缓存若放在依赖归档里，也有相同约束。现有普通 push 只恢复、nightly/Promote 回存可以继续用 Drone 的步骤条件表达，不属于 key 算法。

首版建议不引入每提交 SHA、每次构建编号或时间戳默认 key，不加入自动扫描最新前缀、多组缓存配置、Git 历史 key 或服务端缓存目录。这些都可独立讨论。

## 验证记录与后续验收

原仓库在 macOS Go 1.22.12 默认链接下测试二进制被 dyld 拒绝（missing LC_UUID）；改用 go test -ldflags=-linkmode=external -cover ./... 能通过，但覆盖率为 0%，不能证明缓存行为正确。

实现验收应包含：原配置兼容；哈希稳定性、文件变化/缺失/路径越界；回退顺序与仅写主 key；异常路径限时退出；MinIO path-style 与 OBS virtual-host 真实往返。OBS 使用独立诊断前缀；先小数据兼容性，再以实际缓存量测试。不改动 WeKnora 正式流水线、不替换现有生产缓存，直到新插件验证通过。

## 来源

- [原插件实现](https://github.com/drone-plugins/drone-s3-cache/blob/aa0b81e5ccba0225ab62f3f709bd96090cf4ee09/plugin/impl.go)
- [原 S3 存储适配](https://github.com/drone-plugins/drone-s3-cache/blob/aa0b81e5ccba0225ab62f3f709bd96090cf4ee09/storage/s3/s3.go)
- [固定版本 cache-lib 管道编排](https://github.com/drone/drone-cache-lib/blob/981868645a25/cache/cache.go)
- [GitLab 缓存说明](https://docs.gitlab.com/ci/caching/)
- [GitLab key/files/files_commits/policy 参考](https://docs.gitlab.com/ci/yaml/#cachekey)
- [GitHub Actions 缓存](https://docs.github.com/en/actions/reference/workflows-and-actions/dependency-caching)
- [CircleCI 缓存](https://circleci.com/docs/guides/optimize/caching/)
- [Meltwater key 模板](https://github.com/drone-plugins/drone-meltwater-cache/blob/master/docs/cache_key_templates.md)
