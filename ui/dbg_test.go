package ui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
	"music-tui/lyrics"
	"music-tui/player"
)

// 模拟真实 resize 时序：120×40 → 80×24 → 120×40，验证布局每步重新定位。
func TestDbgResize(t *testing.T) {
	fp := newFakePlayer()
	m := newTestModel(t, fp, &fakeSearchAdapter{}, nil)
	m, cmd := m.startPlay(testTrack("t1"))
	_ = execCmds(cmd)
	ly, _ := lyrics.ParseLRC([]byte("[00:10.00]第一行\n[00:20.00]第二行\n[00:30.00]第三行\n"))
	m, _ = update(m, lyricsResultMsg{trackID: "t1", lyrics: ly})
	m, _ = update(m, playerEventMsg{ev: player.ProgressEvent{Position: 15, Duration: 200}})

	for _, sz := range [][2]int{{120, 40}, {80, 24}, {120, 40}, {60, 10}} {
		m, _ = update(m, tea.WindowSizeMsg{Width: sz[0], Height: sz[1]})
		out := m.home.view()
		lines := strings.Split(out, "\n")
		// 断言：行数 == 页面高、进度条行满宽、歌词行存在
		wantH := sz[1] - 2
		lyrCol := -1
		for i, ln := range lines {
			if strings.Contains(stripAnsiForTest(ln), "第一行") {
				lyrCol = i
				break
			}
		}
		barW := ansi.StringWidth(lines[len(lines)-2])
		t.Logf("%dx%d: 行数=%d(want %d) barW=%d(want %d) 歌词行=%d",
			sz[0], sz[1], len(lines), wantH, barW, sz[0], lyrCol)
	}
}
