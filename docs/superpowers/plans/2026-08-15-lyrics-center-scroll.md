# 歌词区居中滚动排版 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 首页歌词区改为居中滚动排版：首行歌词在视口中央起步、随播放上移、末行停在中央；视口行数动态计算（min(21, 中间区高−上下各 2 行留白)）；resize 自适应。

**Architecture:** 视口内容 = `H/2 行空白 + N 行歌词 + H/2 行空白`（padding 模型），滚动偏移 `YOffset = clamp(当前行, 0, N−1)`，歌词行恒显示在视口中央行。数学等价于参考公式 `top = clamp(idx−H/2, −H/2, N−1−H/2)`（top 为视口顶部行号，可为负）。核心算法抽为纯函数（`ui/lyricscroll.go`），集成到 `ui/home.go` 的 `lyricsHeight`/`scrollLyricsTo`/`rebuildLyrics`/`setSize`。

**Tech Stack:** Go 1.25，charmbracelet/bubbles viewport，testing（TDD 红绿循环）。

**已与用户对齐的决策（2026-08-15）：**
- Q1 留白：上下各 **2 行**（共 4），视口 H = min(21, midH−4)，midH ≤ 4 时 H 至少 1。
- Q2 歌词行数少于视口（N < H）：**方案 A**——不收缩视口，统一 padding 模型，首行恒在视口中央，播放到最后一行也停在中央（滚动 N−1 行）。
- Q3 高亮：当前行恒在视口中央行（H/2）。
- 纯文本态（lyricsPlain）保持现状语义（整页展示 + 手动滚轮），仅应用新动态视口高度公式；N < H 时 viewport.View() 输出 N 行、外层 Place 垂直居中（与现状收缩效果一致）。
- 滚轮手动滚动保留，播放推进时自动重新居中（现有语义）。

---

## 文件结构

- 新建 `ui/lyricscroll.go`：两个纯函数（视口高度、滚动偏移）。
- 新建 `ui/lyricscroll_test.go`：纯函数单测（TDD Task 1 的核心）。
- 修改 `ui/home.go`：`lyricsHeight`、`scrollLyricsTo`、`rebuildLyrics`、`setSize`；删除 `lyricLineCount`；常量 `lyricMaxLines`/`lyricPadLines` 移入 `ui/lyricscroll.go`（home.go 删除原定义）。
- 修改 `ui/home_test.go`：更新 3 个既有断言的 YOffset 期望值（19→29、39→49）；新增集成测试。

**背景知识（worker 必读）：**
- 视口语义：`lyricView` 是 bubbles viewport；`SetContent` 重置内容并把 YOffset clamp 到 `[0, len(内容行)−Height]`；`SetYOffset` 同样 clamp。`View()` 输出 `[YOffset, YOffset+Height)` 行，内容行数不足 Height 时只输出实际行数。
- content 总行数 = H/2 + N + H/2 = N + H ≥ H（N ≥ 1），因此 `View()` 恒输出恰好 H 行，`maxYOffset = N`；`YOffset = clamp(idx, 0, N−1) ≤ N−1` 恒合法。
- 渲染路径：`syncState`（推进）→ `rebuildLyrics`（重建 content，含高亮）→ `scrollLyricsTo(idx)`（设 YOffset）。`setSize`（resize）→ Height 可能变化 → **必须先 rebuild（padding 随 H/2 变）再 scroll**。
- 页面结构：中间区高 `middleHeight() = height−2`；歌词列外层 `lipgloss.Place(lyricsW, midH, Center, Center, ...)` 垂直居中 → 视口外留白 = (midH−H)/2 ≥ 2（H = midH−4 时恰 2，H=21 时 ≥ 8）。
- view 输出行号 = 页面行 + 3（顶部空行 + Tab 栏 + 分隔线）。歌词行 j 在页面内行号 = 3 + (midH−H)/2 + H/2 + (j − YOffset)。

**与 feat/lyrics-ai 的并行重叠（worker 不用管，rebase 时处理）：** 另一会话正在把 feat/lyrics-ai 合入 master（会删除 lyricsPlain 态、lyricsHeight 加 AI 标识减 1 行）。本分支基于 master 现状实现（保留 lyricsPlain）。注意实现时把 `lyricLineCount` 删除干净、`lyricsHeight` 保持简单，减少未来 rebase 冲突面。

---

### Task 1: 纯函数 + 单测（TDD 红绿）

**Files:**
- Create: `ui/lyricscroll.go`
- Test: `ui/lyricscroll_test.go`

- [ ] **Step 1: 写失败测试 `ui/lyricscroll_test.go`**

```go
package ui

import "testing"

// TestLyricViewportHeight 视口行数动态计算：min(21, midH−上下各 2 行留白)，至少 1 行。
func TestLyricViewportHeight(t *testing.T) {
	cases := []struct {
		midH, want int
	}{
		{60, 21}, // 上限 21
		{37, 21}, // min(21, 33)
		{25, 21},
		{24, 20}, // 24−4
		{21, 17},
		{10, 6},
		{6, 2},
		{5, 1}, // 5−4=1
		{4, 1}, // clamp 下限
		{3, 1},
		{1, 1},
	}
	for _, c := range cases {
		if got := lyricViewportHeight(c.midH); got != c.want {
			t.Errorf("lyricViewportHeight(%d) = %d, want %d", c.midH, got, c.want)
		}
	}
}

// TestLyricScrollOffset 滚动偏移：内容 = H/2 空白 + N 歌词 + H/2 空白，
// YOffset = clamp(idx, 0, N−1) → 歌词行 idx 恒显示在视口中央行 H/2。
// 等价验证：显示行 = H/2 + idx − YOffset 必须恒等于 H/2。
func TestLyricScrollOffset(t *testing.T) {
	cases := []struct {
		name       string
		idx, n, h  int
		wantOffset int
	}{
		{"首行中央 N>H", 0, 60, 21, 0},
		{"中间行中央", 29, 60, 21, 29},
		{"末行中央 N>H", 59, 60, 21, 59},
		{"首行中央 N<H", 0, 5, 21, 0},
		{"末行中央 N<H", 4, 5, 21, 4},
		{"单行歌词", 0, 1, 21, 0},
		{"H=1", 0, 5, 1, 0},
		{"H=2", 0, 5, 2, 0},
		{"idx 越界下界", -3, 5, 21, 0},
		{"idx 越界上界", 99, 5, 21, 4},
		{"N=0", 0, 0, 21, 0},
	}
	for _, c := range cases {
		got := lyricScrollOffset(c.idx, c.n, c.h)
		if got != c.wantOffset {
			t.Errorf("%s: lyricScrollOffset(%d,%d,%d) = %d, want %d", c.name, c.idx, c.n, c.h, got, c.wantOffset)
			continue
		}
		// 核心不变量：当前行显示行 = h/2 + idx − offset 恒等于 h/2（视口中央）。
		// 仅对合法 idx 成立（越界 idx 是 clamp 场景，不是真实歌词行）。
		if c.n > 0 && c.idx >= 0 && c.idx < c.n {
			if row := c.h/2 + c.idx - got; row != c.h/2 {
				t.Errorf("%s: 当前行显示行 = %d, want %d（视口中央）", c.name, row, c.h/2)
			}
		}
	}
}
```

- [ ] **Step 2: 运行测试确认失败**

Run: `cd /data/code/music-tui/.worktrees/lyrics-center-scroll && export PATH=$HOME/go-sdk/go/bin:$PATH && go test ./ui/ -run 'TestLyric' -v`
Expected: FAIL — `undefined: lyricViewportHeight`

- [ ] **Step 3: 实现 `ui/lyricscroll.go`**

```go
package ui

// 歌词视口行数常量。
const (
	// lyricMaxLines 歌词视口最大行数：当前行恒居中（上 10 下 10），大窗口不再增大。
	lyricMaxLines = 21
	// lyricPadLines 视口外上下留白行数（窗口较小时保证歌词不贴中间区边缘）。
	lyricPadLines = 2
)

// lyricViewportHeight 歌词视口行数（动态）：min(21, midH−上下留白 2*lyricPadLines)，
// 至少 1 行。窗口大时 21 行封顶；窗口小时按留白收缩（例如 midH=24 → 20 行，
// 视口外上下各 2 行留白）。
func lyricViewportHeight(midH int) int {
	h := midH - lyricPadLines*2
	if h > lyricMaxLines {
		h = lyricMaxLines
	}
	if h < 1 {
		h = 1
	}
	return h
}

// lyricScrollOffset 歌词视口滚动偏移：内容 = H/2 行空白 + N 行歌词 + H/2 行空白，
// 歌词行 idx 在内容中的行号 = H/2 + idx，显示在视口行 = H/2 + idx − YOffset。
// 令其恒等于视口中央行 H/2 → YOffset = idx，clamp 到 [0, N−1]。
// 等价于参考公式（视口顶部行号，可为负）top = clamp(idx−H/2, −H/2, N−1−H/2)：
//
//	开头（idx=0）→ top=−H/2，首行在视口中央，上方整片空白；
//	中间 → top=idx−H/2，当前行恒居中；
//	结尾（idx=N−1）→ top=N−1−H/2，末行停在视口中央，下方可空白。
//
// viewport 侧 maxYOffset = (H/2+N+H/2) − H = N，故返回 N−1 恒合法。
func lyricScrollOffset(idx, n, h int) int {
	if n <= 0 {
		return 0
	}
	off := idx
	if off < 0 {
		off = 0
	}
	if off > n-1 {
		off = n-1
	}
	return off
}
```

- [ ] **Step 4: 运行测试确认通过**

Run: `export PATH=$HOME/go-sdk/go/bin:$PATH && go test ./ui/ -run 'TestLyric' -v`
Expected: PASS（2 个测试）

- [ ] **Step 5: Commit**

> 实现修正：`lyricMaxLines` 常量从 home.go 移入 lyricscroll.go 后，home.go 的
> 常量定义必须在 Task 1 一并删除（同包重名无法编译），因此本 commit 额外
> 包含 home.go 的常量删除（仅删定义行，lyricsHeight 引用自动落到新常量，
> 行为不变）。

```bash
git add ui/lyricscroll.go ui/lyricscroll_test.go ui/home.go
git commit -m "feat(ui): 歌词居中滚动纯函数（视口动态行数 + 中央滚动偏移，TDD）"
```

---

### Task 2: home.go 集成 + 既有测试更新 + 集成测试

**Files:**
- Modify: `ui/home.go`（lyricsHeight、scrollLyricsTo、rebuildLyrics、setSize、常量、删除 lyricLineCount）
- Test: `ui/home_test.go`（更新 3 处 YOffset 断言 + 新增 4 个集成测试）

- [ ] **Step 1: 更新既有测试断言（先改测试，红）**

`ui/home_test.go` 中：

1. `TestHomeLyricsViewport21`（约 857 行）：
   - 第 60 行歌词注释不变；`if got := m.home.lyricView.YOffset; got != 19` → `got != 29`，注释改为 `// LineAt(155)=29 → YOffset=29（padding 模型，当前行恒在视口中央）`。

2. `TestHomeLyricsCurrentLineCentered`（约 857 行）：
   - `第 30 行（0-based 29，155s）：offset = 29 - 10 = 19 → 当前行在视口第 10 行（正中）` → 期望 `19` 改 `29`，注释 `YOffset = 29（内容含 H/2=10 行前导空白，当前行显示在视口第 10 行正中）`。
   - 末段：Position 495 → LineAt = **idx 59**（305s ≤ 495，不是注释所说的 49）→ 期望 `39` 改 `59`，注释 `YOffset = clamp(59, 0, N−1=59) = 59（padding 模型末行停中央；旧模型 59−10=49 被 viewport clamp 到 39 贴底）`。
   - 首行期望 `0` 不变（注释保留）。

3. `TestHomeLyricsCenteredWhenFew` 与 `TestHomeLyricsFillWhenMany`：断言不变（`lyricView.Height` 仍为 21：midH=37 → min(21,33)=21）——但需在 `TestHomeLyricsCenteredWhenFew` 中新增一条断言：视口不再收缩到 5 行（方案 A）：

```go
	// 方案 A：视口不收缩（统一 padding 模型），5 行歌词时首行仍在视口中央
	if got := m.home.lyricView.Height; got != 21 {
		t.Errorf("歌词少时视口高 = %d, want 21（不再收缩）", got)
	}
```

4. `TestHomeLyricsHeightReservesAITag`（计划遗漏，同步修正）：master 已含
   feat/ai-lyrics（AI 来源标识减 1 行逻辑在 lyricsHeight 内），计划未覆盖。
   新公式下该测试断言改为：midH=21 → H=17（非 AI 与 AI 相同，留白模型本身
   保证 AI 标识 1 行 + 视口 17 行 = 18 ≤ midH 不溢出）；AI 少行也不再收缩（方案 A）。

Run: `export PATH=$HOME/go-sdk/go/bin:$PATH && go test ./ui/ -run 'TestHomeLyrics' 2>&1 | tail -20`
Expected: FAIL — YOffset 断言不匹配（红，符合 TDD）

- [ ] **Step 2: 修改 `ui/home.go`**

2a. 删除常量 `lyricMaxLines`（移入 lyricscroll.go，同包无需重复定义）：

```go
- // lyricMaxLines 歌词视口最大行数：当前行恒居中（上 10 下 10）。
- // 窄窗口（中间区高 < 21）时视口收缩到中间区高。
- const lyricMaxLines = 21
```

2b. 删除 `lyricLineCount`（不再收缩视口，无其他调用者）：

```go
- // lyricLineCount 歌词总行数（synced = 同步歌词行数；plain = 纯文本行数；
- // 其他态 = 0）。用于歌词行数少于视口时收缩视口高度实现内容垂直居中。
- func (m homeModel) lyricLineCount() int { ... 整个函数 ... }
```

2c. `lyricsHeight` 改为：

```go
// lyricsHeight 歌词视口高度：动态行数 min(21, 中间区高−上下各 2 行留白)，
// 至少 1 行（见 lyricViewportHeight）。synced 态视口恒为 H（padding 模型，
// 不随歌词行数收缩）；plain 态内容不足 H 时 viewport.View() 只输出实际行数，
// 外层 Place 垂直居中——与旧的"收缩视口"视觉效果一致。
func (m homeModel) lyricsHeight() int {
	return lyricViewportHeight(m.middleHeight())
}
```

2d. `rebuildLyrics` 加 padding：

```go
// rebuildLyrics 用当前高亮行重渲染歌词内容：内容 = H/2 行空白 + 歌词行
// + H/2 行空白（padding 模型，配合 scrollLyricsTo 使当前行恒在视口中央；
// H 变化后必须重调本函数，padding 行数随 H/2 变化）。
func (m *homeModel) rebuildLyrics() {
	if m.lyrics == nil {
		return
	}
	active := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("212"))
	pad := m.lyricView.Height / 2
	var sb strings.Builder
	sb.WriteString(strings.Repeat("\n", pad))
	for i, line := range m.lyrics.Lines {
		text := line.Text
		if i == m.currentLine {
			text = active.Render(text)
		}
		sb.WriteString(text)
		sb.WriteString("\n")
	}
	sb.WriteString(strings.Repeat("\n", pad))
	m.lyricView.SetContent(strings.TrimSuffix(sb.String(), "\n"))
}
```

2e. `scrollLyricsTo` 改为：

```go
// scrollLyricsTo 让当前行保持在歌词区视口中央（padding 模型：YOffset = 当前行）。
// 开头首行在中央（上方整片空白）、结尾末行停中央（下方可空白）；行数少时
// 同样滚动 N−1 行，首末行都在中央。
func (m *homeModel) scrollLyricsTo(idx int) {
	if m.lyricView.Height <= 0 || m.lyrics == nil {
		return
	}
	m.lyricView.SetYOffset(lyricScrollOffset(idx, len(m.lyrics.Lines), m.lyricView.Height))
}
```

2f. `setSize` 中 Height 变化后先重建 padding 内容再重算偏移：

```go
	// 视口高度可能已变化（留白/上限动态计算），先重建 padding 内容再重算
	// 滚动偏移：此前基于未知尺寸（Height=1）的 scrollLyricsTo 会留下越界
	// YOffset，导致歌词首行被吞（回归：TestHomeLyricsCenteredWhenFew）。
	if m.lyricsState == lyricsSynced && m.lyrics != nil {
		m.rebuildLyrics()
		if m.currentLine >= 0 {
			m.scrollLyricsTo(m.currentLine)
		}
	}
```

（替换现有 `if m.lyricsState == lyricsSynced && m.currentLine >= 0 { m.scrollLyricsTo(m.currentLine) }` 块；`setSize` 中 `m.lyricView.Height = m.lyricsHeight()` 行不变。）

- [ ] **Step 3: 跑既有歌词测试，确认绿**

Run: `export PATH=$HOME/go-sdk/go/bin:$PATH && go test ./ui/ -run 'TestHomeLyrics|TestHomeResize|TestHomeView' -v 2>&1 | tail -30`
Expected: 全部 PASS

- [ ] **Step 4: 新增集成测试（追加到 `ui/home_test.go` 末尾）**

```go
// TestHomeLyricsFirstAndLastLineCentered 核心需求：首行歌词起步于视口中央、
// 末行歌词停在视口中央（N > H 长歌词）。
// 坐标修正：lines 来自 m.home.view()（不含顶部 3 行），centerRow 用 view 内
// 坐标 = 留白 + H/2，不再加 3（计划初版误用页面坐标，恒差 3 行）。
func TestHomeLyricsFirstAndLastLineCentered(t *testing.T) {
	fp := newFakePlayer()
	m := newTestModel(t, fp, &fakeSearchAdapter{}, nil)
	m, cmd := m.startPlay(testTrack("t1"))
	_ = execCmds(cmd)

	var sb strings.Builder
	for i := 0; i < 60; i++ {
		sb.WriteString(fmt.Sprintf("[%02d:%02d.00]歌词行", (10+i*5)/60, (10+i*5)%60))
		sb.WriteString(string(rune('A' + i%26)))
		sb.WriteString("\n")
	}
	ly, _ := lyrics.ParseLRC([]byte(sb.String()))
	m, _ = update(m, lyricsResultMsg{trackID: "t1", lyrics: ly})
	m.home = m.home.setSize(120, 39) // midH=37 → H=21，视口外留白 (37-21)/2=8

	// 视口中央行（view 内坐标，页面行 = view 行 + 3 顶部行）：
	// = 8（留白 (37-21)/2）+ 10（H/2）= 18
	centerRow := (37-21)/2 + 10

	// 首行（10s 内 idx=0）：首行歌词显示在中央行
	m, _ = update(m, playerEventMsg{ev: player.ProgressEvent{Position: 12, Duration: 2000}})
	if got := m.home.lyricView.YOffset; got != 0 {
		t.Fatalf("首行 YOffset = %d, want 0", got)
	}
	lines := strings.Split(m.home.view(), "\n")
	firstRow := -1
	for i, ln := range lines {
		if strings.Contains(stripAnsiForTest(ln), "歌词行A") {
			firstRow = i
			break
		}
	}
	if firstRow != centerRow {
		t.Errorf("首行歌词出现在行 %d, want %d（视口中央）", firstRow, centerRow)
	}
	// 中央行上方应为空白（无任何歌词文本）
	for i := 0; i < centerRow; i++ {
		if strings.Contains(stripAnsiForTest(lines[i]), "歌词行") {
			t.Errorf("中央行上方第 %d 行不应有歌词", i)
		}
	}

	// 末行（idx=59）：末行歌词停在中央行
	m, _ = update(m, playerEventMsg{ev: player.ProgressEvent{Position: 495, Duration: 2000}})
	if got := m.home.lyricView.YOffset; got != 59 {
		t.Fatalf("末行 YOffset = %d, want 59", got)
	}
	lines = strings.Split(m.home.view(), "\n")
	lastRow := -1
	for i, ln := range lines {
		if strings.Contains(stripAnsiForTest(ln), "歌词行H") { // idx 59 → 'A'+59%26 = 'H'
			lastRow = i
			break
		}
	}
	if lastRow != centerRow {
		t.Errorf("末行歌词出现在行 %d, want %d（视口中央）", lastRow, centerRow)
	}
}

// TestHomeLyricsFewLinesScroll 方案 A：N < H 时视口不收缩，首行与末行都在
// 视口中央，播放推进滚动 N−1 行。
func TestHomeLyricsFewLinesScroll(t *testing.T) {
	fp := newFakePlayer()
	m := newTestModel(t, fp, &fakeSearchAdapter{}, nil)
	m, cmd := m.startPlay(testTrack("t1"))
	_ = execCmds(cmd)

	ly, _ := lyrics.ParseLRC([]byte("[00:10.00]第一行\n[00:20.00]第二行\n[00:30.00]第三行\n"))
	m, _ = update(m, lyricsResultMsg{trackID: "t1", lyrics: ly})
	m.home = m.home.setSize(120, 39) // H=21 > N=3

	if got := m.home.lyricView.Height; got != 21 {
		t.Fatalf("视口高 = %d, want 21（N<H 不收缩）", got)
	}
	// 首行（idx=0）：YOffset=0，第一行在视口中央
	m, _ = update(m, playerEventMsg{ev: player.ProgressEvent{Position: 12, Duration: 2000}})
	if got := m.home.lyricView.YOffset; got != 0 {
		t.Errorf("首行 YOffset = %d, want 0", got)
	}
	// 末行（idx=2）：YOffset=2，第三行停在视口中央
	m, _ = update(m, playerEventMsg{ev: player.ProgressEvent{Position: 32, Duration: 2000}})
	if got := m.home.lyricView.YOffset; got != 2 {
		t.Errorf("末行 YOffset = %d, want 2", got)
	}
	// 方案 A：N<H 时首末行都在视口中央（view 内坐标 8+10=18）
	centerRow := (37-21)/2 + 10
	lines := strings.Split(m.home.view(), "\n")
	if !strings.Contains(stripAnsiForTest(lines[centerRow]), "第三行") {
		t.Errorf("末行应显示在中央行 %d: %q", centerRow, lines[centerRow])
	}
}

// TestHomeLyricsMinWhitespace 窗口较小时上下留白至少 2 行（视口外），
// 视口行数动态收缩。
func TestHomeLyricsMinWhitespace(t *testing.T) {
	fp := newFakePlayer()
	m := newTestModel(t, fp, &fakeSearchAdapter{}, nil)
	m, cmd := m.startPlay(testTrack("t1"))
	_ = execCmds(cmd)

	var sb strings.Builder
	for i := 0; i < 40; i++ {
		sb.WriteString(fmt.Sprintf("[%02d:%02d.00]歌词行", (10+i*5)/60, (10+i*5)%60))
		sb.WriteString(string(rune('A' + i%26)))
		sb.WriteString("\n")
	}
	ly, _ := lyrics.ParseLRC([]byte(sb.String()))
	m, _ = update(m, lyricsResultMsg{trackID: "t1", lyrics: ly})

	for _, sz := range [][2]int{{80, 22}, {80, 16}, {80, 10}} { // midH = 20, 14, 8
		m.home = m.home.setSize(sz[0], sz[1])
		midH := sz[1] - 2
		wantH := midH - 4
		if wantH > 21 {
			wantH = 21
		}
		if wantH < 1 {
			wantH = 1
		}
		if got := m.home.lyricView.Height; got != wantH {
			t.Errorf("%dx%d 视口高 = %d, want %d", sz[0], sz[1], got, wantH)
		}
		// 歌词列视口上下留白 ≥ 2（外层 Place 垂直居中 + 视口 H = midH−4）
		topPad := 3 + (midH-wantH)/2 // 页面顶 3 行 + 列内上留白
		if topPad < 5 {              // 3 + 2 = 5
			t.Errorf("%dx%d 上留白 = %d, want ≥ 2 行", sz[0], sz[1], topPad-3)
		}
	}
}

// TestHomeLyricsResizeRepads 回归：resize 后 padding 随新视口高重建，
// 首行仍显示在视口中央（此前只重算 YOffset 不重建内容，padding 模型下
// 会残留旧 padding 导致首行偏移）。
func TestHomeLyricsResizeRepads(t *testing.T) {
	fp := newFakePlayer()
	m := newTestModel(t, fp, &fakeSearchAdapter{}, nil)
	m, cmd := m.startPlay(testTrack("t1"))
	_ = execCmds(cmd)

	ly, _ := lyrics.ParseLRC([]byte("[00:10.00]第一行\n[00:20.00]第二行\n[00:30.00]第三行\n[00:40.00]第四行\n[00:50.00]第五行\n"))
	m, _ = update(m, lyricsResultMsg{trackID: "t1", lyrics: ly})
	m, _ = update(m, playerEventMsg{ev: player.ProgressEvent{Position: 12, Duration: 2000}})
	m.home = m.home.setSize(120, 39) // H=21，首行在中央行
	m.home = m.home.setSize(80, 20)  // midH=18 → H=14，padding 7

	if got := m.home.lyricView.Height; got != 14 {
		t.Fatalf("resize 后视口高 = %d, want 14", got)
	}
	centerRow := (18-14)/2 + 7 // view 内坐标：2（留白）+ 7（H/2）= 9（页面行 12）
	lines := strings.Split(m.home.view(), "\n")
	if !strings.Contains(stripAnsiForTest(lines[centerRow]), "第一行") {
		t.Errorf("resize 后首行应显示在新中央行 %d: %q", centerRow, lines[centerRow])
	}
}
```

Run: `export PATH=$HOME/go-sdk/go/bin:$PATH && go test ./ui/ -run 'TestHomeLyrics' -v 2>&1 | tail -40`
Expected: 全部 PASS（含新增 4 个）

- [ ] **Step 5: 全量验证**

```bash
export PATH=$HOME/go-sdk/go/bin:$PATH
go build ./...
go vet ./...
go test ./... -race 2>&1 | tail -15
```

Expected: 全部 ok，无 vet 警告

- [ ] **Step 6: Commit**

```bash
git add ui/home.go ui/home_test.go
git commit -m "feat(ui): 歌词区居中滚动排版——首行中央起步/末行停中央、动态视口行数与上下留白、resize 重建 padding"
```

---

## 验收标准

1. `go build ./...`、`go vet ./...`、`go test ./... -race` 全绿。
2. 纯函数单测覆盖：N<H、N>H、idx=0、idx=N−1、中间、H=1/2、越界 clamp、N=0、留白边界（midH 24/21/10/5/4/3）。
3. 集成测试覆盖：长歌词首末行都在视口中央（页面行 21）、N<H 滚动 N−1 行、小窗口留白 ≥ 2、resize 重建 padding。
4. 不回归：`TestHomeLyricsCenteredWhenFew`/`FillWhenMany`/`Viewport21`/`CurrentLineCentered`/`WheelScroll`/`HorizontallyCentered`/`CenterFallback`/`ResizeRelayout`/`ViewNarrowWindow`/`AISourceTag`/`HeightReservesAITag`（适配后）全过。
