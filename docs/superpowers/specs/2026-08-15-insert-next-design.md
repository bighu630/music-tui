# "添加到下一首"设计

日期：2026-08-15
状态：已与用户确认

## 背景

用户原话："a 快捷键需要支持添加到下一首"——当前 `a` 键只能把歌曲追加到队列尾部（经"添加到"选择器首项"当前播放队列"），希望支持**插入到当前曲目之后**（下一首播放，插队）。

## 范围（用户已确认）

- `queue` 包：新增 `InsertNext` API
- "添加到"选择器（ui/playlists.go plPicker）：首项改为"下一首播放"，第二项为现有"追加到队尾"
- root 层：新增 insert 消息路由（`queue.InsertNext` + `syncQueueViews`）
- 搜索/历史页提示文案：不变（`a` 键位未变，选择器内部自解释）

## 交互（用户已确认，方案 B）

`a` 键仍弹出"添加到"选择器，条目顺序改为：

1. **▶ 下一首播放**（新增，默认选中，描述"插入到当前曲之后"）→ Enter 插队
2. **▶ 当前播放队列**（现有，描述"追加到队尾"）→ Enter 追加
3. 各播放列表（不变）
4. ＋ 新建列表（不变）

- 选择器底部提示 `↑↓ 选择 · Enter 确认 · Esc 取消` 不变
- 搜索页 hint `↑↓ 选择 · Enter/p 播放 · a 添加到…`、历史页 hint `Enter/p 重播 · d 删除 · c 清空 · a 添加到…` 均不变

## queue 包：InsertNext

```go
// InsertNext 插入到当前曲目之后（下一首播放）。不改变当前曲目；
// 无当前曲目（currentIdx=-1，如从未播放/清空后）时插入到队首（index 0）。
// 随机模式不重洗牌：插入位即实际下一首（"下一首播放"语义优先）。
func (q *Queue) InsertNext(t model.Track)
```

- 有当前曲：插入到 `currentIdx+1`
- 无当前曲（`currentIdx == -1`）：插入到队首（index 0）——用户确认
- 不自动开播（同 `Add`：从未播放时仅入队）
- 模式无关：三种模式下插入位不变

## root 层

- 新增 `trackInsertNextMsg` 类型 + `emitTrackInsertNext(track)` cmd
- root 处理：`m.queue.InsertNext(msg.track)` + `syncQueueViews()`，无成功 toast（与追加一致）

## 选择器实现

- 新增 `pickerQueueNextItem`（首项，加粗）：Title `▶ 下一首播放`，Description `插入到当前曲之后`
- 现有 `pickerQueueItem` 保持加粗，Description `追加到队尾`，顺序降为第二项
- Enter 分发：`pickerQueueNextItem` → `emitTrackInsertNext`；`pickerQueueItem` → `emitTrackAppend`（现有）

## 测试（TDD）

- queue：`InsertNext` 单测——
  - 有当前曲：插到其后，后续曲目顺延
  - 无当前曲（-1）：插入队首
  - 空队列：成为唯一曲目
  - 随机模式：插入后不重洗，InsertNext 的曲目就是 Next 的下一首
  - 与 Remove 组合：删当前曲后 InsertNext 位置正确
- picker：首项默认选中；Enter 首项发 insert 消息、第二项发 append 消息
- root：`trackInsertNextMsg` 处理后队列顺序正确（currentIdx+1 处）

## 验证

- `go build ./...`、`go vet ./...`、`go test ./...`（含 -race）全绿
- git 纪律：只 add 本次涉及文件（queue/queue.go、queue/queue_test.go、ui/playlists.go、ui/playlists_ui_test.go、ui/root.go、ui/root_test.go），commit 前 git status 检查，绝不用 `git add -A`
