# Client2 与 Core、Runtime、CLI 的边界

完整职责、当前实现缺口和验收要求见 CLI 仓库的
[组件职责与发布验收边界](https://github.com/risense-ai/respire-cli/blob/main/docs/component-boundaries.md)。
该文档是共同约定；本页只记录 Client2 的对应职责，避免维护两套不同的协议说明。

当前已发布基线为 Runtime `v2.0.0-dev.24`，对应 CLI `v2.0.0-dev.19` 和 Client2 `v2.0.0-dev.17`。
本轮沿用已发布 Core SDK。实际发布与验收证据见 [共同发布记录](https://github.com/risense-ai/respire-cli/blob/main/docs/component-boundaries.md#本次发布的验收证据)。
Client2 与 CLI 独立版本，按协议及必需能力配合，不要求版本号相同，不扩展旧宿主与新 Runtime 的组合矩阵。
本仓执行摘要见 [Runtime24 与宿主 DEV 发布目标](runtime24-host-release-goal.md)；
共同目标见 CLI 的[完整执行约定](https://github.com/risense-ai/respire-cli/blob/main/docs/runtime24-host-release-goal.md)。

- Client2 负责界面、桌面偏好、组件与模型的下载安装、资源选择、系统钥匙串、
  私钥保管、开机自启，以及 Runtime 的拉起、停止和维护。
- Client2 经核验运行身份后，通过私有通道传钥。Runtime 不自行读取钥匙串；
  缺少宿主输入的 Runtime 重启必须保持锁库，随后由宿主读取密钥并解锁。
- Runtime 提供核心登录、同步、数据库、recall／remember 和 RPC／MCP；
  Core 提供底层存储、加密、模型执行及记忆算法。
- 代理和宿主配置由 CLI／共享宿主层读取、校验及持久化。`agent-config` 不是
  Runtime 业务命令；`--client-only` 不改变其归属。Client2 不得经业务 `cli.exec`
  绕回 Runtime 的宿主兼容实现。
- 涉及账号或密钥的交互使用明确的宿主接口；补充输入绑定原操作，未知结果不能重放。
  核心设置的生效通过明确控制或安全生命周期操作完成，不能用“已保存”冒充“已生效”。

历史兼容限数据库：保留旧密钥解密、完整性和待同步数据，并验证升级后记忆读写及同步。
数据库未变的 DEV 不增加 DEV→DEV 组合。完整原生矩阵仅在最终候选／公开 Release 阶段执行；
绿色源码检查不代表真实桌面启动、解锁或数据库升级已经通过。

Runtime24 仅接收宿主传入的核心配置候选，负责核心验证、运行态应用及真实状态回执，
不写宿主配置文件。CLI／Client2 负责持久化、并发检查、错误与中断处理，并分别报告保存和应用结果。
Client2 仅在接口契约、公开 Runtime 字节与宿主能力核验完成后升级基线；不得用本地配置回读冒充核心已应用。

当前未完成项必须按共同文档及实际 PR／验收报告报告。超出本轮窄范围授权的基线问题，
先提供证据及方案交用户决策，不通过禁用功能或静默回退隐藏。
