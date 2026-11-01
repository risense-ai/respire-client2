# respire-client2 agent conventions

## 发版（每个新版本必须做）

1. commit → push main → `git tag vX.Y.Z && git push origin vX.Y.Z` → CI 构建 6 平台并自动发 Release → `gh release download` 下载 exe 跑 `--version` 核对。
2. **更新公告**：`bash scripts/release-notes.sh vX.Y.Z .release-notes.md` 会在文件末尾生成「更新说明（可直接复制给用户）」代码块（中文、按 新功能/问题修复 分类、带下载链接）。**发布完成后把这个代码块原样贴给用户**，并在回复里用 code block 包裹方便复制。不要贴原始 commit 主题或链接给用户。
3. 面向用户的解释用中文写在公告代码块里；commit message 保持英文 conventional 前缀（feat/fix/perf/docs）。
4. Release 正文的「更新说明」块由生成器自动写入；如手工改 Release 正文，保留该块结构。

## UI 改动验证

- `.verification/ui-mock/server.mjs` 提供 mock Runtime API（`node .verification/ui-mock/server.mjs`，端口 4173）。
- 用浏览器自动化打开 `http://127.0.0.1:4173/workspace.html#memories` 截图核对；注意后台标签页 rAF 节流会让力导向图谱动画看似不动，用同步渲染或前台验证。
- CSS 文件使用 CRLF 行尾；脚本化替换时保留原行尾。

## 已知约定

- 根字号由 `clamp(14px, 0.5rem + 0.62vw, 18.8px)` 随窗口宽度缩放；新增样式一律用 rem，不要写死 px 字号/控件尺寸。
- 记忆页四种视图：树形/列表/卡片/图谱；行选择 = 整行点选（打开阅读器），Ctrl 多选，Shift 连选；合并所选基于多选集。
