package ui

import (
	"bytes"
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

	"music-tui/lyrics"
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

	// 纯文本歌词
	m, _ = update(m, lyricsResultMsg{trackID: "t1", lyrics: &lyrics.Lyrics{Plain: "纯文本歌词"}})
	if m.home.lyricsState != lyricsPlain {
		t.Fatalf("state = %v, want lyricsPlain", m.home.lyricsState)
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
		Y:      m.home.height, // 屏幕 Y = height（Tab 2 行）→ 页面 Y = height-2（进度条行）
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
		Y:      m.home.height,
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
		Y:      m.home.height,
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
			Y:      m.home.height + 1, // 屏幕 Y = height+1（Tab 2 行）→ 页面 Y = height-1（按钮行）
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
	if got := m.home.lyricView.YOffset; got != 19 { // LineAt(155)=29 → 29-10
		t.Errorf("YOffset = %d, want 19", got)
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

	// 第 30 行（0-based 29，155s）：offset = 29 - 10 = 19 → 当前行在视口第 10 行（正中）
	m, _ = update(m, playerEventMsg{ev: player.ProgressEvent{Position: 155, Duration: 2000}})
	if got := m.home.lyricView.YOffset; got != 19 {
		t.Errorf("第 30 行 YOffset = %d, want 19（视口正中）", got)
	}
	// 第 50 行（idx 49）：offset = 49-10 = 39 = maxYOffset（59-21+1？60-21=39）→ 底部
	m, _ = update(m, playerEventMsg{ev: player.ProgressEvent{Position: 495, Duration: 2000}})
	if got := m.home.lyricView.YOffset; got != 39 {
		t.Errorf("第 50 行 YOffset = %d, want 39（maxYOffset）", got)
	}
	// 首行（第一行 10s 后）：offset = 0-10 → clamp 到 0
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
	// 先滚动到中间（第 30 行居中，YOffset 19）
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
		if len(lines) != sz[1]-3 {
			t.Errorf("%dx%d: 行数 = %d, want %d", sz[0], sz[1], len(lines), sz[1]-3)
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
