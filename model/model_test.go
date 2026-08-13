package model

import (
	"encoding/json"
	"testing"
)

func TestTrackFields(t *testing.T) {
	tr := Track{
		ID:       "abc123",
		Title:    "晴天",
		Artist:   "周杰倫",
		Duration: 269.5,
		URL:      "https://www.youtube.com/watch?v=abc123",
		Source:   "youtube",
		CoverURL: "https://i.ytimg.com/vi/abc123/maxresdefault.jpg",
	}
	if tr.ID != "abc123" || tr.Title != "晴天" || tr.Artist != "周杰倫" {
		t.Errorf("字段赋值错误: %+v", tr)
	}
	if tr.Duration != 269.5 || tr.URL == "" || tr.Source != "youtube" || tr.CoverURL == "" {
		t.Errorf("字段赋值错误: %+v", tr)
	}
}

// TestTrackJSONRoundTrip 保证 Track 可被 encoding/json 往返序列化
// （history 包以 JSON 持久化 Track，必须稳定可还原）。
func TestTrackJSONRoundTrip(t *testing.T) {
	in := Track{
		ID:       "abc123",
		Title:    "晴天",
		Artist:   "周杰倫",
		Duration: 269.5,
		URL:      "https://www.youtube.com/watch?v=abc123",
		Source:   "youtube",
		CoverURL: "https://i.ytimg.com/vi/abc123/maxresdefault.jpg",
	}
	data, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	var out Track
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatal(err)
	}
	if out != in {
		t.Errorf("round trip = %+v, want %+v", out, in)
	}
}

func TestPlaybackStateZeroValue(t *testing.T) {
	var s PlaybackState
	if s.Track != nil {
		t.Errorf("Track 应为 nil，got %+v", s.Track)
	}
	if s.Position != 0 || s.Duration != 0 || s.Playing {
		t.Errorf("zero value 不干净: %+v", s)
	}
}

func TestPlaybackStateWithTrack(t *testing.T) {
	tr := Track{ID: "x", Title: "y"}
	s := PlaybackState{Track: &tr, Position: 10, Duration: 200, Playing: true}
	if s.Track.ID != "x" || s.Position != 10 || s.Duration != 200 || !s.Playing {
		t.Errorf("state = %+v", s)
	}
}
