package playlists

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"music-tui/model"
)

func testTrack(id string) model.Track {
	return model.Track{
		ID:       id,
		Title:    "歌曲 " + id,
		Artist:   "歌手",
		Duration: 180,
		URL:      "https://www.youtube.com/watch?v=" + id,
		Source:   "youtube",
	}
}

// newTestStore 创建临时目录下的 Store。
func newTestStore(t *testing.T) (*Store, string) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "playlists.json")
	s, err := NewStore(path)
	if err != nil {
		t.Fatal(err)
	}
	return s, path
}

func TestNewStoreCreatesDirAndEmpty(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "dir", "playlists.json")
	s, err := NewStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := s.Lists(); len(got) != 0 {
		t.Fatalf("初始 Lists = %v, want 空", got)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("空 store 不应写盘, err = %v", err)
	}
}

func TestCreateList(t *testing.T) {
	s, _ := newTestStore(t)
	l, err := s.Create(" 我的最爱 ")
	if err != nil {
		t.Fatal(err)
	}
	if l.Name != "我的最爱" {
		t.Errorf("Create 应 TrimSpace 名称, got %q", l.Name)
	}
	if l.CreatedAt.IsZero() {
		t.Error("CreatedAt 不应为零值")
	}
	lists := s.Lists()
	if len(lists) != 1 || lists[0].Name != "我的最爱" {
		t.Fatalf("Lists = %+v, want 1 个「我的最爱」", lists)
	}
}

func TestCreateRejectsBlankAndDuplicate(t *testing.T) {
	s, _ := newTestStore(t)
	if _, err := s.Create("  "); err == nil {
		t.Error("空白名应报错")
	}
	if _, err := s.Create("a"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Create("a"); err == nil {
		t.Error("重名应报错")
	}
	if _, err := s.Create(" a "); err == nil {
		t.Error("TrimSpace 后重名应报错")
	}
}

func TestRenameList(t *testing.T) {
	s, _ := newTestStore(t)
	if _, err := s.Create("旧名"); err != nil {
		t.Fatal(err)
	}
	if err := s.Rename("旧名", "新名"); err != nil {
		t.Fatal(err)
	}
	lists := s.Lists()
	if len(lists) != 1 || lists[0].Name != "新名" {
		t.Fatalf("Rename 后 Lists = %+v, want「新名」", lists)
	}
}

func TestRenameErrors(t *testing.T) {
	s, _ := newTestStore(t)
	if _, err := s.Create("a"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Create("b"); err != nil {
		t.Fatal(err)
	}
	if err := s.Rename("不存在", "x"); err == nil {
		t.Error("重命名不存在的列表应报错")
	}
	if err := s.Rename("a", "  "); err == nil {
		t.Error("重命名为空白名应报错")
	}
	if err := s.Rename("a", "b"); err == nil {
		t.Error("重命名为既有名应报错")
	}
	// 改名前后列表名 TrimSpace
	if err := s.Rename("a", " a2 "); err != nil {
		t.Fatalf("TrimSpace 后不重名应成功: %v", err)
	}
}

func TestDeleteList(t *testing.T) {
	s, _ := newTestStore(t)
	if _, err := s.Create("a"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Create("b"); err != nil {
		t.Fatal(err)
	}
	if err := s.Delete("a"); err != nil {
		t.Fatal(err)
	}
	lists := s.Lists()
	if len(lists) != 1 || lists[0].Name != "b" {
		t.Fatalf("Delete 后 Lists = %+v, want 仅「b」", lists)
	}
	if err := s.Delete("不存在"); err != nil {
		t.Errorf("删除不存在的列表应返回 nil, got %v", err)
	}
}

func TestAddTrack(t *testing.T) {
	s, _ := newTestStore(t)
	if _, err := s.Create("a"); err != nil {
		t.Fatal(err)
	}
	if err := s.AddTrack("a", testTrack("t1")); err != nil {
		t.Fatal(err)
	}
	if err := s.AddTrack("a", testTrack("t2")); err != nil {
		t.Fatal(err)
	}
	// 同一首歌重复添加：允许（播放列表语义）
	if err := s.AddTrack("a", testTrack("t1")); err != nil {
		t.Fatal(err)
	}
	trs := s.Tracks("a")
	if len(trs) != 3 {
		t.Fatalf("Tracks len = %d, want 3", len(trs))
	}
	if trs[0].ID != "t1" || trs[2].ID != "t1" {
		t.Errorf("重复添加应保序, got %+v", trs)
	}
	if err := s.AddTrack("不存在", testTrack("t1")); err == nil {
		t.Error("向不存在的列表添加应报错")
	}
	if got := s.Tracks("不存在"); got != nil {
		t.Errorf("不存在的列表 Tracks 应为 nil, got %v", got)
	}
}

func TestRemoveTrack(t *testing.T) {
	s, _ := newTestStore(t)
	if _, err := s.Create("a"); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		if err := s.AddTrack("a", testTrack("t"+string(rune('1'+i)))); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.RemoveTrack("a", 1); err != nil {
		t.Fatal(err)
	}
	trs := s.Tracks("a")
	if len(trs) != 2 || trs[0].ID != "t1" || trs[1].ID != "t3" {
		t.Fatalf("移除中间项后 = %+v, want [t1 t3]", trs)
	}
	if err := s.RemoveTrack("a", 5); err == nil {
		t.Error("越界下标应报错")
	}
	if err := s.RemoveTrack("不存在", 0); err == nil {
		t.Error("不存在的列表应报错")
	}
}

// TestAddTracks 批量追加：保序一次性写入；列表不存在返回错误（含空切片）。
func TestAddTracks(t *testing.T) {
	s, _ := newTestStore(t)
	if _, err := s.Create("a"); err != nil {
		t.Fatal(err)
	}
	tracks := []model.Track{testTrack("t1"), testTrack("t2"), testTrack("t3")}
	if err := s.AddTracks("a", tracks); err != nil {
		t.Fatal(err)
	}
	trs := s.Tracks("a")
	if len(trs) != 3 || trs[0].ID != "t1" || trs[1].ID != "t2" || trs[2].ID != "t3" {
		t.Fatalf("AddTracks 后 = %+v, want [t1 t2 t3]（保序）", trs)
	}
	// 与已有歌曲衔接追加
	if err := s.AddTracks("a", []model.Track{testTrack("t4")}); err != nil {
		t.Fatal(err)
	}
	if trs := s.Tracks("a"); len(trs) != 4 || trs[3].ID != "t4" {
		t.Fatalf("追加后 = %+v, want 4 首且 t4 在末尾", trs)
	}
	// 列表不存在：返回错误（空切片也不放行）
	if err := s.AddTracks("不存在", tracks); err == nil {
		t.Error("向不存在的列表批量添加应报错")
	}
	if err := s.AddTracks("不存在", nil); err == nil {
		t.Error("向不存在的列表批量添加空切片也应报错（列表存在性优先）")
	}
	// 失败不落盘：store 内容不变
	if err := s.AddTracks("不存在", tracks); err == nil {
		t.Fatal("再次向不存在的列表添加应报错")
	}
}

// TestAddTracksEmptyIsNoOp 空切片是 no-op：返回 nil、不写盘、不加歌。
func TestAddTracksEmptyIsNoOp(t *testing.T) {
	s, path := newTestStore(t)
	if _, err := s.Create("a"); err != nil {
		t.Fatal(err)
	}
	if err := s.AddTracks("a", nil); err != nil {
		t.Fatalf("空切片应为 no-op 返回 nil: %v", err)
	}
	if err := s.AddTracks("a", []model.Track{}); err != nil {
		t.Fatalf("空切片（非 nil）同样 no-op: %v", err)
	}
	if got := s.Tracks("a"); len(got) != 0 {
		t.Errorf("空切片不应添加任何歌曲: %v", got)
	}
	// no-op 不触发写盘：磁盘文件保持空列表状态
	s2, err := NewStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if lists := s2.Lists(); len(lists) != 1 || len(lists[0].Tracks) != 0 {
		t.Fatalf("重载后 = %+v, want 1 个空列表", lists)
	}
}

// TestAddTracksPersists 批量添加原子落盘：重载后歌曲完整保留。
func TestAddTracksPersists(t *testing.T) {
	s, path := newTestStore(t)
	if _, err := s.Create("a"); err != nil {
		t.Fatal(err)
	}
	tracks := []model.Track{testTrack("t1"), testTrack("t2")}
	if err := s.AddTracks("a", tracks); err != nil {
		t.Fatal(err)
	}
	s2, err := NewStore(path)
	if err != nil {
		t.Fatal(err)
	}
	trs := s2.Tracks("a")
	if len(trs) != 2 || trs[0].ID != "t1" || trs[1].ID != "t2" {
		t.Fatalf("重载后歌曲 = %+v, want [t1 t2]", trs)
	}
}

// TestPersistenceReload 落盘后可重新加载：CRUD 结果跨实例保留。
func TestPersistenceReload(t *testing.T) {
	s, path := newTestStore(t)
	if _, err := s.Create("a"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Create("b"); err != nil {
		t.Fatal(err)
	}
	if err := s.AddTrack("a", testTrack("t1")); err != nil {
		t.Fatal(err)
	}

	s2, err := NewStore(path)
	if err != nil {
		t.Fatal(err)
	}
	lists := s2.Lists()
	if len(lists) != 2 {
		t.Fatalf("重载 Lists = %+v, want 2 个", lists)
	}
	if lists[0].Name != "a" || len(lists[0].Tracks) != 1 || lists[0].Tracks[0].ID != "t1" {
		t.Fatalf("重载列表 a 内容不符: %+v", lists[0])
	}
	if lists[0].CreatedAt.IsZero() {
		t.Error("重载后 CreatedAt 不应为零值")
	}
	// 重载后的实例继续可写
	if err := s2.Rename("a", "a2"); err != nil {
		t.Fatal(err)
	}
	s3, err := NewStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := s3.Lists(); len(got) != 2 || got[0].Name != "a2" {
		t.Fatalf("重命名未持久化: %+v", got)
	}
}

func TestCorruptFileReturnsError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "playlists.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := NewStore(path); err == nil {
		t.Fatal("损坏文件应返回错误（由 main 层备份重建）")
	}
}

func TestEmptyFileOK(t *testing.T) {
	path := filepath.Join(t.TempDir(), "playlists.json")
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	s, err := NewStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Lists()) != 0 {
		t.Fatalf("空文件应视为空 store, got %v", s.Lists())
	}
}

// TestListsAndTracksReturnCopies 外部修改返回值不得污染存储。
func TestListsAndTracksReturnCopies(t *testing.T) {
	s, _ := newTestStore(t)
	if _, err := s.Create("a"); err != nil {
		t.Fatal(err)
	}
	if err := s.AddTrack("a", testTrack("t1")); err != nil {
		t.Fatal(err)
	}
	s.Lists()[0].Tracks[0].Title = "篡改"
	s.Lists()[0].Name = "篡改名"
	s.Tracks("a")[0].Title = "篡改"
	if got := s.Tracks("a"); got[0].Title != "歌曲 t1" {
		t.Errorf("Tracks 副本被外部污染: %v", got[0].Title)
	}
	if got := s.Lists()[0].Name; got != "a" {
		t.Errorf("Lists 副本被外部污染: %v", got)
	}
}

// TestConcurrentAccess 并发读写不 panic、不产生数据竞争（-race 验证）。
func TestConcurrentAccess(t *testing.T) {
	s, _ := newTestStore(t)
	if _, err := s.Create("a"); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			name := strings.Repeat("x", 0) + string(rune('a'+i%5))
			_ = s.AddTrack("a", testTrack(name))
			_ = s.Lists()
			_ = s.Tracks("a")
		}(i)
	}
	wg.Wait()
	if n := len(s.Tracks("a")); n != 20 {
		t.Fatalf("并发添加后 Tracks len = %d, want 20", n)
	}
}

// TestRenamePreservesTracks 重命名列表不丢失歌曲。
func TestRenamePreservesTracks(t *testing.T) {
	s, _ := newTestStore(t)
	if _, err := s.Create("a"); err != nil {
		t.Fatal(err)
	}
	if err := s.AddTrack("a", testTrack("t1")); err != nil {
		t.Fatal(err)
	}
	if err := s.Rename("a", "b"); err != nil {
		t.Fatal(err)
	}
	trs := s.Tracks("b")
	if len(trs) != 1 || trs[0].ID != "t1" {
		t.Fatalf("重命名丢失歌曲: %+v", trs)
	}
}

// TestCreatedAtStable 多次写盘 CreatedAt 保持首次创建时间。
func TestCreatedAtStable(t *testing.T) {
	s, path := newTestStore(t)
	before := time.Now()
	if _, err := s.Create("a"); err != nil {
		t.Fatal(err)
	}
	first := s.Lists()[0].CreatedAt
	if first.Before(before) {
		t.Error("CreatedAt 应晚于创建前时刻")
	}
	// 后续写盘操作不改变 CreatedAt
	_ = s.AddTrack("a", testTrack("t1"))
	_ = s.Rename("a", "a2")
	s2, err := NewStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if !s2.Lists()[0].CreatedAt.Equal(first) {
		t.Errorf("CreatedAt 变化: first=%v now=%v", first, s2.Lists()[0].CreatedAt)
	}
}
