package cache

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func newTestIndex() *index {
	return &index{}
}

func TestUpsertAppendsNewestLast(t *testing.T) {
	ix := newTestIndex()
	base := time.Now()
	ix.upsert("b", base.Add(-time.Hour))
	ix.upsert("a", base)

	if ix.len() != 2 {
		t.Fatalf("len = %d, want 2", ix.len())
	}
	// entries 按 LastPlayed 升序：entries[0] 最旧
	if ix.entries[0].ID != "b" || ix.entries[1].ID != "a" {
		t.Errorf("order = [%s, %s], want [b, a]", ix.entries[0].ID, ix.entries[1].ID)
	}
}

func TestUpsertSameIDRefreshesAndMoves(t *testing.T) {
	ix := newTestIndex()
	base := time.Now()
	ix.upsert("a", base.Add(-2*time.Hour))
	ix.upsert("b", base.Add(-time.Hour))
	ix.upsert("a", base) // a 刷新为最新

	// 顺序应为 b（最旧）, a（最新）
	if ix.entries[0].ID != "b" || ix.entries[1].ID != "a" {
		t.Errorf("order = [%s, %s], want [b, a]", ix.entries[0].ID, ix.entries[1].ID)
	}
	if ix.len() != 2 {
		t.Errorf("len = %d, want 2", ix.len())
	}
	e, ok := ix.get("a")
	if !ok || !e.LastPlayed.Equal(base) {
		t.Errorf("get(a) = %+v, ok=%v; want LastPlayed=%v", e, ok, base)
	}
}

func TestGetMissing(t *testing.T) {
	ix := newTestIndex()
	if _, ok := ix.get("nope"); ok {
		t.Error("get(nope) ok = true, want false")
	}
}

func TestRemove(t *testing.T) {
	ix := newTestIndex()
	ix.upsert("a", time.Now())
	if !ix.remove("a") {
		t.Error("remove(a) = false, want true")
	}
	if _, ok := ix.get("a"); ok {
		t.Error("get(a) after remove = ok, want miss")
	}
	if ix.remove("a") {
		t.Error("second remove(a) = true, want false")
	}
}

func TestOldest(t *testing.T) {
	ix := newTestIndex()
	if _, ok := ix.oldest(); ok {
		t.Error("oldest on empty = ok, want false")
	}
	base := time.Now()
	ix.upsert("newer", base)
	ix.upsert("older", base.Add(-time.Hour))
	e, ok := ix.oldest()
	if !ok || e.ID != "older" {
		t.Errorf("oldest = %+v ok=%v, want older", e, ok)
	}
}

func TestSaveLoadRoundtrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "index.json")
	ix := newTestIndex()
	base := time.Now()
	ix.upsert("a", base.Add(-time.Hour))
	ix.upsert("b", base)
	if err := ix.save(path); err != nil {
		t.Fatalf("save: %v", err)
	}

	got, err := load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got.len() != 2 {
		t.Fatalf("len = %d, want 2", got.len())
	}
	// 顺序保持（升序）
	if got.entries[0].ID != "a" || got.entries[1].ID != "b" {
		t.Errorf("order = [%s, %s], want [a, b]", got.entries[0].ID, got.entries[1].ID)
	}
	ea, _ := got.get("a")
	eb, _ := got.get("b")
	if !ea.LastPlayed.Equal(ix.entries[0].LastPlayed) {
		t.Errorf("a.LastPlayed mismatch: %v vs %v", ea.LastPlayed, ix.entries[0].LastPlayed)
	}
	if !eb.LastPlayed.Equal(ix.entries[1].LastPlayed) {
		t.Errorf("b.LastPlayed mismatch: %v vs %v", eb.LastPlayed, ix.entries[1].LastPlayed)
	}
	// 无 .tmp 残留
	if _, err := os.Stat(path + ".tmp"); !os.IsNotExist(err) {
		t.Errorf("tmp file remains: %v", err)
	}
}

func TestLoadMissingFileEmpty(t *testing.T) {
	dir := t.TempDir()
	ix, err := load(filepath.Join(dir, "nope.json"))
	if err != nil {
		t.Fatalf("load missing: %v", err)
	}
	if ix.len() != 0 {
		t.Errorf("len = %d, want 0", ix.len())
	}
}

func TestLoadEmptyFileEmpty(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "empty.json")
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	ix, err := load(path)
	if err != nil {
		t.Fatalf("load empty: %v", err)
	}
	if ix.len() != 0 {
		t.Errorf("len = %d, want 0", ix.len())
	}
}

func TestLoadCorruptReturnsError(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bad.json")
	if err := os.WriteFile(path, []byte("{invalid"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := load(path); err == nil {
		t.Error("load corrupt = nil error, want error")
	}
}

func TestLoadPreservesOrderAndValues(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "index.json")
	// 预写一份乱序 JSON（旧→新），load 后应保持文件顺序
	content := `[
		{"id":"b","file":"b.mp3","last_played":"2024-01-01T00:00:00Z"},
		{"id":"a","file":"a.mp3","last_played":"2024-01-02T00:00:00Z"}
	]`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	ix, err := load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if ix.len() != 2 {
		t.Fatalf("len = %d, want 2", ix.len())
	}
	if ix.entries[0].ID != "b" || ix.entries[1].ID != "a" {
		t.Errorf("order = [%s, %s], want [b, a]", ix.entries[0].ID, ix.entries[1].ID)
	}
	e, _ := ix.get("b")
	if e.File != "b.mp3" {
		t.Errorf("b.File = %q, want b.mp3", e.File)
	}
}
