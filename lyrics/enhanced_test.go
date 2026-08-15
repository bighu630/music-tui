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

// aiArtist 是本测试约定的 AI 清洗结果歌手（简化字）——mock 用它区分
// 「AI 严格重查」（artist=aiArtist）与「确定性兜底」（频道名/繁体）。
const aiArtist = "周杰伦"

// aiHitSong 是 AI 严格重查命中返回的歌词。
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

// isAIQuery 判断请求是否为 AI 严格重查（artist_name=aiArtist）。
func isAIQuery(r *http.Request) bool {
	return r.URL.Query().Get("artist_name") == aiArtist
}

// isDetQuery 判断请求是否为确定性兜底查询（非 AI 重查）。
func isDetQuery(r *http.Request) bool {
	return r.URL.Query().Get("artist_name") != aiArtist
}

// lrclibAIMatch 查询感知 mock：/api/get 仅对 AI 严格重查
// （artist_name=aiArtist）返回歌词并计数（aiGetCount 可为 nil），
// 其余 get 404、search 空——确定性兜底恒未命中，AI 重查按需命中。
func lrclibAIMatch(aiGetCount *int32) func(http.ResponseWriter, *http.Request) {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/get" {
			if isAIQuery(r) {
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

// aiOK 返回识别「晴天/周杰伦」的 AI mock。
func aiOK(w http.ResponseWriter, r *http.Request) {
	aiRespond(w, r, `{"is_song": true, "title": "晴天", "artist": "`+aiArtist+`"}`)
}

// ── AI 优先流程 ──────────────────────────────────────────────────

// TestEnhancedAIHitsWithoutDeterministic AI 识别成功 + 严格重查命中：
// 确定性路径完全不跑（lrclib 只收到 1 次 AI 重查请求）。
func TestEnhancedAIHitsWithoutDeterministic(t *testing.T) {
	c, aiCalls, lrclibCalls := newEnhancedTestEnv(t, lrclibAIMatch(nil), aiOK)
	res, err := c.Fetch(context.Background(), model.Track{Title: "【周杰倫】晴天 MV", Artist: "周杰倫官方頻道", Duration: 269.0})
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if res.Lyrics == nil || len(res.Lyrics.Lines) != 1 || res.Lyrics.Lines[0].Text != "故事的小黄花" {
		t.Errorf("Lyrics = %+v", res.Lyrics)
	}
	if res.Lyrics.Source != LyricsSourceAI {
		t.Errorf("Source = %q, want %q", res.Lyrics.Source, LyricsSourceAI)
	}
	if res.Title != "晴天" || res.Artist != aiArtist {
		t.Errorf("Title/Artist = %q/%q", res.Title, res.Artist)
	}
	if *aiCalls != 1 {
		t.Errorf("AI 调用 %d 次, want 1", *aiCalls)
	}
	if *lrclibCalls != 1 {
		t.Errorf("lrclib 请求 %d 次, want 1（AI 优先：确定性路径不跑）", *lrclibCalls)
	}
}

// TestEnhancedNoAIStillDegrades AI 未配置（nil）：行为与纯确定性客户端
// 一致——未命中返回 ErrNotFound。
func TestEnhancedNoAIStillDegrades(t *testing.T) {
	c, aiCalls, _ := newEnhancedTestEnv(t, lrclibNotFound, nil)
	_, err := c.Fetch(context.Background(), model.Track{Title: "晴天", Artist: "周杰倫", Duration: 269.0})
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
	if *aiCalls != 0 {
		t.Errorf("AI 调用 %d 次, want 0", *aiCalls)
	}
}

// TestEnhancedStrictMissFallsBackToDeterministic AI 严格重查未命中
// （>3s 候选）→ 确定性兜底（30s 多候选）命中：返回兜底歌词，
// 展示信息仍用 AI 清洗标题。
func TestEnhancedStrictMissFallsBackToDeterministic(t *testing.T) {
	var aiGets, detGets int32
	c, _, _ := newEnhancedTestEnv(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/get" {
			if isAIQuery(r) {
				atomic.AddInt32(&aiGets, 1)
				w.WriteHeader(http.StatusNotFound) // AI 严格重查 get 未命中
				return
			}
			atomic.AddInt32(&detGets, 1)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if isAIQuery(r) {
			// AI 严格重查 search：全部差距 >3s → 未命中
			_ = json.NewEncoder(w).Encode([]lrclibSong{
				{TrackName: "晴天", ArtistName: aiArtist, Duration: 275.0, SyncedLyrics: "[00:01.00]现场版"},
			})
			return
		}
		// 确定性兜底 search：差距 10s ≤30s → 命中
		_ = json.NewEncoder(w).Encode([]lrclibSong{
			{TrackName: "晴天", ArtistName: "周杰倫", Duration: 279.0, SyncedLyrics: "[00:01.00]确定性兜底"},
		})
	}, aiOK)

	res, err := c.Fetch(context.Background(), model.Track{Title: "晴天", Artist: "周杰倫", Duration: 269.0})
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if len(res.Lyrics.Lines) != 1 || res.Lyrics.Lines[0].Text != "确定性兜底" {
		t.Errorf("应返回确定性兜底歌词, got %+v", res.Lyrics.Lines)
	}
	if res.Title != "晴天" || res.Artist != aiArtist {
		t.Errorf("兜底路径也应携带 AI 展示信息: %q/%q", res.Title, res.Artist)
	}
	if res.Lyrics.Source != LyricsSourceAI {
		t.Errorf("兜底结果 Source = %q, want ai（AI 参与识别）", res.Lyrics.Source)
	}
	if aiGets != 1 || detGets != 1 {
		t.Errorf("aiGets=%d detGets=%d, want 各 1（先严格后兜底）", aiGets, detGets)
	}
}

// TestEnhancedStrictAndDetBothMiss 严格重查与确定性兜底都未命中 →
// ErrNotFound（不返回差距大的歌词）。
func TestEnhancedStrictAndDetBothMiss(t *testing.T) {
	c, _, _ := newEnhancedTestEnv(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/get" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_ = json.NewEncoder(w).Encode([]lrclibSong{})
	}, aiOK)
	_, err := c.Fetch(context.Background(), model.Track{Title: "晴天", Artist: "周杰倫", Duration: 269.0})
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

// TestEnhancedNotSongFallsBackToDeterministic AI 判定非歌曲 → 确定性
// 兜底；负缓存生效（不重复调 AI）。
func TestEnhancedNotSongFallsBackToDeterministic(t *testing.T) {
	detHit := false
	c, aiCalls, _ := newEnhancedTestEnv(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/get" {
			if isDetQuery(r) {
				detHit = true
				_ = json.NewEncoder(w).Encode(lrclibSong{
					TrackName: "城市漫步", ArtistName: "C", Duration: 600.0,
					SyncedLyrics: "[00:01.00]Vlog 配乐",
				})
				return
			}
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_ = json.NewEncoder(w).Encode([]lrclibSong{})
	}, func(w http.ResponseWriter, r *http.Request) {
		aiRespond(w, r, `{"is_song": false}`)
	})
	track := model.Track{Title: "城市漫步 Vlog", Artist: "C", Duration: 600.0}
	res, err := c.Fetch(context.Background(), track)
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if !detHit {
		t.Error("非歌曲应走确定性兜底")
	}
	if len(res.Lyrics.Lines) != 1 {
		t.Errorf("兜底歌词 = %+v", res.Lyrics.Lines)
	}
	if res.Title != "" || res.Lyrics.Source != "" {
		t.Errorf("纯确定性兜底不应携带 AI 信息: %q/%q", res.Title, res.Lyrics.Source)
	}
	// 负缓存：二次播放不调 AI
	if _, err := c.Fetch(context.Background(), track); err != nil {
		t.Fatalf("二次 Fetch: %v", err)
	}
	if *aiCalls != 1 {
		t.Errorf("AI 调用 %d 次, want 1（负缓存应生效）", *aiCalls)
	}
}

// TestEnhancedAIFailureFallsBackToDeterministic AI 调用失败（降级）→
// 确定性兜底；失败不缓存（下次仍尝试 AI）。
func TestEnhancedAIFailureFallsBackToDeterministic(t *testing.T) {
	fail := true
	var aiCalls *int32
	var c *EnhancedClient
	c, aiCalls, _ = newEnhancedTestEnv(t, lrclibAIMatch(nil), func(w http.ResponseWriter, r *http.Request) {
		if fail {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		aiOK(w, r)
	})
	track := model.Track{Title: "晴天", Artist: "周杰倫", Duration: 269.0}
	if _, err := c.Fetch(context.Background(), track); !errors.Is(err, ErrNotFound) {
		t.Fatalf("AI 失败 + 确定性未命中: err = %v, want ErrNotFound", err)
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

// TestEnhancedStrictErrorPassthrough AI 严格重查遇到服务端错误：原样
// 透传（不再兜底——服务端故障兜底只会再撞一次）。
func TestEnhancedStrictErrorPassthrough(t *testing.T) {
	c, aiCalls, lrclibCalls := newEnhancedTestEnv(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}, aiOK)
	_, err := c.Fetch(context.Background(), model.Track{Title: "晴天", Artist: "周杰倫", Duration: 269.0})
	if err == nil || errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want 非 NotFound 原样透传", err)
	}
	if *aiCalls != 1 {
		t.Errorf("AI 调用 %d 次, want 1", *aiCalls)
	}
	if *lrclibCalls != 1 {
		t.Errorf("lrclib 请求 %d 次, want 1（严格查询错误不兜底）", *lrclibCalls)
	}
}

// TestEnhancedAICachesAvoidRepeatRequests 第二次播放（同标题不同 track
// ID）：AI 结果缓存 + 歌词缓存命中，既不调用 AI 也不请求 lrclib。
func TestEnhancedAICachesAvoidRepeatRequests(t *testing.T) {
	var aiGetCount int32
	c, aiCalls, _ := newEnhancedTestEnv(t, lrclibAIMatch(&aiGetCount), aiOK)
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

// TestEnhancedAICacheHitStillQueriesLrclib AI 结果缓存命中（无歌词
// 缓存）：跳过 AI，但严格重查 + 确定性兜底仍发生。
func TestEnhancedAICacheHitStillQueriesLrclib(t *testing.T) {
	aiSearches := int32(0)
	c, aiCalls, _ := newEnhancedTestEnv(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/get" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if isAIQuery(r) {
			atomic.AddInt32(&aiSearches, 1)
		}
		_ = json.NewEncoder(w).Encode([]lrclibSong{})
	}, aiOK)
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
		t.Errorf("AI 严格重查 search %d 次, want 2（无歌词缓存时每次仍重查）", aiSearches)
	}
}

// TestEnhancedNullTitleFallsBackToDeterministic AI 识别成功但空标题：
// 不拿空标题查询，走确定性兜底（结果被负缓存，不重复调 AI）。
func TestEnhancedNullTitleFallsBackToDeterministic(t *testing.T) {
	c, aiCalls, _ := newEnhancedTestEnv(t, lrclibNotFound, func(w http.ResponseWriter, r *http.Request) {
		aiRespond(w, r, `{"is_song": true, "title": ""}`)
	})
	track := model.Track{Title: "???", Artist: "?", Duration: 100.0}
	if _, err := c.Fetch(context.Background(), track); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
	if _, err := c.Fetch(context.Background(), track); !errors.Is(err, ErrNotFound) {
		t.Fatalf("二次 err = %v, want ErrNotFound", err)
	}
	if *aiCalls != 1 {
		t.Errorf("AI 调用 %d 次, want 1（空标题结果负缓存，不再调 AI）", *aiCalls)
	}
}

// TestEnhancedLRCCachePersistsAcrossRestart 歌词缓存落盘：重建客户端后
// （模拟重启）AI 结果与歌词都从磁盘命中，不再调用 AI、不再请求 lrclib。
func TestEnhancedLRCCachePersistsAcrossRestart(t *testing.T) {
	dir := t.TempDir()
	var aiGetCount int32
	lrclib := httptest.NewServer(http.HandlerFunc(lrclibAIMatch(&aiGetCount)))
	defer lrclib.Close()
	aiCalls := int32(0)
	aiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		aiCalls++
		if aiCalls > 1 {
			t.Error("重启后不应再调用 AI")
		}
		aiOK(w, r)
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
	if err != nil {
		t.Fatalf("重启后 Fetch: %v", err)
	}
	if len(res.Lyrics.Lines) != 1 || res.Lyrics.Lines[0].Text != "故事的小黄花" {
		t.Errorf("重启后歌词 = %+v", res.Lyrics.Lines)
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
		aiOK(w, r)
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

// TestEnhancedAIResultCarriesCleanTitle AI 命中：FetchResult 携带清洗后
// 歌名/歌手（live 路径），展示层可覆盖原始 YouTube 标题。
func TestEnhancedAIResultCarriesCleanTitle(t *testing.T) {
	c, _, _ := newEnhancedTestEnv(t, lrclibAIMatch(nil), aiOK)
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
// 携带 AI 标题。
func TestEnhancedAIResultCarriesCleanTitleFromCache(t *testing.T) {
	c, _, _ := newEnhancedTestEnv(t, lrclibAIMatch(nil), aiOK)
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

// TestEnhancedPlainOnlyRejected 纯文本（无时间轴）歌词：严格重查与
// 确定性兜底都拒绝（sync-only 规则约束全链路）。
func TestEnhancedPlainOnlyRejected(t *testing.T) {
	c, _, _ := newEnhancedTestEnv(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/get" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_ = json.NewEncoder(w).Encode([]lrclibSong{
			{TrackName: "晴天", ArtistName: "周杰伦", Duration: 269.0,
				PlainLyrics: "故事的小黄花"},
		})
	}, aiOK)
	_, err := c.Fetch(context.Background(), model.Track{Title: "晴天", Artist: "周杰倫", Duration: 269.0})
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound（纯文本歌词必须拒绝）", err)
	}
}

// TestEnhancedFailurePathsCarryNoTitle AI 失败/非歌曲路径的兜底结果
// 不携带 AI 标题（契约：只有 AI 识别成功才填充）。
func TestEnhancedFailurePathsCarryNoTitle(t *testing.T) {
	c, _, _ := newEnhancedTestEnv(t, lrclibNotFound, func(w http.ResponseWriter, r *http.Request) {
		aiRespond(w, r, `{"is_song": false}`)
	})
	res, err := c.Fetch(context.Background(), model.Track{Title: "城市漫步 Vlog", Artist: "C", Duration: 600})
	if !errors.Is(err, ErrNotFound) || res.Title != "" || res.Artist != "" {
		t.Errorf("非歌曲路径 = %+v, %v, want ErrNotFound 且 Title/Artist 空", res, err)
	}

	c2, _, _ := newEnhancedTestEnv(t, lrclibNotFound, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})
	res, err = c2.Fetch(context.Background(), model.Track{Title: "晴天", Artist: "A", Duration: 269})
	if !errors.Is(err, ErrNotFound) || res.Title != "" {
		t.Errorf("AI 失败路径 = %+v, %v, want ErrNotFound 且 Title 空", res, err)
	}
}
