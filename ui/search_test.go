package ui

import (
	"errors"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"music-tui/model"
)

func TestSearchTypingAndEnter(t *testing.T) {
	fp := newFakePlayer()
	fa := &fakeSearchAdapter{tracks: []model.Track{testTrack("t1")}}
	m := newTestModel(t, fp, fa, nil)

	m, _ = update(m, tea.KeyPressMsg{Text: "4", Code: '4'}) // 数字键直达搜索页
	if m.current != pageSearch {
		t.Fatal("按 4 后应在搜索页")
	}
	m, _ = update(m, tea.KeyPressMsg{Text: "晴天"})
	if got := m.searchPage.input.Value(); got != "晴天" {
		t.Fatalf("input = %q", got)
	}
	m, cmd := update(m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if m.searchPage.state != searchLoading {
		t.Fatalf("state = %v, want searchLoading", m.searchPage.state)
	}
	msgs := execSearchCmds(cmd)
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
	m, _ = update(m, tea.KeyPressMsg{Text: "4", Code: '4'}) // 数字键直达搜索页
	m, cmd := update(m, tea.KeyPressMsg{Code: tea.KeyEnter})
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
	m, _ = update(m, tea.KeyPressMsg{Text: "4", Code: '4'}) // 数字键直达搜索页
	m, _ = update(m, tea.KeyPressMsg{Text: "x", Code: 'x'})
	m, cmd := update(m, tea.KeyPressMsg{Code: tea.KeyEnter})
	msgs := execSearchCmds(cmd)
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
	m, _ = update(m, tea.KeyPressMsg{Text: "4", Code: '4'}) // 数字键直达搜索页
	m, _ = update(m, tea.KeyPressMsg{Text: "zzz"})
	m, cmd := update(m, tea.KeyPressMsg{Code: tea.KeyEnter})
	msgs := execSearchCmds(cmd)
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
	m, _ = update(m, tea.KeyPressMsg{Text: "4", Code: '4'}) // 数字键直达搜索页
	m, cmd := update(m, tea.KeyPressMsg{Text: "晴天"})
	_ = execCmds(cmd)
	m, cmd = update(m, tea.KeyPressMsg{Code: tea.KeyEnter})
	msgs := execSearchCmds(cmd)
	var res searchResultsMsg
	for _, msg := range msgs {
		if sm, ok := msg.(searchResultsMsg); ok {
			res = sm
		}
	}
	m, _ = update(m, res)

	// 下移选中第二项后 Enter
	m, _ = update(m, tea.KeyPressMsg{Code: tea.KeyDown})
	m, cmd = update(m, tea.KeyPressMsg{Code: tea.KeyEnter})
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

func TestSearchEscClearsResults(t *testing.T) {
	fa := &fakeSearchAdapter{tracks: []model.Track{testTrack("t1"), testTrack("t2")}}
	m := newTestModel(t, newFakePlayer(), fa, nil)
	m, _ = update(m, tea.KeyPressMsg{Text: "4", Code: '4'}) // 数字键直达搜索页
	m, cmd := update(m, tea.KeyPressMsg{Text: "晴天"})
	_ = execCmds(cmd)
	m, cmd = update(m, tea.KeyPressMsg{Code: tea.KeyEnter})
	msgs := execSearchCmds(cmd)
	var res searchResultsMsg
	for _, msg := range msgs {
		if sm, ok := msg.(searchResultsMsg); ok {
			res = sm
		}
	}
	m, _ = update(m, res)
	if m.searchPage.state != searchDone || len(m.searchPage.results) != 2 {
		t.Fatalf("前置: state = %v, results = %d, want searchDone/2", m.searchPage.state, len(m.searchPage.results))
	}

	// Esc 返回输入框：结果清空、文字保留、状态复位
	// 注：bubbles v1 的 input.Focus() 返回 cursor blink cmd（纯 UI 副作用），
	// 与既有测试惯例（root_test.go 中忽略 esc cmd）一致，此处忽略。
	m, _ = update(m, tea.KeyPressMsg{Code: tea.KeyEsc})
	if m.searchPage.state != searchIdle {
		t.Fatalf("state = %v, want searchIdle", m.searchPage.state)
	}
	if len(m.searchPage.results) != 0 {
		t.Fatalf("results = %d, want 0", len(m.searchPage.results))
	}
	if n := len(m.searchPage.list.Items()); n != 0 {
		t.Fatalf("list items = %d, want 0", n)
	}
	if !m.searchPage.input.Focused() {
		t.Error("Esc 后输入框应聚焦")
	}
	if got := m.searchPage.input.Value(); got != "晴天" {
		t.Fatalf("input = %q, want 保留 晴天", got)
	}

	// 子用例：清空后 Enter 可立即重新搜索（adapter 再次被调用）
	m, cmd = update(m, tea.KeyPressMsg{Code: tea.KeyEnter})
	_ = execSearchCmds(cmd)
	if fa.calls != 2 {
		t.Fatalf("adapter calls = %d, want 2（清空后可重新搜索）", fa.calls)
	}
}
