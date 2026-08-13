package ui

import (
	"errors"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"music-tui/model"
)

func TestSearchTypingAndEnter(t *testing.T) {
	fp := newFakePlayer()
	fa := &fakeSearchAdapter{tracks: []model.Track{testTrack("t1")}}
	m := newTestModel(t, fp, fa, nil)

	m, _ = update(m, tea.KeyMsg{Type: tea.KeyTab})
	if m.current != pageSearch {
		t.Fatal("Tab 后应在搜索页")
	}
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("晴天")})
	if got := m.searchPage.input.Value(); got != "晴天" {
		t.Fatalf("input = %q", got)
	}
	m, cmd := update(m, tea.KeyMsg{Type: tea.KeyEnter})
	if m.searchPage.state != searchLoading {
		t.Fatalf("state = %v, want searchLoading", m.searchPage.state)
	}
	msgs := execCmds(cmd)
	if fa.calls != 1 {
		t.Fatalf("adapter calls = %d, want 1", fa.calls)
	}
	var res searchResultsMsg
	for _, msg := range msgs {
		if sm, ok := msg.(searchResultsMsg); ok {
			res = sm
		}
	}
	m, _ = update(m, res)
	if m.searchPage.state != searchDone {
		t.Fatalf("state = %v, want searchDone", m.searchPage.state)
	}
	if len(m.searchPage.results) != 1 {
		t.Fatalf("results = %+v", m.searchPage.results)
	}
	if m.searchPage.input.Focused() {
		t.Error("结果到达后输入框应失焦（方便列表操作）")
	}
}

func TestSearchEmptyQueryIgnored(t *testing.T) {
	fa := &fakeSearchAdapter{}
	m := newTestModel(t, newFakePlayer(), fa, nil)
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyTab})
	m, cmd := update(m, tea.KeyMsg{Type: tea.KeyEnter})
	if cmd != nil {
		t.Error("空查询不应触发搜索")
	}
	if fa.calls != 0 {
		t.Error("adapter 不应被调用")
	}
}

func TestSearchErrorState(t *testing.T) {
	fa := &fakeSearchAdapter{err: errors.New("yt-dlp 搜索失败")}
	m := newTestModel(t, newFakePlayer(), fa, nil)
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyTab})
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("x")})
	m, cmd := update(m, tea.KeyMsg{Type: tea.KeyEnter})
	msgs := execCmds(cmd)
	var res searchResultsMsg
	for _, msg := range msgs {
		if sm, ok := msg.(searchResultsMsg); ok {
			res = sm
		}
	}
	m, _ = update(m, res)
	if m.searchPage.state != searchDone || !strings.Contains(m.searchPage.err, "yt-dlp") {
		t.Fatalf("state = %v, err = %q", m.searchPage.state, m.searchPage.err)
	}
	if !m.searchPage.input.Focused() {
		t.Error("失败后应回到输入框便于重试")
	}
}

func TestSearchEmptyResults(t *testing.T) {
	fa := &fakeSearchAdapter{}
	m := newTestModel(t, newFakePlayer(), fa, nil)
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyTab})
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("zzz")})
	m, cmd := update(m, tea.KeyMsg{Type: tea.KeyEnter})
	msgs := execCmds(cmd)
	var res searchResultsMsg
	for _, msg := range msgs {
		if sm, ok := msg.(searchResultsMsg); ok {
			res = sm
		}
	}
	m, _ = update(m, res)
	if m.searchPage.state != searchDone || len(m.searchPage.results) != 0 {
		t.Fatalf("state = %v, results = %d", m.searchPage.state, len(m.searchPage.results))
	}
}

func TestSearchEnterPlaysSelected(t *testing.T) {
	fa := &fakeSearchAdapter{tracks: []model.Track{testTrack("t1"), testTrack("t2")}}
	m := newTestModel(t, newFakePlayer(), fa, nil)
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyTab})
	m, cmd := update(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("晴天")})
	_ = execCmds(cmd)
	m, cmd = update(m, tea.KeyMsg{Type: tea.KeyEnter})
	msgs := execCmds(cmd)
	var res searchResultsMsg
	for _, msg := range msgs {
		if sm, ok := msg.(searchResultsMsg); ok {
			res = sm
		}
	}
	m, _ = update(m, res)

	// 下移选中第二项后 Enter
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyDown})
	m, cmd = update(m, tea.KeyMsg{Type: tea.KeyEnter})
	msgs = execCmds(cmd)
	var sel trackSelectedMsg
	for _, msg := range msgs {
		if sm, ok := msg.(trackSelectedMsg); ok {
			sel = sm
		}
	}
	if sel.track.ID != "t2" {
		t.Fatalf("selected = %s, want t2", sel.track.ID)
	}
}
