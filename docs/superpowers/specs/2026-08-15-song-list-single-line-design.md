# 歌曲列表条目单行化设计

日期：2026-08-15
状态：已与用户确认

## 背景

用户原话："列表里面最好是一行显示，一行歌词，一行作者有点占空间"——当前歌曲列表条目两行显示（第一行标题、第二行作者），希望改为单行节省空间。

## 范围（用户已确认）

4 处歌曲列表全部改为单行：

- 搜索结果（ui/search.go `trackItem`）
- 队列（ui/queue.go `queueItem`）
- 历史（ui/history.go `historyItem`）
- 播放列表详情（ui/playlists.go `plTrackItem`）

不动的列表（保持双行）：播放列表概览（overviewItem）、登录设置菜单（setupItem/browserItem）、"添加到"选择器（picker 条目）。

## 格式（用户已确认）

`标题 - 作者 · 附加信息`，分隔符 " - " 与 " · "：

| 页面 | 单行格式 |
|---|---|
| 搜索 | `晴天 - 周杰伦 · 3:45` |
| 队列 | `▶  1. 晴天 - 周杰伦 · 3:45`（当前曲整行加粗粉色 212） |
| 历史 | `晴天 - 周杰伦 · 今天 15:04` |
| 播放列表详情 | ` 1. 晴天 - 周杰伦 · 3:45` |

- 序号 / ▶ 标记 / 当前曲加粗：保留（用户确认）
- 条目间距：保持默认 1 行（用户确认，不设 SetSpacing(0)）
- 作者为空时省略分隔符：`晴天 · 3:45`（沿用队列页现有空 Artist 处理）

## 实现机制

- 4 个列表的 `list.DefaultDelegate` 设 `ShowDescription = false`：条目高度变 1，长内容由 delegate 自动 `ansi.Truncate` 省略号截断（从右截：先丢时长 → 再丢作者 → 标题保底），无需自写截断
- 各条目 `Title()` 返回拼接后的单行字符串，`Description()` 返回空串
- `ui/format.go` 新增共享函数 `formatTrackLine(title, artist, meta string) string`：单行拼接 + 空作者处理，4 处条目统一调用
- 其余列表 delegate 不动

## 测试

- 先写 `formatTrackLine` 单测（ui/format_test.go）：正常拼接 / 空作者 / 空附加信息
- 更新现有条目断言（ui/history_test.go 的 `Description()==''` 检查等）
- 全量跑测试找出其他受影响的渲染断言（root_test / queue_test / playlists_ui_test）

## 验证

- `go build ./...`、`go vet ./...`、`go test ./...`（含 -race）全绿
- 用户终端确认 4 个列表的单行显示效果与长标题截断
