package session

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"music-tui/model"
	"music-tui/queue"
)

func testTrack(id string) model.Track {
	return model.Track{
		ID:       id,
		Title:    "曲 " + id,
		Artist:   "歌手",
		Duration: 180,
		URL:      "https://youtu.be/" + id,
		Source:   "youtube",
	}
}

func testState() State {
	q := queue.New()
	q.Replace(testTrack("a"))
	q.Add(testTrack("b"))
	q.Add(testTrack("c"))
	q.Next() // b 当前
	q.SetMode(queue.Shuffle)
	return State{
		Queue:    q.Snapshot(),
		Position: 66.6,
		Ended:    false,
	}
}

func TestNewStoreMissing(t *testing.T) {
	s, err := NewStore(filepath.Join(t.TempDir(), "session.json"))
	if err != nil {
		t.Fatal(err)
	}
	if s.State() != nil {
		t.Error("文件不存在时应视为无会话")
	}
}

func TestSaveThenStateRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.json")
	s, err := NewStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Save(testState()); err != nil {
		t.Fatal(err)
	}

	// 同一实例读回
	st := s.State()
	if st == nil {
		t.Fatal("Save 后 State 不应为 nil")
	}
	if got := ids(st.Queue.Tracks); !reflect.DeepEqual(got, []string{"a", "b", "c"}) {
		t.Errorf("Tracks = %v", got)
	}
	if st.Queue.CurrentIdx != 1 || st.Queue.Mode != queue.Shuffle {
		t.Errorf("Queue = %+v, want current=1 mode=Shuffle", st.Queue)
	}
	if st.Position != 66.6 || st.Ended {
		t.Errorf("State = %+v, want position=66.6 ended=false", st)
	}

	// 重新 NewStore（模拟重启）读回
	s2, err := NewStore(path)
	if err != nil {
		t.Fatal(err)
	}
	st2 := s2.State()
	if st2 == nil || st2.Position != 66.6 || st2.Queue.CurrentIdx != 1 {
		t.Errorf("重启读回 = %+v", st2)
	}
}

func TestSaveOverwrites(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.json")
	s, _ := NewStore(path)
	if err := s.Save(testState()); err != nil {
		t.Fatal(err)
	}
	st := testState()
	st.Position = 1.5
	st.Ended = true
	if err := s.Save(st); err != nil {
		t.Fatal(err)
	}
	got := s.State()
	if got.Position != 1.5 || !got.Ended {
		t.Errorf("覆盖后 = %+v", got)
	}
}

func TestStateReturnsCopy(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.json")
	s, _ := NewStore(path)
	if err := s.Save(testState()); err != nil {
		t.Fatal(err)
	}
	st := s.State()
	st.Position = 999
	st.Queue.Tracks[0] = testTrack("mutated")
	if s.State().Position == 999 {
		t.Error("修改 State() 返回值不应影响 store")
	}
}

func TestClear(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.json")
	s, _ := NewStore(path)
	if err := s.Save(testState()); err != nil {
		t.Fatal(err)
	}
	if err := s.Clear(); err != nil {
		t.Fatal(err)
	}
	if s.State() != nil {
		t.Error("Clear 后 State 应为 nil")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("Clear 后文件应被删除")
	}
	if err := s.Clear(); err != nil {
		t.Errorf("重复 Clear 不应报错: %v", err)
	}
}

func TestNewStoreCorrupt(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := NewStore(path); err == nil {
		t.Error("损坏文件应返回错误（由调用方备份重建）")
	}
}

func TestNewStoreEmptyFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.json")
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	s, err := NewStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if s.State() != nil {
		t.Error("空文件应视为无会话")
	}
}

func ids(ts []model.Track) []string {
	out := make([]string, len(ts))
	for i, t := range ts {
		out[i] = t.ID
	}
	return out
}
