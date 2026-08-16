package lyrics

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
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
	res, err := c.Fetch(context.Background(), model.Track{Title: "晴天", Artist: "周杰倫", Duration: 269.0})
	ly := res.Lyrics
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
	res, err := c.Fetch(context.Background(), model.Track{Title: "晴天", Artist: "周杰倫", Duration: 269.0})
	ly := res.Lyrics
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

// TestFetchUnknownDurationSkipsGet 时长未知（Duration<1，如本地文件快照
// 未持久化时长）时跳过 /api/get（lrclib 对 duration<1 返回 400 会中断整条
// 候选链）直接走 /api/search，命中即返回歌词。
func TestFetchUnknownDurationSkipsGet(t *testing.T) {
	for _, d := range []float64{0, 0.5} {
		t.Run(fmt.Sprintf("duration=%g", d), func(t *testing.T) {
			var getCalls, searchCalls int
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch r.URL.Path {
				case "/api/get":
					getCalls++
					t.Error("时长未知不应请求 /api/get（lrclib 对 duration<1 返回 400）")
				case "/api/search":
					searchCalls++
					_ = json.NewEncoder(w).Encode([]lrclibSong{
						{TrackName: "晴天", ArtistName: "周杰倫", Duration: 10, SyncedLyrics: "[00:01.00]search 命中"},
					})
				}
			}))
			defer server.Close()

			c := NewClientWithBaseURL(server.URL, testUA)
			res, err := c.Fetch(context.Background(), model.Track{Title: "晴天", Artist: "周杰倫", Duration: d})
			ly := res.Lyrics
			if err != nil {
				t.Fatalf("Fetch: %v", err)
			}
			if getCalls != 0 || searchCalls != 1 {
				t.Errorf("calls = get:%d search:%d, want 0/1", getCalls, searchCalls)
			}
			if len(ly.Lines) != 1 || ly.Lines[0].Text != "search 命中" {
				t.Errorf("Lines = %+v, want [search 命中]", ly.Lines)
			}
		})
	}
}

// TestFetchGet400FallsBackToSearch /api/get 返回 400（duration 非法等）：
// 视为可降级（同 ErrNotFound 语义），记录 Warn 后走 /api/search，命中即返回。
func TestFetchGet400FallsBackToSearch(t *testing.T) {
	var getCalls, searchCalls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/get":
			getCalls++
			w.WriteHeader(http.StatusBadRequest)
		case "/api/search":
			searchCalls++
			_ = json.NewEncoder(w).Encode([]lrclibSong{
				{TrackName: "晴天", ArtistName: "周杰倫", Duration: 269, SyncedLyrics: "[00:01.00]降级命中"},
			})
		}
	}))
	defer server.Close()

	c := NewClientWithBaseURL(server.URL, testUA)
	res, err := c.Fetch(context.Background(), model.Track{Title: "晴天", Artist: "周杰倫", Duration: 269})
	ly := res.Lyrics
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if getCalls != 1 || searchCalls != 1 {
		t.Errorf("calls = get:%d search:%d, want 1/1", getCalls, searchCalls)
	}
	if len(ly.Lines) != 1 || ly.Lines[0].Text != "降级命中" {
		t.Errorf("Lines = %+v, want [降级命中]", ly.Lines)
	}
}

// TestDoErrorIncludesURL do() 的非 2xx/404/429 错误（如 400）必须携带
// 完整请求 URL（含 duration 参数），便于日志定位。
func TestDoErrorIncludesURL(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
	}))
	defer server.Close()

	q := url.Values{}
	q.Set("track_name", "晴天")
	q.Set("artist_name", "周杰倫")
	q.Set("duration", "0.00")
	u := server.URL + "/api/get?" + q.Encode()

	c := NewClientWithBaseURL(server.URL, testUA)
	var out lrclibSong
	err := c.do(context.Background(), u, &out)
	if err == nil || !strings.Contains(err.Error(), u) {
		t.Errorf("err = %v, want 含完整 URL %q", err, u)
	}
}

// TestDoRetriesOn503 5xx 服务端错误：短退避（Retry-After 或 1s）后重试
// 一次；第一次 503 第二次 200 即成功；连续 503 重试耗尽后硬失败
// （错误带状态码与完整 URL）。
func TestDoRetriesOn503(t *testing.T) {
	// 第一次 503、第二次 200 → 成功，请求共 2 次
	var calls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(lrclibSong{TrackName: "t", SyncedLyrics: "[00:01.00]ok"})
	}))
	c := NewClientWithBaseURL(server.URL, testUA)
	var song lrclibSong
	if err := c.do(context.Background(), server.URL+"/api/get?x=1", &song); err != nil {
		t.Fatalf("do: %v", err)
	}
	if calls != 2 {
		t.Errorf("calls = %d, want 2（503 后退避重试一次）", calls)
	}
	server.Close()

	// 连续 503 → 重试耗尽硬失败，错误带状态码与完整 URL
	calls = 0
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Retry-After", "0")
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer server.Close()
	c = NewClientWithBaseURL(server.URL, testUA)
	err := c.do(context.Background(), server.URL+"/api/get?y=2", &song)
	if err == nil || !strings.Contains(err.Error(), "503") || !strings.Contains(err.Error(), server.URL+"/api/get?y=2") {
		t.Errorf("err = %v, want 含 503 与完整 URL", err)
	}
	if calls != 2 {
		t.Errorf("calls = %d, want 2（503 重试一次后放弃）", calls)
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
		_ = json.NewEncoder(w).Encode(lrclibSong{TrackName: "t", SyncedLyrics: "[00:01.00]plain text"})
	}))
	defer server.Close()

	c := NewClient(testUA)
	c.baseURL = server.URL
	res, err := c.Fetch(context.Background(), model.Track{Title: "t", Artist: "a", Duration: 10})
	ly := res.Lyrics
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if calls != 2 {
		t.Errorf("calls = %d, want 2（429 后重试一次）", calls)
	}
	if len(ly.Lines) != 1 || ly.Lines[0].Text != "plain text" {
		t.Errorf("Lines = %+v, want [plain text]", ly.Lines)
	}
}

func TestFetchEmptyGetFallsBackToSearch(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/get":
			_ = json.NewEncoder(w).Encode(lrclibSong{TrackName: "t", Duration: 10})
		case "/api/search":
			_ = json.NewEncoder(w).Encode([]lrclibSong{{TrackName: "t", Duration: 10, SyncedLyrics: "[00:01.00]fallback"}})
		}
	}))
	defer server.Close()

	c := NewClient(testUA)
	c.baseURL = server.URL
	res, err := c.Fetch(context.Background(), model.Track{Title: "t", Artist: "a", Duration: 10})
	ly := res.Lyrics
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if len(ly.Lines) != 1 || ly.Lines[0].Text != "fallback" {
		t.Errorf("Lines = %+v, want [fallback]", ly.Lines)
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
	res, err := c.Fetch(context.Background(), model.Track{Title: "周杰倫 七里香 歌詞", Artist: "周杰倫", Duration: 300.0})
	ly := res.Lyrics
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
			w.Header().Set("Retry-After", "0")
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
	if searchCalls != 2 {
		t.Errorf("searchCalls = %d, want 2（500 仅同 URL 退避重试一次；非 ErrNotFound 应立即中断，不再尝试后续候选）", searchCalls)
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

// ── FetchForQuery（AI 清洗后重查：get 优先 + ≤3s 严格评分）──────────

func TestFetchForQueryGetHit(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/get" {
			t.Errorf("path = %q, want /api/get（清洗后应优先精确匹配）", r.URL.Path)
		}
		q := r.URL.Query()
		if q.Get("track_name") != "晴天" || q.Get("artist_name") != "周杰伦" {
			t.Errorf("query = %q, want track_name=晴天&artist_name=周杰伦", q)
		}
		_ = json.NewEncoder(w).Encode(lrclibSong{
			TrackName: "晴天", ArtistName: "周杰伦", Duration: 269.0,
			SyncedLyrics: "[00:01.00]故事的小黄花",
		})
	}))
	defer server.Close()

	c := NewClientWithBaseURL(server.URL, testUA)
	ly, err := c.FetchForQuery(context.Background(), "晴天", "周杰伦", 269.5)
	if err != nil {
		t.Fatalf("FetchForQuery: %v", err)
	}
	if len(ly.Lines) != 1 || ly.Lines[0].Text != "故事的小黄花" {
		t.Errorf("got %+v", ly.Lines)
	}
}

func TestFetchForQuerySearchPicksClosestWithin3s(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/get" {
			w.WriteHeader(http.StatusNotFound) // get 未命中 → 降级 search
			return
		}
		if r.URL.Path != "/api/search" {
			t.Errorf("path = %q, want /api/search", r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode([]lrclibSong{
			{TrackName: "晴天", ArtistName: "周杰伦", Duration: 274.0, SyncedLyrics: "[00:01.00]远"},
			{TrackName: "晴天", ArtistName: "周杰伦", Duration: 268.0, SyncedLyrics: "[00:01.00]近"},
		})
	}))
	defer server.Close()

	c := NewClientWithBaseURL(server.URL, testUA)
	ly, err := c.FetchForQuery(context.Background(), "晴天", "周杰伦", 269.0)
	if err != nil {
		t.Fatalf("FetchForQuery: %v", err)
	}
	if len(ly.Lines) != 1 || ly.Lines[0].Text != "近" {
		t.Errorf("应选差距最小（269.5 vs 268→Δ1 < 274→Δ5）的歌词，got %+v", ly.Lines)
	}
}

func TestFetchForQueryRejectsAllAbove3s(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/get" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_ = json.NewEncoder(w).Encode([]lrclibSong{
			{TrackName: "晴天", ArtistName: "周杰伦", Duration: 275.0, SyncedLyrics: "[00:01.00]现场版"},
			{TrackName: "晴天", ArtistName: "周杰伦", Duration: 260.0, SyncedLyrics: "[00:01.00]错歌"},
		})
	}))
	defer server.Close()

	c := NewClientWithBaseURL(server.URL, testUA)
	if _, err := c.FetchForQuery(context.Background(), "晴天", "周杰伦", 269.0); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound（所有候选差距 >3s 必须弃用）", err)
	}
}

func TestFetchForQueryBoundary3s(t *testing.T) {
	// 差距恰 3.0s：采用；3.01s：弃用
	for _, tc := range []struct {
		songDur  float64
		target   float64
		wantText string
	}{
		{272.0, 269.0, "边界内"},
		{272.01, 269.0, ""},
	} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_ = json.NewEncoder(w).Encode([]lrclibSong{
				{TrackName: "晴天", Duration: tc.songDur, SyncedLyrics: "[00:01.00]边界内"},
			})
		}))
		c := NewClientWithBaseURL(server.URL, testUA)
		ly, err := c.FetchForQuery(context.Background(), "晴天", "", tc.target)
		if tc.wantText == "" {
			if !errors.Is(err, ErrNotFound) {
				t.Errorf("songDur=%.2f: err = %v, want ErrNotFound", tc.songDur, err)
			}
		} else if err != nil || len(ly.Lines) == 0 || ly.Lines[0].Text != tc.wantText {
			t.Errorf("songDur=%.2f: got %v %+v, want 命中", tc.songDur, err, ly)
		}
		server.Close()
	}
}

func TestFetchForQueryEmptyArtistSkipsGet(t *testing.T) {
	gotSearch := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/get" {
			t.Error("artist 为空不应请求 /api/get（lrclib 会 400）")
		}
		gotSearch = true
		_ = json.NewEncoder(w).Encode([]lrclibSong{})
	}))
	defer server.Close()

	c := NewClientWithBaseURL(server.URL, testUA)
	if _, err := c.FetchForQuery(context.Background(), "晴天", "", 269.0); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
	if !gotSearch {
		t.Error("未走 /api/search")
	}
}

// TestFetchForQueryRejectsGetHitBeyond3s /api/get 命中但时长差距 >3s
// （非标准服务端不遵守 ±2s 契约）：不采用，降级 search。
func TestFetchForQueryRejectsGetHitBeyond3s(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/get" {
			_ = json.NewEncoder(w).Encode(lrclibSong{
				TrackName: "晴天", ArtistName: "周杰伦", Duration: 300.0, // Δ=31s
				SyncedLyrics: "[00:01.00]现场版",
			})
			return
		}
		_ = json.NewEncoder(w).Encode([]lrclibSong{})
	}))
	defer server.Close()

	c := NewClientWithBaseURL(server.URL, testUA)
	if _, err := c.FetchForQuery(context.Background(), "晴天", "周杰伦", 269.0); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound（get 命中但 Δ=31s > 3s 必须弃用）", err)
	}
}

// TestFetchPlainOnlyRejected 只有纯文本（无时间轴）歌词：视为无歌词
// （用户要求：歌词必须 sync，否则没有时间轴）。
func TestFetchPlainOnlyRejected(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/get":
			_ = json.NewEncoder(w).Encode(lrclibSong{
				TrackName: "晴天", ArtistName: "周杰伦", Duration: 269.0,
				PlainLyrics: "故事的小黄花\n从出生那年就飘着",
			})
		case "/api/search":
			_ = json.NewEncoder(w).Encode([]lrclibSong{})
		}
	}))
	defer server.Close()

	c := NewClientWithBaseURL(server.URL, testUA)
	_, err := c.Fetch(context.Background(), model.Track{Title: "晴天", Artist: "周杰倫", Duration: 269.0})
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound（纯文本歌词必须拒绝）", err)
	}
}

// TestFetchResultShape 确定性命中：FetchResult.Lyrics 非空，
// Title/Artist 为空（无 AI 信息，展示回落原始标题）。
func TestFetchResultShape(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(lrclibSong{
			TrackName: "晴天", ArtistName: "周杰伦", Duration: 269.0,
			SyncedLyrics: "[00:01.00]故事的小黄花",
		})
	}))
	defer server.Close()

	c := NewClientWithBaseURL(server.URL, testUA)
	res, err := c.Fetch(context.Background(), model.Track{Title: "晴天", Artist: "周杰倫", Duration: 269.0})
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if res.Lyrics == nil || len(res.Lyrics.Lines) != 1 {
		t.Fatalf("Lyrics = %+v, want 1 行", res.Lyrics)
	}
	if res.Title != "" || res.Artist != "" {
		t.Errorf("确定性路径 Title/Artist = %q/%q, want 空", res.Title, res.Artist)
	}
}
