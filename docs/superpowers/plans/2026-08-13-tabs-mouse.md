# Tab 栏鼠标交互（点击切换 + hover 高亮）Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 为 music-tui 顶部 Tab 标签栏增加鼠标交互：点击标签切换页面 + 悬停（hover）下划线高亮。

**Architecture:** `main.go` 的 `tea.NewProgram` 启用 `tea.WithMouseAllMotion()`（hover 需要无按钮移动事件）；`ui/root.go` 的 Update 增加 `tea.MouseMsg` 分支（onMouse）：首行（Y==0，bubbletea X/Y 为 0-based）命中标签列区间 → 点击（MouseActionPress+MouseButtonLeft）切换页面、移动（MouseActionMotion）更新 hoverTab；非首行事件不拦截（delegate 给页面，bubbles 列表/歌词区原生获得滚轮滚动与点击选择）。`ui/tabs.go` 重构出 `tabSegments()`（tabBar 渲染与 tabHitAt 命中检测共用），新增 `tabHoverStyle`（Underline）。

**Tech Stack:** Go 1.25、bubbletea v1.3.10（MouseMsg.X/Y 0-based；MouseActionPress/MouseActionMotion/MouseButtonLeft 新 API）、lipgloss v1.1.0、`github.com/charmbracelet/x/ansi`（StringWidth 计算可见宽度，中文 2 列）

**前置状态（已合并 master）：** Tab 栏基础（四标题+高亮+队列数量+播放状态图标）在 ui/tabs.go：`tabStyle`（Bold+212 粉，当前页）、`tabInactiveStyle`（Faint）、`homeTabLabel()`、`queueTabLabel()`、`tabBar()`；root.go `switchPage(key)` 处理 Tab/1/2/3/4。ui 包测试有 `TestMain` 强制 `lipgloss.SetColorProfile(termenv.TrueColor)`（非 TTY 也能断言 ANSI 样式），测试辅助 `newTestModel/update/execCmds/fakePlayer` 在 root_test.go。

**已确认细节（用户拍板）：**
- 点击标签（按下即响应）→ 切换页面；点击 Tab 栏空白/分隔、非首行 → 不拦截不切换
- hover 高亮：非当前页标签悬停显示下划线（`Underline(true)`）；当前页始终 Bold+212（高亮优先于 hover）
- 鼠标移出 Tab 栏（motion 事件 Y!=0）→ 清除 hover
- 启用鼠标后列表/歌词区的滚轮滚动、点击选择为 bubbles 原生增强，接受

---

### Task 1: 鼠标点击 + hover 高亮（TDD）

**Files:**
- Modify: `main.go`（NewProgram 加 `tea.WithMouseAllMotion()`）
- Modify: `ui/tabs.go`（tabHoverStyle + tabSegments 重构 + tabHitAt）
- Modify: `ui/root.go`（Model.hoverTab 字段 + Update MouseMsg 分支 + onMouse + NewModel 初始化）
- Test: `ui/tabs_test.go`（新增 5 个测试）
- Modify: `docs/superpowers/specs/2026-08-13-music-tui-design.md`（第 7 章 Tab 栏小节补鼠标说明）

- [ ] **Step 1: 写失败测试（追加到 `ui/tabs_test.go` 末尾）**

```go
// ---- 鼠标交互（点击切换 + hover 高亮） ----

// 固定状态下的标签文本（无曲目 + 队列 3 首），与 tabBar 分隔约定（2 空格）一致。
// 用 ansi.StringWidth 独立计算各标签 0-based 起始列，避免与实现共享内部函数。
func mouseTabCols() []struct {
	text string
	col  int
	want page
} {
	labels := []struct {
		text string
		want page
	}{
		{"⏹ 首页", pageHome},
		{"搜索", pageSearch},
		{"历史", pageHistory},
		{"队列 (3)", pageQueue},
	}
	col := 0
	for i := range labels {
		if i > 0 {
			col += 2 // 标签间分隔
		}
		labels[i].col = col
		col += ansi.StringWidth(labels[i].text)
	}
	return labels
}

// 点击每个标签（按下即响应）应切到对应页；点击当前页标签幂等。
func TestMouseClickSwitchesPage(t *testing.T) {
	fp := newFakePlayer()
	m := newTestModel(t, fp, &fakeSearchAdapter{}, nil)
	for _, id := range []string{"q1", "q2", "q3"} {
		m, _ = update(m, trackAppendMsg{track: testTrack(id)})
	}
	for _, lb := range mouseTabCols() {
		click := lb.col + 1 // 标签内部一列（0-based）
		m2, _ := update(m, tea.MouseMsg{
			Action: tea.MouseActionPress, Button: tea.MouseButtonLeft, X: click, Y: 0,
		})
		if m2.current != lb.want {
			t.Errorf("点击 %q (x=%d) 后 current = %v, want %v", lb.text, click, m2.current, lb.want)
		}
	}
}

// 点击标签间分隔不应切换页面。
func TestMouseClickOnSeparatorIgnored(t *testing.T) {
	fp := newFakePlayer()
	m := newTestModel(t, fp, &fakeSearchAdapter{}, nil)
	seps := mouseTabCols()
	for i := 1; i < len(seps); i++ {
		// 上一标签末尾与下一标签起始之间的两个分隔格
		for _, x := range []int{seps[i].col - 2, seps[i].col - 1} {
			m2, _ := update(m, tea.MouseMsg{
				Action: tea.MouseActionPress, Button: tea.MouseButtonLeft, X: x, Y: 0,
			})
			if m2.current != pageHome {
				t.Errorf("点击分隔 (x=%d) 不应切页, current = %v", x, m2.current)
			}
		}
	}
}

// 点击非首行、或 X 超出最后一个标签 → 不切页。
func TestMouseClickOutsideTabBarIgnored(t *testing.T) {
	fp := newFakePlayer()
	m := newTestModel(t, fp, &fakeSearchAdapter{}, nil)
	m2, _ := update(m, tea.MouseMsg{
		Action: tea.MouseActionPress, Button: tea.MouseButtonLeft, X: 0, Y: 1, // 第二行
	})
	if m2.current != pageHome {
		t.Error("点击非首行不应切页")
	}
	m2, _ = update(m, tea.MouseMsg{
		Action: tea.MouseActionPress, Button: tea.MouseButtonLeft, X: 500, Y: 0, // 超界
	})
	if m2.current != pageHome {
		t.Error("点击超界位置不应切页")
	}
}

// 悬停非当前页标签 → 下划线高亮；移出 Tab 栏 → 清除。
func TestMouseHoverHighlightsTab(t *testing.T) {
	fp := newFakePlayer()
	m := newTestModel(t, fp, &fakeSearchAdapter{}, nil)
	seps := mouseTabCols()
	x := seps[1].col + 1 // 悬停"搜索"标签
	m2, _ := update(m, tea.MouseMsg{Action: tea.MouseActionMotion, X: x, Y: 0})
	if m2.hoverTab != int(pageSearch) {
		t.Errorf("hoverTab = %d, want %d", m2.hoverTab, int(pageSearch))
	}
	if !strings.Contains(m2.View(), tabHoverStyle.Render("搜索")) {
		t.Error("悬停的标签应显示下划线高亮")
	}
	// 鼠标移出 Tab 栏 → 清除 hover
	m3, _ := update(m2, tea.MouseMsg{Action: tea.MouseActionMotion, X: x, Y: 5})
	if m3.hoverTab != -1 {
		t.Errorf("移出后 hoverTab = %d, want -1", m3.hoverTab)
	}
	if strings.Contains(m3.View(), tabHoverStyle.Render("搜索")) {
		t.Error("移出后不应再有下划线高亮")
	}
}

// 悬停当前页标签 → 保持 tabStyle（当前页高亮优先于 hover）。
func TestMouseHoverOnCurrentTabKeepsTabStyle(t *testing.T) {
	fp := newFakePlayer()
	m := newTestModel(t, fp, &fakeSearchAdapter{}, nil)
	m2, _ := update(m, tea.MouseMsg{Action: tea.MouseActionMotion, X: 1, Y: 0}) // 悬停首页
	if !strings.Contains(m2.View(), tabStyle.Render("⏹ 首页")) {
		t.Error("悬停当前页应保持 tabStyle（高亮优先于 hover）")
	}
}
```

- [ ] **Step 2: 运行测试确认失败**

Run: `export PATH=$HOME/go-sdk/go/bin:$PATH && cd /data/code/music-tui/.worktrees/tabs-mouse && go test ./ui/ -run 'TestMouse' -v`
Expected: 全部 FAIL（`tabHoverStyle` 未定义、`hoverTab` 字段不存在、`tea.MouseMsg` 未处理）

- [ ] **Step 3: 修改 `ui/tabs.go`**

新增 `tabHoverStyle`、`tabSeg`、`tabSegments()`、`tabHitAt()`，`tabBar()` 改为基于 `tabSegments()`：

```go
// tabStyle 当前页标签样式：加粗 + 粉色高亮（与歌词高亮行/队列当前曲目一致）。
var tabStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("212"))

// tabInactiveStyle 非当前页标签样式：弱化。
var tabInactiveStyle = lipgloss.NewStyle().Faint(true)

// tabHoverStyle 鼠标悬停的非当前页标签样式：下划线提示（当前页高亮优先）。
var tabHoverStyle = lipgloss.NewStyle().Underline(true)

// tabSeg 描述 Tab 栏一个标签的渲染信息（tabBar 渲染与 tabHitAt 命中检测共用）。
type tabSeg struct {
	page  page
	label string
	style lipgloss.Style
	col   int // 0-based 起始列（与 bubbletea MouseMsg.X 同基准）
	width int // 渲染后可见宽度（ANSI 剥离，中文按 2 列）
}

// tabSegments 计算四个标签的渲染信息。
// 注意：labels 顺序必须与 page 枚举 iota 顺序一致（pageHome..pageQueue = 0..3），
// 调换顺序须同步调整枚举，否则高亮与鼠标命中会错位。
func (m Model) tabSegments() []tabSeg {
	labels := []string{m.homeTabLabel(), "搜索", "历史", m.queueTabLabel()}
	segs := make([]tabSeg, 0, len(labels))
	col := 0
	for i, label := range labels {
		if i > 0 {
			col += 2 // 标签间分隔宽度
		}
		style := tabInactiveStyle
		switch {
		case page(i) == m.current:
			style = tabStyle
		case i == m.hoverTab:
			style = tabHoverStyle
		}
		segs = append(segs, tabSeg{
			page:  page(i),
			label: label,
			style: style,
			col:   col,
			width: ansi.StringWidth(style.Render(label)),
		})
		col += segs[len(segs)-1].width
	}
	return segs
}

// tabBar 渲染顶部标签栏：四页标题 + 当前页高亮 + 首页播放状态图标 +
// 队列数量标记 + 悬停下划线。纯函数（无状态），由 View 拼在页面内容上方。
func (m Model) tabBar() string {
	var sb strings.Builder
	for i, seg := range m.tabSegments() {
		if i > 0 {
			sb.WriteString("  ")
		}
		sb.WriteString(seg.style.Render(seg.label))
	}
	return sb.String()
}

// tabHitAt 返回点击列 x（0-based，同 bubbletea MouseMsg.X）命中的标签页；
// 命中标签间分隔/空白时返回 false。
func (m Model) tabHitAt(x int) (page, bool) {
	for _, seg := range m.tabSegments() {
		if x >= seg.col && x < seg.col+seg.width {
			return seg.page, true
		}
	}
	return 0, false
}
```

（需在 import 块追加 `"github.com/charmbracelet/x/ansi"`；`strings`、`fmt`、`lipgloss` 已在。）

- [ ] **Step 4: 修改 `ui/root.go` 三处**

(a) `Model` 结构体（`current` 字段附近）追加：

```go
	state     model.PlaybackState
	current   page
	hoverTab  int // Tab 栏悬停标签下标（= page 枚举值）；-1 = 无悬停
	lastError string
```

(b) `NewModel` 的 Model 字面量（`current: pageHome,` 附近）追加：

```go
		current:     pageHome,
		hoverTab:    -1,
```

(c) `Update` 的 `tea.KeyMsg` case 之前追加：

```go
	case tea.MouseMsg:
		return m.onMouse(msg)
```

并在 `switchPage` 之前新增 `onMouse`：

```go
// onMouse 处理鼠标事件：Tab 栏（首行 Y==0，bubbletea X/Y 为 0-based）——
// 点击标签（左键按下）切换页面，移动更新悬停高亮；其余区域事件不拦截，
// 交给当前页面（bubbles 列表/歌词区原生获得滚轮滚动与点击选择）。
func (m Model) onMouse(msg tea.MouseMsg) (Model, tea.Cmd) {
	if msg.Y != 0 {
		// 鼠标不在 Tab 栏：清除悬停高亮，事件交给页面
		if m.hoverTab >= 0 {
			m.hoverTab = -1
		}
		return m.delegate(msg)
	}
	p, ok := m.tabHitAt(msg.X)
	switch msg.Action {
	case tea.MouseActionMotion:
		if ok {
			m.hoverTab = int(p)
		} else {
			m.hoverTab = -1
		}
		return m, nil // 悬停事件不落到页面
	case tea.MouseActionPress:
		m.hoverTab = -1 // 点击后清除悬停
		if msg.Button == tea.MouseButtonLeft && ok {
			m.current = p
		}
		return m, nil
	}
	return m, nil // 其余（释放/滚轮等）在 Tab 栏上不处理
}
```

- [ ] **Step 5: 修改 `main.go`**

```go
	p := tea.NewProgram(model, tea.WithAltScreen(), tea.WithMouseAllMotion())
```

（hover 需要无按钮移动事件，AllMotion；仅点击的话 CellMotion 即可。）

- [ ] **Step 6: 运行测试确认通过**

Run: `export PATH=$HOME/go-sdk/go/bin:$PATH && cd /data/code/music-tui/.worktrees/tabs-mouse && go test ./ui/ -run 'TestMouse' -v`
Expected: 全部 PASS

- [ ] **Step 7: 全量验证**

Run: `go build ./... && go vet ./... && go test ./... -race`
Expected: 全绿（含既有 Tab 栏 6 测试无回归）

- [ ] **Step 8: 设计文档小幅更新**

Modify: `docs/superpowers/specs/2026-08-13-music-tui-design.md` 第 7 章 Tab 栏小节末尾（"- `Tab` / `1` / `2` / `3` / `4` 切换时高亮同步移动" 之后）追加：

```markdown
- 鼠标支持（程序已启用鼠标捕获）：
  - 点击标签切换页面；点击 Tab 栏分隔/空白、或页面区域不拦截（列表/歌词区原生支持滚轮滚动与点击选择）
  - 悬停非当前页标签显示下划线提示，移出 Tab 栏清除；当前页高亮优先
  - 终端文本拖选需按住 Shift（AltScreen 鼠标捕获的通用行为）
```

- [ ] **Step 9: 提交**

```bash
git add main.go ui/tabs.go ui/root.go ui/tabs_test.go docs/superpowers/specs/2026-08-13-music-tui-design.md
git commit -m "feat(ui): Tab 栏鼠标交互——点击切换页面 + 悬停下划线高亮（WithMouseAllMotion）"
```

**注意：commit 前 `git status` 检查，只 add 上述 5 个文件，绝不 `git add -A`；不要改 TODO.md（feature_lead 合并后统一处理）。**

---

## 验收清单（worker 完成后 feature_lead 侧执行）

1. `go build ./...`、`go vet ./...`、`go test ./... -race` 全绿
2. reviewer 审查通过（重点：hoverTab 字段生命周期/零值、MouseMsg 0-based 坐标、delegate 路径）
3. 合并回 master
4. 更新根目录 TODO.md
5. 用户真机确认（点击切换 + 悬停效果 + 列表滚轮可用性）
