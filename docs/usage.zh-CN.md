# Drone S3 Cache 使用说明

最后核对：2026-10-30（当前工作区代码）

本文描述本分支的新接口，供使用与验收。接口已实现；**尚未发布新版镜像，也未在实际 Drone Runner 或远端 S3/OBS 环境实跑**。示例标签 next 仍是占位，使用前先构建镜像并替换为 Runner 可拉取的标签。

已采用全新接口：bucket、显式 mode、对象或数组 cache；不保留 root、filename、policy 等旧入口。归档固定为 tar.gz，文件 key 默认通过纯 Go 的 go-git 按固定提交计算。设计依据与验收场景见 [设计说明](cache-usage-proposal.zh-CN.md)。

## 1. 使用方式

```text
恢复缓存 → 执行依赖安装、构建和测试 → 成功后保存缓存
```

- `mode: restore`：只恢复缓存，先主 key，后依次尝试 fallback。
- `mode: rebuild`：只保存到所列条目的主 key，不自动更新 fallback。
- `mode: flush`：清理指定前缀下超过保留天数的对象。
- 只需读取时，只安排恢复步骤；只需写入时，只安排回存步骤。
- 对象形式表示一组缓存；数组形式表示多组缓存。一个对象与只包含它的单元素数组等价。

一次插件调用不会同时包办构建前后两个阶段，也不会运行用户的构建命令。缓存缺失时仍须能冷构建；缓存命中不等于依赖完整、跳过测试或业务验收通过。

## 2. `drone-s3-cache --help` 完整接口

以下按用途分组说明 `drone-s3-cache --help` 的全部 22 个参数（含 help/version）。排版为文档形式；程序帮助提供相同参数、默认值及环境变量；以下不是帮助输出的逐字副本。不继承整组 Drone 元数据参数，不提供旧参数或隐藏兼容开关。

参数优先级：显式 CLI 参数 > 列出的第一个已设置环境变量 > 默认值。多个别名不要同时设置。参数没有对应的别名时，不会尝试读取已删除名称。CLI 的横线参数在 Drone settings 中使用下划线，例如 `--access-key` 对应 `access_key`。`--repo.owner`、`--repo.name` 由 Drone 环境提供，不放入 settings。

```text
NAME:
  drone-s3-cache - 将工作区目录缓存到 S3 兼容存储

USAGE:
  drone-s3-cache [global options] command [command options] [arguments...]
  drone-s3-cache help

VERSION:
  <构建时注入的版本；未注入为 unknown>

COMMANDS:
  help, h    显示帮助

GLOBAL OPTIONS:
  缓存操作:
    --mode STRING
      必填；restore 只恢复、rebuild 只回存、flush 删除过期对象；无默认。
      环境变量：PLUGIN_MODE
    --cache STRING
      完整 JSON 对象或对象数组；restore/rebuild 必填，[] 明确跳过；flush 不接受。
      不按逗号拆分，也不通过重复 --cache 拼接；未知字段及无效 JSON 报错。
      key.files 支持明确文件、* 和跨目录 **；paths 只接受明确缓存目录。
      环境变量：PLUGIN_CACHE
    --key-from-git[=BOOL]
      默认 true；key.files 从固定提交的 Git 文件树与 Blob 内容计算，而非当前工作区。
      false 时匹配当前工作区文件；只影响文件 key，不改变 paths 的归档来源。
      环境变量：PLUGIN_KEY_FROM_GIT
    --commit-sha STRING
      Git 模式的完整 40 位提交 SHA；默认读取 DRONE_COMMIT_SHA，本地运行可通过 CLI 或所列环境变量提供。
      不以当前 HEAD 自动替代；仅含文件 key 且启用 Git 模式时要求提供。
      环境变量（按优先级）：PLUGIN_COMMIT_SHA, DRONE_COMMIT_SHA
    --bucket STRING
      S3 桶名；非空操作必填，无默认；桶须预先创建，不能含对象路径。
      环境变量：PLUGIN_BUCKET
    --flush-path STRING
      桶内清理前缀；默认 <owner>/<repo>/；只允许缩小到该仓库范围内。
      仅用于 flush；保留仓库前缀的路径边界，不清理前缀相似的其他仓库。
      仓库内仍为字符串前缀匹配，不自动补 /，也不按归档后缀过滤。
      环境变量：PLUGIN_FLUSH_PATH
    --flush-age INT
      删除最后写入时间早于指定天数的对象；默认 30，必须为正整数；仅用于 flush。
      环境变量：PLUGIN_FLUSH_AGE

  S3 连接与凭据:
    --endpoint STRING
      服务入口，默认 https://s3.amazonaws.com；必须为 http/https URL。
      不携带桶名或对象路径，不从入口解析桶名或区域。
      环境变量（按优先级）：PLUGIN_ENDPOINT, CACHE_S3_ENDPOINT
    --region STRING
      S3 区域；默认空，由 SDK 配置获取；最终仍为空时使用 us-east-1。
      环境变量（按优先级）：PLUGIN_REGION, CACHE_S3_REGION
    --access-key STRING
      S3 access key；默认空；显式配置时与 secret-key 成对提供，与凭据文件互斥。
      环境变量（按优先级）：PLUGIN_ACCESS_KEY, CACHE_S3_ACCESS_KEY, AWS_ACCESS_KEY_ID
    --secret-key STRING
      S3 secret key；默认空；与 access-key 成对，不得输出到日志。
      环境变量（按优先级）：PLUGIN_SECRET_KEY, CACHE_S3_SECRET_KEY, AWS_SECRET_ACCESS_KEY
    --session-token STRING
      临时凭据 token；默认空；显式 access/secret 存在时用于静态凭据提供者。
      未显式提供 access/secret 时，凭据及 token 均由 SDK 默认链决定，不单独注入该值。
      环境变量（按优先级）：PLUGIN_SESSION_TOKEN, CACHE_S3_SESSION_TOKEN, AWS_SESSION_TOKEN
    --file-credentials STRING
      共享凭据文件在容器内的路径；默认空；须为已存在的普通文件，不与显式 access/secret 混用；Drone 中路径须在插件容器内可读。
      环境变量（按优先级）：PLUGIN_FILE_CREDENTIALS, AWS_SHARED_CREDENTIALS_FILE
    --profile STRING
      SDK 命名配置；默认空，沿用 SDK 默认选择；可与 file-credentials 配合。
      环境变量（按优先级）：PLUGIN_PROFILE, AWS_PROFILE
    --addressing-style STRING
      桶寻址方式 auto/path/virtual；默认 auto。
      auto 对非 AWS、非 storage.googleapis.com 的自定义入口使用 path；virtual 显式选择虚拟主机寻址。
      环境变量（按优先级）：PLUGIN_ADDRESSING_STYLE, CACHE_S3_ADDRESSING_STYLE
    --timeout DURATION
      单次 S3 操作超时，默认 10m；示例 30s、5m；必须大于 0，不是构建总时限。
      环境变量（按优先级）：PLUGIN_TIMEOUT, CACHE_S3_TIMEOUT
    --accelerated-endpoint STRING
      AWS 专用加速入口主机名，默认空；不带 scheme。
      仅 endpoint 主机为 s3.amazonaws.com 或以 .amazonaws.com 结尾时生效，强制虚拟主机寻址；其他入口忽略。
      环境变量（按优先级）：PLUGIN_ACCELERATED_ENDPOINT, CACHE_S3_ACCELERATED_ENDPOINT

  仓库作用域:
    --repo.owner STRING
      仓库所有者或命名空间；非空操作必填，无默认；用于对象 key 的仓库隔离。
      环境变量（按优先级）：DRONE_REPO_OWNER, DRONE_REPO_NAMESPACE
    --repo.name STRING
      仓库名称；非空操作必填，无默认；与 owner 共同确定对象范围。
      环境变量：DRONE_REPO_NAME

  日志与内置参数:
    --log-level STRING
      默认 info；panic/fatal/error/warn/warning/info/debug/trace；未知值报错。
      环境变量：PLUGIN_LOG_LEVEL
    --help, -h
      显示帮助并退出，不连接 S3。
    --version, -v
      显示版本并退出，不连接 S3。
```

### 2.1 参数的使用约束

- `restore` / `rebuild` 缺少 cache 即报错；不会切换旧模式。明确 `cache: []` 时跳过，无需 bucket、仓库标识或 S3 凭据，也不连接存储。
- 非空操作和 flush 必须取得 bucket、repo.owner、repo.name；flush 不接受 cache。
- key_from_git 默认为 true；关闭时 CLI 使用 `--key-from-git=false`，Drone settings 使用 `key_from_git: false`。Git 模式的文件 key 需要仓库及目标提交对象可读取，否则在传输前失败，不自动联网或切换工作区模式。
- 字符串 key、空 cache 数组和 flush 不读取 Git 对象，也不要求 commit_sha；只有 `key.files` 受该开关控制。`paths` 始终从当前工作区归档并恢复到当前工作区。
- 桶名只放在 bucket 中；endpoint 只放入口。不支持 `s3://bucket`、入口内嵌桶名或区域推断。
- 对象使用固定 `.tar.gz` 后缀；不提供 filename、压缩格式或自定义归档名称参数。
- flush_path、flush_age 仅用于 flush，其他操作提供它们报配置错误；默认值不视为显式提供。
- 凭据参数都未提供时交给 AWS SDK 默认凭据链；这不代表目标 Drone 环境已经配置身份凭据。自定义证书由容器信任库配置，不提供无效的通用 skip-verify 参数。
- 不加载旧接口专用的环境文件或布尔操作开关；文中列出的输入是新接口约定。已移除的旧 CLI 参数报错，不作为别名接受；旧环境变量不映射到新参数，应从配置中删除。

### 2.2 Drone settings 与环境变量

```yaml
settings:
  mode: restore
  bucket: ci-cache
  endpoint: https://s3.example.com
  region: us-east-1
  addressing_style: path
  timeout: 10m
  cache:
    key: go-v1
    paths:
      - .cache/go
```

顶层参数分别成为 `PLUGIN_MODE`、`PLUGIN_BUCKET`、`PLUGIN_ENDPOINT`、`PLUGIN_REGION`、`PLUGIN_ADDRESSING_STYLE`、`PLUGIN_TIMEOUT`。整个 cache 对象成为一个 JSON 字符串 `PLUGIN_CACHE`，不会出现 `PLUGIN_CACHE_KEY` 或 `PLUGIN_CACHE_0_KEY`。

对象数组同样通过一个 `PLUGIN_CACHE` JSON 字符串传入。条目内部的数组保留 JSON 数组，不需要转换成逗号字符串。该传递方式依据 [Drone Runner 编译器](https://github.com/drone-runners/drone-runner-docker/blob/master/engine/compiler/step.go) 及其 [编码器](https://github.com/drone-runners/drone-runner-docker/blob/master/internal/encoder/encoder.go)；目标 Runner 仍需实跑验收。

## 3. cache 对象的全部字段

| 字段 | 类型 | 必填 / 默认 | 说明 |
| --- | --- | --- | --- |
| `key` | 字符串或对象 | 必填 | 固定 key，或按明确文件及 glob 展开的文件集合计算 key |
| `key.prefix` | 字符串 | 对象 key 必填 | 非空业务前缀，支持有限占位符，建议含工具链、OS、架构和手动版本 |
| `key.files` | 字符串数组 | 对象 key 必填，至少一个 | 工作区相对文件路径或 glob，支持 `*`、`**`，用于计算 SHA-256 |
| `paths` | 字符串数组 | 必填，至少一个 | 实际缓存的明确目录，不支持 glob；不是计算文件指纹的输入 |
| `fallback_keys` | 字符串数组 | 可选，默认 `[]` | 主 key 不存在时，按顺序精确匹配完整 key |

不提供 `policy`、每条目 mode、自动 fallback 写回或前缀搜索。key 只支持下述有限变量替换。

### 3.1 对象与数组

以下两个输入等价：

```json
{"key":"go-v1","paths":[".cache/go"]}
```

```json
[{"key":"go-v1","paths":[".cache/go"]}]
```

| 输入情况 | 行为 |
| --- | --- |
| 恢复/回存未提供 cache 参数 | 配置错误 |
| 合法对象 | 转为单元素数组后处理 |
| 合法对象数组 | 按顺序处理 |
| 空数组 `[]` | 明确跳过本次缓存操作，不连接 S3 |
| 显式空值、`null`、空对象、标量、包含非对象的数组、无效 JSON | 配置失败 |
| JSON 内容又被编码成字符串 | 不做第二次 JSON 解码，配置失败 |
| 未知字段，例如 policy | 配置失败，指出字段 |
| 相同最终主 key，或 paths 在同一条目内/不同条目间重叠 | 配置失败，避免重复操作同一对象或目录 |

所有条目完成解析、key 计算与配置校验后才开始缓存传输。`flush` 不接受 cache 参数；空数组也不能用来抑制一次显式 flush。

### 3.2 key、路径和命中规则

文件 key 为 `<展开后的prefix>-<64位SHA-256>`：先展开 `key.files` 的 glob，再按规范化相对文件路径排序、去重，计算路径和内容的指纹。模式字符串本身不参与文件指纹；相同文件集合下，列表顺序、重叠或等价模式不影响结果。匹配文件增删、改名或内容变化都会改变结果。明确指定的文件缺失、文件不可读或任一模式没有匹配到文件时均报错，不降级成默认 key。

指纹算法为：对每个排序后的文件，依次向 SHA-256 输入 8 字节大端路径字节长度、相对路径的原始字节、文件内容的 32 字节 SHA-256；最终输出 64 位小写十六进制。提交 SHA、文件权限及时间戳不参与指纹。

仅修改普通源码时，`go.mod` / `go.sum` 对应的 key 不变。编译工具仍须判断哪些内容失效，不能仅凭 key 命中跳过构建。默认 Git 模式忽略工作区中的 POM 修改和生成物；关闭该开关时，恢复和回存之间必须保持匹配的文件集合与内容一致。Go 示例中的 Git diff 是构建输入一致性检查，不是默认模式固定 key 的必要手段。

key 不能包含 `/`、反斜杠或控制字符，不能只由点组成。分支名若含斜杠，先由流水线转换成安全标识；占位符展开后仍需校验；插件不执行 shell 命令。路径不能为工作区根、绝对路径、.git 或越界路径；恢复内容及链接不得逃逸出声明目录。支持同一缓存目录内可解析的相对符号链接；绝对、越界、悬空链接、硬链接归档条目及特殊文件会被拒绝。

默认对象位置：

```text
s3://<bucket>/<owner>/<repo>/<resolved-key>.tar.gz
```

bucket 不属于对象 key。对象 key 是 `<owner>/<repo>/<resolved-key>.tar.gz`，其中仓库路径隔离不同仓库，最终 cache key 直接构成归档名称，不再作为目录后附加另一个文件名。固定 gzip 压缩，不根据用户名称猜测格式，key 中不填写归档扩展名。

先尝试主 key；SDK 返回对象不存在的 NoSuchKey 或 NotFound 时逐个尝试 fallback。NoSuchBucket、403、其他下载错误或损坏归档均停止该条目的尝试，不再读后续 fallback。第一份成功后停止，不合并多个归档。全部不存在则冷构建。不同仓库独立保存，工具链、架构和需要区分的环境应体现在前缀中。

恢复会用暂存区中通过校验的目录整体替换对应 `paths` 目录，旧目录中的额外文件不会保留，不是增量合并。归档中缺少的声明目录保持工作区原状。目录替换失败会尝试回滚；这不是跨目录原子事务，也不保证进程被强制终止后自动恢复。暂存解压和旧目录备份位于工作区，需预留磁盘空间。

同 key 可以覆盖，主 key 精确命中后仍允许回存。fallback 只提供读取候选，不自动更新。main 稳定缓存需要 main 成功构建显式写入，见场景二。

### 3.3 有限模板变量

字符串 `key`、`key.prefix`、`fallback_keys` 支持相同的占位符；`key.files` 和 `paths` 不执行替换。

| 占位符 | 取值与示例 |
| --- | --- |
| `{{ .OS }}` | 插件运行平台的操作系统标识，如 linux |
| `{{ .ARCH }}` | 插件运行平台的 CPU 架构标识，如 amd64、arm64 |
| `{{ .Env.VAR_NAME }}` | 插件进程可见的指定环境变量，如 `{{ .Env.GO_VERSION }}` |

环境变量名须符合 `[A-Za-z_][A-Za-z0-9_]*`。未知变量、未设置或值为空的环境变量、模板语法错误均在传输前失败，不静默替换为空。只支持变量替换，不支持条件、循环、函数调用、文件读取或命令执行，也不额外支持插件内的 `${VAR}` 展开。

处理顺序：展开占位符 → 校验展开后的 key/prefix/fallback → 计算 key.files 指纹并组合最终 key → 构造仓库范围内的 S3 对象 key。多个条目的最终主 key 在展开后判重。

恢复和回存使用相同变量值。构建步骤的环境变量不会自动传给插件；需在插件步骤中显式提供。key 会进入对象名称和日志，只使用非敏感变量。`.OS`、`.ARCH` 不表示跨平台编译目标；需要目标平台时自行提供 `CACHE_TARGET_OS` 等非敏感变量并用 Env 占位符引用。场景十给出完整片段。

### 3.4 key.files 通配符

| 写法 | 期望匹配 |
| --- | --- |
| `pom.xml` | 工作区根目录的 POM |
| `modules/*/pom.xml` | modules 下直接一层子目录中的 POM，不包含更深层 |
| `modules/**/pom.xml` | modules 自身及其任意层级子目录中的 POM |
| `./**/pom.xml` | 根目录及任意层级子目录中的所有 POM，与 `**/pom.xml` 等价 |

默认在固定提交的仓库文件路径列表上匹配；`key_from_git: false` 时相对当前工作区匹配。`*` 不跨目录分隔符，作为完整路径段的 `**` 匹配零层或多层目录；先去除 `./` 等等价路径写法，再排序去重。首版不提供排除模式、花括号展开或额外的 glob 语法。

每个 files 条目都必须匹配到至少一个普通文件。Git 模式不跟随符号链接，也不递归读取子模块仓库；工作区模式同样不跟随符号链接，并扫描工作区文件列表（跳过 .git 目录），不按 .gitignore 排除文件。只计算所选来源内的普通文件，不将目录作为文件，也不允许通过路径或符号链接越出工作区。这里的通配符能力只用于 `key.files`，与字符串 key 的模板占位符、fallback 的完整 key 匹配是不同规则。

匹配不自动根据 Maven 模块声明筛选。默认 Git 模式仅包含目标提交中的文件，未提交的生成 POM 不参与。关闭开关后，`**/pom.xml` 可能匹配构建生成目录中的 POM；此时应清理生成物或缩小到根 POM 和实际模块范围，并保持恢复与回存时的文件集合及内容一致。

### 3.5 文件 key 来源开关（默认开启 Git 模式）

`key_from_git` 是步骤级布尔参数，作用于本次调用中的所有 `key.files`，不放在单个 cache 条目内。

| 配置 | 文件列表与内容来源 | 必要条件 |
| --- | --- | --- |
| 省略或 `key_from_git: true` | commit_sha 指定的 Git 提交树和 Blob | 本地仓库和该提交的相关对象可读取 |
| `key_from_git: false` | 当前工作区实际文件 | 不要求 Git 元数据；调用前已准备好所需文件 |

Git 模式使用纯 Go 的 go-git 库读取对象，不依赖环境中安装的 git 可执行文件。按固定 SHA 读取提交 → 遍历 Tree → 在路径列表上展开 glob → 读取匹配文件的 Blob → 排序、去重并计算指纹。无需 checkout、reset 或 clean，也不会修改工作区；Git 对象齐全时无需联网。采用 go-git v5.19.3，最低 Go 版本为 1.26。

commit_sha 优先来自显式 CLI 或 `PLUGIN_COMMIT_SHA`，否则来自 `DRONE_COMMIT_SHA`。恢复与回存必须指向同一个完整提交 SHA，不重新采用可能已经变化的 HEAD；本地 CLI 未提供时直接报错。浅克隆只需目标提交及其相关树、Blob 可用，不要求完整历史；对象缺失时失败，不自动 fetch，也不静默读取工作区。

SHA 只用于选择快照，不直接拼入最终 key。不同提交若匹配的路径、内容和 prefix 相同，仍产生相同 key；切换两种来源但有效输入完全相同时也保持同一计算规则。

如果构建前有意修改 POM 或使用未提交的依赖描述文件，默认模式不会纳入这些变化；应显式关闭开关，并在恢复前完成准备，确保回存时仍选择同一批内容。这个开关只控制“怎么算 key”，不改变缓存目录内容的读写范围。

```yaml
# 当前步骤的 settings 片段：显式改用工作区文件。
settings:
  mode: restore
  bucket: ci-cache
  key_from_git: false
  cache:
    key:
      prefix: maven3-jdk21-linux-amd64-v1
      files: ["./**/pom.xml"]
    paths: [.m2/repository]
```

关闭开关时恢复和保存步骤应同时设置 false。不能一边按提交快照计算，另一边按已修改的工作区计算。

## 4. 本地 CLI 示例

先在插件源码目录执行 `go build -o /tmp/drone-s3-cache .`，再切换到待缓存项目的仓库根目录执行以下命令。工作目录同时决定 Git 仓库和 paths 的根，不在插件内部自动切换。先准备 S3 凭据环境变量，示例不把密钥写入命令行。

```sh
export CACHE_S3_ENDPOINT=https://s3.example.com
export CACHE_S3_REGION=us-east-1
export DRONE_REPO_OWNER=acme
export DRONE_REPO_NAME=service
export DRONE_COMMIT_SHA='替换为本次构建的完整提交SHA'
# CACHE_S3_ACCESS_KEY、CACHE_S3_SECRET_KEY 由本机安全配置提供。

# 读取：单对象。
/tmp/drone-s3-cache --mode restore --bucket ci-cache \
  --cache '{"key":{"prefix":"go1.26-linux-amd64-v1","files":["go.mod","go.sum"]},"paths":[".cache/go"],"fallback_keys":["go1.26-linux-amd64-v1-main"]}'

# 写入：在构建成功、工作区已有缓存目录后运行。
/tmp/drone-s3-cache --mode rebuild --bucket ci-cache \
  --cache '{"key":{"prefix":"go1.26-linux-amd64-v1","files":["go.mod","go.sum"]},"paths":[".cache/go"]}'

# 数组：独立回存两组，目录和主 key 均不同。
/tmp/drone-s3-cache --mode rebuild --bucket ci-cache \
  --cache '[{"key":"go-mod-v1","paths":[".cache/go-mod"]},{"key":"go-build-v1","paths":[".cache/go-build"]}]'

# 也可由环境变量提供整个 JSON。
PLUGIN_CACHE='{"key":"go-v1","paths":[".cache/go"]}' \
  /tmp/drone-s3-cache --mode restore --bucket ci-cache
```

归档固定为 tar.gz，后缀由插件统一添加，key 本身不填写后缀。上述写入命令会覆盖同 key 对象；正常未命中不会自动创建空对象。

## 5. Drone 不同场景示例

所有完整流水线均使用 Docker Runner 的默认工作区 `/drone/src`。例中的 secrets 和桶须先创建；受信任的内部 push 构建可以按组织权限提供凭据，不能据此向外部 fork 开放写凭据。流水线的 trigger 与步骤的 when 分别控制是否启动和是否执行，参考 [Drone 触发规则](https://docs.drone.io/pipeline/triggers/) 与 [步骤条件](https://docs.drone.io/pipeline/docker/syntax/conditions/)。

YAML 锚点仅在各自示例内复用配置。标为“片段”的例子需放入实际流水线，不代表另建一个名为 settings_1 或 settings_2 的 Drone 配置段。

### 场景一：npm 单对象，构建前恢复、成功后保存

缓存 npm 下载内容，不缓存 node_modules。恢复与保存通过 YAML 锚点共享同一份 settings，只改变 mode。

```yaml
kind: pipeline
type: docker
name: npm-build
trigger:
  event: [push]
steps:
  - name: cache-restore
    image: docker.io/kit101z/drone-s3-cache:next
    settings: &npm-settings
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
          prefix: npm-node24-linux-amd64-v1
          files: [package-lock.json]
        paths: [.npm]

  - name: build
    image: node:24-alpine
    commands:
      - npm ci --cache .npm
      - npm test
      - npm run build

  - name: cache-save
    image: docker.io/kit101z/drone-s3-cache:next
    settings:
      <<: *npm-settings
      mode: rebuild
    when:
      status: [success]
```

预期：首次冷构建；后续相同 lock 文件命中；改变 lock 文件后使用新 key。安装、测试或构建失败时不保存。此例未配置 fallback，因此不产生稳定 main 缓存。

### 场景二：Go 文件 key + main 稳定 fallback

默认分支假设为 main；若实际为 master，稳定 key 和分支条件一起替换。feature 只保存文件 key；main 成功后额外更新稳定 key，主 key 精确命中时同样更新。

```yaml
kind: pipeline
type: docker
name: build

trigger:
  event:
    - push

steps:
  - name: cache-restore
    image: docker.io/kit101z/drone-s3-cache:next
    environment: &s3-env
      CACHE_S3_ENDPOINT: https://s3.example.com
      CACHE_S3_REGION: us-east-1
      CACHE_S3_ACCESS_KEY:
        from_secret: cache_access_key
      CACHE_S3_SECRET_KEY:
        from_secret: cache_secret_key
    settings:
      mode: restore
      bucket: ci-cache
      cache: &go-cache
        - key:
            prefix: go1.26-linux-amd64-v1
            files:
              - go.mod
              - go.sum
          paths:
            - .cache/go
          fallback_keys:
            - go1.26-linux-amd64-v1-main

  - name: build
    image: golang:1.26
    environment:
      CGO_ENABLED: "0"
      GOMODCACHE: /drone/src/.cache/go/mod
      GOCACHE: /drone/src/.cache/go/build
    commands:
      - go mod download
      - git diff --exit-code -- go.mod go.sum
      - go test ./...
      - go build ./...
      - git diff --exit-code -- go.mod go.sum

  - name: cache-save-primary
    image: docker.io/kit101z/drone-s3-cache:next
    environment: *s3-env
    settings:
      mode: rebuild
      bucket: ci-cache
      cache: *go-cache
    when:
      status:
        - success

  - name: cache-save-main
    image: docker.io/kit101z/drone-s3-cache:next
    environment: *s3-env
    settings:
      mode: rebuild
      bucket: ci-cache
      cache:
        - key: go1.26-linux-amd64-v1-main
          paths:
            - .cache/go
    when:
      branch:
        - main
      status:
        - success
```

预期：依赖指纹从 abc1 变为 abc2 后，先找 abc2，未命中则恢复 main，再由 Go 补齐。main 成功后依次写 abc2 与 main；feature 成功只写 abc2。主 key 写入成功而 main 上传失败时，两者状态分别记录，没有双写事务。

稳定 main 表示最近成功上传的 main 缓存，不保证按 commit 排序。同 key 并发或历史提交重跑会按最后成功写入覆盖；需要顺序保证时由 CI 串行安排写入者。

### 场景三：Go 数组，模块下载与编译缓存分开

两组目录、key 独立，分别归档。模块缓存使用依赖文件指纹；编译缓存使用固定 key 持续积累，由 Go 自行判断失效内容。

```yaml
kind: pipeline
type: docker
name: go-split-cache
trigger:
  event: [push]
steps:
  - name: restore
    image: docker.io/kit101z/drone-s3-cache:next
    settings: &go-settings
      mode: restore
      bucket: ci-cache
      endpoint: https://s3.example.com
      region: us-east-1
      access_key:
        from_secret: cache_access_key
      secret_key:
        from_secret: cache_secret_key
      cache:
        - key:
            prefix: gomod-go1.26-linux-amd64-v1
            files: [go.mod, go.sum]
          paths: [.cache/go-mod]
        - key: gobuild-go1.26-linux-amd64-cgo0-v1
          paths: [.cache/go-build]

  - name: build
    image: golang:1.26
    environment:
      CGO_ENABLED: "0"
      GOMODCACHE: /drone/src/.cache/go-mod
      GOCACHE: /drone/src/.cache/go-build
    commands:
      - go mod download
      - git diff --exit-code -- go.mod go.sum
      - go test ./...
      - go build ./...
      - git diff --exit-code -- go.mod go.sum

  - name: save
    image: docker.io/kit101z/drone-s3-cache:next
    settings:
      <<: *go-settings
      mode: rebuild
    when:
      status: [success]
```

预期：只修改源码时，模块 key 不变，编译缓存继续增量更新。同一个数组中不能给两条配置相同的最终主 key；同一条目内或不同条目间的 paths 均不能指向相同或包含关系的目录。

### 场景四：只恢复，验证任务不回存

适合已有统一缓存生产者、其他验证任务只消费的情况。该固定 key 需先由同仓库、相同环境的生产者回存；未生成时允许冷构建。以下是完整流水线，只使用只读 S3 凭据。

```yaml
kind: pipeline
type: docker
name: go-read-only-cache
trigger:
  event: [push]
steps:
  - name: restore
    image: docker.io/kit101z/drone-s3-cache:next
    settings:
      mode: restore
      bucket: ci-cache
      endpoint: https://s3.example.com
      region: us-east-1
      access_key:
        from_secret: cache_read_access_key
      secret_key:
        from_secret: cache_read_secret_key
      cache:
        key: go1.26-linux-amd64-v1-main
        paths: [.cache/go]
  - name: test
    image: golang:1.26
    environment:
      CGO_ENABLED: "0"
      GOMODCACHE: /drone/src/.cache/go/mod
      GOCACHE: /drone/src/.cache/go/build
    commands:
      - go test ./...
```

预期：不上传任何对象。只配置 restore 是执行选择，S3 只读凭据才是权限限制；不能因此向不可信代码开放含私有依赖的缓存。

### 场景五：只回存，定时预热稳定缓存

先在自托管 Drone 为 main 分支配置名为 `cache-warm` 的 cron；本流水线不创建定时任务。[Drone Cron 官方说明](https://docs.drone.io/cron/) 将定时任务作为仓库独立配置。预热构建不恢复缓存，成功后写固定 key，供场景四等消费者使用。

```yaml
kind: pipeline
type: docker
name: go-cache-warm
trigger:
  event: [cron]
  cron: [cache-warm]
  branch: [main]
steps:
  - name: warm
    image: golang:1.26
    environment:
      CGO_ENABLED: "0"
      GOMODCACHE: /drone/src/.cache/go/mod
      GOCACHE: /drone/src/.cache/go/build
    commands:
      - go mod download
      - git diff --exit-code -- go.mod go.sum
      - go build ./...
      - git diff --exit-code -- go.mod go.sum
  - name: save
    image: docker.io/kit101z/drone-s3-cache:next
    settings:
      mode: rebuild
      bucket: ci-cache
      endpoint: https://s3.example.com
      region: us-east-1
      access_key:
        from_secret: cache_access_key
      secret_key:
        from_secret: cache_secret_key
      cache:
        key: go1.26-linux-amd64-v1-main
        paths: [.cache/go]
    when:
      status: [success]
```

### 场景六：多个 fallback，按完整 key 顺序查找

以下为缓存步骤片段；在构建步骤之前运行。两个 fallback 均需有明确的生产者，插件不会自行生成它们。

```yaml
- name: restore
  image: docker.io/kit101z/drone-s3-cache:next
  settings:
    mode: restore
    bucket: ci-cache
    endpoint: https://s3.example.com
    region: us-east-1
    access_key:
      from_secret: cache_read_access_key
    secret_key:
      from_secret: cache_read_secret_key
    cache:
      key:
        prefix: gomod-go1.26-linux-amd64-v1
        files: [go.mod, go.sum]
      paths: [.cache/go-mod]
      fallback_keys:
        - gomod-go1.26-linux-amd64-v1-main
        - gomod-go1.26-linux-amd64-v1-seed
```

预期：主 key → main → seed，找到一份便停止。`main` 不会前缀匹配 `main-abc2`。回存仍只写主 key，不写 main 或 seed；key 版本及缓存目录需与生产者一致，归档后缀固定。

### 场景七：直接传 JSON 字符串与显式禁用

对象及数组除了 YAML 结构，也可直接以 JSON 文本提供。以下仅为 settings 片段，S3 凭据由现有的环境变量或 SDK 配置提供。

```yaml
settings:
  mode: restore
  bucket: ci-cache
  cache: |
    {"key":"go-v1","paths":[".cache/go"]}
```

改成数组时仅替换 JSON 内容：

```yaml
settings:
  mode: restore
  bucket: ci-cache
  cache: |
    [{"key":"go-mod-v1","paths":[".cache/go-mod"]},{"key":"go-build-v1","paths":[".cache/go-build"]}]
```

显式禁用某次恢复或回存：

```yaml
settings:
  mode: restore
  cache: []
```

预期：空数组明确跳过且不连接 S3。恢复或回存时省略 cache 则报配置错误，两者不同。

### 场景八：Maven 多模块，通过所有 POM 生成 key

以下完整流水线默认按本次提交中的根及各子模块 pom.xml 计算文件 key，实际缓存 Maven 本地依赖仓库。Maven 继续执行依赖检查及自己的更新策略。不要把含服务端密码的 settings.xml 放进缓存。

```yaml
kind: pipeline
type: docker
name: maven-build
trigger:
  event: [push]
steps:
  - name: restore
    image: docker.io/kit101z/drone-s3-cache:next
    settings: &maven-settings
      mode: restore
      bucket: ci-cache
      key_from_git: true
      endpoint: https://s3.example.com
      region: us-east-1
      access_key:
        from_secret: cache_access_key
      secret_key:
        from_secret: cache_secret_key
      cache:
        key:
          prefix: "maven3-jdk21-{{ .OS }}-{{ .ARCH }}-v1"
          files:
            - "./**/pom.xml"
        paths: [.m2/repository]
  - name: build
    image: maven:3-eclipse-temurin-21
    commands:
      - mvn -B -Dmaven.repo.local=.m2/repository verify
  - name: clean
    image: maven:3-eclipse-temurin-21
    commands:
      - mvn -Dmaven.repo.local=.m2/repository clean
  - name: save
    image: docker.io/kit101z/drone-s3-cache:next
    settings:
      <<: *maven-settings
      mode: rebuild
    when:
      status: [success]
```

预期：根或任一匹配到的模块 POM 变化，都会产生新 key；增加或删除模块 POM 也改变文件集合及指纹。同一个文件被多个模式匹配时只计算一次。该示例没有 fallback，新 key 首次未命中会冷构建；SNAPSHOT 刷新仍由 Maven 决定。

保留示例中的 clean 步骤，但默认 Git 模式不依赖 clean 防止 key 污染：构建生成或修改的工作区 POM 不影响提交快照。clean 仍会删除相应构建输出，后续需要发布制品时应先完成传递或上传。

示例显式写出 true，省略时等价。如果恢复和保存都设置 `key_from_git: false`，clean 可帮助清除标准构建目录中的 POM；自定义生成目录或被改写的源 POM 仍需自行处理。该模式要求两次调用选择相同的文件集合和内容，Git diff 无法单独覆盖未跟踪的新增文件。

### 场景九：独立清理过期缓存

这是会删除对象的维护流水线。先在 Drone 配置名为 `cache-cleanup` 的 cron。示例只清理 acme/service 对应的对象，S3 凭据需有该前缀的 List/Delete 权限。flush_path 默认是当前仓库的 `<owner>/<repo>/`，也可显式缩小到其下的前缀；示例须在 acme/service 仓库运行，不允许通过该参数扩大到其他仓库。

```yaml
kind: pipeline
type: docker
name: cache-cleanup
trigger:
  event: [cron]
  cron: [cache-cleanup]
  branch: [main]
steps:
  - name: flush
    image: docker.io/kit101z/drone-s3-cache:next
    settings:
      mode: flush
      bucket: ci-cache
      endpoint: https://s3.example.com
      region: us-east-1
      access_key:
        from_secret: cache_cleanup_access_key
      secret_key:
        from_secret: cache_cleanup_secret_key
      flush_path: acme/service/
      flush_age: 30
```

flush_path 按字符串前缀匹配，不会自动补 `/`：例如 `acme/service/go` 也匹配 `acme/service/golang-v1.tar.gz`。插件列举并清理该前缀下的所有过期对象，不只过滤 `.tar.gz`；同前缀中不要混放其他需保留的数据。

按对象最后写入时间计算，不按最近读取时间；只读热缓存及长期未刷新的 main 也可能过期。清理后允许冷构建。不跨到前缀相似的仓库，不以新 key 的出现自动删除旧 key。

### 场景十：平台及环境变量模板

以下为步骤片段。环境变量属于当前插件容器，构建步骤自己设置的变量不会自动传到插件；恢复和保存步骤必须提供相同的变量值。

```yaml
- name: restore-with-template
  image: docker.io/kit101z/drone-s3-cache:next
  environment:
    GO_VERSION: "1.26"
    CACHE_VERSION: v1
  settings:
    mode: restore
    bucket: ci-cache
    endpoint: https://s3.example.com
    region: us-east-1
    access_key:
      from_secret: cache_read_access_key
    secret_key:
      from_secret: cache_read_secret_key
    cache:
      key:
        prefix: "gomod-go{{ .Env.GO_VERSION }}-{{ .OS }}-{{ .ARCH }}-{{ .Env.CACHE_VERSION }}"
        files: [go.mod, go.sum]
      paths: [.cache/go-mod]
      fallback_keys:
        - "gomod-go{{ .Env.GO_VERSION }}-{{ .OS }}-{{ .ARCH }}-{{ .Env.CACHE_VERSION }}-main"
```

Linux amd64 平台下，prefix 为 `gomod-go1.26-linux-amd64-v1`。文件指纹用 abc2 缩写时，对象 key 为 `acme/service/gomod-go1.26-linux-amd64-v1-abc2.tar.gz`；fallback 对象为 `acme/service/gomod-go1.26-linux-amd64-v1-main.tar.gz`。

## 6. 存储连接配置片段

下列为 settings 连接部分，需合并到实际缓存步骤，不是独立流水线。认证可统一用 Drone secrets，也可使用前述 CACHE_S3_* 环境变量。

```yaml
# MinIO：入口与桶名分开，按路径寻址。
settings:
  endpoint: https://minio.example.com
  bucket: ci-cache
  region: us-east-1
  addressing_style: path
```

```yaml
# OBS：使用实际区域服务入口，桶名仍放 bucket。
settings:
  endpoint: https://obs.cn-southwest-2.myhuaweicloud.com
  bucket: ci-cache
  region: cn-southwest-2
  addressing_style: virtual
```

区域仅为示意，应替换为实际桶所在区域；入口域名、DNS、证书、权限及区域匹配需现场验证。当前 auto 对非 AWS、非 storage.googleapis.com 的自定义入口使用 path；因此示例对 OBS 明确指定 virtual。

## 7. 结果、故障与验收要点

| 情况 | 期望结果 |
| --- | --- |
| 对象不存在（NoSuchKey/NotFound）、所有候选都不存在 | 依次 fallback，最终冷构建，恢复步骤成功 |
| 下载时网络超时、403、NoSuchBucket、存储不可用 | 与 miss 区分，终止该条目的远端尝试，警告后冷构建 |
| 归档损坏或解压失败 | 隔离校验失败不发布；目录替换失败则回滚，回滚未完成时保留本地备份并记录位置 |
| 配置错误、SDK 配置或客户端初始化失败 | 步骤失败；不会按普通 miss 处理 |
| 所有回存目录为空或不存在 | 跳过，不能用空包覆盖旧缓存 |
| 部分目录不存在 | 警告，继续归档其他有效内容 |
| 打包或上传失败 | 回存步骤失败；旧对象仍可读，不暴露半个新对象 |
| 多条目回存中途失败 | 此前成功对象保留，首次失败即终止，不承诺回滚 |
| flush 列举或删除失败 | 步骤失败；此前完成的删除不回滚 |

0 表示操作完成、普通未命中、客户端初始化后可容忍的恢复故障或明确跳过；配置、客户端初始化、回存及清理失败返回非零。首版不承诺区分每种非零错误的固定退出码。若允许回存失败不阻塞业务交付，可在对应 Drone 步骤显式设置 `failure: ignore`，不是由插件静默吞掉。

日志应能辨认主 key、实际命中 key、精确/fallback 命中、未命中、故障、实际写入对象、跳过原因、字节数及耗时；不得输出密钥或签名 URL。是否提速以整个 CI 的恢复、构建、归档、上传总耗时判断。

接口与验收要点：

1. 全新 CLI 使用 bucket、cache 和显式 mode；不保留旧参数、filename 或 policy。
2. 单对象与数组等价，空数组明确禁用；缺少 cache 或错误配置直接失败。
3. 恢复、回存由两个 Drone 步骤控制；稳定 fallback 由明确的生产者更新。
4. 同 key 可覆盖；多对象写入不提供事务、合并或 commit 新旧排序。
5. OS、ARCH 与环境变量占位符一致用于字符串 key、prefix、fallback；最终 key 直接形成对象名并统一附加 .tar.gz。
6. 恢复远端故障容许冷构建，回存和清理失败保持可见。
7. key_from_git 默认 true，通过纯 Go 库读取固定提交；false 才扫描工作区，对象缺失不静默降级。

本次文档核对重新运行本地测试及 CLI 帮助检查；以下构建与 MinIO 结果来自本会话上一轮实现验证，未在本次文档修改中重复执行。

验证已覆盖 race 测试、lint、Linux amd64/arm64 与 Windows amd64 构建，以及 Linux 双架构 scratch 镜像构建。本地 MinIO 通过 scratch 镜像完成 20 MiB 随机数据的分片上传与恢复校验，验证 Git 快照 key 稳定、fallback、工作区模式、失败上传保留旧对象、零残留分片和空缓存不覆盖。真实 Drone Runner、远端 S3/OBS 与镜像发布尚未验证。
