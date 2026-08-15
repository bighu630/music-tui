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

	m, _ = update(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("5")}) // 数字键直达历史页
	if m.current != pageHistory {
		t.Fatal("按 5 后应在历史页")
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
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("5")}) // 数字键直达历史页
	if got := m.historyPage.view(); !strings.Contains(got, "暂无播放历史") {
		t.Errorf("空历史 view = %q", got)
	}
}

func TestHistoryItemTitleAndDescription(t *testing.T) {
	tr := testTrack("t1")
	item := historyItem{entry: historyEntry(tr)}
	if !strings.Contains(item.Title(), tr.Title+" - "+tr.Artist) {
		t.Errorf("Title = %q, want 含 %q", item.Title(), tr.Title+" - "+tr.Artist)
	}
	if !strings.Contains(item.Title(), " · ") {
		t.Errorf("Title = %q, want 含 \" · \"（播放时间段）", item.Title())
	}
	if item.FilterValue() != tr.Title+" "+tr.Artist {
		t.Errorf("FilterValue = %q", item.FilterValue())
	}
	if item.Description() != "" {
		t.Errorf("Description = %q, want 空（单行模式）", item.Description())
	}
}

// TestHistoryHintOnLastLine 历史页（非空）提示行应渲染在页面内容区最后一行。
func TestHistoryHintOnLastLine(t *testing.T) {
	m := newTestModel(t, newFakePlayer(), &fakeSearchAdapter{}, nil)
	m, _ = update(m, tea.WindowSizeMsg{Width: 80, Height: 24})
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("5")}) // 数字键直达历史页
	m.historyPage = m.historyPage.setEntries([]history.Entry{
		{Track: testTrack("t1")},
		{Track: testTrack("t2")},
	})
	assertHintOnLastLine(t, m, "Enter/p 重播")
}

// TestHistoryEmptyHintOnLastLine 空历史页也应显示提示行，且同样在最后一行。
func TestHistoryEmptyHintOnLastLine(t *testing.T) {
	m := newTestModel(t, newFakePlayer(), &fakeSearchAdapter{}, nil)
	m, _ = update(m, tea.WindowSizeMsg{Width: 80, Height: 24})
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("5")}) // 数字键直达历史页
	assertHintOnLastLine(t, m, "Enter/p 重播")
}

// TestHistorySlashFilter 历史页 / 过滤全流程：打开→输入→计数→Enter 确认→
// 过滤态重播/删除（原始记录）→Esc 恢复。
// 注：spec 契约“数字 1-5 全局切页，过滤词无法含 1-5”，故曲目用 t6/t7/t8、
// 过滤词用非切页数字 7（计划原测试用 "2" 会被 root 拦截为切页键）。
func TestHistorySlashFilter(t *testing.T) {
	fp := newFakePlayer()
	m := newTestModel(t, fp, &fakeSearchAdapter{}, nil)
	for _, id := range []string{"t6", "t7", "t8"} {
		if err := m.history.Add(testTrack(id)); err != nil {
			t.Fatal(err)
		}
	}
	m = m.refreshHistory()
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("5")}) // 历史页

	// / 打开过滤
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("/")})
	if !m.historyPage.filtering || !m.historyPage.filterInput.Focused() {
		t.Fatalf("/ 后 filtering=%v focused=%v, want true/true", m.historyPage.filtering, m.historyPage.filterInput.Focused())
	}
	if got := m.historyPage.view(); !strings.Contains(got, "过滤:") {
		t.Errorf("过滤行未渲染: %q", got)
	}

	// 输入 "7" 实时过滤 + 计数（历史最新在前，t7 在第 2 位）
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("7")})
	if got := m.historyPage.view(); !strings.Contains(got, "(1/3)") {
		t.Errorf("计数应显示 (1/3): %q", got)
	}
	if n := len(m.historyPage.list.VisibleItems()); n != 1 {
		t.Fatalf("过滤后可见 %d 项, want 1", n)
	}

	// Enter 确认
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyEnter})
	if m.historyPage.filterInput.Focused() {
		t.Fatal("Enter 应确认过滤并失焦")
	}

	// 确认态 Enter 重播 → trackSelectedMsg 应为 t7
	m, cmd := update(m, tea.KeyMsg{Type: tea.KeyEnter})
	var sel trackSelectedMsg
	for _, msg := range execCmds(cmd) {
		if sm, ok := msg.(trackSelectedMsg); ok {
			sel = sm
		}
	}
	if sel.track.ID != "t7" {
		t.Fatalf("过滤态重播 = %s, want t7", sel.track.ID)
	}

	// 确认态 d 删除 → deleteEntryMsg 应为 t7
	m, cmd = update(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("d")})
	var del deleteEntryMsg
	for _, msg := range execCmds(cmd) {
		if dm, ok := msg.(deleteEntryMsg); ok {
			del = dm
		}
	}
	if del.id != "t7" {
		t.Fatalf("过滤态删除 id = %s, want t7", del.id)
	}
	m, _ = update(m, del) // root 执行删除
	if n := len(m.historyPage.list.VisibleItems()); n != 0 {
		t.Errorf("删除后过滤列表应为空, got %d", n)
	}
	if got := m.historyPage.view(); !strings.Contains(got, "(0/2)") {
		t.Errorf("删除后计数应 (0/2): %q", got)
	}

	// Esc 恢复完整列表
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyEsc})
	if m.historyPage.filtering {
		t.Fatal("Esc 应退出过滤")
	}
	if got := m.historyPage.view(); strings.Contains(got, "过滤:") {
		t.Errorf("退出后不应有过滤行: %q", got)
	}
	if n := len(m.historyPage.list.VisibleItems()); n != 2 {
		t.Fatalf("恢复后可见 %d 项, want 2", n)
	}
}

// TestHistorySlashFilterMapping 多命中 + 聚焦态方向键 + 确认态 a 添加到选择器（全局键）。
func TestHistorySlashFilterMapping(t *testing.T) {
	fp := newFakePlayer()
	m := newTestModel(t, fp, &fakeSearchAdapter{}, nil)
	for _, id := range []string{"t1", "t2", "t3"} {
		if err := m.history.Add(testTrack(id)); err != nil {
			t.Fatal(err)
		}
	}
	m = m.refreshHistory()
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("5")})

	m, _ = update(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("/")})
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("t")}) // 命中全部
	if n := len(m.historyPage.list.VisibleItems()); n != 3 {
		t.Fatalf("过滤后可见 %d 项, want 3", n)
	}
	// 聚焦态 ↑↓ 移动（历史最新在前：t3, t2, t1；down 一次到 t2）
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyDown})
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyEnter}) // 确认
	// 确认态 a 打开"添加到"选择器（root 全局键，作用于过滤后选中项）
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("a")})
	if m.plPicker == nil {
		t.Fatal("确认态按 a 应打开选择器")
	}
	if m.plPicker.track.ID != "t2" {
		t.Fatalf("选择器 track = %s, want t2", m.plPicker.track.ID)
	}
}
