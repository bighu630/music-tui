package ui

import (
	"bytes"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"music-tui/coverrender"
	"music-tui/lyrics"
	"music-tui/player"
)

// lockedWriter 并发安全 io.Writer（overlayOut 捕获用）：sixel 延迟重画 goroutine
// 会异步写 overlayOut，-race 下须与断言读取串行。
type lockedWriter struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (w *lockedWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.b.Write(p)
}

func (w *lockedWriter) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.b.String()
}

func (w *lockedWriter) Reset() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.b.Reset()
}

func (w *lockedWriter) Len() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.b.Len()
}

// ---- 首页封面自适应隐藏（需求：窗口尺寸不足以容纳封面区时隐藏封面） ----
//
// 判断基准（与用户确认）：
//   - OR 语义：窗口宽 < 2×coverW(30)=60 或 窗口高 < coverHideMinH=28 任一不满足即隐藏
//   - 严格小于：恰好等于阈值（60×28）时显示，小于才隐藏
//   - 高度用整个窗口高度（root 经 WindowSizeMsg 注入 windowHeight），非页面高度
//   - 隐藏时封面列移除，歌词区直接占满/屏幕居中；resize 实时生效

// TestCoverHidden 封面隐藏判断纯函数：窗口尺寸 × 阈值的边界矩阵。
func TestCoverHidden(t *testing.T) {
	cases := []struct {
		w, h int
		want bool
	}{
		{120, 40, false}, // 宽高都充足
		{60, 28, false},  // 高恰 28（严格小于才隐藏）
		{60, 29, false},  // 高充足、宽恰好
		{61, 28, false},  // 宽充足、高恰好
		{100, 28, false},
		{60, 34, false},  // 高 34（旧 2×coverH）仍充足
		{60, 27, true},   // 高差 1：隐藏
		{59, 28, true},   // 宽差 1：隐藏
		{59, 27, true},   // 宽高都差 1
		{30, 17, true},   // 恰等于封面本身
		{0, 50, true},    // 宽为 0
		{50, 0, true},    // 高为 0
	}
	for _, c := range cases {
		if got := coverHidden(c.w, c.h); got != c.want {
			t.Errorf("coverHidden(%d,%d) = %v, want %v", c.w, c.h, got, c.want)
		}
	}
}

// assertCoverState 断言当前 home 视图状态。hidden=true 时视图不得出现封面
// （占位框 No Cover），且布局行（进度条/按钮行）保持。
func assertCoverState(t *testing.T, label string, home homeModel, hidden bool) {
	t.Helper()
	v := home.view()
	lines := strings.Split(v, "\n")
	if len(lines) != home.height {
		t.Errorf("%s: view 行数 = %d, want %d（隐藏不得破坏整页撑满）", label, len(lines), home.height)
	}
	hasCover := strings.Contains(v, "No Cover")
	if hidden && hasCover {
		t.Errorf("%s: 应隐藏封面, 但视图出现占位框: %q", label, v)
	}
	if !hidden && !hasCover {
		t.Errorf("%s: 应显示封面, 但视图无占位框: %q", label, v)
	}
	// 布局行保持：倒数第 2 行进度条、最后一行按钮行
	if !strings.Contains(lines[len(lines)-2], "●") {
		t.Errorf("%s: 倒数第 2 行应为进度条行: %q", label, lines[len(lines)-2])
	}
	if !strings.Contains(lines[len(lines)-1], "|<") {
		t.Errorf("%s: 最后一行应为按钮行: %q", label, lines[len(lines)-1])
	}
}

// setWindow 模拟窗口 resize：设整窗口高度并调用页面 setSize（与 root
// WindowSizeMsg 处理一致：页面高度 = 窗口高度 - 4，宽度 = 窗口宽度）。
func setWindow(home homeModel, windowW, windowH int) homeModel {
	home.windowHeight = windowH
	return home.setSize(windowW, windowH-4)
}

// TestHomeCoverHiddenOnSmallWindow 首页窗口小于封面框两倍时隐藏封面单元：
// 高度不足 / 宽度不足 / 恰好阈值显示 / 充足显示，布局行恒保持。
func TestHomeCoverHiddenOnSmallWindow(t *testing.T) {
	fp := newFakePlayer()
	m := newTestModel(t, fp, &fakeSearchAdapter{}, nil)
	m, cmd := m.startPlay(testTrack("t1"))
	_ = execCmds(cmd)
	// 占位封面（No Cover）——隐藏语义适用于整个封面区（含占位框）
	m, _ = update(m, lyricsResultMsg{trackID: "t1", err: lyrics.ErrNotFound})

	// 窗口高 26 < 28（页面高 22）：隐藏封面
	m.home = setWindow(m.home, 120, 26)
	assertCoverState(t, "窗口高26", m.home, true)

	// 窗口宽 59 < 60（页面高 36）：隐藏封面
	m.home = setWindow(m.home, 59, 40)
	assertCoverState(t, "窗口宽59", m.home, true)

	// 恰好阈值 60×28：显示封面
	m.home = setWindow(m.home, 60, 28)
	assertCoverState(t, "窗口60x28", m.home, false)

	// 宽高充足 120×40：显示封面
	m.home = setWindow(m.home, 120, 40)
	assertCoverState(t, "窗口120x40", m.home, false)
}

// TestHomeCoverHiddenLyricsCentered 隐藏封面时歌词区直接占满整页宽度并
// 以屏幕中心居中（不再是封面右侧的歌词列）。
func TestHomeCoverHiddenLyricsCentered(t *testing.T) {
	fp := newFakePlayer()
	m := newTestModel(t, fp, &fakeSearchAdapter{}, nil)
	m, cmd := m.startPlay(testTrack("t1"))
	_ = execCmds(cmd)
	ly, err := lyrics.ParseLRC([]byte("[00:10.00]第一行\n[00:20.00]第二行\n"))
	if err != nil {
		t.Fatal(err)
	}
	m, _ = update(m, lyricsResultMsg{trackID: "t1", lyrics: ly})
	m, _ = update(m, playerEventMsg{ev: player.ProgressEvent{Position: 12, Duration: 2000}})

	// 隐藏封面：歌词列宽 = 整页宽
	m.home = setWindow(m.home, 120, 26)
	if got := m.home.lyricsColumnWidth(); got != 120 {
		t.Errorf("隐藏时歌词列宽 = %d, want 120（占满整行）", got)
	}
	if got := m.home.lyricsStartCol(); got != 0 {
		t.Errorf("隐藏时歌词起点列 = %d, want 0", got)
	}
	// 歌词行以屏幕中心居中：中心列 60，行宽 6 → 起点 ≈ 57
	for _, ln := range strings.Split(m.home.view(), "\n") {
		vis := stripAnsiForTest(ln)
		if !strings.Contains(vis, "第一行") {
			continue
		}
		idx := strings.Index(vis, "第一行")
		col := ansi.StringWidth(vis[:idx])
		if got, want := col+3, 60; got < want-2 || got > want+2 {
			t.Errorf("隐藏时歌词行中心列 = %d, want ≈ %d（屏幕中心）", got, want)
		}
	}

	// 显示封面：歌词列回到封面右侧（起点 = coverW+2，宽度 = 整页-34）
	m.home = setWindow(m.home, 120, 40)
	if got := m.home.lyricsColumnWidth(); got != 120-coverW-4 {
		t.Errorf("显示时歌词列宽 = %d, want %d", got, 120-coverW-4)
	}
	if got := m.home.lyricsStartCol(); got != coverW+2 {
		t.Errorf("显示时歌词起点列 = %d, want %d", got, coverW+2)
	}
}

// TestHomeCoverHiddenWheelRegion 隐藏封面时滚轮滚动整页生效：
// 歌词区从屏幕左侧起即可滚动（不再要求 X ≥ coverW+2）。
func TestHomeCoverHiddenWheelRegion(t *testing.T) {
	fp := newFakePlayer()
	m := newTestModel(t, fp, &fakeSearchAdapter{}, nil)
	m, cmd := m.startPlay(testTrack("t1"))
	_ = execCmds(cmd)

	var sb strings.Builder
	for i := 0; i < 40; i++ {
		sb.WriteString("[00:10.00]歌词行A\n")
	}
	ly, _ := lyrics.ParseLRC([]byte(sb.String()))
	m, _ = update(m, lyricsResultMsg{trackID: "t1", lyrics: ly})
	m.home = setWindow(m.home, 120, 26) // 隐藏封面
	m, _ = update(m, playerEventMsg{ev: player.ProgressEvent{Position: 12, Duration: 2000}})
	base := m.home.lyricView.YOffset

	// X=5（封面列区域，隐藏后属于歌词区）滚轮向下也应滚动
	m, _ = update(m, tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonWheelDown, X: 5, Y: 20})
	if got := m.home.lyricView.YOffset; got <= base {
		t.Errorf("隐藏封面后 X=5 滚轮向下 YOffset = %d, want > %d（整页为歌词区）", got, base)
	}
}

// TestHomeCoverHideResizeRealtime resize 实时生效：
// 足够大显示 → 缩小隐藏 → 放大恢复 → 缩小隐藏；缓存/隐藏标志随尺寸刷新。
func TestHomeCoverHideResizeRealtime(t *testing.T) {
	fp := newFakePlayer()
	m := newTestModel(t, fp, &fakeSearchAdapter{}, nil)
	m, cmd := m.startPlay(testTrack("t1"))
	_ = execCmds(cmd)
	m, _ = update(m, lyricsResultMsg{trackID: "t1", err: lyrics.ErrNotFound})

	m.home = setWindow(m.home, 120, 40)
	assertCoverState(t, "放大40", m.home, false)

	// 缩小窗口高 → 隐藏（resize 实时刷新，无陈旧缓存）
	m.home = setWindow(m.home, 120, 26)
	assertCoverState(t, "缩小26", m.home, true)

	// 放大恢复 → 显示
	m.home = setWindow(m.home, 120, 40)
	assertCoverState(t, "恢复40", m.home, false)

	// 缩小窗口宽 → 隐藏
	m.home = setWindow(m.home, 59, 40)
	assertCoverState(t, "缩宽59", m.home, true)

	// 放大恢复 → 显示
	m.home = setWindow(m.home, 120, 40)
	assertCoverState(t, "恢复120", m.home, false)
}

// TestHomeCoverHiddenNoTrack 空态（无曲目）不涉及封面：任何尺寸都不受影响。
func TestHomeCoverHiddenNoTrack(t *testing.T) {
	fp := newFakePlayer()
	m := newTestModel(t, fp, &fakeSearchAdapter{}, nil)
	m.home = setWindow(m.home, 30, 16)
	want := m.home.height // 页面高度 = 窗口高 - 4 = 12
	if got := strings.Count(m.home.view(), "\n") + 1; got != want {
		t.Errorf("空态 view 行数 = %d, want %d（不因尺寸隐藏逻辑改变）", got, want)
	}
}

// TestHomeCoverHideSixelClearsResidual 封面隐藏时清除已画出的六像素（审查 Major
// #1）：覆盖型终端（konsole/kitty 等）像素驻留——显示态写出载荷 → 缩小到隐藏发出
// 清除（单发，不重排载荷）→ 放大恢复重绘载荷。
func TestHomeCoverHideSixelClearsResidual(t *testing.T) {
	coverModeEnv(t, "sixel")
	var out lockedWriter
	old := overlayOut
	overlayOut = &out
	defer func() { overlayOut = old }()

	fp := newFakePlayer()
	m := newTestModel(t, fp, &fakeSearchAdapter{}, nil)
	m, cmd := m.startPlay(testTrack("t1"))
	_ = execCmds(cmd)
	pngPath := filepath.Join(t.TempDir(), "cover.png")
	writeTestPNG(t, pngPath)
	m, _ = update(m, coverResultMsg{trackID: "t1", path: pngPath})
	payload := m.home.sixelPayload
	if payload == "" {
		t.Fatal("sixel 模式应生成 DCS 载荷")
	}

	// 显示态（大窗口）：view 外带写出载荷 DCS
	m.home = setWindow(m.home, 120, 40)
	_ = m.home.view()
	if !strings.Contains(out.String(), "\x1bPq") {
		t.Fatalf("显示态应写出 sixel 载荷 DCS: %q", out.String())
	}
	if !m.home.sixelSt.drawn {
		t.Fatal("显示态应记录 drawn=true")
	}

	// 缩小到隐藏（窗口高 26 < 28）：发出单次背景色清除帧，且不再画图像
	out.Reset()
	m.home = setWindow(m.home, 120, 26)
	_ = m.home.view()
	cleared := out.String()
	// 精确断言：隐藏过渡应发出与代码同参的背景色清除帧（纯黑测试图与清除帧
	// 内容接近，用排除法断言载荷会被均匀图 payload 重叠误报，改用同源比对）
	cellW, cellH := coverrender.FontCellSize()
	wantClear := coverrender.SixelClear(coverW, coverH, cellW, cellH)
	if !strings.Contains(cleared, wantClear) {
		t.Fatalf("显示→隐藏过渡应发出背景色清除帧: %q", cleared)
	}
	if m.home.sixelSt.drawn {
		t.Error("隐藏后 sixel 状态应复位 drawn=false")
	}
	if m.home.sixelSt.token != "" {
		t.Error("隐藏后 sixel token 应清空")
	}

	// 连续隐藏帧不重复写（幂等：drawn 已复位，不再发清除）
	out.Reset()
	_ = m.home.view()
	if out.Len() != 0 {
		t.Errorf("连续隐藏帧不应重复写出, got %q", out.String())
	}

	// 放大恢复：重绘载荷（payload 未被清除，token 已复位 → 重新写出）
	out.Reset()
	m.home = setWindow(m.home, 120, 40)
	_ = m.home.view()
	if !strings.Contains(out.String(), payload) {
		t.Errorf("恢复显示应重绘 sixel 载荷: %q", out.String())
	}
}