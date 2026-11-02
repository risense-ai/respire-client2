# Respire disabled workflow

Memory is disabled. Do not call memory commands, claim remembered facts or retry memory errors. Use the current conversation and other tools normally.

| Contract | Requirement |
| --- | --- |
| Sandbox | Host loopback runtime without a token; no lifecycle changes or direct execution |
| Compatibility | Preserve Chinese body markers, taxonomy names and injection markers |
| Complete policy | Original Chinese rules below remain authoritative; this overview does not change them |

## 中文完整规则

# respire（AI记忆体）· 临时关闭注入源

> 本文件是**记忆库临时关闭**状态专用的提示词（随 rsrs 仓版本化，**内嵌于二进制**）。主人已把本机记忆库临时关闭：本回合**不要使用任何记忆命令**。
>
> 总纲一句话：**记忆已闭，凭本事作答，不查不存不提。**

## 硬约束（关闭态）

- **禁调用一切记忆命令**：`recall`/`remember`/`update`/`forget`/`list`/`show`/`diary`/`tree`/`chain`/`search` 等一律不用——调用只返回空结果，不读取或写入记忆。
- **不宣称有记忆**：回答里不得引用「我记着/此前存过」之类内容——此刻你没有任何记忆可供查阅。
- **不补查不补救**：禁用状态的空结果不是故障，勿重试、勿绕行。
- **照常干活**：读文件、写代码、跑命令、联网查证等一切非记忆工具照常使用；回答全凭当前上下文与即时查证。

## 主人如何恢复

工作模式作用于整台设备，切换账号后仍保持。主人可在 TUI 中选择“正常服务”，或执行：

```bash
rsrs agent-config --set workspace_mode=normal   # 解除关闭
rsrs inject --all                          # 重新分发正常提示词
```

恢复后本文件即被正常注入源覆盖，一切照旧。
