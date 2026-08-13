package ui

import (
	"bytes"
	"errors"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"music-tui/lyrics"
	"music-tui/model"
	"music-tui/player"
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
	m := newTestModel(t, fp, &fakeSearchAdapter{})
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
	m := newTestModel(t, fp, &fakeSearchAdapter{})
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
	m := newTestModel(t, fp, &fakeSearchAdapter{})
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
	m := newTestModel(t, fp, &fakeSearchAdapter{})
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
	m := newTestModel(t, fp, &fakeSearchAdapter{})
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
	m := newTestModel(t, fp, &fakeSearchAdapter{})
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
	m := newTestModel(t, newFakePlayer(), &fakeSearchAdapter{})
	if got := m.home.view(); got == "" || !strings.Contains(got, "未在播放") {
		t.Errorf("view 应提示未在播放，got %q", got)
	}
}
