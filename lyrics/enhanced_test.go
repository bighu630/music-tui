package lyrics

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"music-tui/model"
)

// Fetcher 接口编译期断言：确定性客户端与增强客户端都可用作歌词来源。
var (
	_ Fetcher = (*Client)(nil)
	_ Fetcher = (*EnhancedClient)(nil)
)

// aiArtist 是本测试约定的 AI 清洗结果歌手（简化字）——mock 用它
// 区分「确定性查询」（channel 名，繁体）与「AI 重查」。
const aiArtist = "周杰伦"

// aiHitSong 是 AI 重查命中返回的歌词。
var aiHitSong = lrclibSong{
	TrackName: "晴天", ArtistName: aiArtist, Duration: 269.0,
	SyncedLyrics: "[00:01.00]故事的小黄花",
}

// newEnhancedTestEnv 组装 lrclib + OpenAI 双 mock 与增强客户端。
// lrclib 行为由 lrclibHandler 决定；aiHandler 为 nil 时不调用 AI。
func newEnhancedTestEnv(t *testing.T, lrclibHandler func(http.ResponseWriter, *http.Request), aiHandler func(http.ResponseWriter, *http.Request)) (*EnhancedClient, *int32, *int32) {
	t.Helper()
	var aiCalls, lrclibCalls int32
	lrclib := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&lrclibCalls, 1)
		if lrclibHandler != nil {
			lrclibHandler(w, r)
		}
	}))
	t.Cleanup(lrclib.Close)

	var ai *OpenAIClient
	if aiHandler != nil {
		aiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			atomic.AddInt32(&aiCalls, 1)
			aiHandler(w, r)
		}))
		t.Cleanup(aiServer.Close)
		ai = NewOpenAIClientWithBaseURL("sk-test", "gpt-4o-mini", aiServer.URL)
	}

	c, err := NewEnhancedClient(NewClientWithBaseURL(lrclib.URL, testUA), ai, t.TempDir())
	if err != nil {
		t.Fatalf("NewEnhancedClient: %v", err)
	}
	return c, &aiCalls, &lrclibCalls
}

// lrclibNotFound 标准"无歌词" mock：get 404、search 空数组。
func lrclibNotFound(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/api/get" {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	_ = json.NewEncoder(w).Encode([]lrclibSong{})
}

// lrclibAIMatch 查询感知 mock：/api/get 仅对 AI 清洗后的查询
// （artist_name=aiArtist）返回歌词并计数（aiGetCount 可为 nil），
// 其余 get 404、search 空——确定性路径永远未命中，AI 重查按需命中。
func lrclibAIMatch(aiGetCount *int32) func(http.ResponseWriter, *http.Request) {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/get" {
			if r.URL.Query().Get("artist_name") == aiArtist {
				if aiGetCount != nil {
					atomic.AddInt32(aiGetCount, 1)
				}
				_ = json.NewEncoder(w).Encode(aiHitSong)
				return
			}
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_ = json.NewEncoder(w).Encode([]lrclibSong{})
	}
}

// aiRespond 输出固定识别结果的 AI mock（正确转义 JSON）。
func aiRespond(w http.ResponseWriter, r *http.Request, content string) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"choices": []map[string]interface{}{
			{"message": map[string]string{"content": content}},
		},
	})
}

func TestEnhancedDeterministicHitSkipsAI(t *testing.T) {
	var aiCalls, lrclibCalls int32
	lrclib := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&lrclibCalls, 1)
		_ = json.NewEncoder(w).Encode(lrclibSong{
			TrackName: "晴天", ArtistName: "周杰伦", Duration: 269.0,
			SyncedLyrics: "[00:01.00]故事的小黄花",
		})
	}))
	defer lrclib.Close()
	aiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&aiCalls, 1)
		t.Error("确定性命中不应调用 AI")
	}))
	defer aiServer.Close()

	c, err := NewEnhancedClient(
		NewClientWithBaseURL(lrclib.URL, testUA),
		NewOpenAIClientWithBaseURL("sk-test", "", aiServer.URL),
		t.TempDir(),
	)
	if err != nil {
		t.Fatal(err)
	}
	res, err := c.Fetch(context.Background(), model.Track{Title: "晴天", Artist: "周杰倫", Duration: 269.0})
	ly := res.Lyrics
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if len(ly.Lines) != 1 {
		t.Errorf("got %+v", ly.Lines)
	}
	if ly.Source != "" {
		t.Errorf("确定性路径 Source = %q, want 空", ly.Source)
	}
	if aiCalls != 0 || lrclibCalls != 1 {
		t.Errorf("AI=%d lrclib=%d, want AI=0 lrclib=1", aiCalls, lrclibCalls)
	}
}

func TestEnhancedNoAIStillDegrades(t *testing.T) {
	// AI 未配置（nil）：行为与纯确定性客户端一致——未命中返回 ErrNotFound
	c, aiCalls, _ := newEnhancedTestEnv(t, lrclibNotFound, nil)
	_, err := c.Fetch(context.Background(), model.Track{Title: "晴天", Artist: "周杰倫", Duration: 269.0})
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
	if *aiCalls != 0 {
		t.Errorf("AI 调用 %d 次, want 0", *aiCalls)
	}
}

func TestEnhancedAIPathHits(t *testing.T) {
	var aiGetCount int32
	c, aiCalls, _ := newEnhancedTestEnv(t, lrclibAIMatch(&aiGetCount), func(w http.ResponseWriter, r *http.Request) {
		aiRespond(w, r, `{"is_song": true, "title": "晴天", "artist": "`+aiArtist+`"}`)
	})
	res, err := c.Fetch(context.Background(), model.Track{Title: "【周杰倫】晴天 MV", Artist: "周杰倫官方頻道", Duration: 269.0})
	ly := res.Lyrics
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if len(ly.Lines) != 1 || ly.Lines[0].Text != "故事的小黄花" {
		t.Errorf("got %+v", ly.Lines)
	}
	if ly.Source != LyricsSourceAI {
		t.Errorf("Source = %q, want %q", ly.Source, LyricsSourceAI)
	}
	if *aiCalls != 1 {
		t.Errorf("AI 调用 %d 次, want 1", *aiCalls)
	}
	if aiGetCount != 1 {
		t.Errorf("AI 重查 /api/get %d 次, want 1", aiGetCount)
	}
}

func TestEnhancedAINotSongNegativeCached(t *testing.T) {
	c, aiCalls, _ := newEnhancedTestEnv(t, lrclibNotFound, func(w http.ResponseWriter, r *http.Request) {
		aiRespond(w, r, `{"is_song": false}`)
	})
	track := model.Track{Title: "城市漫步 Vlog", Artist: "SomeChannel", Duration: 600.0}
	for i := 0; i < 2; i++ {
		if _, err := c.Fetch(context.Background(), track); !errors.Is(err, ErrNotFound) {
			t.Fatalf("第 %d 次 Fetch err = %v, want ErrNotFound", i+1, err)
		}
	}
	if *aiCalls != 1 {
		t.Errorf("AI 调用 %d 次, want 1（负缓存应生效）", *aiCalls)
	}
}

func TestEnhancedAIFailureNotCached(t *testing.T) {
	// AI 调用失败：降级 ErrNotFound；失败不缓存（下次仍尝试 AI）
	fail := true
	var aiCalls *int32
	var c *EnhancedClient
	c, aiCalls, _ = newEnhancedTestEnv(t, lrclibAIMatch(nil), func(w http.ResponseWriter, r *http.Request) {
		if fail {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		aiRespond(w, r, `{"is_song": true, "title": "晴天", "artist": "`+aiArtist+`"}`)
	})
	track := model.Track{Title: "晴天", Artist: "周杰倫", Duration: 269.0}
	if _, err := c.Fetch(context.Background(), track); !errors.Is(err, ErrNotFound) {
		t.Fatalf("AI 失败时 err = %v, want ErrNotFound（降级确定性结果）", err)
	}
	if *aiCalls != 2 {
		t.Errorf("AI 调用 %d 次, want 2（瞬时错误重试一次）", *aiCalls)
	}
	fail = false
	if _, err := c.Fetch(context.Background(), track); err != nil {
		t.Fatalf("AI 恢复后 Fetch: %v", err)
	}
	if *aiCalls != 3 {
		t.Errorf("失败被错误缓存：AI 调用 %d 次, want 3", *aiCalls)
	}
}

func TestEnhancedAIDurationRuleRejects(t *testing.T) {
	// AI 清洗后重查的候选全部差距 >3s：视为无歌词。
	// mock：确定性查询（artist=周杰倫）恒空；AI 重查（artist=周杰伦）
	// 的 search 返回差距 6s/9s 的候选——确定性路径 30s 阈值不可见它们。
	c, _, _ := newEnhancedTestEnv(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/get" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if r.URL.Query().Get("artist_name") == aiArtist {
			_ = json.NewEncoder(w).Encode([]lrclibSong{
				{TrackName: "晴天", ArtistName: aiArtist, Duration: 275.0, SyncedLyrics: "[00:01.00]现场版"},
				{TrackName: "晴天", ArtistName: aiArtist, Duration: 260.0, SyncedLyrics: "[00:01.00]错歌"},
			})
			return
		}
		_ = json.NewEncoder(w).Encode([]lrclibSong{})
	}, func(w http.ResponseWriter, r *http.Request) {
		aiRespond(w, r, `{"is_song": true, "title": "晴天", "artist": "`+aiArtist+`"}`)
	})
	_, err := c.Fetch(context.Background(), model.Track{Title: "晴天", Artist: "周杰倫", Duration: 269.0})
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound（全部候选 >3s 弃用）", err)
	}
}

func TestEnhancedAICachesAvoidRepeatRequests(t *testing.T) {
	// 第二次播放（同标题不同 track ID）：AI 结果缓存 + 歌词缓存命中，
	// 既不调用 AI 也不再 AI 重查 lrclib（确定性路径照常先跑）。
	var aiGetCount int32
	c, aiCalls, _ := newEnhancedTestEnv(t, lrclibAIMatch(&aiGetCount), func(w http.ResponseWriter, r *http.Request) {
		aiRespond(w, r, `{"is_song": true, "title": "晴天", "artist": "`+aiArtist+`"}`)
	})
	track1 := model.Track{Title: "【周杰倫】晴天 MV", Artist: "周杰倫官方頻道", Duration: 269.0, ID: "aaa"}
	track2 := model.Track{Title: "【周杰倫】晴天 MV", Artist: "周杰倫官方頻道", Duration: 269.0, ID: "bbb"}

	if _, err := c.Fetch(context.Background(), track1); err != nil {
		t.Fatalf("首次 Fetch: %v", err)
	}
	if _, err := c.Fetch(context.Background(), track2); err != nil {
		t.Fatalf("二次 Fetch: %v", err)
	}
	if *aiCalls != 1 {
		t.Errorf("AI 调用 %d 次, want 1（AI 结果缓存应命中）", *aiCalls)
	}
	if aiGetCount != 1 {
		t.Errorf("AI 重查 %d 次, want 1（歌词缓存应命中，不再请求）", aiGetCount)
	}
}

func TestEnhancedAICacheHitStillQueriesLrclib(t *testing.T) {
	// AI 结果缓存命中（无歌词缓存）：跳过 AI，但 lrclib 重查仍发生
	aiSearches := int32(0)
	c, aiCalls, _ := newEnhancedTestEnv(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/get" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if r.URL.Query().Get("artist_name") == aiArtist {
			atomic.AddInt32(&aiSearches, 1)
		}
		_ = json.NewEncoder(w).Encode([]lrclibSong{})
	}, func(w http.ResponseWriter, r *http.Request) {
		aiRespond(w, r, `{"is_song": true, "title": "晴天", "artist": "`+aiArtist+`"}`)
	})
	track := model.Track{Title: "晴天", Artist: "周杰倫", Duration: 269.0}
	for i := 0; i < 2; i++ {
		if _, err := c.Fetch(context.Background(), track); !errors.Is(err, ErrNotFound) {
			t.Fatalf("Fetch %d: %v, want ErrNotFound", i, err)
		}
	}
	if *aiCalls != 1 {
		t.Errorf("AI 调用 %d 次, want 1（AI 结果缓存命中，不再调 AI）", *aiCalls)
	}
	if aiSearches != 2 {
		t.Errorf("AI 重查 search %d 次, want 2（无歌词缓存时每次仍重查 lrclib）", aiSearches)
	}
}

func TestEnhancedAINullTitleRejected(t *testing.T) {
	// AI 说 is_song=true 但没给 title：视为识别失败，不拿空标题去查询
	c, _, _ := newEnhancedTestEnv(t, lrclibNotFound, func(w http.ResponseWriter, r *http.Request) {
		aiRespond(w, r, `{"is_song": true, "title": ""}`)
	})
	_, err := c.Fetch(context.Background(), model.Track{Title: "???", Artist: "?", Duration: 100.0})
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

func TestEnhancedLRCCachePersistsAcrossRestart(t *testing.T) {
	// 歌词缓存落盘：重建客户端后（模拟重启）AI 结果与歌词都从磁盘命中，
	// 不再调用 AI、也不再 AI 重查 lrclib。
	dir := t.TempDir()
	var aiGetCount int32
	aiCalls := int32(0)
	lrclib := httptest.NewServer(http.HandlerFunc(lrclibAIMatch(&aiGetCount)))
	defer lrclib.Close()
	aiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		aiCalls++
		if aiCalls > 1 {
			t.Error("重启后不应再调用 AI")
		}
		aiRespond(w, r, `{"is_song": true, "title": "晴天", "artist": "`+aiArtist+`"}`)
	}))
	defer aiServer.Close()

	track := model.Track{Title: "【周杰倫】晴天 MV", Artist: "周杰倫官方頻道", Duration: 269.0}
	c1, err := NewEnhancedClient(
		NewClientWithBaseURL(lrclib.URL, testUA),
		NewOpenAIClientWithBaseURL("sk-test", "", aiServer.URL),
		dir,
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c1.Fetch(context.Background(), track); err != nil {
		t.Fatalf("首次 Fetch: %v", err)
	}

	c2, err := NewEnhancedClient(
		NewClientWithBaseURL(lrclib.URL, testUA),
		NewOpenAIClientWithBaseURL("sk-test", "", aiServer.URL),
		dir,
	)
	if err != nil {
		t.Fatal(err)
	}
	res, err := c2.Fetch(context.Background(), track)
	ly := res.Lyrics
	if err != nil {
		t.Fatalf("重启后 Fetch: %v", err)
	}
	if len(ly.Lines) != 1 || ly.Lines[0].Text != "故事的小黄花" {
		t.Errorf("重启后歌词 = %+v", ly.Lines)
	}
	if aiGetCount != 1 {
		t.Errorf("AI 重查 %d 次, want 1（重启后歌词缓存命中，不再请求）", aiGetCount)
	}
}

// TestEnhancedConcurrentSameTrackSingleFlight 并发播放同一标题：AI 只
// 调用一次（single-flight），等待者复用执行者结果。
func TestEnhancedConcurrentSameTrackSingleFlight(t *testing.T) {
	var aiCalls int32
	aiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&aiCalls, 1)
		time.Sleep(200 * time.Millisecond) // 拉长识别窗口，让并发请求重叠
		aiRespond(w, r, `{"is_song": true, "title": "晴天", "artist": "`+aiArtist+`"}`)
	}))
	defer aiServer.Close()
	lrclib := httptest.NewServer(http.HandlerFunc(lrclibAIMatch(nil)))
	defer lrclib.Close()

	c, err := NewEnhancedClient(
		NewClientWithBaseURL(lrclib.URL, testUA),
		NewOpenAIClientWithBaseURL("sk-test", "", aiServer.URL),
		t.TempDir(),
	)
	if err != nil {
		t.Fatal(err)
	}
	track := model.Track{Title: "【周杰倫】晴天 MV", Artist: "周杰倫官方頻道", Duration: 269.0}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	const n = 4
	var wg sync.WaitGroup
	errs := make([]error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, errs[i] = c.Fetch(ctx, track)
		}(i)
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Errorf("并发 Fetch %d: %v", i, err)
		}
	}
	if aiCalls != 1 {
		t.Errorf("AI 调用 %d 次, want 1（single-flight 应合并并发识别）", aiCalls)
	}
}

// TestEnhancedPassesThroughNonNotFound 确定性路径的非 NotFound 错误
// （如 lrclib 5xx）必须原样透传，不得吞成 ErrNotFound 或触发 AI。
func TestEnhancedPassesThroughNonNotFound(t *testing.T) {
	var aiCalls int32
	aiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&aiCalls, 1)
		aiRespond(w, r, `{"is_song": true, "title": "晴天", "artist": "`+aiArtist+`"}`)
	}))
	defer aiServer.Close()
	lrclib := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer lrclib.Close()

	c, err := NewEnhancedClient(
		NewClientWithBaseURL(lrclib.URL, testUA),
		NewOpenAIClientWithBaseURL("sk-test", "", aiServer.URL),
		t.TempDir(),
	)
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.Fetch(context.Background(), model.Track{Title: "晴天", Artist: "周杰倫", Duration: 269.0})
	if err == nil || errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want 非 NotFound 原样透传", err)
	}
	if aiCalls != 0 {
		t.Errorf("AI 调用 %d 次, want 0（服务端错误不应进入 AI 路径）", aiCalls)
	}
}

// TestEnhancedAIResultCarriesCleanTitle AI 命中：FetchResult 携带清洗后
// 歌名/歌手（live 路径），展示层可覆盖原始 YouTube 标题。
func TestEnhancedAIResultCarriesCleanTitle(t *testing.T) {
	var aiGetCount int32
	c, _, _ := newEnhancedTestEnv(t, lrclibAIMatch(&aiGetCount), func(w http.ResponseWriter, r *http.Request) {
		aiRespond(w, r, `{"is_song": true, "title": "晴天", "artist": "`+aiArtist+`"}`)
	})
	res, err := c.Fetch(context.Background(), model.Track{Title: "【周杰倫】晴天 MV", Artist: "周杰倫官方頻道", Duration: 269.0})
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if res.Title != "晴天" || res.Artist != aiArtist {
		t.Errorf("Title/Artist = %q/%q, want 晴天/%s", res.Title, res.Artist, aiArtist)
	}
	if res.Lyrics == nil || len(res.Lyrics.Lines) != 1 {
		t.Errorf("Lyrics = %+v", res.Lyrics)
	}
}

// TestEnhancedAIResultCarriesCleanTitleFromCache 双缓存命中路径同样
// 携带 AI 标题（AI 结果缓存 + 歌词缓存均需返回展示信息）。
func TestEnhancedAIResultCarriesCleanTitleFromCache(t *testing.T) {
	var aiGetCount int32
	c, _, _ := newEnhancedTestEnv(t, lrclibAIMatch(&aiGetCount), func(w http.ResponseWriter, r *http.Request) {
		aiRespond(w, r, `{"is_song": true, "title": "晴天", "artist": "`+aiArtist+`"}`)
	})
	track := model.Track{Title: "【周杰倫】晴天 MV", Artist: "周杰倫官方頻道", Duration: 269.0}
	first, err := c.Fetch(context.Background(), track)
	if err != nil {
		t.Fatalf("首次 Fetch: %v", err)
	}
	second, err := c.Fetch(context.Background(), track)
	if err != nil {
		t.Fatalf("二次 Fetch: %v", err)
	}
	for i, res := range []FetchResult{first, second} {
		if res.Title != "晴天" || res.Artist != aiArtist {
			t.Errorf("第 %d 次 Title/Artist = %q/%q, want 晴天/%s", i+1, res.Title, res.Artist, aiArtist)
		}
	}
}

// TestEnhancedPlainOnlyRejected AI 重查只返回纯文本歌词：视为无歌词
// （sync-only 规则同样约束 AI 路径）。
func TestEnhancedPlainOnlyRejected(t *testing.T) {
	c, _, _ := newEnhancedTestEnv(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/get" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if r.URL.Query().Get("artist_name") == aiArtist {
			_ = json.NewEncoder(w).Encode([]lrclibSong{
				{TrackName: "晴天", ArtistName: aiArtist, Duration: 269.0,
					PlainLyrics: "故事的小黄花"},
			})
			return
		}
		_ = json.NewEncoder(w).Encode([]lrclibSong{})
	}, func(w http.ResponseWriter, r *http.Request) {
		aiRespond(w, r, `{"is_song": true, "title": "晴天", "artist": "`+aiArtist+`"}`)
	})
	_, err := c.Fetch(context.Background(), model.Track{Title: "晴天", Artist: "周杰倫", Duration: 269.0})
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound（AI 路径同样拒绝纯文本歌词）", err)
	}
}

// TestEnhancedFailurePathsCarryNoTitle 负缓存/AI 失败路径不携带 AI 标题
// （契约：只有成功识别才填充 FetchResult.Title/Artist）。
func TestEnhancedFailurePathsCarryNoTitle(t *testing.T) {
	// is_song=false（负缓存）
	c, _, _ := newEnhancedTestEnv(t, lrclibNotFound, func(w http.ResponseWriter, r *http.Request) {
		aiRespond(w, r, `{"is_song": false}`)
	})
	res, err := c.Fetch(context.Background(), model.Track{Title: "城市漫步 Vlog", Artist: "C", Duration: 600})
	if !errors.Is(err, ErrNotFound) || res.Title != "" || res.Artist != "" {
		t.Errorf("负缓存路径 = %+v, %v, want ErrNotFound 且 Title/Artist 空", res, err)
	}

	// AI 调用失败（降级）
	c2, _, _ := newEnhancedTestEnv(t, lrclibNotFound, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})
	res, err = c2.Fetch(context.Background(), model.Track{Title: "晴天", Artist: "A", Duration: 269})
	if !errors.Is(err, ErrNotFound) || res.Title != "" {
		t.Errorf("AI 失败路径 = %+v, %v, want ErrNotFound 且 Title 空", res, err)
	}
}
