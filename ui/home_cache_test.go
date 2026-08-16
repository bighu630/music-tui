package ui

import (
	"image"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"music-tui/lyrics"
	"music-tui/player"
)

// ---- 中间区渲染缓存（P1-2）：播放中每帧省去 3 个全屏 Place ----
// 缓存内容仅随封面/歌词/尺寸变化；下列测试覆盖每个失效点（漏失效 = 陈旧渲染）。

// 歌词行切换（rebuildLyrics）必须使缓存失效：进度推进到下一行后中间区
// 反映新高亮行（缓存不失效会永久显示旧行）。
func TestMiddleViewCacheTracksLyricsLineChange(t *testing.T) {
	fp := newFakePlayer()
	m := newTestModel(t, fp, &fakeSearchAdapter{}, nil)
	m, cmd := m.startPlay(testTrack("t1"))
	_ = execCmds(cmd)
	ly, _ := lyrics.ParseLRC([]byte("[00:10.00]第一行\n[00:20.00]第二行\n"))
	m, _ = update(m, lyricsResultMsg{trackID: "t1", lyrics: ly})
	m, _ = update(m, playerEventMsg{ev: player.ProgressEvent{Position: 12}})
	if got := m.home.middleView(); !strings.Contains(got, lyricActiveStyle.Render("第一行")) {
		t.Fatalf("前提：第一行应高亮, got %q", got)
	}
	// 推进到第二行：缓存必须失效重建
	m, _ = update(m, playerEventMsg{ev: player.ProgressEvent{Position: 22}})
	if got := m.home.middleView(); !strings.Contains(got, lyricActiveStyle.Render("第二行")) {
		t.Fatalf("行切换后中间区应显示新高亮（缓存未失效）, got %q", got)
	}
}

// 滚轮滚动歌词视口（直接 SetYOffset，不经 rebuildLyrics）必须使缓存失效。
func TestMiddleViewCacheInvalidatedByWheel(t *testing.T) {
	fp := newFakePlayer()
	m := newTestModel(t, fp, &fakeSearchAdapter{}, nil)
	m, _ = update(m, tea.WindowSizeMsg{Width: 120, Height: 24})
	m, cmd := m.startPlay(testTrack("t1"))
	_ = execCmds(cmd)
	ly, _ := lyrics.ParseLRC([]byte("[00:10.00]第一行\n[00:20.00]第二行\n[00:30.00]第三行\n"))
	m, _ = update(m, lyricsResultMsg{trackID: "t1", lyrics: ly})
	m, _ = update(m, playerEventMsg{ev: player.ProgressEvent{Position: 12}})
	before := m.home.middleView()
	// 滚轮下滚 3 行（歌词区：X ≥ coverW+2，页面 Y 在中间区）
	m, _ = update(m, tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonWheelDown, X: coverW + 5, Y: 8})
	after := m.home.middleView()
	if before == after {
		t.Fatal("滚轮滚动后中间区应变化（缓存未失效）")
	}
}

// 窗口尺寸变化（setSize）必须使缓存失效（中间区尺寸/裁剪逻辑随尺寸变化）。
func TestMiddleViewCacheInvalidatedByResize(t *testing.T) {
	fp := newFakePlayer()
	m := newTestModel(t, fp, &fakeSearchAdapter{}, nil)
	m, _ = update(m, tea.WindowSizeMsg{Width: 120, Height: 24})
	m, cmd := m.startPlay(testTrack("t1"))
	_ = execCmds(cmd)
	before := m.home.middleView()
	m, _ = update(m, tea.WindowSizeMsg{Width: 100, Height: 24})
	after := m.home.middleView()
	if before == after {
		t.Fatal("窗口尺寸变化后中间区应变化（缓存未失效）")
	}
}

// 封面结果到达（setCover）必须使缓存失效：占位框 → 像素封面。
func TestMiddleViewCacheInvalidatedByCover(t *testing.T) {
	fp := newFakePlayer()
	m := newTestModel(t, fp, &fakeSearchAdapter{}, nil)
	m, cmd := m.startPlay(testTrack("t1"))
	_ = execCmds(cmd)
	placeholder := m.home.middleView()
	// 写入 8x8 PNG 临时文件并回灌封面结果
	pngPath := filepath.Join(t.TempDir(), "cover.png")
	f, err := os.Create(pngPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := png.Encode(f, image.NewRGBA(image.Rect(0, 0, 8, 8))); err != nil {
		t.Fatal(err)
	}
	f.Close()
	m, _ = update(m, coverResultMsg{trackID: "t1", path: pngPath})
	if !m.home.coverFallback && m.home.coverRenderCache != "" {
		// 前提：封面已渲染（非占位框）
	} else {
		t.Fatal("前提：封面结果应成功渲染")
	}
	covered := m.home.middleView()
	if placeholder == covered {
		t.Fatal("封面到达后中间区应变化（缓存未失效）")
	}
}

// 同秒内进度推进（行未变）不得触发中间区重建：缓存命中时输出与首帧一致
//（进度推进只影响进度行，中间区不变——缓存正确性的核心场景）。
func TestMiddleViewCacheStableWithinLine(t *testing.T) {
	fp := newFakePlayer()
	m := newTestModel(t, fp, &fakeSearchAdapter{}, nil)
	m, cmd := m.startPlay(testTrack("t1"))
	_ = execCmds(cmd)
	ly, _ := lyrics.ParseLRC([]byte("[00:10.00]第一行\n[00:20.00]第二行\n"))
	m, _ = update(m, lyricsResultMsg{trackID: "t1", lyrics: ly})
	m, _ = update(m, playerEventMsg{ev: player.ProgressEvent{Position: 12}})
	first := m.home.middleView()
	// 同歌词行内推进（14s 仍属"第一行"）：中间区应保持一致
	m, _ = update(m, playerEventMsg{ev: player.ProgressEvent{Position: 14}})
	if got := m.home.middleView(); got != first {
		t.Fatal("同歌词行内中间区应稳定（缓存未命中或内容漂移）")
	}
}

// 缓存命中路径：行定位/内容变化时填充缓存；同歌词行内推进不重建。
func TestMiddleViewCacheHitWithinLine(t *testing.T) {
	fp := newFakePlayer()
	m := newTestModel(t, fp, &fakeSearchAdapter{}, nil)
	m, _ = update(m, tea.WindowSizeMsg{Width: 120, Height: 24})
	m, cmd := m.startPlay(testTrack("t1"))
	_ = execCmds(cmd)
	ly, _ := lyrics.ParseLRC([]byte("[00:10.00]第一行\n[00:20.00]第二行\n"))
	m, _ = update(m, lyricsResultMsg{trackID: "t1", lyrics: ly})
	m, _ = update(m, playerEventMsg{ev: player.ProgressEvent{Position: 12}})
	first := m.home.middleCache
	if first == "" {
		t.Fatal("歌词行定位后中间区缓存应已填充")
	}
	// 同歌词行内推进：缓存不被重建
	m, _ = update(m, playerEventMsg{ev: player.ProgressEvent{Position: 14}})
	if m.home.middleCache != first {
		t.Fatal("同歌词行内推进不应重建中间区缓存")
	}
	// 行切换：缓存重建（内容变化）
	m, _ = update(m, playerEventMsg{ev: player.ProgressEvent{Position: 22}})
	if m.home.middleCache == first {
		t.Fatal("歌词行切换后缓存应重建")
	}
}
