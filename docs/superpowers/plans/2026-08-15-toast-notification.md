# Toast 通知 + 底部常驻状态栏实现计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 把 music-tui 的错误/成功提示从"替换中间区末行的横幅"改为 toast 通知（临时浮现、定时自动消失、不参与布局），并新增底部常驻状态栏作为布局稳定的报错/状态"家"。

**Architecture:** 新增 `ui/toast.go` 单条覆盖式 toast 状态机（`toast`/`toastKind`/`toastExpireMsg`，纯逻辑可单测）；`root.go` 删除 `lastError`/`notice` 字段，42 处赋值点改走 `showToast(text, kind)`；`View()` 改为 `tabBar + body + statusBar` 三明治，活跃 toast 对状态栏上方一行的右端做覆盖渲染（行数不变、其余行不变 → 排版零跳动）；页面 setSize 高度 `Height-2` → `Height-3`。

**Tech Stack:** Go + bubbletea + lipgloss + x/ansi（均为现有依赖，无新增）

**Spec:** `docs/superpowers/specs/2026-08-15-toast-notification-design.md`

---

## 前置信息（worker 必读）

- 主工作区 `/data/code/music-tui`，**在 worktree `.worktrees/toast/` 中开发**（branch `feat/toast-notification`）。
- **git 纪律**：只 `git add` 自己修改的文件（`ui/toast.go`、`ui/toast_test.go`、`ui/root.go`、`ui/root_test.go`、`docs/superpowers/plans/2026-08-15-toast-notification.md`），**绝不 `git add -A`**；每次 commit 前 `git status` 确认无他人文件混入。master 上有并行会话推进，worktree 分支固定，勿 merge。
- 现有行为参考（勿破坏）：`root.View()` 全屏约束（tabBar 2 行 + body 必须 ≤ 高度）；`TestRootViewBannerStaysWithinHeight` 将被重写（Task 4）。
- `modeName(m queue.Mode) string` 已存在于 `ui/queue.go:175`（顺序/随机/单曲循环），状态栏直接复用。
- `retryBackoff` 是包级变量（测试可调）——toast 时长照此模式。

---

### Task 1: toast 状态机（ui/toast.go + ui/toast_test.go）

**Files:**
- Create: `ui/toast.go`
- Create: `ui/toast_test.go`

- [ ] **Step 1: 写失败测试** `ui/toast_test.go`

```go
package ui

import (
	"testing"
	"time"
)

// TestToastDuration kind→时长映射：错误/警告 5s，成功/信息 3s。
func TestToastDuration(t *testing.T) {
	toastErrorDuration = 5 * time.Second
	toastSuccessDuration = 3 * time.Second
	cases := []struct {
		kind toastKind
		want time.Duration
	}{
		{toastError, 5 * time.Second},
		{toastWarning, 5 * time.Second},
		{toastSuccess, 3 * time.Second},
		{toastInfo, 3 * time.Second},
	}
	for _, tc := range cases {
		if got := toastDuration(tc.kind); got != tc.want {
			t.Errorf("toastDuration(%v) = %v, want %v", tc.kind, got, tc.want)
		}
	}
}

// TestExpireToastIDMatch 过期消息 id 与当前 toast 匹配 → 清除。
func TestExpireToastIDMatch(t *testing.T) {
	cur := &toast{text: "播放失败", kind: toastError, id: 1}
	if got := expireToast(cur, 1); got != nil {
		t.Errorf("expireToast 匹配 id 应清除, got %+v", got)
	}
}

// TestExpireToastIDMismatch 过期消息 id 不匹配（当前 toast 已被新消息覆盖）→ 保留。
func TestExpireToastIDMismatch(t *testing.T) {
	cur := &toast{text: "新消息", kind: toastWarning, id: 2}
	if got := expireToast(cur, 1); got != cur {
		t.Errorf("expireToast 不匹配 id 应保留当前 toast, got %+v", got)
	}
}

// TestExpireToastNil 无活跃 toast 时过期消息安全丢弃。
func TestExpireToastNil(t *testing.T) {
	if got := expireToast(nil, 1); got != nil {
		t.Errorf("无 toast 时过期消息应丢弃, got %+v", got)
	}
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `cd /data/code/music-tui/.worktrees/toast && go test ./ui/ -run TestToast -v`
Expected: FAIL（undefined: toastKind / toastDuration / expireToast）

- [ ] **Step 3: 实现** `ui/toast.go`

```go
// toast 通知状态机：单条覆盖 + 定时自动消失。
// toast 渲染不参与布局计算（root.View 末尾对状态栏上方一行的右端做覆盖），
// 因此出现/消失都不会引起排版跳动。本文件为纯逻辑（无 bubbletea 依赖），
// 定时器命令 showToast 在 root.go（Model 方法，需 tea.Tick）。

package ui

import "time"

// toastKind toast 类型：决定样式颜色与显示时长。
type toastKind int

const (
	toastError   toastKind = iota // 红 ⚠，5s
	toastWarning                  // 黄 ⚠，5s
	toastSuccess                  // 绿 ✔，3s
	toastInfo                     // 默认色 ℹ，3s
)

// toast 显示时长（包级变量：测试可调小以快进定时器，同 retryBackoff 模式）。
var (
	toastErrorDuration   = 5 * time.Second
	toastSuccessDuration = 3 * time.Second
)

// toast 单条通知（覆盖语义：新 toast 替换旧 toast 并重置定时器）。
type toast struct {
	text string
	kind toastKind
	id   uint64 // 单调递增；过期消息按 id 匹配，防止误清被新 toast 覆盖后的旧消息
}

// toastExpireMsg toast 消失定时器到期消息。
type toastExpireMsg struct {
	id uint64
}

// toastDuration 返回 kind 对应的显示时长：错误/警告 5s，成功/信息 3s。
func toastDuration(kind toastKind) time.Duration {
	switch kind {
	case toastError, toastWarning:
		return toastErrorDuration
	default:
		return toastSuccessDuration
	}
}

// expireToast 处理过期消息：id 与当前 toast 匹配才清除；不匹配
// （当前 toast 已被新消息覆盖）则保持现状。
func expireToast(cur *toast, id uint64) *toast {
	if cur != nil && cur.id == id {
		return nil
	}
	return cur
}
```

- [ ] **Step 4: 跑测试确认通过**

Run: `go test ./ui/ -run TestToast -v`
Expected: PASS（4 个测试）

- [ ] **Step 5: Commit**

```bash
git add ui/toast.go ui/toast_test.go
git status   # 确认只有这两个文件
git commit -m "feat(ui): toast 通知状态机（单条覆盖 + 定时消失，纯逻辑）"
```

---

### Task 2: root.go 字段与消息路由改造

**Files:**
- Modify: `ui/root.go`

- [ ] **Step 1: 替换 Model 字段**（`root.go` 中 `lastError string` / `notice    string` 两行）

原文（在 Model 结构体 `hoverTab` 之后）：

```go
	hoverTab  int // Tab 栏悬停标签下标（= page 枚举值）；-1 = 无悬停
	lastError string
	notice    string // 绿色成功提示（如“已添加到「xxx」”；新按键分发时清除）
	ended     bool   // 当前歌曲是否已播放结束/出错（空格语义：重播同曲而非 Resume）
```

新文：

```go
	hoverTab int // Tab 栏悬停标签下标（= page 枚举值）；-1 = 无悬停
	// toast 活跃 toast（单条覆盖；定时自动消失，不参与布局）。替代旧 lastError/notice 横幅。
	toast   *toast
	toastID uint64 // toast 自增 id：过期消息按 id 匹配，防误清被覆盖后的新 toast
	ended   bool   // 当前歌曲是否已播放结束/出错（空格语义：重播同曲而非 Resume）
```

- [ ] **Step 2: Update 增加 toastExpireMsg 分支**（在 `case tea.BatchMsg:` 之前插入）

```go
	case toastExpireMsg:
		m.toast = expireToast(m.toast, msg.id)
		return m, nil
```

- [ ] **Step 3: 新增 showToast 方法**（放在 `notifyTrack` 方法附近）

```go
// showToast 显示一条 toast（覆盖语义）：替换旧 toast 并重置消失定时器。
// 返回的 cmd 产生 toastExpireMsg（时长见 toastDuration）。
func (m Model) showToast(text string, kind toastKind) (Model, tea.Cmd) {
	m.toastID++
	id := m.toastID
	m.toast = &toast{text: text, kind: kind, id: id}
	return m, tea.Tick(toastDuration(kind), func(time.Time) tea.Msg {
		return toastExpireMsg{id: id}
	})
}
```

- [ ] **Step 4: 消息路由替换**——按下表逐处替换。规则：
  - `m.lastError = "X"` / `m.notice = "X"`（设置）→ 调 `showToast`，把返回的 cmd 合并进 return：
    - 原 `return m, nil` → `m, cmd := m.showToast(...); return m, cmd`
    - 原 `return m, tea.Batch(cmds...)`（cmds 为 []tea.Cmd 变量，如 onPlayerEvent 内）→ `m, cmd := m.showToast(...); return m, tea.Batch(cmd, cmds...)`（变参展开，合法）
    - 原 `return m.syncQueueViews(), nil` 等带后处理的 → 先 `m, cmd := m.showToast(...)` 再做后处理，最后 `return m, cmd`
    - **注意**：`showToast` 用 `:=` 时新 `m` 会遮蔽，后续代码若还引用 `m`（如 `m.plPage = ...`）必须用新 `m` 继续。
  - `m.lastError = ""` / `m.notice = ""`（清除）→ **直接删除该行**（toast 覆盖语义：新消息自然替换旧消息），例外见 Step 5。
  - `m.notice = "" // 新按键分发前清除提示` 两处（picker 分支与 KeyMsg 主分支）→ **直接删除**（toast 生命周期只由定时器管理）。

| # | 位置（case/函数） | 原文 | 新文 kind |
|---|---|---|---|
| 1 | plLoadMsg 不存在分支 | `m.lastError = "播放列表「" + msg.name + "」不存在"` | error |
| 2 | plCreateMsg 开头 | `m.lastError = ""` | 删 |
| 3 | plCreateMsg 失败分支 | `m.lastError = "新建播放列表失败: " + err.Error()` | error |
| 4 | plRenameMsg 开头 | `m.lastError = ""` | 删 |
| 5 | plRenameMsg 失败分支 | `m.lastError = "重命名失败: " + err.Error()` | error |
| 6 | plDeleteMsg 开头 | `m.lastError = ""` | 删 |
| 7 | plDeleteMsg 失败分支 | `m.lastError = "删除播放列表失败: " + err.Error()` | error |
| 8 | plRemoveTrackMsg 开头 | `m.lastError = ""` | 删 |
| 9 | plRemoveTrackMsg 失败分支 | `m.lastError = "移除歌曲失败: " + err.Error()` | error |
| 10 | ytLoginMsg 开头 | `m.lastError = ""` | 删 |
| 11 | ytLoginMsg 失败分支 | `m.lastError = "保存登录配置失败: " + err.Error()` | error |
| 12 | ytLoginMsg 成功分支 | `m.notice = "已保存登录配置，验证中…"` | info |
| 13 | ytLoginFileMsg 开头 | `m.lastError = ""` | 删 |
| 14 | ytLoginFileMsg cookies 不可读 | `m.lastError = "cookies.txt 不可读: " + err.Error()` | error |
| 15 | ytLoginFileMsg 保存失败 | `m.lastError = "保存登录配置失败: " + err.Error()` | error |
| 16 | ytLoginFileMsg 成功分支 | `m.notice = "已保存登录配置，验证中…"` | info |
| 17 | ytLoginPasteMsg 开头 | `m.lastError = ""` | 删 |
| 18 | ytLoginPasteMsg 保存失败 | `m.lastError = "保存登录配置失败: " + err.Error()` | error |
| 19 | ytLoginPasteMsg 成功分支 | `m.notice = "已保存登录配置，验证中…"` | info |
| 20 | ytLogoutMsg 开头 | `m.lastError = ""` | 删 |
| 21 | ytLogoutMsg 失败分支 | `m.lastError = "退出登录失败: " + err.Error()` | error |
| 22 | ytLogoutMsg 成功分支 | `m.notice = "已退出 YT Music 登录"` | success |
| 23 | ytVerifyDoneMsg 开头两行 | `m.notice = ""` + `m.lastError = ""` | 删（两行都删） |
| 24 | ytVerifyDoneMsg 失败分支 | `m.lastError = ytVerifyErrorText(msg.err)` | error |
| 25 | ytVerifyDoneMsg 成功分支 | `m.notice = "YT Music 登录有效"` | success |
| 26 | ytSyncDoneMsg 失败分支 | `m.notice = ""` + `m.lastError = "同步失败: " + msg.err.Error()` | 删清除行；错误行 → error |
| 27 | ytSyncDoneMsg 成功分支 | `m.lastError = ""` + `m.notice = fmt.Sprintf("已同步 %d 个歌单 · 共 %d 首", len(msg.results), total)` | 删清除行；成功行 → success |
| 28 | ytImportDoneMsg 失败分支 | `m.notice = ""` + `m.lastError = "导入失败: " + msg.err.Error()` | 删清除行；错误行 → error |
| 29 | ytImportDoneMsg 成功分支 | `m.lastError = ""` + `m.notice = fmt.Sprintf("已导入「%s」%d 首", msg.res.Remote.Title, msg.res.TrackCount)` | 删清除行；成功行 → success |
| 30 | ytRefreshMsg 非同步列表 | `m.notice = ""` + `m.lastError = "该列表不是 YT Music 同步列表"` | 删清除行；错误行 → error |
| 31 | ytRefreshDoneMsg 失败分支 | `m.notice = ""` + `m.lastError = "刷新失败: " + msg.err.Error()` | 删清除行；错误行 → error |
| 32 | ytRefreshDoneMsg 成功分支 | `m.lastError = ""` + `m.notice = fmt.Sprintf("已刷新「%s」%d 首", msg.res.ListName, msg.res.TrackCount)` | 删清除行；成功行 → success |
| 33 | playResultMsg 失败分支 | `m.lastError = "播放失败: " + msg.err.Error()` | error |
| 34 | playerOpResultMsg | `m.lastError = msg.err.Error()` | error |
| 35 | retryPlayMsg 队列清空 | `m.lastError = "播放失败：队列已清空，已停止自动重试"` | error |
| 36 | resumeResultMsg 恢复失败 | `m.lastError = "恢复播放失败: " + msg.err.Error()` | error |
| 37 | resumeResultMsg SetLoop 失败 | `m.lastError = "设置循环失败: " + err.Error()` | error |
| 38 | deleteEntryMsg 失败分支 | `m.lastError = "删除历史失败: " + err.Error()` | error |
| 39 | clearHistoryMsg 失败分支 | `m.lastError = "清空历史失败: " + err.Error()` | error |
| 40 | plPicker 关闭且有 notice | `m.notice = picker.notice` | success |
| 41 | plPicker 未关闭 | `m.notice = "" // 新按键分发前清除提示` | 删 |
| 42 | KeyMsg 主分支开头 | `m.notice = "" // 新按键分发前清除提示` | 删 |
| 43 | p 键无选中 | `m.lastError = "当前没有可添加的歌曲（请先在搜索/历史/播放列表页选中歌曲）"` | error |
| 44 | onPlayerEvent 恢复失败 | `m.lastError = "恢复播放失败: " + hint` | error |
| 45 | onPlayerEvent 重试中 | `m.lastError = fmt.Sprintf("播放失败：%s，正在自动重试（%d/%d）…", hint, m.retryCount, maxPlayRetries)` | warning |
| 46 | onPlayerEvent 重试耗尽停止 | `m.lastError = fmt.Sprintf("播放失败：%s，已重试 %d 次。请稍后重试或更换歌曲", hint, maxPlayRetries)` | error |
| 47 | onPlayerEvent 通用错误 | `m.lastError = ev.Err.Error()` | error |
| 48 | skipFailedTrack 末尾 | `m2.lastError = fmt.Sprintf("「%s」播放失败：%s，已重试 %d 次，跳过继续播放", failedTitle, hint, maxPlayRetries)` | warning |
| 49 | beginPlay 开头 | `m.lastError = ""` | 见 Step 5 |
| 50 | beginPlay 播放失败 | `m.lastError = "播放失败: " + err.Error()` | error |
| 51 | beginPlay SetLoop 失败 | `m.lastError = "设置循环失败: " + err.Error()` | error |
| 52 | cycleMode SetLoop 失败 | `m.lastError = "设置循环失败: " + err.Error()` | error |

代表性改写示例（#1 plLoadMsg、#3 plCreateMsg、#26 ytSyncDoneMsg 失败分支、#40 plPicker）：

```go
	// #1 plLoadMsg 不存在分支
	if !exists {
		m, cmd := m.showToast("播放列表「"+msg.name+"」不存在", toastError)
		return m, cmd
	}

	// #3 plCreateMsg（整体改写：清除行删除，失败分支先 showToast 再刷列表）
	case plCreateMsg:
		if _, err := m.pl.Create(msg.name); err != nil {
			m, cmd := m.showToast("新建播放列表失败: "+err.Error(), toastError)
			m.plPage = m.plPage.setLists(m.pl.Lists())
			return m, cmd
		}
		m.plPage = m.plPage.exitNaming() // 成功退出命名输入；失败保留输入便于修改
		m.plPage = m.plPage.setLists(m.pl.Lists())
		return m, nil

	// #26 ytSyncDoneMsg 失败分支
	if msg.err != nil {
		m, cmd := m.showToast("同步失败: "+msg.err.Error(), toastError)
		m.plPage = m.plPage.setLists(m.pl.Lists())
		m.plPage = m.plPage.setYTSyncs(m.yt.SyncEntries())
		return m, cmd
	}

	// #40 plPicker 关闭分支（picker 的 cmd 与 toast cmd 合并，防丢退出输入等命令）
	if picker.closed {
		if picker.notice != "" {
			m2, tcmd := m.showToast(picker.notice, toastSuccess)
			m = m2
			cmd = tea.Batch(cmd, tcmd) // Batch 跳过 nil
		}
		m.plPicker = nil
		m.plPage = m.plPage.setLists(m.pl.Lists())
		return m, cmd
	}
	m.plPicker = &picker
	return m, cmd
```

注意 #26/#28/#31/#32 等分支中，showToast 之后还要执行 `m.plPage = ...` 等收尾代码（用新 m 继续）；其余 case 里 `m, cmd := ...; return m, cmd` 即可。

- [ ] **Step 5: beginPlay 成功清除 toast**

原文（beginPlay 中 `m.state = model.PlaybackState{...}` 之后）：`m.lastError = ""`
新文：`m.toast = nil // 新曲目开始 = 新状态：清除活跃 toast`

（beginPlay 失败路径随后 showToast 会重新设置；顺序为先清后设，保持原语义。）

- [ ] **Step 6: 编译检查**

Run: `cd /data/code/music-tui/.worktrees/toast && go build ./...`
Expected: 编译通过（root_test.go 此时编译失败属预期，Task 4 修复）

---

### Task 3: View 渲染（状态栏 + toast 浮层）与 setSize

**Files:**
- Modify: `ui/root.go`

- [ ] **Step 1: 重写 View**（删除横幅逻辑，追加状态栏与 toast 覆盖）

原文 View 中横幅块（`if m.lastError != "" || m.notice != "" { ... }` 整块）删除，返回语句改为：

```go
	out := m.tabBar() + "\n" + body + "\n" + m.statusBarView()
	return m.overlayToast(out)
```

新 View 全文（注意函数上方注释同步更新）：

```go
// View 渲染当前页面（选择器打开时全屏替换），底部附常驻状态栏；
// 活跃 toast 覆盖在状态栏上方一行的右端（不参与布局，行数不变）。
func (m Model) View() string {
	var body string
	if m.plPicker != nil {
		body = m.plPicker.view()
	} else {
		switch m.current {
		case pageHome:
			body = m.home.view()
		case pageQueue:
			body = m.queuePage.view()
		case pagePlaylists:
			body = m.plPage.view()
		case pageSearch:
			body = m.searchPage.view()
		case pageHistory:
			body = m.historyPage.view()
		}
	}
	out := m.tabBar() + "\n" + body + "\n" + m.statusBarView()
	return m.overlayToast(out)
}
```

- [ ] **Step 2: 新增 statusBarView / toastText / overlayToast 方法**（放在 View 之后）

```go
// statusBarView 底部常驻状态栏（恒 1 行，布局稳定）：左 = 播放状态 + 模式 +
// 队列位置；右 = 当前曲目标题（截断）。toast 覆盖在其上方一行的右端。
func (m Model) statusBarView() string {
	left := "⏹ 未在播放"
	if m.state.Track != nil {
		icon := "⏵"
		if !m.state.Playing {
			icon = "⏸"
		}
		pos := 0
		if m.queue.CurrentIndex() >= 0 {
			pos = m.queue.CurrentIndex() + 1
		}
		left = fmt.Sprintf("%s %s · %d/%d", icon, modeName(m.queue.Mode()), pos, m.queue.Len())
	}
	right := ""
	if m.state.Track != nil {
		right = ansi.Truncate(m.state.Track.Title+" - "+m.state.Track.Artist, m.width/2, "…")
	}
	style := lipgloss.NewStyle().Faint(true)
	if m.width <= 0 {
		return style.Render(left)
	}
	rightW := ansi.StringWidth(style.Render(right))
	pad := m.width - ansi.StringWidth(style.Render(left)) - rightW
	if pad < 0 {
		pad = 0
	}
	return style.Render(left) + strings.Repeat(" ", pad) + style.Render(right)
}

// toastText 按类型渲染 toast 文案（图标 + 颜色，与 lipgloss 主题一致）。
func (m Model) toastText(t toast) string {
	switch t.kind {
	case toastError:
		return lipgloss.NewStyle().Foreground(lipgloss.Color("9")).Render("⚠ " + t.text)
	case toastWarning:
		return lipgloss.NewStyle().Foreground(lipgloss.Color("11")).Render("⚠ " + t.text)
	case toastSuccess:
		return lipgloss.NewStyle().Foreground(lipgloss.Color("10")).Render("✔ " + t.text)
	default:
		return lipgloss.NewStyle().Faint(true).Render("ℹ " + t.text)
	}
}

// overlayToast 把活跃 toast 覆盖到完整输出（tabBar+body+statusBar）中状态栏
// 上方一行的右端：行数不变、其余内容不变 → 出现/消失排版零跳动。
// 无 toast 或行数不足时原样返回。超宽 toast 按窗口宽度截断。
func (m Model) overlayToast(out string) string {
	if m.toast == nil || out == "" {
		return out
	}
	lines := strings.Split(out, "\n")
	if len(lines) < 2 {
		return out
	}
	idx := len(lines) - 2 // 状态栏上方一行
	text := m.toastText(*m.toast)
	tw := ansi.StringWidth(text)
	if m.width > 0 && tw > m.width {
		text = ansi.Truncate(text, m.width, "…")
		tw = ansi.StringWidth(text)
	}
	keep := m.width - tw - 2
	if keep < 0 {
		keep = 0
	}
	line := ""
	if keep > 0 {
		line = ansi.Truncate(lines[idx], keep, "")
	}
	lines[idx] = line + "  " + text
	return strings.Join(lines, "\n")
}
```

- [ ] **Step 3: setSize 高度适配**（`case tea.WindowSizeMsg:`）

原文：

```go
	case tea.WindowSizeMsg:
		// 顶部 Tab 栏 + 分隔线占 2 行，页面高度相应减 2
		m.width = msg.Width
		m.home = m.home.setSize(msg.Width, msg.Height-2)
		m.searchPage = m.searchPage.setSize(msg.Width, msg.Height-2)
		m.historyPage = m.historyPage.setSize(msg.Width, msg.Height-2)
		m.queuePage = m.queuePage.setSize(msg.Width, msg.Height-2)
		m.plPage = m.plPage.setSize(msg.Width, msg.Height-2)
		if m.plPicker != nil {
			picker := m.plPicker.setSize(msg.Width, msg.Height-2)
			m.plPicker = &picker
		}
		return m, nil
```

新文：

```go
	case tea.WindowSizeMsg:
		// 顶部 Tab 栏 + 分隔线占 2 行、底部状态栏占 1 行，页面高度相应减 3
		m.width = msg.Width
		m.home = m.home.setSize(msg.Width, msg.Height-3)
		m.searchPage = m.searchPage.setSize(msg.Width, msg.Height-3)
		m.historyPage = m.historyPage.setSize(msg.Width, msg.Height-3)
		m.queuePage = m.queuePage.setSize(msg.Width, msg.Height-3)
		m.plPage = m.plPage.setSize(msg.Width, msg.Height-3)
		if m.plPicker != nil {
			picker := m.plPicker.setSize(msg.Width, msg.Height-3)
			m.plPicker = &picker
		}
		return m, nil
```

- [ ] **Step 4: 编译检查**

Run: `go build ./...`
Expected: 编译通过（root_test.go 编译失败仍属预期）

- [ ] **Step 5: Commit**

```bash
git add ui/root.go
git status
git commit -m "feat(ui): 错误/成功提示统一走 toast 通道 + 底部常驻状态栏（布局零跳动）"
```

---

### Task 4: root_test.go 更新与新测试

**Files:**
- Modify: `ui/root_test.go`
- Modify: `ui/toast_test.go`（可选：追加集成用例，亦可放 root_test.go）

- [ ] **Step 1: 新增测试 helper**（root_test.go 中 `update` 函数附近）

```go
// activeToastText 返回当前活跃 toast 的文本（无 toast 时返回空串）。测试断言用。
// （注意与 Model 方法 toastText(t toast) 区分：后者渲染样式文本。）
func activeToastText(m Model) string {
	if m.toast == nil {
		return ""
	}
	return m.toast.text
}
```

- [ ] **Step 2: 现有 lastError/notice 断言更新**（机械替换，约 13 处）

规则：
- `strings.Contains(m.lastError, "X")` → `strings.Contains(activeToastText(m), "X")`
- `t.Errorf("lastError = %q, ...", m.lastError)` → `t.Errorf("toast = %q, ...", activeToastText(m))`
- 直接赋值 `m.lastError = "..."` / `m.notice = "..."`（测试构造场景）→ `m, _ = m.showToast("...", toastError/toastSuccess)`（kind 按语义：错误=error，成功提示=success）
- 若断言中同时检查多个字段（如 `m.lastError` 与 `m.notice` 成对出现），逐个按上表处理。

代表性示例（TestPlayFailureShowsError 附近）：

```go
	if !strings.Contains(activeToastText(m), "播放失败") {
		t.Errorf("toast = %q, want 含“播放失败”", activeToastText(m))
	}
```

- [ ] **Step 3: 重写横幅回归测试** `TestRootViewBannerStaysWithinHeight`（`ui/root_test.go:1539` 起）

整体替换为：

```go
// TestRootViewToastLayoutStable 回归：toast 与状态栏不得改变 View 行数、不得替换
// 或挤压页面内容——错误提示出现/消失排版零跳动（旧横幅替换中间区末行曾致内容跳动）。
func TestRootViewToastLayoutStable(t *testing.T) {
	m := newTestModel(t, newFakePlayer(), &fakeSearchAdapter{}, nil)
	m, _ = update(m, tea.WindowSizeMsg{Width: 80, Height: 24})

	plain := m.View()
	if got := len(strings.Split(plain, "\n")); got != 24 {
		t.Fatalf("空态无 toast View 行数 = %d, want 24", got)
	}
	if last := strings.Split(plain, "\n")[23]; !strings.Contains(last, "未在播放") {
		t.Errorf("空态状态栏应在末行, got %q", last)
	}

	m, _ = m.showToast("恢复播放失败: 测试错误", toastError)
	withToast := m.View()
	if got := len(strings.Split(withToast, "\n")); got != 24 {
		t.Errorf("有 toast 时 View 行数 = %d, want 24", got)
	}
	if !strings.Contains(withToast, "⚠") || !strings.Contains(withToast, "恢复播放失败") {
		t.Error("View 应包含错误 toast")
	}
	// 除状态栏上方一行（toast 覆盖区）外，其余行与无 toast 时逐行相同
	p, wt := strings.Split(plain, "\n"), strings.Split(withToast, "\n")
	for i := range p {
		if i == 22 { // 覆盖区
			continue
		}
		if p[i] != wt[i] {
			t.Errorf("第 %d 行被 toast 改变:\n无 toast: %q\n有 toast: %q", i, p[i], wt[i])
		}
	}
	if !strings.Contains(wt[22], "恢复播放失败") {
		t.Errorf("toast 应覆盖在状态栏上方一行（倒数第 2 行）, got %q", wt[22])
	}

	// 播放态
	fp := newFakePlayer()
	m2 := newTestModel(t, fp, &fakeSearchAdapter{}, nil)
	m2, cmd := m2.startPlay(testTrack("t1"))
	_ = execCmds(cmd)
	m2, _ = update(m2, tea.WindowSizeMsg{Width: 80, Height: 24})
	if got := len(strings.Split(m2.View(), "\n")); got != 24 {
		t.Errorf("播放态 View 行数 = %d, want 24", got)
	}
	m2, _ = m2.showToast("播放失败: 测试错误", toastError)
	l2 := strings.Split(m2.View(), "\n")
	if !strings.Contains(l2[22], "播放失败") {
		t.Errorf("播放态 toast 应覆盖在状态栏上方一行, got %q", l2[22])
	}
	if !strings.Contains(l2[23], "顺序") {
		t.Errorf("播放态状态栏应含模式信息, got %q", l2[23])
	}
}
```

- [ ] **Step 4: 新增 toast 生命周期集成测试**（root_test.go，`TestRootViewToastLayoutStable` 之后）

```go
// TestToastLifecycle 集成：showToast 覆盖语义 + 过期消息 id 匹配/不匹配。
func TestToastLifecycle(t *testing.T) {
	m := newTestModel(t, newFakePlayer(), &fakeSearchAdapter{}, nil)
	m, _ = m.showToast("错误 A", toastError)
	if m.toast == nil || m.toast.text != "错误 A" || m.toast.kind != toastError {
		t.Fatalf("showToast 后应显示 toast A, got %+v", m.toast)
	}
	m, _ = m.showToast("错误 B", toastWarning)
	if m.toast == nil || m.toast.text != "错误 B" || m.toast.kind != toastWarning {
		t.Fatalf("覆盖后应显示 toast B, got %+v", m.toast)
	}
	// 旧 toast 的过期消息（id=1）不应清掉新 toast
	m, _ = update(m, toastExpireMsg{id: 1})
	if m.toast == nil || m.toast.text != "错误 B" {
		t.Fatalf("过期消息 id 不匹配不应清除新 toast, got %+v", m.toast)
	}
	// 当前 toast 的过期消息应清除
	m, _ = update(m, toastExpireMsg{id: m.toast.id})
	if m.toast != nil {
		t.Fatalf("过期消息 id 匹配应清除 toast, got %+v", m.toast)
	}
}

// TestShowToastTickCmd 校验 showToast 返回的 cmd 产生匹配 id 的过期消息
//（用 execCmds 执行；时长调小避免测试等待）。
func TestShowToastTickCmd(t *testing.T) {
	toastErrorDuration = time.Millisecond
	defer func() { toastErrorDuration = 5 * time.Second }()
	m := newTestModel(t, newFakePlayer(), &fakeSearchAdapter{}, nil)
	m, cmd := m.showToast("播放失败: 测试", toastError)
	msgs := execCmds(cmd)
	found := false
	for _, msg := range msgs {
		if em, ok := msg.(toastExpireMsg); ok && em.id == m.toast.id {
			found = true
		}
	}
	if !found {
		t.Errorf("showToast 的 cmd 应产生匹配当前 toast id 的过期消息, got %#v", msgs)
	}
}
```

（`TestShowToastTickCmd` 需要 `import "time"`——检查 root_test.go 是否已导入，未导入则添加。）

- [ ] **Step 5: 修复受 Height-3 影响的其他测试**

Run: `go test ./ui/`
Expected: 若有失败，逐个排查——失败来源应为"页面高度从 H-2 变 H-3 导致的布局断言"，按新高度修正断言（如进度条宽度/歌词位置类断言若直接依赖 root 的 WindowSizeMsg 传参，则调整期望值；home_test 等直接调用 `setSize(w, h)` 的测试不受影响）。**不得放宽不超屏断言**（View 行数 ≤ 窗口高度恒成立）。

- [ ] **Step 6: 全量 ui 测试**

Run: `go test ./ui/ -count=1`
Expected: PASS

- [ ] **Step 7: Commit**

```bash
git add ui/root_test.go ui/toast_test.go
git status
git commit -m "test(ui): toast/状态栏测试（布局稳定 + 生命周期 + 定时器 cmd）"
```

---

### Task 5: 全量验证

- [ ] **Step 1: 全量验证**

Run:
```bash
cd /data/code/music-tui/.worktrees/toast
go build ./...
go vet ./...
go test ./... -race -count=1
```
Expected: 全绿（build 无输出、vet 无告警、test 全 PASS）

- [ ] **Step 2: git 状态检查与收尾 commit**

```bash
git status   # 确认只有计划内文件
git add docs/superpowers/plans/2026-08-15-toast-notification.md
git commit -m "docs: toast 通知实现计划"
git log --oneline -5
```

- [ ] **Step 3: 汇报**

汇报内容：改动文件清单、测试结果（含 -race）、commit 列表、以及真实终端手动验收步骤（播放失败触发 toast → 3-5s 自动消失 → 排版零跳动 → 状态栏恒在）。
