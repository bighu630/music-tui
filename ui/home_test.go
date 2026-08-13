package ui

import (
	"bytes"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"strings"
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
	if got := m.home.coverView(); got != m.home.coverRenderCache {
		t.Error("coverView 应直接返回缓存")
	}

	// setSize 失效 → coverView 重新渲染（仍可用、非空）
	m.home = m.home.setSize(120, 40)
	if m.home.coverRenderCache != "" {
		t.Error("setSize 后 coverRenderCache 应失效（置空）")
	}
	if got := m.home.coverView(); got == "" {
		t.Error("缓存失效后 coverView 应重新渲染封面")
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
