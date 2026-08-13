package history

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"music-tui/model"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := NewStore(filepath.Join(t.TempDir(), "history.json"))
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func track(id string) model.Track {
	return model.Track{
		ID:     id,
		Title:  "歌" + id,
		Artist: "歌手",
		URL:    "https://www.youtube.com/watch?v=" + id,
		Source: "youtube",
	}
}

func TestAddDedupesAndMovesToTop(t *testing.T) {
	s := newTestStore(t)
	if err := s.Add(track("A")); err != nil {
		t.Fatal(err)
	}
	time.Sleep(2 * time.Millisecond)
	if err := s.Add(track("B")); err != nil {
		t.Fatal(err)
	}
	time.Sleep(2 * time.Millisecond)
	if err := s.Add(track("A")); err != nil {
		t.Fatal(err)
	}
	entries := s.Entries()
	if len(entries) != 2 {
		t.Fatalf("len = %d, want 2（A 去重）", len(entries))
	}
	if entries[0].Track.ID != "A" || entries[1].Track.ID != "B" {
		t.Errorf("顺序 = [%s %s], want [A B]", entries[0].Track.ID, entries[1].Track.ID)
	}
	if !entries[0].PlayedAt.After(entries[1].PlayedAt) {
		t.Error("A 重播后 PlayedAt 应晚于 B")
	}
}

func TestAddDistinctSourceNotDeduped(t *testing.T) {
	s := newTestStore(t)
	_ = s.Add(model.Track{ID: "A", Title: "t", Source: "youtube"})
	_ = s.Add(model.Track{ID: "A", Title: "t", Source: "netease"})
	if got := len(s.Entries()); got != 2 {
		t.Fatalf("len = %d, want 2（不同 Source 不去重）", got)
	}
}

func TestAddTrimsToLimit(t *testing.T) {
	s := newTestStore(t)
	total := MaxEntries + 20
	for i := 0; i < total; i++ {
		if err := s.Add(track(fmt.Sprintf("%03d", i))); err != nil {
			t.Fatal(err)
		}
	}
	entries := s.Entries()
	if len(entries) != MaxEntries {
		t.Fatalf("len = %d, want %d", len(entries), MaxEntries)
	}
	if entries[0].Track.ID != fmt.Sprintf("%03d", total-1) {
		t.Errorf("最新 = %s, want %03d", entries[0].Track.ID, total-1)
	}
	if entries[MaxEntries-1].Track.ID != "020" {
		t.Errorf("最旧 = %s, want 020", entries[MaxEntries-1].Track.ID)
	}
}

func TestRemove(t *testing.T) {
	s := newTestStore(t)
	_ = s.Add(track("A"))
	_ = s.Add(track("B"))
	if err := s.Remove("A", "youtube"); err != nil {
		t.Fatal(err)
	}
	entries := s.Entries()
	if len(entries) != 1 || entries[0].Track.ID != "B" {
		t.Errorf("entries = %+v, want [B]", entries)
	}
	// 删除不存在的记录不报错
	if err := s.Remove("A", "youtube"); err != nil {
		t.Fatalf("重复删除: %v", err)
	}
}

func TestClear(t *testing.T) {
	s := newTestStore(t)
	_ = s.Add(track("A"))
	if err := s.Clear(); err != nil {
		t.Fatal(err)
	}
	if got := len(s.Entries()); got != 0 {
		t.Fatalf("len = %d, want 0", got)
	}
}

func TestPersistReload(t *testing.T) {
	path := filepath.Join(t.TempDir(), "history.json")
	s1, err := NewStore(path)
	if err != nil {
		t.Fatal(err)
	}
	_ = s1.Add(track("A"))

	s2, err := NewStore(path)
	if err != nil {
		t.Fatal(err)
	}
	entries := s2.Entries()
	if len(entries) != 1 || entries[0].Track.ID != "A" || entries[0].Track.Title != "歌A" {
		t.Fatalf("重载后 entries = %+v", entries)
	}
	if !entries[0].PlayedAt.Equal(s1.Entries()[0].PlayedAt) {
		t.Error("PlayedAt 应精确还原")
	}
}

func TestNewStoreMissingFile(t *testing.T) {
	s, err := NewStore(filepath.Join(t.TempDir(), "nope.json"))
	if err != nil {
		t.Fatalf("want nil error, got %v", err)
	}
	if len(s.Entries()) != 0 {
		t.Error("want empty entries")
	}
}

func TestNewStoreCreatesParentDir(t *testing.T) {
	path := filepath.Join(t.TempDir(), "a", "b", "history.json")
	s, err := NewStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Add(track("A")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("文件未创建: %v", err)
	}
}

func TestNewStoreCorruptFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "history.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := NewStore(path); err == nil {
		t.Error("损坏文件应报错")
	}
}
