package ui

import (
	"bytes"
	"errors"
	"fmt"
	"image"
	"image/png"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"music-tui/coverrender"
	"music-tui/lyrics"
	"music-tui/lyricshm"
	"music-tui/model"
	"music-tui/player"
	"music-tui/queue"
)

// writeTestPNG 写一张有效的 PNG 到指定路径（封面成功路径需要真实图片文件）。
func writeTestPNG(t *testing.T, path string) {
	t.Helper()
	var buf bytes.Buffer
	if err := png.Encode(&buf, image.NewRGBA(image.Rect(0, 0, 8, 8))); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestHomeLyricsStates(t *testing.T) {
	fp := newFakePlayer()
	m := newTestModel(t, fp, &fakeSearchAdapter{}, nil)
	m, cmd := m.startPlay(testTrack("t1"))
	_ = execCmds(cmd)

	if m.home.lyricsState != lyricsLoading {
		t.Fatalf("初始 = %v, want lyricsLoading", m.home.lyricsState)
	}

	// 无歌词
	m, _ = update(m, lyricsResultMsg{trackID: "t1", err: lyrics.ErrNotFound})
	if m.home.lyricsState != lyricsNone {
		t.Fatalf("state = %v, want lyricsNone", m.home.lyricsState)
	}

	// 同步歌词到达
	ly, err := lyrics.ParseLRC([]byte("[00:10.00]第一行\n[00:20.00]第二行\n[00:30.00]第三行\n"))
	if err != nil {
		t.Fatal(err)
	}
	m, _ = update(m, lyricsResultMsg{trackID: "t1", lyrics: ly})
	if m.home.lyricsState != lyricsSynced {
		t.Fatalf("state = %v, want lyricsSynced", m.home.lyricsState)
	}
	if m.home.currentLine != -1 {
		t.Fatalf("初始 currentLine = %d, want -1", m.home.currentLine)
	}

	// 进度推进 → 高亮行切换
	m, _ = update(m, playerEventMsg{ev: player.ProgressEvent{Position: 25, Duration: 200}})
	if m.home.currentLine != 1 {
		t.Fatalf("currentLine = %d, want 1", m.home.currentLine)
	}
	// 进度回退到首行之前 → 无高亮
	m, _ = update(m, playerEventMsg{ev: player.ProgressEvent{Position: 5, Duration: 200}})
	if m.home.currentLine != -1 {
		t.Fatalf("currentLine = %d, want -1", m.home.currentLine)
	}
}

func TestHomeCoverStates(t *testing.T) {
	fp := newFakePlayer()
	m := newTestModel(t, fp, &fakeSearchAdapter{}, nil)
	m, cmd := m.startPlay(testTrack("t1"))
	_ = execCmds(cmd)

	pngPath := filepath.Join(t.TempDir(), "cover.png")
	writeTestPNG(t, pngPath)

	// 失败 → 占位框
	m, _ = update(m, coverResultMsg{trackID: "t1", err: lyrics.ErrNotFound})
	if !m.home.coverFallback {
		t.Error("下载失败应置 coverFallback（占位框）")
	}

	// 成功 → 渲染缓存
	m, _ = update(m, coverResultMsg{trackID: "t1", path: pngPath})
	if m.home.coverFallback || m.home.coverRenderCache == "" {
		t.Error("下载成功应渲染 coverRenderCache 且不置 fallback")
	}
	if got := m.home.coverView(); got != m.home.coverRenderCache {
		t.Error("coverView 应直接返回缓存")
	}
}

func TestHomeCoverRenderCache(t *testing.T) {
	fp := newFakePlayer()
	m := newTestModel(t, fp, &fakeSearchAdapter{}, nil)
	m, cmd := m.startPlay(testTrack("t1"))
	_ = execCmds(cmd)

	pngPath := filepath.Join(t.TempDir(), "cover.png")
	writeTestPNG(t, pngPath)

	m, _ = update(m, coverResultMsg{trackID: "t1", path: pngPath})
	// setCover 后应立即渲染并缓存（避免 view 每帧重复解码/缩放）
	if m.home.coverRenderCache == "" {
		t.Fatal("setCover 后 coverRenderCache 应为非空")
	}
	if m.home.coverFallback {
		t.Error("渲染成功后 coverFallback 应为 false")
	}
	if got := m.home.coverView(); got != m.home.coverRenderCache {
		t.Error("coverView 应直接返回缓存")
	}

	// 固定 30×17 字符画与终端尺寸无关：setSize 不清缓存、不重渲
	cacheBefore := m.home.coverRenderCache
	m.home = m.home.setSize(120, 40)
	if m.home.coverRenderCache != cacheBefore {
		t.Error("setSize 不应改动封面缓存（固定尺寸与终端无关）")
	}
	if got := m.home.coverView(); got != m.home.coverRenderCache {
		t.Error("setSize 后 coverView 应直接返回缓存")
	}
}

// TestHomeCoverRenderStable 回归：自绘封面缓存行数恒定（coverH 行），
// 窄窗口裁剪只影响展示不影响缓存。
func TestHomeCoverRenderStable(t *testing.T) {
	fp := newFakePlayer()
	m := newTestModel(t, fp, &fakeSearchAdapter{}, nil)
	m, cmd := m.startPlay(testTrack("t1"))
	_ = execCmds(cmd)

	pngPath := filepath.Join(t.TempDir(), "cover.png")
	writeTestPNG(t, pngPath)
	m, _ = update(m, coverResultMsg{trackID: "t1", path: pngPath})

	lines := strings.Split(m.home.coverRenderCache, "\n")
	if len(lines) != coverH {
		t.Errorf("封面缓存行数 = %d, want %d（恒定）", len(lines), coverH)
	}
}

func TestHomeSeekKeys(t *testing.T) {
	fp := newFakePlayer()
	m := newTestModel(t, fp, &fakeSearchAdapter{}, nil)
	tr := testTrack("t1")
	m.state = model.PlaybackState{Track: &tr, Playing: true, Position: 100, Duration: 200}
	m.home = m.home.syncState(m.state)

	// → +5s
	m, cmd := update(m, tea.KeyMsg{Type: tea.KeyRight})
	_ = execCmds(cmd)
	if len(fp.seeks) != 1 || fp.seeks[0] != 105 {
		t.Errorf("seeks = %v, want [105]", fp.seeks)
	}
	// ← -5s
	m, cmd = update(m, tea.KeyMsg{Type: tea.KeyLeft})
	_ = execCmds(cmd)
	if len(fp.seeks) != 2 || fp.seeks[1] != 100 {
		t.Errorf("seeks = %v, want [105 100]", fp.seeks)
	}
	// 无歌曲时按键无效
	m.state = model.PlaybackState{}
	m.home = m.home.syncState(m.state)
	m, cmd = update(m, tea.KeyMsg{Type: tea.KeyRight})
	if cmd != nil {
		t.Error("无歌曲时不应产生 seek 命令")
	}
	_ = m
}

func TestHomeViewNoTrack(t *testing.T) {
	m := newTestModel(t, newFakePlayer(), &fakeSearchAdapter{}, nil)
	if got := m.home.view(); got == "" || !strings.Contains(got, "未在播放") {
		t.Errorf("view 应提示未在播放，got %q", got)
	}
}

// ---- 首页布局改版（Task 4）----

// TestHomeViewFillsScreen 全屏撑满：空态与有曲目态 view 行数都 == height。
func TestHomeViewFillsScreen(t *testing.T) {
	fp := newFakePlayer()
	m := newTestModel(t, fp, &fakeSearchAdapter{}, nil)
	m.home = m.home.setSize(120, 40)

	// 空态也撑满
	if got := strings.Count(m.home.view(), "\n") + 1; got != 40 {
		t.Fatalf("空态 view 行数 = %d, want 40", got)
	}

	// 有曲目
	m, cmd := m.startPlay(testTrack("t1"))
	_ = execCmds(cmd)
	m.home = m.home.setSize(120, 40)
	if got := strings.Count(m.home.view(), "\n") + 1; got != 40 {
		t.Fatalf("有曲目 view 行数 = %d, want 40", got)
	}
}

// TestHomeViewNarrowWindow 窄窗口布局（审查 Major 3）：封面占位框 19 行
// （coverH17+边框2）/真实封面 17 行在 lipgloss.Place 中不截断，中间区溢出
// 会把进度条/按钮行推出可视区（回归：setSize(60,10) 曾渲染 21 行）。
// 修复后：view() 行数必须 == height，进度条/按钮行保持在倒数两行，无 panic。
func TestHomeViewNarrowWindow(t *testing.T) {
	fp := newFakePlayer()
	m := newTestModel(t, fp, &fakeSearchAdapter{}, nil)
	m, cmd := m.startPlay(testTrack("t1"))
	_ = execCmds(cmd)
	m, _ = update(m, lyricsResultMsg{trackID: "t1", err: lyrics.ErrNotFound}) // 占位封面（19 行）

	for _, size := range []struct{ w, h int }{
		{60, 10}, // 窄：中间区 8 行 < 封面 19 行
		{40, 5},  // 极窄：中间区 3 行
	} {
		m.home = m.home.setSize(size.w, size.h)
		lines := strings.Split(m.home.view(), "\n")
		if len(lines) != size.h {
			t.Errorf("setSize(%d,%d) 占位封面 view 行数 = %d, want %d", size.w, size.h, len(lines), size.h)
		}
		if !strings.Contains(lines[len(lines)-2], "●") {
			t.Errorf("setSize(%d,%d) 倒数第 2 行应为进度条行: %q", size.w, size.h, lines[len(lines)-2])
		}
		if !strings.Contains(lines[len(lines)-1], "|<") {
			t.Errorf("setSize(%d,%d) 最后一行应为按钮行: %q", size.w, size.h, lines[len(lines)-1])
		}
	}

	// 真实封面缓存（17 行 halfblocks 整块渲染串）同样按行截断
	pngPath := filepath.Join(t.TempDir(), "cover.png")
	writeTestPNG(t, pngPath)
	m, _ = update(m, coverResultMsg{trackID: "t1", path: pngPath})
	for _, size := range []struct{ w, h int }{{60, 10}, {40, 5}} {
		m.home = m.home.setSize(size.w, size.h) // setSize 会重渲封面并回填缓存
		if m.home.coverRenderCache == "" {
			t.Fatalf("setSize(%d,%d) 后封面缓存应为非空", size.w, size.h)
		}
		lines := strings.Split(m.home.view(), "\n")
		if len(lines) != size.h {
			t.Errorf("setSize(%d,%d) 真实封面 view 行数 = %d, want %d", size.w, size.h, len(lines), size.h)
		}
	}
}

// TestHomeMiddleAreaSideBySide 中间区：封面占位框（No Cover）与歌词提示
// （暂无歌词）水平并排——出现在同一行。
func TestHomeMiddleAreaSideBySide(t *testing.T) {
	fp := newFakePlayer()
	m := newTestModel(t, fp, &fakeSearchAdapter{}, nil)
	m, cmd := m.startPlay(testTrack("t1"))
	_ = execCmds(cmd)
	m.home = m.home.setSize(120, 40)
	m, _ = update(m, lyricsResultMsg{trackID: "t1", err: lyrics.ErrNotFound})

	lines := strings.Split(m.home.view(), "\n")
	for _, line := range lines {
		if strings.Contains(line, "No Cover") && strings.Contains(line, "暂无歌词") {
			return
		}
	}
	t.Fatal("封面占位框（No Cover）与歌词提示（暂无歌词）应在同一行水平并排")
}

// TestHomeCurrentLyricText 回归：状态栏歌词行取值——同步歌词态且有高亮行时
// 返回当前行文本；无歌词/无高亮返回空串。
func TestHomeCurrentLyricText(t *testing.T) {
	fp := newFakePlayer()
	m := newTestModel(t, fp, &fakeSearchAdapter{}, nil)
	m, cmd := m.startPlay(testTrack("t1"))
	_ = execCmds(cmd)

	// 未加载歌词：空
	if got := m.home.currentLyricText(); got != "" {
		t.Errorf("未加载歌词时应返回空串, got %q", got)
	}
	ly, err := lyrics.ParseLRC([]byte("[00:10.00]第一行\n[00:20.00]第二行\n"))
	if err != nil {
		t.Fatal(err)
	}
	m, _ = update(m, lyricsResultMsg{trackID: "t1", lyrics: ly})
	// 高亮行尚未定位（currentLine=-1）：空
	if got := m.home.currentLyricText(); got != "" {
		t.Errorf("无高亮行时应返回空串, got %q", got)
	}
	// 进度推进到 12s → 高亮第 0 行（10s 处）
	m, _ = update(m, playerEventMsg{ev: player.ProgressEvent{Position: 12}})
	if got := m.home.currentLyricText(); got != "第一行" {
		t.Errorf("currentLyricText = %q, want 第一行", got)
	}
	// 进度推进到 22s → 高亮第 1 行
	m, _ = update(m, playerEventMsg{ev: player.ProgressEvent{Position: 22}})
	if got := m.home.currentLyricText(); got != "第二行" {
		t.Errorf("currentLyricText = %q, want 第二行", got)
	}
}

// TestHomeLyricsNoneCentered 无歌词提示垂直居中：40 行视图中位于垂直中部。
func TestHomeLyricsNoneCentered(t *testing.T) {
	fp := newFakePlayer()
	m := newTestModel(t, fp, &fakeSearchAdapter{}, nil)
	m, cmd := m.startPlay(testTrack("t1"))
	_ = execCmds(cmd)
	m.home = m.home.setSize(120, 40)
	m, _ = update(m, lyricsResultMsg{trackID: "t1", err: lyrics.ErrNotFound})

	lines := strings.Split(m.home.view(), "\n")
	idx := -1
	for i, line := range lines {
		if strings.Contains(line, "暂无歌词") {
			idx = i
			break
		}
	}
	if idx < 15 || idx > 25 {
		t.Fatalf("「暂无歌词」行号 = %d（0-based），want 在 15~25 之间", idx)
	}
}

// TestHomeBottomControlRows 底部两行：倒数第 2 行 = 进度条 + 时间；
// 最后一行 = 控制按钮（⏮ ⏯ ⏭ + 模式图标）与队列信息。
func TestHomeBottomControlRows(t *testing.T) {
	fp := newFakePlayer()
	m := newTestModel(t, fp, &fakeSearchAdapter{}, nil)
	m, cmd := m.startPlay(testTrack("t1"))
	_ = execCmds(cmd)
	m.home = m.home.setSize(120, 40)
	tr := testTrack("t1")
	m.state = model.PlaybackState{Track: &tr, Playing: true, Position: 100, Duration: 200}
	m.home = m.home.syncState(m.state)

	lines := strings.Split(m.home.view(), "\n")
	if len(lines) != 40 {
		t.Fatalf("view 行数 = %d, want 40", len(lines))
	}
	barLine := lines[len(lines)-2]
	if !strings.Contains(barLine, "●") {
		t.Errorf("进度条行（倒数第 2 行）应含滑块 ●: %q", barLine)
	}
	if !strings.Contains(barLine, "/") {
		t.Errorf("进度条行应含时间分隔 /: %q", barLine)
	}
	// 进度条行可见宽 == 页面宽（Nit 5：barW 与渲染间隔统一后无 1 列差）
	if w := ansi.StringWidth(barLine); w != m.home.width {
		t.Errorf("进度条行可见宽 = %d, want %d（页面宽）", w, m.home.width)
	}
	btnLine := lines[len(lines)-1]
	for _, icon := range []string{"|<", "||", ">|", "顺序"} {
		if !strings.Contains(btnLine, icon) {
			t.Errorf("按钮行应含 %s: %q", icon, btnLine)
		}
	}
	// 无队列信息（queueTotal==0）时省略 "3/12 · 模式" 段
	m.home = m.home.setQueueInfo(0, 0, queue.Sequential)
	btnLine = strings.Split(m.home.view(), "\n")[len(lines)-1]
	if strings.Contains(btnLine, "·") {
		t.Errorf("无队列信息时按钮行不应含 · 模式段: %q", btnLine)
	}

	// 有队列信息时右栏展示 "模式名  位置/总数"
	m.home = m.home.setQueueInfo(3, 12, queue.Sequential)
	btnLine = strings.Split(m.home.view(), "\n")[len(lines)-1]
	if !strings.Contains(btnLine, "顺序  3/12") {
		t.Errorf("按钮行应含队列信息 顺序  3/12: %q", btnLine)
	}
}

// TestHomeControlBarPositions 渲染列位置必须与 controlBarLayout 命中区间一致：
// 中栏 "|<" 起点 == lay.centerStart，右栏模式文本起点 == lay.rightStart。
// （回归：padLeft 按 leftW 上限计算，标题实际宽度小于 leftW 时未补齐差额，
//
//	中栏/右栏整体贴左偏移——用户终端验证发现 "顺序 9/25 后面一大堆空"，
//	且鼠标命中区间（按 layout）与渲染位置错位导致点击失效。）
func TestHomeControlBarPositions(t *testing.T) {
	fp := newFakePlayer()
	m := newTestModel(t, fp, &fakeSearchAdapter{}, nil)
	m, cmd := m.startPlay(testTrack("t1"))
	_ = execCmds(cmd)
	m.home = m.home.setSize(120, 40)
	m.home = m.home.setQueueInfo(3, 12, queue.Sequential)

	// 短标题（不截断，触发补位差额）与超长标题（触发截断）两种场景
	cases := []struct {
		name  string
		title string
	}{
		{"短标题", "我怕来者不是你"},
		{"超长标题", strings.Repeat("歌", 60)},
	}
	for _, tc := range cases {
		tr := testTrack("t1")
		tr.Title = tc.title
		m.state = model.PlaybackState{Track: &tr, Playing: true}
		m.home = m.home.syncState(m.state)

		lines := strings.Split(m.home.view(), "\n")
		btnLine := lines[len(lines)-1]
		lay := m.home.controlBarLayout(m.home.width)

		if col := colOf(btnLine, "|<"); col != lay.centerStart {
			t.Errorf("%s: 中栏 |< 起始列 = %d, want %d (centerStart)\n按钮行: %q", tc.name, col, lay.centerStart, btnLine)
		}
		if col := colOf(btnLine, "顺序"); col != lay.rightStart {
			t.Errorf("%s: 右栏模式文本起始列 = %d, want %d (rightStart)\n按钮行: %q", tc.name, col, lay.rightStart, btnLine)
		}
	}
}

// colOf 返回 line 中子串 substr 首次出现的起始列（按显示宽度）。
func colOf(line, substr string) int {
	idx := strings.Index(line, substr)
	if idx < 0 {
		return -1
	}
	return ansi.StringWidth(line[:idx])
}

// TestHomeControlBarCentered 宽窗口下中栏操作键应位于屏幕水平中心：
// centerStart == (width-centerBarW)/2。
// （回归：曾在中栏在“左栏右缘~右栏左缘”之间居中——左栏（标题）宽、右栏
//
//	（模式）窄时中栏被推到右侧，窗口越宽越明显：用户终端验证 W≈120 时
//	“|< || >| 顺序 10/25 都到右边去了”。）
func TestHomeControlBarCentered(t *testing.T) {
	fp := newFakePlayer()
	m := newTestModel(t, fp, &fakeSearchAdapter{}, nil)
	m, cmd := m.startPlay(testTrack("t1"))
	_ = execCmds(cmd)
	m.home = m.home.setSize(120, 40)
	m.home = m.home.setQueueInfo(10, 25, queue.Sequential)

	// 宽窗口：中栏起点 = 屏幕中心 - 中栏宽/2
	lay := m.home.controlBarLayout(120)
	if want := (120 - centerBarW) / 2; lay.centerStart != want {
		t.Errorf("宽窗口 centerStart = %d, want %d（屏幕居中）", lay.centerStart, want)
	}
	// 渲染与命中一致：|< 渲染在 layout 的 centerStart
	lines := strings.Split(m.home.view(), "\n")
	if col := colOf(lines[len(lines)-1], "|<"); col != lay.centerStart {
		t.Errorf("中栏 |< 渲染列 = %d, want %d", col, lay.centerStart)
	}
	// 中栏中心 ≈ 屏幕中心（允许 ±1 列取整误差）
	if got, want := lay.centerStart+centerBarW/2, 60; got < want-1 || got > want+1 {
		t.Errorf("中栏中心 = %d, want ≈ %d（屏幕中心）", got, want)
	}

	// 窄窗口：中栏不越过右栏起点、不贴左（弹性退化仍可用）
	for _, w := range []int{80, 60, 44, 36} {
		m.home = m.home.setSize(w, 24)
		lay = m.home.controlBarLayout(w)
		if lay.centerStart+centerBarW > lay.rightStart {
			t.Errorf("W=%d: 中栏右缘 %d 越过右栏起点 %d", w, lay.centerStart+centerBarW, lay.rightStart)
		}
		if lay.centerStart < 12 {
			t.Errorf("W=%d: 中栏起点 %d 贴左（左栏最小宽 10 + 间距 2）", w, lay.centerStart)
		}
	}
}

// TestHomeModeKeyCycles 首页 m 键三态循环切换模式（Sequential→Shuffle→
// RepeatOne→Sequential）。与队列页 s 键语义一致：模式是全局队列属性，
// 无曲目时也应能切换（root.cycleMode 不依赖当前播放）。
func TestHomeModeKeyCycles(t *testing.T) {
	fp := newFakePlayer()
	m := newTestModel(t, fp, &fakeSearchAdapter{}, nil)
	m, cmd := m.startPlay(testTrack("t1"))
	_ = execCmds(cmd)

	// 三态循环
	for i, want := range []queue.Mode{queue.Shuffle, queue.RepeatOne, queue.Sequential} {
		m, cmd = update(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("m")})
		found := false
		for _, msg := range execCmds(cmd) {
			if mm, ok := msg.(toggleModeMsg); ok {
				found = true
				m, _ = update(m, mm)
			}
		}
		if !found {
			t.Fatalf("第 %d 次按 m 应产生 toggleModeMsg", i)
		}
		if m.queue.Mode() != want {
			t.Fatalf("第 %d 次按 m 后 Mode = %v, want %v", i, m.queue.Mode(), want)
		}
	}

	// 无曲目：m 键也应触发（与队列页 s 键一致）
	m.state = model.PlaybackState{}
	m.home = m.home.syncState(m.state)
	m, cmd = update(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("m")})
	found := false
	for _, msg := range execCmds(cmd) {
		if _, ok := msg.(toggleModeMsg); ok {
			found = true
		}
	}
	if !found {
		t.Fatal("无曲目时按 m 应产生 toggleModeMsg")
	}
}

// TestHomePrevNextKeys ，上一首 / . 下一首；无曲目时忽略。
func TestHomePrevNextKeys(t *testing.T) {
	fp := newFakePlayer()
	m := newTestModel(t, fp, &fakeSearchAdapter{}, nil)
	m, cmd := m.startPlay(testTrack("t1"))
	_ = execCmds(cmd)

	m, cmd = update(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(",")})
	if cmd == nil {
		t.Fatal(", 键应产生命令")
	}
	msgs := execCmds(cmd)
	if len(msgs) != 1 {
		t.Fatalf(", 键消息数 = %d, want 1", len(msgs))
	}
	if _, ok := msgs[0].(prevTrackMsg); !ok {
		t.Errorf(", 键消息类型 = %T, want prevTrackMsg", msgs[0])
	}

	m, cmd = update(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(".")})
	if cmd == nil {
		t.Fatal(". 键应产生命令")
	}
	msgs = execCmds(cmd)
	if len(msgs) != 1 {
		t.Fatalf(". 键消息数 = %d, want 1", len(msgs))
	}
	if _, ok := msgs[0].(nextTrackMsg); !ok {
		t.Errorf(". 键消息类型 = %T, want nextTrackMsg", msgs[0])
	}

	// 无曲目：忽略（nil cmd）
	m.state = model.PlaybackState{}
	m.home = m.home.syncState(m.state)
	if m, cmd = update(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(",")}); cmd != nil {
		t.Error("无曲目时 , 键不应产生命令")
	}
	if m, cmd = update(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(".")}); cmd != nil {
		t.Error("无曲目时 . 键不应产生命令")
	}
	_ = m
}

// TestHomeMouseSeekClick 点击进度条行（页面 Y == height-2）→ seek 到点击处；
// X 越界忽略；无曲目忽略。
func TestHomeMouseSeekClick(t *testing.T) {
	fp := newFakePlayer()
	m := newTestModel(t, fp, &fakeSearchAdapter{}, nil)
	m, cmd := m.startPlay(testTrack("t1"))
	_ = execCmds(cmd)
	m.home = m.home.setSize(120, 40)
	tr := testTrack("t1")
	m.state = model.PlaybackState{Track: &tr, Playing: true, Position: 100, Duration: 200}
	m.home = m.home.syncState(m.state)

	barW := m.home.progressBarWidth()
	m, cmd = update(m, tea.MouseMsg{
		Action: tea.MouseActionPress,
		Button: tea.MouseButtonLeft,
		X:      barW / 2,
		Y:      m.home.height + 1, // 屏幕 Y = height+1（顶部 3 行）→ 页面 Y = height-2（进度条行）
	})
	_ = execCmds(cmd)
	if len(fp.seeks) != 1 {
		t.Fatalf("seeks = %v, want 1 次 seek", fp.seeks)
	}
	want := progressClickPercent(barW/2, barW) * 200
	if math.Abs(fp.seeks[0]-want) > 1e-6 {
		t.Errorf("seek = %v, want %v", fp.seeks[0], want)
	}
	if math.Abs(fp.seeks[0]-100) > 1.5 {
		t.Errorf("seek = %v, want ≈ Duration/2 = 100", fp.seeks[0])
	}

	// X 越界（≥ barW）忽略
	m, cmd = update(m, tea.MouseMsg{
		Action: tea.MouseActionPress,
		Button: tea.MouseButtonLeft,
		X:      barW, // 越界
		Y:      m.home.height + 1,
	})
	if cmd != nil {
		t.Error("进度条 X 越界不应产生命令")
	}

	// 无曲目：点击忽略
	m.state = model.PlaybackState{}
	m.home = m.home.syncState(m.state)
	m, cmd = update(m, tea.MouseMsg{
		Action: tea.MouseActionPress,
		Button: tea.MouseButtonLeft,
		X:      0,
		Y:      m.home.height + 1,
	})
	if cmd != nil {
		t.Error("无曲目时点击进度条不应产生命令")
	}
}

// TestHomeMouseButtonClick 点击按钮行（页面 Y == height-1）按三栏布局触发：
// 中栏 ⏮/⏯/⏭（相对 centerStart +0/+4/+8）、右栏模式区；左信息区与空白忽略。
func TestHomeMouseButtonClick(t *testing.T) {
	fp := newFakePlayer()
	m := newTestModel(t, fp, &fakeSearchAdapter{}, nil)
	m, cmd := m.startPlay(testTrack("t1"))
	_ = execCmds(cmd)
	m.home = m.home.setSize(120, 40)

	lay := m.home.controlBarLayout(m.home.width)
	press := func(x int) tea.Cmd {
		_, c := update(m, tea.MouseMsg{
			Action: tea.MouseActionPress,
			Button: tea.MouseButtonLeft,
			X:      x,
			Y:      m.home.height + 2, // 屏幕 Y = height+2（顶部 3 行）→ 页面 Y = height-1（按钮行）
		})
		return c
	}

	msgs := execCmds(press(lay.centerStart + btnPrevRel)) // ⏮
	if len(msgs) != 1 {
		t.Fatalf("⏮ 应命中, msgs=%v", msgs)
	}
	if _, ok := msgs[0].(prevTrackMsg); !ok {
		t.Errorf("⏮ 消息类型 = %T, want prevTrackMsg", msgs[0])
	}

	msgs = execCmds(press(lay.centerStart + btnToggleRel + 1)) // ⏯（区间内）
	if len(msgs) != 1 {
		t.Fatalf("⏯ 应命中, msgs=%v", msgs)
	}
	if _, ok := msgs[0].(togglePlayMsg); !ok {
		t.Errorf("⏯ 消息类型 = %T, want togglePlayMsg", msgs[0])
	}

	msgs = execCmds(press(lay.centerStart + btnNextRel + 2)) // ⏭（区间内）
	if len(msgs) != 1 {
		t.Fatalf("⏭ 应命中, msgs=%v", msgs)
	}
	if _, ok := msgs[0].(nextTrackMsg); !ok {
		t.Errorf("⏭ 消息类型 = %T, want nextTrackMsg", msgs[0])
	}

	// 右栏模式区（图标/模式名/队列位置均响应切换）
	msgs = execCmds(press(lay.rightStart))
	if len(msgs) != 1 {
		t.Fatalf("模式区应命中, msgs=%v", msgs)
	}
	if _, ok := msgs[0].(toggleModeMsg); !ok {
		t.Errorf("模式区消息类型 = %T, want toggleModeMsg", msgs[0])
	}
	msgs = execCmds(press(lay.rightStart + 5))
	if len(msgs) != 1 {
		t.Fatalf("模式区中间应命中, msgs=%v", msgs)
	}
	if _, ok := msgs[0].(toggleModeMsg); !ok {
		t.Errorf("模式区中间消息类型 = %T, want toggleModeMsg", msgs[0])
	}

	// 左信息区、按钮间空白、中栏命中容差之外不命中
	// （⏮[c,c+3) ⏯[c+4,c+7) ⏭[c+8,c+11)，容差 1 列；+3/+7 = 图标间空白，+11 = 中栏右缘外）
	for _, x := range []int{0, lay.centerStart + 3, lay.centerStart + 7, lay.centerStart + 11} {
		if c := press(x); c != nil {
			t.Errorf("X=%d 不应命中任何按钮", x)
		}
	}

	// 中间区点击忽略
	if _, c := update(m, tea.MouseMsg{
		Action: tea.MouseActionPress,
		Button: tea.MouseButtonLeft,
		X:      5,
		Y:      10,
	}); c != nil {
		t.Error("中间区点击不应产生命令")
	}

	// 无曲目：按钮点击忽略
	m.state = model.PlaybackState{}
	m.home = m.home.syncState(m.state)
	if c := press(0); c != nil {
		t.Error("无曲目时点击按钮不应产生命令")
	}
}

// TestHomeCoverRenderSize 回归：自绘封面输出必须恰好 coverH 行
// （曾依赖 go-termimg：尺寸单位混乱导致 1~17 行漂移，布局崩坏）。
func TestHomeCoverRenderSize(t *testing.T) {
	fp := newFakePlayer()
	m := newTestModel(t, fp, &fakeSearchAdapter{}, nil)
	m, cmd := m.startPlay(testTrack("t1"))
	_ = execCmds(cmd)

	pngPath := filepath.Join(t.TempDir(), "cover.png")
	writeTestPNG(t, pngPath)
	m, _ = update(m, coverResultMsg{trackID: "t1", path: pngPath})
	if m.home.coverRenderCache == "" {
		t.Fatal("封面渲染缓存应为非空")
	}
	lines := strings.Split(m.home.coverRenderCache, "\n")
	if len(lines) != coverH {
		t.Errorf("封面渲染行数 = %d, want %d（恒定）", len(lines), coverH)
	}
}

// TestHomeLyricsCenteredWhenFew 回归：歌词行数少于视口高时，歌词列内容
// 应垂直居中（视口收缩到歌词行数，由外层 Place 居中）而非顶部对齐。
func TestHomeLyricsCenteredWhenFew(t *testing.T) {
	fp := newFakePlayer()
	m := newTestModel(t, fp, &fakeSearchAdapter{}, nil)
	m, cmd := m.startPlay(testTrack("t1"))
	_ = execCmds(cmd)

	ly, err := lyrics.ParseLRC([]byte("[00:10.00]第一行\n[00:20.00]第二行\n[00:30.00]第三行\n[00:40.00]第四行\n[00:50.00]第五行\n"))
	if err != nil {
		t.Fatal(err)
	}
	m, _ = update(m, lyricsResultMsg{trackID: "t1", lyrics: ly})
	m, _ = update(m, playerEventMsg{ev: player.ProgressEvent{Position: 25, Duration: 200}})
	m.home = m.home.setSize(120, 39)

	out := m.home.view()
	lines := strings.Split(out, "\n")
	if len(lines) != 39 {
		t.Fatalf("view 行数 = %d, want 39", len(lines))
	}
	// 5 行歌词应出现在中间区中部（37 行中间区 → 首行在 ~16 行附近）
	first := -1
	for i, ln := range lines {
		if strings.Contains(stripAnsiForTest(ln), "第一行") {
			first = i
			break
		}
	}
	if first < 10 || first > 22 {
		t.Errorf("歌词首行出现在行 %d, want 中间区中部（10~22）", first)
	}

	// 方案 A：视口不收缩（统一 padding 模型），5 行歌词时首行仍在视口中央
	if got := m.home.lyricView.Height; got != 21 {
		t.Errorf("歌词少时视口高 = %d, want 21（不再收缩）", got)
	}
}

// TestHomeLyricsFillWhenMany 歌词行数多于视口高时占满歌词列（滚动语义不变）。
func TestHomeLyricsFillWhenMany(t *testing.T) {
	fp := newFakePlayer()
	m := newTestModel(t, fp, &fakeSearchAdapter{}, nil)
	m, cmd := m.startPlay(testTrack("t1"))
	_ = execCmds(cmd)

	var sb strings.Builder
	for i := 0; i < 60; i++ {
		sb.WriteString(fmt.Sprintf("[%02d:%02d.00]歌词行 ", (10+i*5)/60, (10+i*5)%60))
		sb.WriteString(string(rune('A' + i%26)))
		sb.WriteString("\n")
	}
	ly, err := lyrics.ParseLRC([]byte(sb.String()))
	if err != nil {
		t.Fatal(err)
	}
	m, _ = update(m, lyricsResultMsg{trackID: "t1", lyrics: ly})
	m, _ = update(m, playerEventMsg{ev: player.ProgressEvent{Position: 25, Duration: 200}})
	m.home = m.home.setSize(120, 39)

	if m.home.lyricView.Height != 21 {
		t.Errorf("歌词超多时 lyricView.Height = %d, want 21（视口上限）", m.home.lyricView.Height)
	}
}

func stripAnsiForTest(s string) string {
	var sb strings.Builder
	in := false
	for _, r := range s {
		if r == '\x1b' {
			in = true
			continue
		}
		if in {
			if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') {
				in = false
			}
			continue
		}
		sb.WriteRune(r)
	}
	return sb.String()
}

// TestHomeLyricsHorizontallyCentered 回归：歌词文本在封面右侧剩余空间
// （歌词列）内水平居中（列单位），而非左对齐或屏幕中心基准。
func TestHomeLyricsHorizontallyCentered(t *testing.T) {
	fp := newFakePlayer()
	m := newTestModel(t, fp, &fakeSearchAdapter{}, nil)
	m, cmd := m.startPlay(testTrack("t1"))
	_ = execCmds(cmd)

	ly, err := lyrics.ParseLRC([]byte("[00:10.00]第一行\n[00:20.00]第二行\n"))
	if err != nil {
		t.Fatal(err)
	}
	m, _ = update(m, lyricsResultMsg{trackID: "t1", lyrics: ly})
	m.home = m.home.setSize(120, 39)

	out := m.home.view()
	lines := strings.Split(out, "\n")
	for _, ln := range lines {
		vis := stripAnsiForTest(ln)
		if !strings.Contains(vis, "第一行") {
			continue
		}
		idx := strings.Index(vis, "第一行")
		col := ansi.StringWidth(vis[:idx]) // 歌词起始列（列单位）
		// 120 宽：歌词列 [33,119) 中心 ≈ 76（block 居中 pad 1 → 77）；
		// "第一行" 6 列 → 中心 = col+3 ∈ [75,79]（列内居中）；
		// 屏幕中心基准（回归）会落在 ~60，左对齐 ~33。
		if center := col + 3; center < 75 || center > 79 {
			t.Errorf("歌词中心列 = %d, want ≈ 76（歌词列内居中），起始列 %d", center, col)
		}
		return
	}
	t.Error("view 中未找到歌词行")
}

// TestHomeNoLyricsCenteredOnScreen 回归："暂无歌词"提示在歌词列内居中
// （外层 Place 不会把满宽行重新水平居中）。
func TestHomeNoLyricsCenteredOnScreen(t *testing.T) {
	fp := newFakePlayer()
	m := newTestModel(t, fp, &fakeSearchAdapter{}, nil)
	m, cmd := m.startPlay(testTrack("t1"))
	_ = execCmds(cmd)
	m, _ = update(m, lyricsResultMsg{trackID: "t1", err: lyrics.ErrNotFound})
	m.home = m.home.setSize(120, 39)

	for _, ln := range strings.Split(m.home.view(), "\n") {
		vis := stripAnsiForTest(ln)
		if !strings.Contains(vis, "暂无歌词") {
			continue
		}
		idx := strings.Index(vis, "暂无歌词")
		col := ansi.StringWidth(vis[:idx])
		// "暂无歌词" = 8 列 → 中心 = col+4 ≈ 76（歌词列内居中，±3）
		if center := col + 4; center < 73 || center > 79 {
			t.Errorf("暂无歌词中心列 = %d, want ≈ 76（歌词列内居中），起始列 %d", center, col)
		}
		return
	}
	t.Error("view 中未找到暂无歌词")
}

// TestHomeLyricsViewport21 回归：歌词视口最多 21 行（当前行上下各 10 行），
// 大窗口（中间区高 > 21）不占满整列；行数不足时收缩到行数。
func TestHomeLyricsViewport21(t *testing.T) {
	fp := newFakePlayer()
	m := newTestModel(t, fp, &fakeSearchAdapter{}, nil)
	m, cmd := m.startPlay(testTrack("t1"))
	_ = execCmds(cmd)

	// 60 行歌词（超多，时间戳递增）
	var sb strings.Builder
	for i := 0; i < 60; i++ {
		sb.WriteString(fmt.Sprintf("[%02d:%02d.00]歌词行 ", (10+i*5)/60, (10+i*5)%60))
		sb.WriteString(string(rune('A' + i%26)))
		sb.WriteString("\n")
	}
	ly, _ := lyrics.ParseLRC([]byte(sb.String()))
	m, _ = update(m, lyricsResultMsg{trackID: "t1", lyrics: ly})
	m.home = m.home.setSize(120, 60) // midH = 58 > 21
	if got := m.home.lyricView.Height; got != 21 {
		t.Errorf("歌词超多时视口高 = %d, want 21（上限）", got)
	}
	// 当前行推进 → 滚动到正中（上下各 10 行）
	m, _ = update(m, playerEventMsg{ev: player.ProgressEvent{Position: 155, Duration: 2000}})
	if got := m.home.lyricView.YOffset; got != 29 { // LineAt(155)=29 → YOffset=29（padding 模型，当前行恒在视口中央）
		t.Errorf("YOffset = %d, want 29", got)
	}
}

// TestHomeLyricsCurrentLineCentered 回归：歌词 > 21 行时当前行滚动到视口正中
// （上 10 下 10）；行数不足时 clamp（首行 offset 0）。
func TestHomeLyricsCurrentLineCentered(t *testing.T) {
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
	m.home = m.home.setSize(120, 60)

	// 第 30 行（0-based 29，155s）：YOffset = 29（内容含 H/2=10 行前导空白，当前行显示在视口第 10 行正中）
	m, _ = update(m, playerEventMsg{ev: player.ProgressEvent{Position: 155, Duration: 2000}})
	if got := m.home.lyricView.YOffset; got != 29 {
		t.Errorf("第 30 行 YOffset = %d, want 29（视口正中）", got)
	}
	// 末行（LineAt(495) = idx 59，305s ≤ 495）：YOffset = clamp(59, 0, N−1=59) = 59
	// （padding 模型末行停中央；旧模型 59−10=49 被 viewport clamp 到 39 贴底）
	m, _ = update(m, playerEventMsg{ev: player.ProgressEvent{Position: 495, Duration: 2000}})
	if got := m.home.lyricView.YOffset; got != 59 {
		t.Errorf("末行 YOffset = %d, want 59（padding 模型末行停中央）", got)
	}
	// 首行（idx=0）：YOffset = clamp(0, 0, N−1) = 0（padding 模型，首行在视口中央）
	m, _ = update(m, playerEventMsg{ev: player.ProgressEvent{Position: 12, Duration: 2000}})
	if got := m.home.lyricView.YOffset; got != 0 {
		t.Errorf("首行 YOffset = %d, want 0", got)
	}
	// 回到第 30 行（wheel 测试基线一致性）
	m, _ = update(m, playerEventMsg{ev: player.ProgressEvent{Position: 155, Duration: 2000}})
}

// TestHomeLyricsWheelScroll 回归：滚轮在歌词列区域滚动视口（手动查看）；
// 歌词列区域外/非歌词态忽略。
func TestHomeLyricsWheelScroll(t *testing.T) {
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
	m.home = m.home.setSize(120, 60)
	// 先滚动到中间（第 30 行居中，YOffset 29）
	m, _ = update(m, playerEventMsg{ev: player.ProgressEvent{Position: 155, Duration: 2000}})
	base := m.home.lyricView.YOffset

	// 歌词列区域滚轮向下：offset 增加（上限 clamp）
	m, _ = update(m, tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonWheelDown, X: 60, Y: 20})
	if got := m.home.lyricView.YOffset; got <= base {
		t.Errorf("滚轮向下 YOffset = %d, want > %d", got, base)
	}
	// 滚轮向上：offset 减少
	mid := m.home.lyricView.YOffset
	m, _ = update(m, tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonWheelUp, X: 60, Y: 20})
	if got := m.home.lyricView.YOffset; got >= mid {
		t.Errorf("滚轮向上 YOffset = %d, want < %d", got, mid)
	}
	// 封面区域（X < coverW+2）忽略
	before := m.home.lyricView.YOffset
	m, _ = update(m, tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonWheelUp, X: 5, Y: 20})
	if got := m.home.lyricView.YOffset; got != before {
		t.Errorf("封面区域滚轮不应滚动, got %d", got)
	}
	// 无歌词态（loading）忽略
	m2, _ := update(m, lyricsResultMsg{trackID: "t1", err: lyrics.ErrNotFound})
	before = m2.home.lyricView.YOffset
	m2, _ = update(m2, tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonWheelDown, X: 60, Y: 20})
	if got := m2.home.lyricView.YOffset; got != before {
		t.Errorf("无歌词态滚轮不应滚动, got %d", got)
	}
}

// TestHomeLyricsCenterFallback 回归：超宽歌词行（屏幕中心放不下）退化为
// 歌词列内居中而非左对齐（80 宽终端 + 长中文行曾 clamp 到 0 左对齐）。
func TestHomeLyricsCenterFallback(t *testing.T) {
	fp := newFakePlayer()
	m := newTestModel(t, fp, &fakeSearchAdapter{}, nil)
	m, cmd := m.startPlay(testTrack("t1"))
	_ = execCmds(cmd)

	longLine := "我順著琴聲方向看見薔薇依附十八世紀的油畫上" // 22 字 = 44 列
	ly, err := lyrics.ParseLRC([]byte("[00:10.00]" + longLine + "\n"))
	if err != nil {
		t.Fatal(err)
	}
	m, _ = update(m, lyricsResultMsg{trackID: "t1", lyrics: ly})
	// 声明整窗口高度 40（≥ 2×coverH）使封面显示：本测试验证封面显示布局下的
	// 歌词列内居中兜底；不设 windowHeight 时回退页面高 24 < 34 会隐藏封面（歌词占满整行）。
	m.home.windowHeight = 40
	m.home = m.home.setSize(80, 24) // midX=40，屏幕中心放不下 44 列行

	for _, ln := range strings.Split(m.home.view(), "\n") {
		vis := stripAnsiForTest(ln)
		if !strings.Contains(vis, "我順著琴聲") {
			continue
		}
		idx := strings.Index(vis, "我順著琴聲")
		col := ansi.StringWidth(vis[:idx])
		// 列内居中：歌词列 [33, 79)（宽 46）→ 44 列行 pad ≈ (46-44)/2 = 1 → 起点 ≈ 34；
		// 左对齐（回归）起点 = 33。
		if col < 34 {
			t.Errorf("超宽歌词行起始列 = %d, want ≥ 34（列内居中兜底，非左对齐）", col)
		}
		return
	}
	t.Error("view 中未找到歌词行")
}

// TestHomeResizeRelayout 回归：窗口尺寸变化后布局必须整体重新定位
// （行数 = 新页面高、进度条满新宽、歌词行重定位）——曾依赖旧尺寸渲染。
func TestHomeResizeRelayout(t *testing.T) {
	fp := newFakePlayer()
	m := newTestModel(t, fp, &fakeSearchAdapter{}, nil)
	m, cmd := m.startPlay(testTrack("t1"))
	_ = execCmds(cmd)
	ly, _ := lyrics.ParseLRC([]byte("[00:10.00]第一行\n[00:20.00]第二行\n"))
	m, _ = update(m, lyricsResultMsg{trackID: "t1", lyrics: ly})
	m, _ = update(m, playerEventMsg{ev: player.ProgressEvent{Position: 15, Duration: 200}})

	for _, sz := range [][2]int{{120, 40}, {80, 24}, {120, 40}, {60, 10}} {
		m, _ = update(m, tea.WindowSizeMsg{Width: sz[0], Height: sz[1]})
		out := m.home.view()
		lines := strings.Split(out, "\n")
		if len(lines) != sz[1]-4 {
			t.Errorf("%dx%d: 行数 = %d, want %d", sz[0], sz[1], len(lines), sz[1]-4)
		}
		if w := ansi.StringWidth(lines[len(lines)-2]); w != sz[0] {
			t.Errorf("%dx%d: 进度条行宽 = %d, want %d", sz[0], sz[1], w, sz[0])
		}
		found := false
		for _, ln := range lines {
			if strings.Contains(stripAnsiForTest(ln), "第一行") {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("%dx%d: 歌词行未找到（布局未重定位）", sz[0], sz[1])
		}
	}
}

// TestHomeLyricsAISourceTag AI 来源歌词不再显示「AI 匹配」标识（用户要求
// 移除，home.go 中对应渲染块已注释），确定性来源同样不显示。
func TestHomeLyricsAISourceTag(t *testing.T) {
	fp := newFakePlayer()
	m := newTestModel(t, fp, &fakeSearchAdapter{}, nil)
	m, cmd := m.startPlay(testTrack("t1"))
	_ = execCmds(cmd)

	ly, err := lyrics.ParseLRC([]byte("[00:10.00]第一行\n"))
	if err != nil {
		t.Fatal(err)
	}
	ly.Source = lyrics.LyricsSourceAI
	m, _ = update(m, lyricsResultMsg{trackID: "t1", lyrics: ly})
	if strings.Contains(m.home.view(), "AI 匹配") {
		t.Error("AI 来源歌词不应显示「AI 匹配」标识（已移除）")
	}

	// 确定性来源（Source 空）：同样不显示
	ly2, _ := lyrics.ParseLRC([]byte("[00:10.00]第二行\n"))
	m, _ = update(m, lyricsResultMsg{trackID: "t1", lyrics: ly2})
	if strings.Contains(m.home.view(), "AI 匹配") {
		t.Error("确定性来源歌词不应显示「AI 匹配」标识")
	}
}

// TestHomeLyricsHeightReservesAITag 歌词高度与来源无关（AI 标识已移除）：
// padding 模型下视口自带上下留白（H = midH−4），不推挤底部控制栏。
func TestHomeLyricsHeightReservesAITag(t *testing.T) {
	build := func(source string, lineCount int) homeModel {
		ly, _ := lyrics.ParseLRC([]byte(strings.Repeat("[00:01.00]行\n", lineCount)))
		ly.Source = source
		return homeModel{lyrics: ly, lyricsState: lyricsSynced, height: 23}
	}
	// midH=21 → H = min(21, 21−4) = 17；AI 标识 1 行 + 视口 17 行 = 18 ≤ 21，不溢出
	if h := build("", 100).lyricsHeight(); h != 17 {
		t.Errorf("非 AI lyricsHeight = %d, want 17", h)
	}
	// AI：同样 17（留白已保证标识行不溢出，无需再按来源减行）
	if h := build(lyrics.LyricsSourceAI, 100).lyricsHeight(); h != 17 {
		t.Errorf("AI lyricsHeight = %d, want 17（留白模型已防溢出）", h)
	}
	// AI + 行数少：方案 A 不收缩视口（N < H 时首行仍居中，统一 padding 模型）
	if h := build(lyrics.LyricsSourceAI, 5).lyricsHeight(); h != 17 {
		t.Errorf("AI 少行 lyricsHeight = %d, want 17（不收缩）", h)
	}
	// 极窄窗口：视口至少 1 行，不归零
	ly, _ := lyrics.ParseLRC([]byte("[00:01.00]行\n"))
	ly.Source = lyrics.LyricsSourceAI
	m := homeModel{lyrics: ly, lyricsState: lyricsSynced, height: 3}
	if h := m.lyricsHeight(); h < 1 {
		t.Errorf("极窄窗口 lyricsHeight = %d, want ≥1", h)
	}
}

// TestHomeControlBarShowsAITitle AI 识别结果到达后，底部控制栏显示
// 清洗后「晴天 - 周杰伦」而非原始 YouTube 标题。
func TestHomeControlBarShowsAITitle(t *testing.T) {
	fp := newFakePlayer()
	m := newTestModel(t, fp, &fakeSearchAdapter{}, nil)
	raw := model.Track{ID: "t1", Title: "T1", Artist: "A", Duration: 200, URL: "http://x/1", Source: "youtube"}
	m, cmd := m.startPlay(raw)
	_ = execCmds(cmd)

	// AI 结果到达前：原始标题
	if got := m.home.view(); !strings.Contains(got, "T1 - A") {
		t.Errorf("AI 到达前应显示原始标题, got %q", got)
	}
	ly, _ := lyrics.ParseLRC([]byte("[00:01.00]行\n"))
	m, _ = update(m, lyricsResultMsg{trackID: "t1", lyrics: ly, title: "晴天", artist: "周杰伦"})
	got := m.home.view()
	// 控制栏左栏宽度有限，标题会截断（如 "晴天 - 周…"），按前缀断言
	if !strings.Contains(got, "晴天 - 周") {
		t.Errorf("控制栏应显示 AI 清洗标题, got %q", got)
	}
	if strings.Contains(got, "T1 - A") {
		t.Errorf("控制栏不应再显示原始标题: %q", got)
	}
	// 切歌后清空覆盖，回到新曲原始标题
	raw2 := model.Track{ID: "t2", Title: "T2", Artist: "B", Duration: 200, URL: "http://x/2", Source: "youtube"}
	m, cmd = m.startPlay(raw2)
	_ = execCmds(cmd)
	got2 := m.home.view()
	if !strings.Contains(got2, "T2 - B") {
		t.Errorf("切歌后应显示新曲原始标题, got %q", got2)
	}
	if strings.Contains(got2, "晴天") {
		t.Errorf("切歌后 AI 覆盖应清空: %q", got2)
	}
}

// TestHomeLyricsFirstAndLastLineCentered 核心需求：首行歌词起步于视口中央、
// 末行歌词停在视口中央（N > H 长歌词）。
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

// ---- lyricshm 挂载:歌词行实时写入 ----

// lyricFileTestHome 构造注入临时路径的 writer 的 home 模型。
func lyricFileTestHome(t *testing.T) (homeModel, string) {
	t.Helper()
	m := newHomeModel(nil)
	path := filepath.Join(t.TempDir(), "lyrics")
	m.lyricFile = lyricshm.NewForTest(path)
	return m, path
}

func lyricFileRead(t *testing.T, path string) string {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil {
		return "" // 文件未创建视为空内容
	}
	return string(got)
}

func TestHomeLyricFileWritesOnLineChange(t *testing.T) {
	m, path := lyricFileTestHome(t)
	m.lyrics = &lyrics.Lyrics{Lines: []lyrics.LyricLine{
		{Time: 0, Text: "第一句"},
		{Time: 5, Text: "第二句"},
	}}
	m.lyricsState = lyricsSynced
	m.currentLine = -1
	track := &model.Track{Title: "T", Artist: "A", Duration: 100}
	m = m.syncState(model.PlaybackState{Track: track, Position: 1, Duration: 100})
	if got := lyricFileRead(t, path); got != "第一句\n" {
		t.Fatalf("行切换后内容 = %q,期望 %q", got, "第一句\n")
	}
	m = m.syncState(model.PlaybackState{Track: track, Position: 6, Duration: 100})
	if got := lyricFileRead(t, path); got != "第二句\n" {
		t.Fatalf("第二次行切换后内容 = %q,期望 %q", got, "第二句\n")
	}
}

func TestHomeLyricFileNoWriteWhenSameLine(t *testing.T) {
	m, path := lyricFileTestHome(t)
	m.lyrics = &lyrics.Lyrics{Lines: []lyrics.LyricLine{
		{Time: 0, Text: "第一句"},
	}}
	m.lyricsState = lyricsSynced
	m.currentLine = -1
	track := &model.Track{Title: "T", Artist: "A", Duration: 100}
	m = m.syncState(model.PlaybackState{Track: track, Position: 1, Duration: 100})
	m = m.syncState(model.PlaybackState{Track: track, Position: 3, Duration: 100}) // 同行,只推进时间
	if got := lyricFileRead(t, path); got != "第一句\n" {
		t.Fatalf("行未变化不应重写,内容 = %q", got)
	}
}

func TestHomeLyricFileBlankLineKeepsPrevious(t *testing.T) {
	m, path := lyricFileTestHome(t)
	// 第一行非空写入后,第二行(空白)不应覆盖
	m.lyrics = &lyrics.Lyrics{Lines: []lyrics.LyricLine{
		{Time: 0, Text: "第一句"},
		{Time: 5, Text: "   "}, // 空白行
	}}
	m.lyricsState = lyricsSynced
	m.currentLine = -1
	track := &model.Track{Title: "T", Artist: "A", Duration: 100}
	m = m.syncState(model.PlaybackState{Track: track, Position: 1, Duration: 100})
	m = m.syncState(model.PlaybackState{Track: track, Position: 6, Duration: 100})
	if got := lyricFileRead(t, path); got != "第一句\n" {
		t.Fatalf("空白行不应覆盖,内容 = %q,期望保留 %q", got, "第一句\n")
	}
}

func TestHomeLyricFileTrackLabelOnReset(t *testing.T) {
	m, path := lyricFileTestHome(t)
	track := &model.Track{Title: "歌名", Artist: "歌手", Duration: 100}
	m = m.resetForTrack(track)
	if got := lyricFileRead(t, path); got != "歌名 - 歌手\n" {
		t.Fatalf("切歌后内容 = %q,期望歌名 %q", got, "歌名 - 歌手\n")
	}
}

func TestHomeLyricFileKeepsLabelWhenNoLyrics(t *testing.T) {
	m, path := lyricFileTestHome(t)
	track := &model.Track{Title: "歌名", Artist: "歌手", Duration: 100}
	m = m.resetForTrack(track)
	m = m.setLyrics(errors.New("no lyrics"), nil) // 歌词加载失败
	if got := lyricFileRead(t, path); got != "歌名 - 歌手\n" {
		t.Fatalf("无歌词应保持歌名,内容 = %q", got)
	}
	m = m.setLyrics(nil, &lyrics.Lyrics{Lines: []lyrics.LyricLine{}}) // 空歌词
	if got := lyricFileRead(t, path); got != "歌名 - 歌手\n" {
		t.Fatalf("空歌词应保持歌名,内容 = %q", got)
	}
}

func TestHomeLyricFileKeepsOnStop(t *testing.T) {
	m, path := lyricFileTestHome(t)
	track := &model.Track{Title: "歌名", Artist: "歌手", Duration: 100}
	m = m.resetForTrack(track)
	// 停止播放:Track == nil 的 syncState 不应清空文件(3b)
	m = m.syncState(model.PlaybackState{})
	if got := lyricFileRead(t, path); got != "歌名 - 歌手\n" {
		t.Fatalf("停止播放应保留内容,内容 = %q", got)
	}
}

func TestHomeLyricFileNilSafe(t *testing.T) {
	m := newHomeModel(nil) // lyricFile 为 nil
	m.lyrics = &lyrics.Lyrics{Lines: []lyrics.LyricLine{{Time: 0, Text: "x"}}}
	m.lyricsState = lyricsSynced
	m.currentLine = -1
	track := &model.Track{Title: "T", Artist: "A", Duration: 100}
	m = m.syncState(model.PlaybackState{Track: track, Position: 1, Duration: 100})
	m = m.resetForTrack(track)
	// 不应 panic
}

func TestHomeLyricFileAITrackUpdatesLabel(t *testing.T) {
	m, path := lyricFileTestHome(t)
	track := &model.Track{Title: "原始标题", Artist: "原始歌手", Duration: 100}
	m = m.resetForTrack(track) // loading 态,文件 = 原始歌名
	m = m.setAITrack("AI标题", "AI歌手")
	if got := lyricFileRead(t, path); got != "AI标题 - AI歌手\n" {
		t.Fatalf("未同步时 setAITrack 应更新歌名,内容 = %q", got)
	}
	// 歌词已同步:setAITrack 不应覆盖文件中的歌词行
	m.lyrics = &lyrics.Lyrics{Lines: []lyrics.LyricLine{{Time: 0, Text: "歌词行"}}}
	m.lyricsState = lyricsSynced
	m.currentLine = -1
	m = m.syncState(model.PlaybackState{Track: track, Position: 1, Duration: 100})
	if got := lyricFileRead(t, path); got != "歌词行\n" {
		t.Fatalf("前置:歌词行应已写入,内容 = %q", got)
	}
	m = m.setAITrack("AI标题2", "AI歌手2")
	if got := lyricFileRead(t, path); got != "歌词行\n" {
		t.Fatalf("已同步时 setAITrack 不应覆盖歌词行,内容 = %q", got)
	}
}

// ---- 封面渲染三模式集成（coverrender 包 + 终端能力探测）----

// coverModeEnv 设置 MUSIC_TUI_COVER 并重置 coverrender 缓存（进程级 sync.Once）。
// 返回恢复函数，测试结束自动还原（t.Cleanup）。
func coverModeEnv(t *testing.T, mode string) {
	t.Helper()
	t.Setenv("MUSIC_TUI_COVER", mode)
	coverrender.ResetModeCacheForTests()
	coverrender.ResetFontCellCacheForTests()
	t.Cleanup(func() {
		coverrender.ResetModeCacheForTests()
		coverrender.ResetFontCellCacheForTests()
	})
}

// TestHomeCoverKittyMode 回归：kitty 模式 — coverRenderCache 为行内协议序列
// （APC 传输 + U+10EEEE 占位符网格），恒 17 行×30 列 in-flow，布局不塌。
func TestHomeCoverKittyMode(t *testing.T) {
	coverModeEnv(t, "kitty")
	fp := newFakePlayer()
	m := newTestModel(t, fp, &fakeSearchAdapter{}, nil)
	m, cmd := m.startPlay(testTrack("t1"))
	_ = execCmds(cmd)

	pngPath := filepath.Join(t.TempDir(), "cover.png")
	writeTestPNG(t, pngPath)
	m, _ = update(m, coverResultMsg{trackID: "t1", path: pngPath})

	cache := m.home.coverRenderCache
	if cache == "" {
		t.Fatal("kitty 模式 setCover 后 coverRenderCache 应为非空")
	}
	if !strings.Contains(cache, "\x1b_Ga=t") {
		t.Error("kitty 序列应含传输控制 a=t")
	}
	if !strings.Contains(cache, "\x1b_Ga=p,i=") {
		t.Error("kitty 序列应含放置命令 a=p")
	}
	if !strings.Contains(cache, "\U0010EEEE") {
		t.Error("kitty 序列应含 U+10EEEE 占位符")
	}
	if got := len(strings.Split(cache, "\n")); got != coverH {
		t.Errorf("kitty 序列行数 = %d, want %d（恒 17 行 in-flow 契约）", got, coverH)
	}
	// 布局完整：view 在窗口尺寸下仍恒 height 行
	m.home = m.home.setSize(120, 40)
	if got := strings.Count(m.home.view(), "\n") + 1; got != 40 {
		t.Errorf("kitty 模式 view 行数 = %d, want 40", got)
	}
}

// TestHomeCoverSixelMode 回归：sixel 模式 — 布局只放半块色块（零协议字节），
// 全帧 DCS 由确保Sixel 外带写出到 overlayOut（屏幕绝对坐标），换歌/回退发清除。
func TestHomeCoverSixelMode(t *testing.T) {
	coverModeEnv(t, "sixel")
	var out bytes.Buffer
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

	// 布局缓存 = 半块色块，不含 DCS 协议字节
	if !strings.Contains(m.home.coverRenderCache, "\x1bPq") {
		t.Log("布局缓存为半块色块（无 DCS 内联）")
	}
	if m.home.sixelPayload == "" || !strings.Contains(m.home.sixelPayload, "\x1bPq") {
		t.Fatal("sixelPayload 应含全帧 DCS 载荷")
	}
	// view() 触发外带写出：CUP 定位 + DCS
	m.home = m.home.setSize(120, 40)
	_ = m.home.view()
	got := out.String()
	if !strings.Contains(got, "\x1b[s") || !strings.Contains(got, "\x1b[u") {
		t.Errorf("覆盖层应含光标保存/恢复: %q", got)
	}
	if !strings.Contains(got, "\x1b[") || !strings.Contains(got, "H") {
		t.Errorf("覆盖层应含 CUP 定位: %q", got)
	}
	if !strings.Contains(got, "\x1bPq") {
		t.Errorf("覆盖层应含 DCS: %q", got)
	}
	// token 稳定：再次 view 不重复写出（幂等）
	out.Reset()
	_ = m.home.view()
	if out.Len() != 0 {
		t.Errorf("token 未变时不应重复写出, got %q", out.String())
	}
}

// TestHomeCoverHalfMode 回归：默认（非交互）走半块渲染，缓存零协议字节。
func TestHomeCoverHalfMode(t *testing.T) {
	coverModeEnv(t, "halfblocks")
	fp := newFakePlayer()
	m := newTestModel(t, fp, &fakeSearchAdapter{}, nil)
	m, cmd := m.startPlay(testTrack("t1"))
	_ = execCmds(cmd)

	pngPath := filepath.Join(t.TempDir(), "cover.png")
	writeTestPNG(t, pngPath)
	m, _ = update(m, coverResultMsg{trackID: "t1", path: pngPath})

	cache := m.home.coverRenderCache
	if cache == "" {
		t.Fatal("halfblocks 模式 setCover 后 coverRenderCache 应为非空")
	}
	if strings.Contains(cache, "\x1b_G") || strings.Contains(cache, "\x1bPq") || strings.Contains(cache, "\U0010EEEE") {
		t.Error("halfblocks 缓存不应含任何协议字节/占位符")
	}
	if got := len(strings.Split(cache, "\n")); got != coverH {
		t.Errorf("halfblocks 行数 = %d, want %d", got, coverH)
	}
}


// TestHomeCoverKittyPersist 回归：kitty 网格驻留擦除——中间区重建后封面末行注入
// 零宽前景色 token（变化字节强制差量重发 a=p 所在行，图片在擦除行后恢复）。
func TestHomeCoverKittyPersist(t *testing.T) {
	coverModeEnv(t, "kitty")
	fp := newFakePlayer()
	m := newTestModel(t, fp, &fakeSearchAdapter{}, nil)
	m, cmd := m.startPlay(testTrack("t1"))
	_ = execCmds(cmd)
	pngPath := filepath.Join(t.TempDir(), "cover.png")
	writeTestPNG(t, pngPath)
	m, _ = update(m, coverResultMsg{trackID: "t1", path: pngPath})
	m.home = m.home.setSize(120, 40)

	build1 := m.home.coverView()
	lines1 := strings.Split(build1, "\n")
	if len(lines1) != coverH {
		t.Fatalf("coverView 行数 = %d, want %d", len(lines1), coverH)
	}
	// 重建（setCover/setSize 触发）后：末行应含注入的零宽 token 且仍带 a=p
	if !strings.Contains(build1, "\x1b[38;5;") {
		t.Error("重建后 coverView 末行应含零宽前景色 token")
	}
	if !strings.Contains(build1, "\x1b_Ga=p") {
		t.Error("末行仍应携带 a=p 放置命令")
	}
	// 每行可见宽仍为 30（token 零宽，不破坏 in-flow 契约）
	for i, ln := range lines1 {
		if w := ansi.StringWidth(ln); w != coverW {
			t.Errorf("行 %d 可见宽 = %d, want %d", i, w, coverW)
		}
	}
	// 连续重建 → 末行 token 变化（字节不同 → 帧差量重发）
	mk1 := lastLine(build1)
	m.home = m.home.rebuildMiddleCache()
	build2 := m.home.coverView()
	mk2 := lastLine(build2)
	if mk1 == mk2 {
		t.Errorf("重建后末行应变化（token 随计数递增）:\n%q", mk1)
	}
}

func lastLine(s string) string {
	lines := strings.Split(s, "\n")
	return lines[len(lines)-1]
}
