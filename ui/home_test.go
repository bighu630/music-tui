package ui

import (
	"bytes"
	"errors"
	"image"
	"image/png"
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

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
	if m.home.coverWidget != nil || !m.home.coverFallback {
		t.Error("下载失败应显示占位框")
	}

	// 成功 → 创建 widget
	m, _ = update(m, coverResultMsg{trackID: "t1", path: pngPath})
	if m.home.coverWidget == nil || m.home.coverFallback {
		t.Error("下载成功应创建 coverWidget")
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
	if m.home.coverWidget == nil {
		t.Fatal("封面下载成功应创建 widget")
	}
	// setCover 后应立即渲染并缓存（避免 view 每帧重复 Render）
	if m.home.coverRenderCache == "" {
		t.Fatal("setCover 后 coverRenderCache 应为非空")
	}
	if m.home.coverFailed {
		t.Error("渲染成功后 coverFailed 应为 false")
	}
	if got := m.home.coverView(); got != m.home.coverRenderCache {
		t.Error("coverView 应直接返回缓存")
	}

	// setSize 失效并立即重试渲染一次（结果赋回模型）：缓存回填后仍命中
	m.home = m.home.setSize(120, 40)
	if m.home.coverRenderCache == "" {
		t.Error("setSize 后应立即重渲并回填 coverRenderCache")
	}
	if m.home.coverFailed {
		t.Error("setSize 重渲成功后 coverFailed 应为 false")
	}
	if got := m.home.coverView(); got != m.home.coverRenderCache {
		t.Error("setSize 后 coverView 应直接返回缓存")
	}
}

// failCover 渲染总是失败的 fake 封面 widget（用于验证首次 Render 失败后
// coverView 不再每帧重试；渲染成功后可翻转）。
type failCover struct {
	mu    sync.Mutex
	calls int
	fail  bool // true 时 Render 返回错误
	out   string
}

func (f *failCover) Render() (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if f.fail {
		return "", errors.New("render boom")
	}
	return f.out, nil
}

func (f *failCover) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

// setFail 切换渲染失败/成功模式。
func (f *failCover) setFail(fail bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.fail = fail
}

// TestHomeCoverRenderFailureStopsPerFrameRetry 守护：setCover 首次 Render
// 失败（等价状态：widget 存在、缓存为空、coverFailed=true）后，coverView
// 不再每帧重试（16MiB 解码+缩放）；仅 setSize 时允许重试一次，再失败重新置位。
func TestHomeCoverRenderFailureStopsPerFrameRetry(t *testing.T) {
	fp := newFakePlayer()
	m := newTestModel(t, fp, &fakeSearchAdapter{}, nil)
	m, cmd := m.startPlay(testTrack("t1"))
	_ = execCmds(cmd)

	// 构造 setCover 首次 Render 失败后的状态
	fc := &failCover{fail: true}
	m.home.coverWidget = fc
	m.home.coverRenderCache = ""
	m.home.coverFailed = true

	// 失败标记置位：coverView 应直接占位框，不触发任何 Render
	if got := m.home.coverView(); got == "" || !strings.Contains(got, "No Cover") {
		t.Errorf("coverView 应显示占位框, got %q", got)
	}
	if n := fc.callCount(); n != 0 {
		t.Errorf("coverFailed 置位后 coverView 不应重试 Render, calls = %d", n)
	}

	// setSize 失效缓存并重置失败标记 → 仅重试一次，仍失败则重新置位
	m.home = m.home.setSize(120, 40)
	if got := m.home.coverView(); got == "" || !strings.Contains(got, "No Cover") {
		t.Errorf("重试失败后 coverView 应显示占位框, got %q", got)
	}
	if n := fc.callCount(); n != 1 {
		t.Errorf("setSize 后应只重试一次 Render, calls = %d", n)
	}
	if !m.home.coverFailed {
		t.Error("重试仍失败应重新置位 coverFailed")
	}

	// 再次渲染（如下一帧）不得再触发 Render
	_ = m.home.coverView()
	if n := fc.callCount(); n != 1 {
		t.Errorf("重试失败后 coverView 不应再每帧重试, calls = %d", n)
	}
}

// TestHomeCoverRenderRetryAfterSetSizeRecovers 守护：setSize 重试一次成功
// 后应回填缓存并正常展示，后续 view 不再触发 Render。
func TestHomeCoverRenderRetryAfterSetSizeRecovers(t *testing.T) {
	fp := newFakePlayer()
	m := newTestModel(t, fp, &fakeSearchAdapter{}, nil)
	m, cmd := m.startPlay(testTrack("t1"))
	_ = execCmds(cmd)

	fc := &failCover{fail: true, out: "▶" + strings.Repeat("\n", 4)}
	m.home.coverWidget = fc
	m.home.coverRenderCache = ""
	m.home.coverFailed = true

	// setSize 触发一次重试：成功 → 缓存回填，coverFailed 复位
	fc.setFail(false)
	m.home = m.home.setSize(120, 40)
	if got := m.home.coverView(); got == "" || !strings.Contains(got, "▶") {
		t.Errorf("重试成功后 coverView 应返回渲染结果, got %q", got)
	}
	if m.home.coverFailed {
		t.Error("重试成功后 coverFailed 应为 false")
	}
	if m.home.coverRenderCache == "" {
		t.Error("重试成功后应回填 coverRenderCache")
	}
	if n := fc.callCount(); n != 1 {
		t.Errorf("重试成功应只渲染一次, calls = %d", n)
	}
	// 命中缓存：后续 view 不再触发 Render
	_ = m.home.coverView()
	_ = m.home.coverView()
	if n := fc.callCount(); n != 1 {
		t.Errorf("命中缓存后不应再 Render, calls = %d", n)
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
	btnLine := lines[len(lines)-1]
	for _, icon := range []string{"⏮", "⏯", "⏭", "🔁"} {
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

	// 有队列信息时展示 "位置/总数 · 模式名"
	m.home = m.home.setQueueInfo(3, 12, queue.Sequential)
	btnLine = strings.Split(m.home.view(), "\n")[len(lines)-1]
	if !strings.Contains(btnLine, "3/12 · 顺序") {
		t.Errorf("按钮行应含队列信息 3/12 · 顺序: %q", btnLine)
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
		Y:      m.home.height - 1, // 屏幕 Y = height-1 → 页面 Y = height-2（进度条行）
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
		Y:      m.home.height - 1,
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
		Y:      m.home.height - 1,
	})
	if cmd != nil {
		t.Error("无曲目时点击进度条不应产生命令")
	}
}

// TestHomeMouseButtonClick 点击按钮行（页面 Y == height-1）按 X 区间触发
// 对应动作：⏮[0,3) ⏯[4,7) ⏭[8,11) 模式[13,16)；空白处与中间区忽略。
func TestHomeMouseButtonClick(t *testing.T) {
	fp := newFakePlayer()
	m := newTestModel(t, fp, &fakeSearchAdapter{}, nil)
	m, cmd := m.startPlay(testTrack("t1"))
	_ = execCmds(cmd)
	m.home = m.home.setSize(120, 40)

	press := func(x int) tea.Cmd {
		_, c := update(m, tea.MouseMsg{
			Action: tea.MouseActionPress,
			Button: tea.MouseButtonLeft,
			X:      x,
			Y:      m.home.height, // 屏幕 Y = height → 页面 Y = height-1（按钮行）
		})
		return c
	}

	msgs := execCmds(press(0)) // ⏮ [0,3)
	if len(msgs) != 1 {
		t.Fatalf("X=0 应命中 ⏮ 按钮, msgs=%v", msgs)
	}
	if _, ok := msgs[0].(prevTrackMsg); !ok {
		t.Errorf("X=0 消息类型 = %T, want prevTrackMsg", msgs[0])
	}

	msgs = execCmds(press(5)) // ⏯ [4,7)
	if len(msgs) != 1 {
		t.Fatalf("X=5 应命中 ⏯ 按钮, msgs=%v", msgs)
	}
	if _, ok := msgs[0].(togglePlayMsg); !ok {
		t.Errorf("X=5 消息类型 = %T, want togglePlayMsg", msgs[0])
	}

	msgs = execCmds(press(9)) // ⏭ [8,11)
	if len(msgs) != 1 {
		t.Fatalf("X=9 应命中 ⏭ 按钮, msgs=%v", msgs)
	}
	if _, ok := msgs[0].(nextTrackMsg); !ok {
		t.Errorf("X=9 消息类型 = %T, want nextTrackMsg", msgs[0])
	}

	msgs = execCmds(press(14)) // 模式 [13,16)
	if len(msgs) != 1 {
		t.Fatalf("X=14 应命中模式按钮, msgs=%v", msgs)
	}
	if _, ok := msgs[0].(toggleModeMsg); !ok {
		t.Errorf("X=14 消息类型 = %T, want toggleModeMsg", msgs[0])
	}

	// 间距/空白处（3、7、12）与远端（100）不命中任何按钮
	for _, x := range []int{3, 7, 12, 100} {
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
