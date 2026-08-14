package ui

// 音频缓存播放链路集成测试：
// 命中播本地文件 / 未命中播网络 URL + 后台下载 / LoadFailed 移除损坏条目并
// 回退网络重试 / 续播恢复命中本地。

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"music-tui/cache"
	"music-tui/cover"
	"music-tui/history"
	"music-tui/lyrics"
	"music-tui/player"
	"music-tui/playlists"
	"music-tui/session"
)

// newCacheTestModel 与 newResumeTestModel 同款组装，但额外返回 cache Manager
// 与缓存目录（测试预置缓存条目/断言命中需要）。ytdlpPath 指向不存在的路径：
// CacheAsync 的后台 goroutine 立即 exec 失败退出（无网络请求、无泄漏）。
func newCacheTestModel(t *testing.T, st *session.State) (Model, *fakePlayer, *cache.Manager, string) {
	t.Helper()
	fp := newFakePlayer()
	lyricServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(lyricServer.Close)

	hist, err := history.NewStore(filepath.Join(t.TempDir(), "history.json"))
	if err != nil {
		t.Fatal(err)
	}
	cf, err := cover.NewFetcher(filepath.Join(t.TempDir(), "covers"))
	if err != nil {
		t.Fatal(err)
	}
	sess, err := session.NewStore(filepath.Join(t.TempDir(), "session.json"))
	if err != nil {
		t.Fatal(err)
	}
	if st != nil {
		if err := sess.Save(*st); err != nil {
			t.Fatal(err)
		}
	}
	pls, err := playlists.NewStore(filepath.Join(t.TempDir(), "playlists.json"))
	if err != nil {
		t.Fatal(err)
	}
	cacheDir := filepath.Join(t.TempDir(), "cache")
	cm, err := cache.New(cache.Options{Enabled: true, MaxEntries: 100, Dir: cacheDir}, "/nonexistent/yt-dlp")
	if err != nil {
		t.Fatal(err)
	}
	m := NewModel(fp, &fakeSearchAdapter{},
		lyrics.NewClientWithBaseURL(lyricServer.URL, "music-tui test (https://example.com)"),
		cf, hist, sess, pls, cm, nil)
	return m, fp, cm, cacheDir
}

// presetCache 预置缓存条目：写缓存文件（SafeName 命名）+ 注册进索引，
// 返回缓存文件完整路径（命中时应作为播放目标）。
func presetCache(t *testing.T, cm *cache.Manager, dir, id string) string {
	t.Helper()
	full := filepath.Join(dir, cache.SafeName(id))
	if err := os.WriteFile(full, []byte("cached-audio"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := cm.Register(id); err != nil {
		t.Fatal(err)
	}
	return full
}

// 命中缓存：beginPlay 应播本地缓存文件而非网络 URL，并置 playingFromCache。
func TestPlayFromCacheUsesLocalPath(t *testing.T) {
	m, fp, cm, dir := newCacheTestModel(t, nil)
	tr := testTrack("t1")
	want := presetCache(t, cm, dir, tr.ID)

	m, cmd := m.startPlay(tr)
	_ = execCmds(cmd)
	if fp.playCount() != 1 {
		t.Fatalf("playCount = %d, want 1", fp.playCount())
	}
	if got := fp.lastPlayed(); got != want {
		t.Errorf("命中缓存应播放本地文件: got %q, want %q", got, want)
	}
	if got := fp.lastPlayed(); got == tr.URL {
		t.Error("命中缓存不应播放网络 URL")
	}
	if !m.playingFromCache {
		t.Error("命中缓存后 playingFromCache 应为 true")
	}
}

// 未命中缓存：beginPlay 应播网络 URL，置 playingFromCache=false，
// 并触发后台下载（CacheAsync 不阻塞播放，本测试 yt-dlp 不存在 → 立即失败退出）。
func TestPlayCacheMissUsesURL(t *testing.T) {
	m, fp, _, _ := newCacheTestModel(t, nil)
	tr := testTrack("t1")

	m, cmd := m.startPlay(tr)
	_ = execCmds(cmd)
	if got := fp.lastPlayed(); got != tr.URL {
		t.Errorf("未命中应播放网络 URL: got %q, want %q", got, tr.URL)
	}
	if m.playingFromCache {
		t.Error("未命中时 playingFromCache 应为 false")
	}
}

// 缓存文件损坏（下载不完整/已过期）：LoadFailed 时移除条目 + 复位标记，
// 自动重试回退网络 URL 重新取流（beginPlay 重走 Lookup → miss）。
func TestLoadFailedFromCacheEvictsThenRetriesNetwork(t *testing.T) {
	old := retryBackoff
	retryBackoff = 10 * time.Millisecond
	defer func() { retryBackoff = old }()

	m, fp, cm, dir := newCacheTestModel(t, nil)
	tr := testTrack("t1")
	presetCache(t, cm, dir, tr.ID)

	m, cmd := m.startPlay(tr)
	_ = execCmds(cmd)
	if !m.playingFromCache {
		t.Fatal("前置：应从缓存播放")
	}

	// 缓存播放取流失败 → 条目移除（损坏缓存），playingFromCache 复位
	m, cmd = update(m, playerEventMsg{ev: player.ErrorEvent{Err: &player.LoadFailedError{FileError: "no audio or video data played"}}})
	if _, ok := cm.Lookup(tr.ID); ok {
		t.Error("LoadFailed 后缓存条目应被移除")
	}
	if m.playingFromCache {
		t.Error("LoadFailed 后 playingFromCache 应为 false")
	}
	if cmd == nil {
		t.Fatal("应调度自动重试 cmd")
	}
	if m.state.Playing {
		t.Error("重试等待期间 Playing 应为 false")
	}

	// 重试触发：Lookup miss → 播放网络 URL
	m = execRetryBatch(m, cmd, fp)
	if fp.playCount() != 2 {
		t.Fatalf("重试后 playCount = %d, want 2", fp.playCount())
	}
	if got := fp.lastPlayed(); got != tr.URL {
		t.Errorf("重试应回退网络 URL: got %q, want %q", got, tr.URL)
	}
	if m.playingFromCache {
		t.Error("重试播网络后 playingFromCache 应为 false")
	}
}

// 续播恢复命中缓存：resumeCmd 应 PlayPaused 本地缓存文件（含定位），
// 成功后 playingFromCache 回填 true。
func TestResumeFromCacheUsesLocalPath(t *testing.T) {
	m, fp, cm, dir := newCacheTestModel(t, sessionState(66.6, false))
	if m.state.Track == nil {
		t.Fatal("恢复场景应有当前曲目")
	}
	curID := m.state.Track.ID
	want := presetCache(t, cm, dir, curID)

	msgs := execCmds(resumeCmd(m))
	if fp.pausedCount() != 1 {
		t.Fatalf("pausedCount = %d, want 1", fp.pausedCount())
	}
	if got := fp.lastPaused(); got != want {
		t.Errorf("恢复命中缓存应 PlayPaused 本地文件: got %q, want %q", got, want)
	}
	if fp.pausedStart() != 66.6 {
		t.Errorf("恢复起点 = %v, want 66.6", fp.pausedStart())
	}

	// 结果回灌：fromCache=true → playingFromCache 回填
	m, _ = update(m, msgs[0])
	if !m.playingFromCache {
		t.Error("恢复命中缓存后 playingFromCache 应为 true")
	}
	if m.state.Track == nil || m.state.Track.ID != curID {
		t.Errorf("恢复结果后 state = %+v", m.state)
	}
}

// 恢复命中缓存但 PlayPaused 失败（缓存文件损坏）：条目移除 + 标记复位，
// 下次恢复/播放走网络。
func TestResumeCacheCorruptOnFailure(t *testing.T) {
	m, fp, cm, dir := newCacheTestModel(t, sessionState(66.6, false))
	if m.state.Track == nil {
		t.Fatal("恢复场景应有当前曲目")
	}
	curID := m.state.Track.ID
	presetCache(t, cm, dir, curID)

	// PlayPaused 失败（注入）：应移除损坏缓存条目
	fp.playErr = true
	msgs := execCmds(resumeCmd(m))
	m, _ = update(m, msgs[0])
	if _, ok := cm.Lookup(curID); ok {
		t.Error("恢复失败且来自缓存时条目应被移除（损坏缓存）")
	}
	if !strings.Contains(m.lastError, "恢复播放失败") {
		t.Errorf("lastError = %q, want 含恢复播放失败", m.lastError)
	}
}
