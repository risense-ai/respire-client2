# Runtime24 与 Client2 DEV 发布目标

更新日期：2026-11-17。本页是 Client2 的执行摘要；完整目标见 CLI 的
[Runtime24 核心配置接口与宿主 DEV 发布目标](https://github.com/risense-ai/respire-cli/blob/main/docs/runtime24-host-release-goal.md)。
组件职责见本仓[职责边界](component-boundaries.md)及 CLI 的
[共同约定](https://github.com/risense-ai/respire-cli/blob/main/docs/component-boundaries.md)。

## 授权和职责

当前已发布基线为 Runtime `v2.0.0-dev.23`。本轮授权发布 `v2.0.0-dev.24`，
只补核心配置候选验证、运行态应用及真实状态回执；不修改 Core SDK 或数据库格式。
Runtime24 接收宿主传入的候选，不接管宿主配置文件、钥匙串或下载与进程维护。

Core 负责底层存储、加密、模型执行及记忆算法。Runtime 负责核心登录、同步、数据库生命周期、
recall、remember 及 RPC/MCP。CLI 与 Client2 负责组件和模型下载、安装与选择、系统钥匙串、
私钥保管、安全传钥、自启、Runtime 拉起、停止和维护。

`agent-config` 的读取、校验和持久化属于宿主。核心候选必须经纯核心接口验证与应用，
或使用已约定且身份核验通过的安全生命周期流程；不得让 Runtime 代写宿主文件。
界面分别显示“配置已保存”“核心已验证／已应用”和待确认结果；本地配置回读不证明运行态已生效。
未知结果不重放，不以延长等待、吞错、关闭插件通道或静默 fallback 制造通过。

## Client2 执行顺序

1. 等待 Runtime24 的合并契约、公开 Release 与真实元数据，核对版本、来源和 SHA256 后升级基线。
   发布前仅准备版本补丁，不猜产物哈希。桌面包继续不内置 CLI、Runtime、Core 或模型。
2. 使用包含 CLI 插件宿主寿命修复的新候选验证 ExistingKeys 第二次启动。已有
   [启动诊断](../.github/workflows/published-startup-diagnostic.yml)固定公开 Client2 dev15 和 Runtime23，
   用于单独验证该修复；它不替代新基线或完整 GUI 验收。新 CLI 的 run、attempt 和完整 commit 必须显式输入。
3. 按实际核心回执适配配置界面，保留宿主保存和核心应用之间的区别。接口字段以合并契约为准。
4. PR 阶段只运行受影响的现有检查及必要定向复验。最终候选／公开 Release 阶段并行完成必要原生验收，
   发布已验证的同一批字节，并核对公开版本、来源和摘要。CLI 与 Client2 独立版本，CLI 优先发布。

验收使用隔离合成账号、钥匙串和数据库。覆盖公开安装升级、拉起、停止、自启、
无宿主输入的 Runtime 重启保持锁库，以及宿主取钥后自动解锁、登录与密钥续接、同步和记忆读写。
历史升级聚焦旧正式版及改变数据库格式的 DEV 检查点，验证原密钥解密、完整性和待同步数据；
格式未变的 DEV 不增加组合。不要求保留历史会话、凭据位置或派生索引。

## 状态边界

Client2 PR50 已合并为诊断设施；它不修复 CLI 插件宿主通道寿命，也不证明真实启动已通过。
后续启动复验必须使用包含该生产修复的新 CLI 候选。Runtime24 和 Client2 新 DEV 的发布、
核心接口适配及最终公开资产验收，均须分别记录实际证据；源码 CI 通过不能代替产物验收。
继续执行前核对远端状态，不从本页推导发布成功。
