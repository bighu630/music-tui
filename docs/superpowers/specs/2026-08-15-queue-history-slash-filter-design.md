# 队列页 / 历史页 `/` 过滤功能 设计文档

> 日期：2026-08-15 ｜ 分支：feat/slash-filter ｜ 范围：仅队列页 + 历史页（播放列表页明确不做）

## 需求

用户原话：「在队列，历史两个tag中都加上 / 搜索功能」。经确认：
- 队列页、历史页按 `/` 打开过滤输入框（复用搜索页 textinput 模式）
- 输入关键词实时过滤列表（标题/歌手子串匹配，大小写不敏感）
- `Esc` 关闭过滤：清空关键词、恢复完整列表
- 过滤中显示当前结果数（如「过滤: xxx (3/20)」）
- 播放列表页不做（用户确认：内容不多，不需要）

## 交互设计（已与用户确认，方案 A：vim 式两段设计）

| 按键 | 状态：输入框聚焦（过滤编辑中） | 状态：已确认（输入框失焦、过滤生效） |
|---|---|---|
| `/` | 作为字符输入（可输入 "/"） | 重新聚焦输入框，继续编辑过滤词 |
| 字符键（含 d/c/s/p/a/q/空格/数字 6-0） | 输入过滤词，实时过滤 | 正常页面操作（j/k 浏览、p 播放等） |
| 数字 1-5 | 全局切页（既有行为，root 拦截，与搜索页一致——过滤词无法含 1-5） | 全局切页（既有行为） |
| ↑↓ | 移动列表选中（textinput 不消费方向键，需页面层转发给 list） | 移动列表选中 |
| Enter | **确认过滤**：输入框失焦、过滤保持生效 | 播放选中项（与未过滤一致） |
| Esc | 退出过滤：清词、恢复完整列表、失焦 | 退出过滤：清词、恢复完整列表 |
| 空格 / a / q | 均为过滤词字符（root.typingText 让位） | 空格=播放/暂停，a=添加到…，q=退出（既有行为） |

已知限制（与搜索页一致，非本次引入）：
- 数字 1-5 在过滤聚焦时仍切页，过滤词无法包含这些字符
- 过滤聚焦时 ctrl+c 被 textinput 吞掉，无法退出程序（q 也被输入框消费）

## 技术设计

### 文件与职责

| 文件 | 改动 |
|---|---|
| `ui/filter.go`（新建） | 纯函数 `filterMatches(keyword, value string) bool`：Trim 后大小写不敏感子串匹配；空 keyword 匹配一切。页面无关，可单测 |
| `ui/filter_test.go`（新建） | filterMatches 单测 |
| `ui/queue.go` | queueModel 增加 `filtering bool` + `filterInput textinput.Model`；`/` 打开/重聚焦；聚焦态按键路由（↑↓→list、Enter→确认、Esc→退出、其余→input）；`applyFilter()` 重算过滤列表（保留 `queueItem.idx` 原始下标，播放/删除零改动）；`sync()` 数据刷新时重放过滤；view 顶部过滤行 + 提示行同步 |
| `ui/history.go` | historyModel 同样改动（条目保留 `historyItem.entry` 完整引用） |
| `ui/root.go` | `typingText()` 增加 pageQueue/pageHistory 分支（调用两页新增的 `typing()` 方法：过滤聚焦中返回 true） |
| `ui/queue_test.go` / `ui/history_test.go` | 页面集成测试（见测试节） |
| `ui/root_test.go` | 全局键让位集成测试（空格/a/q 聚焦时不触发全局动作） |

### 匹配规则

- 匹配对象：`FilterValue()`（即 `标题 + " " + 歌手`），与 list 既有 FilterValue 一致
- 规则：`strings.Contains(strings.ToLower(value), strings.ToLower(strings.TrimSpace(keyword)))`
- 空 keyword：匹配一切（全量列表）
- 队列页当前曲目使用 AI 清洗标题（sync 已处理，FilterValue 自动受益）

### 过滤列表与索引映射

- 过滤后条目**复用原 queueItem/historyItem 结构**，`queueItem.idx` 仍是队列内原始下标、`historyItem.entry` 仍是完整历史记录
- 因此 Enter/p/d/c/s/a 等操作键**零逻辑改动**，天然作用于过滤后列表项
- 选中保持：过滤词变化/退出过滤时，按曲目 ID 保持选中（命中则 Select，未命中 clamp 到可见列表末尾；列表为空时不选中）；复用 queue.sync 既有 clamp 模式

### 数据刷新重放

- queueModel.sync()（队列变化：增删/切歌/清空）与 historyModel.setEntries()（历史加载/删除/清空）在 `filtering` 为 true 时必须重放过滤，保证过滤态下删除/外部变化后计数与列表不错位
- 过滤词本身（filterInput.Value）在 sync/setEntries 中保持不变

### 渲染

- 过滤行：内容区顶部，`"过滤: " + input.View() + " (可见数/总数)"`；过滤开启（含已确认态）均显示，失焦时 textinput 渲染无光标
- 列表高度：过滤时不变（现布局 content = list(h-3) ≤ h-1 已余 2 行，加 1 行过滤行仍 ≤ h-1），无需改 setSize 的高度逻辑；但 setSize 需同步设置 filterInput.Width（建议 `width-14`，下限 10）
- 提示行（bottomHint，同步更新）：
  - 聚焦中：队列 `列表循环 · 输入过滤 · Enter 确认 · Esc 取消`，历史 `输入过滤 · Enter 确认 · Esc 取消`
  - 已确认：队列 `列表循环 · Enter/p 跳转播放 · d 删除 · c 清空 · s 切换模式 · Esc 退出过滤`，历史 `Enter/p 重播 · d 删除 · c 清空 · a 添加到… · Esc 退出过滤`
  - 空列表（0 条）时 `/` 仍可打开，显示 (0/0)，空态提示在过滤开启时被过滤行替换

### 测试

1. **纯函数**（filter_test.go）：大小写不敏感；子串；空词匹配一切；Trim；非空不匹配
2. **队列页集成**（queue_test.go，通过 root Model 驱动）：
   - `/` 打开过滤（view 含过滤行）；输入实时过滤 + 计数 (n/m) 更新
   - Enter 确认：input 失焦、过滤保持（view 计数仍在）
   - 过滤态 ↑↓ 移动、Enter 播放 → queuePlayMsg.index 为**原始队列下标**（构造多曲目、过滤后选中非首项验证映射）
   - 过滤态 d 删除 → queueDeleteMsg.index 为原始下标
   - Esc 恢复完整列表 + 计数消失
   - sync 重放：过滤态删除后计数与列表一致
   - 聚焦态 `/` 字符可输入
3. **历史页集成**（history_test.go）：同模式（trackSelectedMsg / deleteEntryMsg 验证原始记录）
4. **root 全局键让位**（root_test.go）：队列/历史过滤聚焦时 空格不切播放、a 不开选择器、q 不退；数字 1-5 仍切页
5. **回归**：现有测试全绿；`go build ./... && go vet ./... && go test ./... -race` 通过

### 交付标准

- 队列/历史 `/` 过滤可用：打开、实时过滤、Enter 确认、Esc 恢复、计数显示、过滤态播放/删除索引正确
- 测试全绿（含 -race）
- 用户终端确认（过滤输入、结果数、Esc 恢复、过滤状态下操作）
