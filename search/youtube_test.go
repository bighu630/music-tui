package search

import (
	"context"
	"testing"

	"music-tui/model"
)

// fakeYTDLPOutput 模拟 yt-dlp --dump-json 的逐行 JSON 输出（每行一条结果）。
const fakeYTDLPOutput = `{"id":"abc123","title":"晴天","channel":"周杰倫","duration":269.0,"webpage_url":"https://www.youtube.com/watch?v=abc123","thumbnail":"https://i.ytimg.com/vi/abc123/maxresdefault.jpg"}
{"id":"def456","title":"七里香","channel":"周杰倫","duration":311.0,"webpage_url":"https://www.youtube.com/watch?v=def456","thumbnail":"https://i.ytimg.com/vi/def456/hqdefault.jpg"}`

func TestParseYTDLPOutput(t *testing.T) {
	tracks, err := parseYTDLPOutput([]byte(fakeYTDLPOutput))
	if err != nil {
		t.Fatal(err)
	}
	if len(tracks) != 2 {
		t.Fatalf("len = %d, want 2", len(tracks))
	}
	want := model.Track{
		ID:       "abc123",
		Title:    "晴天",
		Artist:   "周杰倫",
		Duration: 269.0,
		URL:      "https://www.youtube.com/watch?v=abc123",
		Source:   "youtube",
		CoverURL: "https://i.ytimg.com/vi/abc123/maxresdefault.jpg",
	}
	if tracks[0] != want {
		t.Errorf("tracks[0] = %+v, want %+v", tracks[0], want)
	}
	if tracks[1].ID != "def456" || tracks[1].Source != "youtube" {
		t.Errorf("tracks[1] = %+v", tracks[1])
	}
}

func TestParseYTDLPOutputSkipsGarbageLines(t *testing.T) {
	out := "WARNING: [youtube] something\n" + fakeYTDLPOutput + "\nnot json at all\n"
	tracks, err := parseYTDLPOutput([]byte(out))
	if err != nil {
		t.Fatal(err)
	}
	if len(tracks) != 2 {
		t.Fatalf("len = %d, want 2（非 JSON 行应跳过）", len(tracks))
	}
}

func TestParseYTDLPOutputEmpty(t *testing.T) {
	tracks, err := parseYTDLPOutput(nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(tracks) != 0 {
		t.Errorf("len = %d, want 0", len(tracks))
	}
}

func TestParseYTDLPOutputSkipsEmptyID(t *testing.T) {
	out := `{"id":"","title":"无ID"}`
	tracks, err := parseYTDLPOutput([]byte(out))
	if err != nil {
		t.Fatal(err)
	}
	if len(tracks) != 0 {
		t.Errorf("len = %d, want 0（空 ID 条目应跳过）", len(tracks))
	}
}

func TestSearchBinaryMissing(t *testing.T) {
	a := NewYouTubeAdapter("/nonexistent/yt-dlp")
	if _, err := a.Search(context.Background(), "test"); err == nil {
		t.Error("yt-dlp 不存在时应报错")
	}
}
