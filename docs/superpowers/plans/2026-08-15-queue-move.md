# 队列页 m 键移动模式实现计划（用户已确认语义）

## 需求

队列页按 `m` 进入移动模式，`↑↓←→/hjkl` 移动当前光标下的歌曲，`Enter`/`Esc` 结束；
移动模式期间底部快捷键提示行切换；移动后列表顺序更新、当前播放曲目索引跟随。

## 用户确认的语义（2026-08-15）

1. `←/h` = 移到队首，`→/l` = 移到队尾；`↑/k` = 上移一格，`↓/j` = 下移一格
2. `Esc` = 直接结束（保留已移动的结果，与 Enter 等效，不撤销）
3. 过滤生效（确认态）时按 `m`：先退出过滤再进入移动模式；过滤输入框聚焦时 `m` 是过滤字符（输入优先）
4. 普通提示行追加 "m 移动"；移动模式提示行切换为移动键位
5. 移动模式不拦截全局键（数字键切页/空格/q 照常；模式状态随页面保留）

## 设计

### 架构（沿用现有"页面发消息 → root 执行 → sync 回灌"模式，同 d 删除）

```
queue 页按键 m → moving=true（页面状态）
移动键 ↑↓←→/hjkl → emitQueueMove{from, to} → root: queue.Move(from,to) → syncQueueViews 回灌
  （applyFilter 按曲目 ID 保持选中 → 光标跟随被移动的歌曲）
Enter/Esc → moving=false（Enter 不触发跳转播放；Esc 同 Enter 直接结束）
```

每次移动走完整往返（同 queueDeleteMsg 模式，亚毫秒级，按键连发安全：
选中项按 ID 跟随，下一按键从新位置继续）。

### Task 1：queue 包 `Move(from, to int) bool`（queue/queue.go + queue_test.go）

- 语义：把 `from` 下标曲目移到**最终位置** `to`；`currentIdx` 跟随同一首歌
  （被移曲是当前曲 → 新位置即当前；跨过当前曲 → 相应 ±1）
- 非法返回 false 且不改状态：from/to 越界、from==to、空队列/单曲
- 算法：
  1. `t = tracks[from]`，`movedIsCurrent = (from == currentIdx)`
  2. 移除 from：`currentIdx > from` → `--`；`movedIsCurrent` → 临时 `-1`
  3. 插入 to（新数组最终下标）：`movedIsCurrent` → `currentIdx = to`；
     `else if currentIdx >= to` → `++`
- 测试四象限：
  - 普通曲跨当前曲移动（前→后 / 后→前）：顺序正确、currentIdx 指向同一首歌
  - 移动当前曲（含移到首/尾）：currentIdx = 新位置
  - 边界：from==to false、越界 false、空队列 false、单曲 false、首项上移/末项下移（页面层）
  - 移动后 Next 连播顺序 = 新数组顺序（回归：连播顺序正确）

### Task 2：UI 移动模式（ui/queue.go + ui/root.go + 测试）

**ui/queue.go**：
- `queueModel` 新增 `moving bool`
- Update 顶部（tea.KeyMsg 内，最先）：`if q.moving`：
  - `enter`/`esc` → `moving=false`，返回（**不**产生 queuePlayMsg）
  - `up`/`k` → `moveBy(-1)`；`down`/`j` → `moveBy(+1)`；
    `left`/`h` → `moveTo(0)`；`right`/`l` → `moveTo(-1)`（-1 = 队尾）
  - 其余按键一律忽略（d/c/s/p// 等在移动模式无操作）
- `case "m"`（进入移动模式）：
  - 过滤输入框聚焦 → break 落到输入框（m 是过滤字符）
  - `len(items) < 2` → no-op（无可移动）
  - 过滤确认态 → 先退出过滤（filtering=false、blur、清词、applyFilter）再 `moving=true`
- 辅助：`moveBy(delta)`：选中项 idx+delta 越界不发消息；`moveTo(0/-1)`：已在目标位不发消息；
  均返回 `emitQueueMove(item.idx, to)`
- view()：moving 分支最前 —— hint = `modeLabel() + " · ↑↓←→/hjkl 移动 · Enter/Esc 结束"`；
  普通 hint 追加 `· m 移动`；过滤确认态 hint 也追加 `· m 移动`
- sync() 不重置 moving（页面状态）；选中项经 applyFilter 按 ID 保持（移动后光标跟随）

**ui/root.go**：
- `queueMoveMsg{from, to int}` + `emitQueueMove(from, to)`
- Update case：`m.queue.Move(msg.from, msg.to)` 失败（false）→ 原样返回；
  成功 → `refreshPreload()`（跨当前曲移动会改变"下一首"候选）+ `syncQueueViews()`

**测试**（ui/queue_test.go + ui/root_test.go，走 newTestModel 全链路）：
- 进入：m → moving=true、hint 含 "↑↓←→/hjkl 移动"；空队列/单曲 m no-op
- 移动键：↑/k、↓/j、←/h、→/l 各产生 queueMoveMsg{from,to} 正确（选中跟随：连按 ↓ 依次下移）
- 边界：首项 ↑ 无消息；末项 ↓ 无消息
- 结束：移动模式 Enter 退出且不产生 queuePlayMsg；Esc 退出；退出后 hint 恢复
- 移动模式中 d/c/s/p// 忽略（无消息、moving 保持）
- 过滤交互：确认过滤态 m → 退出过滤 + 进入移动模式；输入框聚焦 m → 字符进过滤词
- root 往返：queueMoveMsg 后队列顺序变、currentIdx 跟随（含移动当前曲）、队列页 items 同步、
  选中项 = 被移动歌曲
- 连播回归：移动后 TrackEnded 播放新顺序下一首（沿用 TestTrackEndedAutoAdvances 模式）

### Task 3：文档同步（feature_lead 执行）

- `docs/superpowers/specs/2026-08-13-music-tui-design.md` 14.4 键位表：
  队列页追加 `m` 行（进入移动模式）与移动模式行（↑↓←→/hjkl 移动 · Enter/Esc 结束）
- 队列页描述句提及移动模式

## Git 纪律

- 分支 `feat/queue-move`（worktree `.worktrees/queue-move`），只 add 自己负责的文件，
  绝不 `git add -A`；commit 前 `git status` 检查；每个 Task 独立 commit
- go 命令 PATH：`export PATH=/home/ivhu/go-sdk/go/bin:$PATH`

## 验收

- `go build ./...`、`go vet ./...`、`go test ./...`（含 `-race`）全绿
- 用户终端确认：m 进入/移动/提示切换/Enter/Esc 退出/连播顺序正确
