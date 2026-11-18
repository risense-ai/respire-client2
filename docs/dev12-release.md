# Client2 DEV12 发布交接

## 范围与当前结论

2026-11-16，以 Client2 `67d5d88` 为准备基线。PR #42 已合并，公开桌面版本为 `v2.0.0-dev.11`。本任务只准备 `v2.0.0-dev.12`，不合并、不打 tag、不发布、不触发重验收，也不包含 PR #43 系统代理。

Client2、CLI、Runtime 独立发版。桌面包不捆绑 CLI、Runtime、Core 或模型，因此本仓没有需要同步升版的组件依赖 pin。Go 依赖保持现状。本次只更新源码和构建脚本的默认桌面版本，以及非 tag 构建的主版本前缀；正式包仍由 tag 注入精确版本。

## 本轮固定的组件输入

| 组件 | 公开版本 | 发布源码 | 发布运行 |
| --- | --- | --- | --- |
| CLI | `v2.0.0-dev.11` | `0824bbd3769f1f92b060e511a9826c5a689f8f64` | [43785170439](https://github.com/risense-ai/respire-cli/actions/runs/43785170439)，成功 |
| Runtime | `v2.0.0-dev.19` | `3c3efde1feba7c5d9053977fcadcc8b01f8bcd87` | [43794937197](https://github.com/risense-ai/respire-runtime/actions/runs/43794937197)，失败，publish 已成功 |
| Client2 | 待发布 `v2.0.0-dev.12` | 由主线程锁定最终合并提交 | 待发布 |

主线程后续告知：Runtime PR #86、提交前缀 `db892d5` 正在修复发布后源码测试的平台夹具路径，准备 dev.20；CLI agent 正在修旧 dev.11 的验收链路，预计 dev.12，但尚待实际确认。这是协作进展，不是新版本公开或验收成功的证据。当前表格只保留本任务已核实的公开版本；最终应重新冻结实际公开且协议相容的组件，不提前加入尚不存在的 CLI pin。

已读取这两个公开 Release 的八个平台元数据。Runtime dev.19 均声明 `compat.min_cli = 2.0.0-dev.11`、`compat.max_cli = *`、`protocol_range = {minimum: 1, maximum: 1}`，并声明 `business_executor = 1`、`mcp_server = 1`。CLI dev.11 的 `runtime-pin.json` 为 `required = 2.0.0-dev.18`、`accept = >=2.0.0-dev.18,<3.0.0`。因此双方声明的版本和协议范围相容；这不代表原生业务验收已通过。

Client2 的 `CheckCLICompat` 按 Runtime 声明检查 CLI 范围。`receiptFiles` 还检查协议 1、业务和 MCP 能力、发布提交、完整文件清单和摘要。不要把三个组件改成相同版本，也不要把本表变成产品里的强制版本配置。产品继续按用户选定通道发现组件，最终验收则必须固定公开资产 ID 和 SHA-256。

## 原生能力的真实实现

| 能力 | 当前实现 | 已核实的边界 |
| --- | --- | --- |
| 系统凭据 | Client2 经 `hostExecutable` 选择独立 CLI；CLI dev.11 的 `crates/client-host/src/host_credentials.rs` 使用原生 keyring 读取会话凭据 | Client2 不直接读系统凭据库。缺少会话密钥时 CLI 返回错误并保持库锁定，不能创建替代密钥掩盖错误 |
| 账户输入 | `internal/host/host_operations.go` 将 `auth.*` 编码成版本 1 JSON，经 stdin 传给 `--host-rpc-stdio` | 请求最多 64 KiB；校验回执版本和 ID；未知结果不自动重放；账户失败的 stderr 不进入回执 |
| CLI 向 Runtime 传钥 | CLI 的 `unlock_runtime` 使用 V2 JSON 请求访问字面量 loopback HTTP `/api/runtime/unlock`，携带 Bearer token，禁用重定向 | JSON 绑定规范化 profile、session SHA-256、instance ID 和 generation；密钥不放在 argv。它是受认证的本机 HTTP 传递，不应描述为加密传输或全部走 stdin |
| 已锁定实例解锁 | `internal/host/lifecycle.go` 的 `unlockIfNeeded` 仅在 `!store_ready && credential_state == required` 时调用 `rsrs --json runtime unlock` | Client2 只发宿主命令；CLI 自行读取和递交凭据。CLI 的 provider 凭据失败会明确标成 unavailable，不能据此声称 provider 已就绪 |
| Runtime 冷启动 | `Start` 检查现有 health、安装信息和发布凭据，再调用 CLI `runtime start`；随后检查实际活动实例 | 状态不能确认时拒绝另起实例。只有 CLI 路径确实缺失才允许进入首装流程；CLI 执行失败不能伪装成“未安装” |
| 登录自启 | `internal/desktop/api.go` 用 Wails `Autostart.EnableWithOptions` 注册 `--tray`；`app.go` 按 `AutoRuntime` 连接或安装并启动 Runtime | 桌面自启和 Runtime 拉起是两个步骤。现有源码实现不能代替真实登录会话中的托盘、窗口及原生凭据权限验收 |

CLI 参考源码均固定在 [dev.11](https://github.com/risense-ai/respire-cli/tree/v2.0.0-dev.11)。上述内容是源码与公开元数据核实，不是本次运行真实凭据链路的结果。本任务没有读取个人凭据或数据库。

## 发布与最终验收

`.github/workflows/desktop.yml` 的 PR/main 只跑轻检查；tag 才构建六个平台并发布。`native-acceptance` 依赖 `release`，下载公开包后检查摘要、版本、字体、Windows 安装卸载和 Apple Silicon DMG。DEV 发现索引在这些检查通过后更新。该工作流没有实际登录自启或原生传钥验收，不应单凭全绿认定这些能力通过。

下面的发布命令只交给主线程。本任务没有执行。主线程应先合并本 PR，并明确最终合并提交；不要直接给本准备分支打 tag。

```powershell
$ErrorActionPreference = 'Stop'
git fetch origin main
$approvedCommit = (git rev-parse FETCH_HEAD).Trim()
# 主线程核对该提交就是批准发布的最终 main 后执行：
git tag v2.0.0-dev.12 $approvedCommit
git push origin v2.0.0-dev.12
```

发布完成后，主线程下载公开包核对版本，并按仓库约定生成更新公告：

```powershell
gh release download v2.0.0-dev.12 --repo risense-ai/respire-client2 --dir .verification/dev12-published --pattern 'respire-windows-amd64.exe' --pattern SHA256SUMS --pattern client-updates.json
# 在原生 Windows 上核对公开 EXE 的 --version；GUI 子系统须等待进程退出并捕获输出。
```

```bash
bash scripts/release-notes.sh v2.0.0-dev.12 .release-notes.md
```

最后仅运行一次 CLI 已有的 `release-full-regression.yml`。先使用已审查 CLI 源码里的 `scripts/create-published-assets.mjs`，冻结 CLI/Runtime 的公开资产 ID、大小、SHA-256、源码和生产运行；不要选择 candidate 或动态 latest。生成器必须在主线程拥有的 CLI 任务 Worktree 运行，不在干净主仓切分支。

当前输入对应的命令如下，但它会因 Runtime 生产运行失败而拒绝生成清单。主线程必须先解决下一节的阻塞；如果组件版本改变，应重新审查其元数据并替换精确 tag 和生产运行，不能跳过校验。

```bash
node scripts/create-published-assets.mjs v2.0.0-dev.11 43785170439 v2.0.0-dev.19 43794937197 > published-assets.json
```

取得有效清单并完成三个产品发布后，主线程可从任意获授权目录提交最终验收。以下命令读取本地清单的原始 UTF-8 字节并计算摘要；`published-assets.json` 必须是生成器成功输出的文件。

以下 dispatch 展示已核实 dev.11 的入口。如果 CLI 修复后发布新版本，须先审查新版本的清单生成器及工作流，再将 `--ref` 改为实际确认的精确 tag；不要把这条旧入口命令当作新验收链路已经修好的证据。

```powershell
$ErrorActionPreference = 'Stop'
$inventoryPath = (Resolve-Path -LiteralPath 'published-assets.json').Path
$inventory = [IO.File]::ReadAllText($inventoryPath, [Text.Encoding]::UTF8)
$inventoryHash = (Get-FileHash -LiteralPath $inventoryPath -Algorithm SHA256).Hash.ToLowerInvariant()
$clientCommit = (gh api repos/risense-ai/respire-client2/commits/v2.0.0-dev.12 --jq .sha).Trim()
gh workflow run release-full-regression.yml --repo risense-ai/respire-cli --ref v2.0.0-dev.11 -f "published_assets=$inventory" -f "published_assets_sha256=$inventoryHash" -f client2_release=v2.0.0-dev.12 -f "client2_commit=$clientCommit"
```

这次 dispatch 只启动后续 Release 运行，不是验收通过。最终必须检查该运行的 `published-full-complete` 和 `published-full-result.json`。既有流程覆盖八个 CLI/Runtime 原生目标、四个 macOS/Windows 原生迁移目标、GNU 原生凭据恢复及 Windows x64 Client2 GUI 子集。它不等于六个平台都做了 Client2 GUI、托盘或真实登录自启；这些项目仍须由主线程在同一最终验收轮次中记录原生证据，缺项应报阻塞，不能引用旧 candidate 结果补齐。

所有原生检查只能使用一次性账户、合成凭据、独立数据根和受控系统凭据命名空间。不得复用个人数据库或个人 keyring 条目，不允许 mock 成功或缺环境时自动改用其他后端。

## 当前阻塞

Runtime dev.19 的 publish 成功，但发布运行整体失败：Windows x64 的两个 fivekeys 测试比较临时目录与规范化路径失败；macOS ARM64 除相同路径比较外，还有迁移 readiness 测试报 `migration intent path is a symbolic link`。这只是日志中的失败表现，根因尚未在本任务范围内修复。

CLI 的清单生成器明确要求 CLI 和 Runtime 的生产运行均 `completed/success`，所以当前 dev.19 无法直接进入既有最终验收。需要 Runtime 负责人提供符合现有校验的成功生产证据或修复后的新公开版本。Client2 本次不修改该校验、不替换组件版本、不重跑上游门禁。

另外，Client2 的最终 tag、公开包和最终合并提交尚不存在于本次准备结果中。因此本任务不能报告 DEV12 已发布，或原生验收已通过。

## 本地验证记录

- Windows amd64：`go test -tags production ./...`、`go vet -tags production ./...` 通过，未新增测试。
- 既有 `test-account-session-state.cjs` 的 34 项和 `test-client-refresh.cjs` 的 12 项通过；这些使用前端替身，不是原生 GUI 证据。
- `go build -tags production` 通过，生成的 EXE `--version` 返回 `2.0.0-dev.12`、`windows-amd64`。
- `build.ps1` 的 PowerShell 语法检查通过；CLI 与 Runtime 共 16 份公开元数据均匹配 Release API 的 SHA-256，并核对了版本、布局和 Runtime 协议范围。
- 未执行最终发布、多平台重验收、真实系统凭据操作或登录自启；这些留给主线程在最终公开资产上完成。
