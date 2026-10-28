# Cache key 与 S3 修复设计

状态：用户已于 2026-10-28 确认设计；研究依据见[调研记录](cache-key-research.zh-CN.md)。

## 缓存身份与存储

新增扁平参数 key、key_files、fallback_keys。key_files 未指定时，key 原样使用；指定时，按规范化相对路径排序、去重，对路径长度、路径、文件长度和文件内容计算组合 SHA-256，生成 key-hex。使用固定域分隔标记定义 v1 摘要算法。mtime、文件配置顺序、构建事件、分支均不参与。文件须存在、为工作区内的普通文件，拒绝绝对路径、越界路径与指向工作区外的符号链接；不支持 glob、Git 历史或模板函数。恢复和回存各自在执行前计算摘要，调用方保证输入文件不被构建改写。

对象路径为 root/owner/repo/keys/resolved-key/filename。key 模式要求 root、owner、repo 有效，key 和 filename 必须是单一路径段，不能为 . 或 ..，禁止 /、反斜杠与控制字符；owner、repo 同样禁止目录跳转。限制最终 key 为 512 字节，超限报错，不静默截断或替换。平台、工具链、格式版本由 key 显式编码；不隐式追加分支或 tag。

配置 key 后禁止 path、fallback_path、flush_path；未配置 key 则禁止 key_files 和 fallback_keys。旧模式默认路径与默认分支回退保持原样。flush 使用原 flush_path，不能配置新 key 参数，避免误删。

## 恢复与回写

新 key 模式依次精确恢复主 key、fallback_keys，去重并保持优先级；只在对象不存在时继续，首次成功即停止。备用 key 是完整 key，不自动追加文件摘要，不做前缀列表查询，不合并归档。全部缺失为正常 miss；权限、网络、解包失败返回错误。旧模式保留回退及记录警告后继续的兼容行为。

rebuild 仅写主 key，同 key 允许覆盖，不根据远端是否存在自动跳过。不自动生成备用对象。key_files 仅决定对象身份。使用方通过步骤条件和存储权限约束写入；历史缓存由桶生命周期管理。记录 resolved_key、cache_result（exact_hit、fallback_hit、miss、error）和 matched_key，错误不记录凭据。

## S3 兼容与失败处理

新增 addressing_style: auto|path|virtual，默认 auto。透传 MinIO SDK BucketLookup，不硬编码云厂商。S3 请求默认每次操作超时 10 分钟，timeout 可用 Go duration 配置；提前发生归档或网络错误时取消对端，并关闭管道传播原始错误。

依赖库 cache.go 的两段传输编排由插件本地接管；保留其 Archive、Storage 和 Flusher 接口，不再增加第二个 fork。Get 直接请求对象，仅 NoSuchKey/NoSuchObject 标为 miss；NoSuchBucket、403 与网络错误不属于 miss。下载对象必须关闭。归档提前结束时取消下载；解包成功后排空流，保证传输尾部错误不会被忽略。

本次保留原有 tar/tgz 实现，不扩展归档格式或重写解包器；现有归档应来自可信写入者，不宣称具备恶意归档沙箱能力。

## 验收

使用真实文件证明摘要稳定、跨事件相同、输入变化会变 key；覆盖缺失、路径与符号链接越界、旧新配置冲突、有序回退、非 miss 不回退、仅写主 key、同 key 覆盖。覆盖上传提前失败、打包失败、解包提前失败、下载失败及取消、正常 tar/tgz 往返和尾部网络错误。通过本地 HTTP S3 模拟检查 path/virtual 的 Host/URL、错误分类和超时。运行 race 测试与 Linux amd64/arm64、Windows 构建检查。真实 OBS 只用独立诊断前缀，不替换 WeKnora 正式配置或缓存。
