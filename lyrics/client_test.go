package lyrics

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"music-tui/model"
)

const testUA = "music-tui 0.1.0 (https://github.com/example/music-tui)"

func TestFetchGetHit(t *testing.T) {
	var gotUA, gotQuery string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/get" {
			t.Errorf("path = %q, want /api/get", r.URL.Path)
		}
		gotUA = r.Header.Get("User-Agent")
		gotQuery = r.URL.RawQuery
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(lrclibSong{
			TrackName:    "晴天",
			ArtistName:   "周杰倫",
			Duration:     269.0,
			SyncedLyrics: "[00:12.00]故事的小黄花\n[00:16.50]从出生那年就飘着\n",
		})
	}))
	defer server.Close()

	c := NewClient(testUA)
	c.baseURL = server.URL
	ly, err := c.Fetch(context.Background(), model.Track{Title: "晴天", Artist: "周杰倫", Duration: 269.0})
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if gotUA != testUA {
		t.Errorf("User-Agent = %q, want %q", gotUA, testUA)
	}
	if !strings.Contains(gotQuery, "track_name=") || !strings.Contains(gotQuery, "artist_name=") || !strings.Contains(gotQuery, "duration=269.00") {
		t.Errorf("query = %q, want track_name/artist_name/duration", gotQuery)
	}
	if len(ly.Lines) != 2 || ly.Lines[0].Text != "故事的小黄花" || ly.Lines[0].Time != 12.0 {
		t.Errorf("parsed lines = %+v", ly.Lines)
	}
}

func TestFetchGet404FallsBackToSearch(t *testing.T) {
	var getCalls, searchCalls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/get":
			getCalls++
			w.WriteHeader(http.StatusNotFound)
		case "/api/search":
			searchCalls++
			_ = json.NewEncoder(w).Encode([]lrclibSong{
				{TrackName: "晴天", ArtistName: "周杰倫", Duration: 300.0, SyncedLyrics: "[00:01.00]时长差太多"},
				{TrackName: "晴天", ArtistName: "周杰倫", Duration: 268.0, SyncedLyrics: "[00:01.00]正确歌词"},
			})
		}
	}))
	defer server.Close()

	c := NewClient(testUA)
	c.baseURL = server.URL
	ly, err := c.Fetch(context.Background(), model.Track{Title: "晴天", Artist: "周杰倫", Duration: 269.0})
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if getCalls != 1 || searchCalls != 1 {
		t.Errorf("calls = get:%d search:%d, want 1/1", getCalls, searchCalls)
	}
	if len(ly.Lines) != 1 || ly.Lines[0].Text != "正确歌词" {
		t.Errorf("未选中时长最接近的匹配: %+v", ly.Lines)
	}
}

func TestFetchNotFound(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	c := NewClient(testUA)
	c.baseURL = server.URL
	_, err := c.Fetch(context.Background(), model.Track{Title: "x", Artist: "y", Duration: 1})
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
}

func TestFetchRetryOn429(t *testing.T) {
	var calls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(lrclibSong{TrackName: "t", PlainLyrics: "plain text"})
	}))
	defer server.Close()

	c := NewClient(testUA)
	c.baseURL = server.URL
	ly, err := c.Fetch(context.Background(), model.Track{Title: "t", Artist: "a", Duration: 10})
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if calls != 2 {
		t.Errorf("calls = %d, want 2（429 后重试一次）", calls)
	}
	if ly.Plain != "plain text" {
		t.Errorf("Plain = %q, want %q", ly.Plain, "plain text")
	}
}

func TestFetchEmptyGetFallsBackToSearch(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/get":
			_ = json.NewEncoder(w).Encode(lrclibSong{TrackName: "t", Duration: 10})
		case "/api/search":
			_ = json.NewEncoder(w).Encode([]lrclibSong{{TrackName: "t", Duration: 10, PlainLyrics: "fallback"}})
		}
	}))
	defer server.Close()

	c := NewClient(testUA)
	c.baseURL = server.URL
	ly, err := c.Fetch(context.Background(), model.Track{Title: "t", Artist: "a", Duration: 10})
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if ly.Plain != "fallback" {
		t.Errorf("Plain = %q, want %q", ly.Plain, "fallback")
	}
}

func TestFetchSkipsInstrumental(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/get":
			w.WriteHeader(http.StatusNotFound)
		case "/api/search":
			_ = json.NewEncoder(w).Encode([]lrclibSong{
				{TrackName: "t", ArtistName: "a", Duration: 10, Instrumental: true, SyncedLyrics: "[00:01.00]x"},
			})
		}
	}))
	defer server.Close()

	c := NewClient(testUA)
	c.baseURL = server.URL
	_, err := c.Fetch(context.Background(), model.Track{Title: "t", Artist: "a", Duration: 10})
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound（纯器乐应视为无歌词）", err)
	}
}

func TestChooseBestPrefersClosestDuration(t *testing.T) {
	songs := []lrclibSong{
		{TrackName: "t", Duration: 100, SyncedLyrics: "[00:01.00]a"},
		{TrackName: "t", Duration: 12, SyncedLyrics: "[00:01.00]b"},
		{TrackName: "t", Duration: 10, SyncedLyrics: "[00:01.00]c"},
	}
	ly := chooseBest(songs, model.Track{Duration: 10})
	if ly == nil || ly.Lines[0].Text != "c" {
		t.Errorf("chooseBest = %+v, want c", ly)
	}
}
