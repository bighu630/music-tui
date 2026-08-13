package lyrics

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

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

func TestFetchNoisyTitleFallsBackToCleanedCandidates(t *testing.T) {
	var searchCalls []string
	var searchArtists []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/get":
			w.WriteHeader(http.StatusNotFound)
		case "/api/search":
			searchCalls = append(searchCalls, r.URL.Query().Get("track_name"))
			searchArtists = append(searchArtists, r.URL.Query().Get("artist_name"))
			if r.URL.Query().Get("track_name") == "七里香" {
				_ = json.NewEncoder(w).Encode([]lrclibSong{
					{TrackName: "七里香", ArtistName: "周杰倫", Duration: 300.0, SyncedLyrics: "[00:01.00]七里香歌词"},
				})
			} else {
				_ = json.NewEncoder(w).Encode([]lrclibSong{})
			}
		}
	}))
	defer server.Close()

	c := NewClient(testUA)
	c.baseURL = server.URL
	ly, err := c.Fetch(context.Background(), model.Track{Title: "周杰倫 七里香 歌詞", Artist: "周杰倫", Duration: 300.0})
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	cleanCalled := false
	for i, tn := range searchCalls {
		if tn == "七里香" {
			cleanCalled = true
			if searchArtists[i] != "" {
				t.Errorf("七里香 的 search 带 artist_name=%q, want 空（清洗候选不带 artist）", searchArtists[i])
			}
		}
	}
	if !cleanCalled {
		t.Errorf("search 未以清洗候选 七里香 调用: %v", searchCalls)
	}
	if len(ly.Lines) != 1 || ly.Lines[0].Text != "七里香歌词" {
		t.Errorf("lyrics = %+v", ly.Lines)
	}
}

func TestFetchStopsOnNonNotFoundError(t *testing.T) {
	var searchCalls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/get":
			w.WriteHeader(http.StatusNotFound)
		case "/api/search":
			searchCalls++
			w.WriteHeader(http.StatusInternalServerError)
		}
	}))
	defer server.Close()

	c := NewClient(testUA)
	c.baseURL = server.URL
	_, err := c.Fetch(context.Background(), model.Track{Title: "周杰倫 七里香 歌詞", Artist: "周杰倫", Duration: 300})
	if err == nil || !strings.Contains(err.Error(), "500") {
		t.Errorf("err = %v, want 500 服务端错误", err)
	}
	if searchCalls != 1 {
		t.Errorf("searchCalls = %d, want 1（非 ErrNotFound 应立即中断，不再尝试后续候选）", searchCalls)
	}
}

func TestRetryAfter(t *testing.T) {
	// 缺省 1 秒
	if d := retryAfter(&http.Response{}); d != time.Second {
		t.Errorf("缺省 retryAfter = %v, want 1s", d)
	}
	// 秒数
	resp := &http.Response{Header: http.Header{"Retry-After": []string{"3"}}}
	if d := retryAfter(resp); d != 3*time.Second {
		t.Errorf("秒数 retryAfter = %v, want 3s", d)
	}
	// HTTP 日期格式
	resp = &http.Response{Header: http.Header{"Retry-After": []string{time.Now().Add(2 * time.Second).UTC().Format(http.TimeFormat)}}}
	d := retryAfter(resp)
	if d <= 0 || d > 5*time.Second {
		t.Errorf("日期格式 retryAfter = %v, want (0, 5s]", d)
	}
	// 非法值 → 缺省 1 秒
	resp = &http.Response{Header: http.Header{"Retry-After": []string{"abc"}}}
	if d := retryAfter(resp); d != time.Second {
		t.Errorf("非法值 retryAfter = %v, want 1s", d)
	}
}

func TestFetchRetry429Exhausted(t *testing.T) {
	var calls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Retry-After", "0")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer server.Close()

	c := NewClient(testUA)
	c.baseURL = server.URL
	_, err := c.Fetch(context.Background(), model.Track{Title: "t", Artist: "a", Duration: 10})
	if err == nil || !strings.Contains(err.Error(), "限流") {
		t.Errorf("err = %v, want 限流重试后仍失败", err)
	}
	if calls != 2 {
		t.Errorf("calls = %d, want 2（429 重试两次后放弃）", calls)
	}
}

func TestFetchRetryCanceledDuringWait(t *testing.T) {
	got := make(chan struct{}, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "60")
		w.WriteHeader(http.StatusTooManyRequests)
		select {
		case got <- struct{}{}:
		default:
		}
	}))
	defer server.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c := NewClient(testUA)
	c.baseURL = server.URL
	done := make(chan error, 1)
	go func() {
		_, err := c.Fetch(ctx, model.Track{Title: "t", Artist: "a", Duration: 10})
		done <- err
	}()
	select {
	case <-got:
	case <-time.After(2 * time.Second):
		t.Fatal("服务器未收到请求")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("err = %v, want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("取消后 Fetch 未返回")
	}
}
