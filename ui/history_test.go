package ui

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"music-tui/history"
	"music-tui/model"
)

// historyEntry 构造一条带播放时间的历史记录。
func historyEntry(tr model.Track) history.Entry {
	return history.Entry{Track: tr, PlayedAt: time.Now()}
}

func TestHistoryReplayDeleteClear(t *testing.T) {
	fp := newFakePlayer()
	m := newTestModel(t, fp, &fakeSearchAdapter{}, nil)
	tr1, tr2 := testTrack("t1"), testTrack("t2")
	if err := m.history.Add(tr1); err != nil {
		t.Fatal(err)
	}
	if err := m.history.Add(tr2); err != nil {
		t.Fatal(err)
	}
	m = m.refreshHistory()

	m, _ = update(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("3")})
	if m.current != pageHistory {
		t.Fatal("按 3 后应在历史页")
	}
	if len(m.historyPage.entries) != 2 {
		t.Fatalf("entries = %d, want 2", len(m.historyPage.entries))
	}

	// Enter 重播选中项（第一项 t2）
	m, cmd := update(m, tea.KeyMsg{Type: tea.KeyEnter})
	msgs := execCmds(cmd)
	var sel trackSelectedMsg
	for _, msg := range msgs {
		if sm, ok := msg.(trackSelectedMsg); ok {
			sel = sm
		}
	}
	if sel.track.ID != "t2" {
		t.Fatalf("selected = %s, want t2", sel.track.ID)
	}

	// d 删除选中项
	m, cmd = update(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("d")})
	msgs = execCmds(cmd)
	var del deleteEntryMsg
	for _, msg := range msgs {
		if dm, ok := msg.(deleteEntryMsg); ok {
			del = dm
		}
	}
	m, _ = update(m, del)
	if entries := m.history.Entries(); len(entries) != 1 || entries[0].Track.ID != "t1" {
		t.Fatalf("删除后 history = %+v", entries)
	}
	if len(m.historyPage.entries) != 1 {
		t.Error("删除后页面未刷新")
	}

	// c 清空
	m, cmd = update(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("c")})
	msgs = execCmds(cmd)
	var clr clearHistoryMsg
	for _, msg := range msgs {
		if cm, ok := msg.(clearHistoryMsg); ok {
			clr = cm
		}
	}
	m, _ = update(m, clr)
	if entries := m.history.Entries(); len(entries) != 0 {
		t.Fatalf("清空后 history = %+v", entries)
	}
}

func TestHistoryEmptyView(t *testing.T) {
	m := newTestModel(t, newFakePlayer(), &fakeSearchAdapter{}, nil)
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("3")})
	if got := m.historyPage.view(); !strings.Contains(got, "暂无播放历史") {
		t.Errorf("空历史 view = %q", got)
	}
}

func TestHistoryItemTitleAndDescription(t *testing.T) {
	tr := testTrack("t1")
	item := historyItem{entry: historyEntry(tr)}
	if item.Title() != tr.Title {
		t.Errorf("Title = %q", item.Title())
	}
	if item.FilterValue() != tr.Title+" "+tr.Artist {
		t.Errorf("FilterValue = %q", item.FilterValue())
	}
	if item.Description() == "" {
		t.Error("Description 不应为空")
	}
}
