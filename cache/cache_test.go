package cache

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"music-tui/model"
)

// newTestManager 构造测试用 Manager（真实文件系统，stub extract 由各测试注入）。
func newTestManager(t *testing.T, opts Options) *Manager {
	t.Helper()
	cm, err := New(opts, "/nonexistent/yt-dlp")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return cm
}

func testOpts(t *testing.T) Options {
	return Options{Enabled: true, MaxEntries: 100, Dir: filepath.Join(t.TempDir(), "cache")}
}

func writeIndexFile(t *testing.T, dir string, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, "index.json"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func writeCacheFile(t *testing.T, dir, file string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, file), []byte("data-"+file), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestNewCreatesDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "a", "b")
	cm, err := New(Options{Enabled: true, MaxEntries: 100, Dir: dir}, "/nonexistent")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if !cm.Enabled() {
		t.Error("Enabled() = false, want true")
	}
	if _, err := os.Stat(dir); err != nil {
		t.Errorf("dir not created: %v", err)
	}
}

func TestNewEmptyDirError(t *testing.T) {
	if _, err := New(Options{Enabled: true, Dir: ""}, "/nonexistent"); err == nil {
		t.Fatal("New with empty Dir = nil error, want error")
	}
}

func TestNewClampsMaxEntries(t *testing.T) {
	cm := newTestManager(t, Options{Enabled: true, MaxEntries: 0, Dir: t.TempDir()})
	if cm.maxEntries != 100 {
		t.Errorf("maxEntries = %d, want 100", cm.maxEntries)
	}
	cm2 := newTestManager(t, Options{Enabled: true, MaxEntries: -5, Dir: t.TempDir()})
	if cm2.maxEntries != 100 {
		t.Errorf("maxEntries = %d, want 100", cm2.maxEntries)
	}
}

func TestNewMissingIndexIsEmpty(t *testing.T) {
	cm := newTestManager(t, testOpts(t))
	if cm.idx.len() != 0 {
		t.Errorf("idx len = %d, want 0", cm.idx.len())
	}
	if _, ok := cm.Lookup("whatever"); ok {
		t.Error("Lookup on empty cache = hit, want miss")
	}
}

func TestNewCorruptIndexError(t *testing.T) {
	dir := t.TempDir()
	writeIndexFile(t, dir, "{invalid")
	if _, err := New(Options{Enabled: true, MaxEntries: 100, Dir: dir}, "/nonexistent"); err == nil {
		t.Fatal("New with corrupt index = nil error, want error")
	}
}

func TestNewPrunesMissingFiles(t *testing.T) {
	dir := t.TempDir()
	writeIndexFile(t, dir, `[
		{"id":"gone","file":"gone","last_played":"2024-01-01T00:00:00Z"},
		{"id":"here","file":"here","last_played":"2024-01-02T00:00:00Z"}
	]`)
	writeCacheFile(t, dir, "here")
	cm, err := New(Options{Enabled: true, MaxEntries: 100, Dir: dir}, "/nonexistent")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if cm.idx.len() != 1 {
		t.Fatalf("idx len = %d, want 1", cm.idx.len())
	}
	if _, ok := cm.idx.get("gone"); ok {
		t.Error("stale entry gone still present")
	}
	if _, ok := cm.idx.get("here"); !ok {
		t.Error("valid entry here missing")
	}
	// 清理结果已持久化：重新加载不应再有 gone
	ix, err := load(filepath.Join(dir, "index.json"))
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if _, ok := ix.get("gone"); ok {
		t.Error("stale entry gone persisted back")
	}
}

func TestNewEvictsOverLimit(t *testing.T) {
	dir := t.TempDir()
	writeIndexFile(t, dir, `[
		{"id":"old","file":"old","last_played":"2024-01-01T00:00:00Z"},
		{"id":"mid","file":"mid","last_played":"2024-01-02T00:00:00Z"},
		{"id":"new","file":"new","last_played":"2024-01-03T00:00:00Z"}
	]`)
	for _, f := range []string{"old", "mid", "new"} {
		writeCacheFile(t, dir, f)
	}
	cm, err := New(Options{Enabled: true, MaxEntries: 2, Dir: dir}, "/nonexistent")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if cm.idx.len() != 2 {
		t.Fatalf("idx len = %d, want 2", cm.idx.len())
	}
	if _, ok := cm.idx.get("old"); ok {
		t.Error("oldest still in index")
	}
	if _, err := os.Stat(filepath.Join(dir, "old")); !os.IsNotExist(err) {
		t.Errorf("oldest file not deleted: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "mid")); err != nil {
		t.Errorf("mid file missing: %v", err)
	}
}

func TestNewDropsTraversalFileEntries(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "cache")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	// 目录外受害者文件：被篡改的索引条目不得删除它
	outside := filepath.Join(filepath.Dir(dir), "escape.txt")
	if err := os.WriteFile(outside, []byte("victim"), 0o644); err != nil {
		t.Fatal(err)
	}
	writeIndexFile(t, dir, `[
		{"id":"evil","file":"../escape.txt","last_played":"2024-01-01T00:00:00Z"},
		{"id":"abs","file":"/abs/path","last_played":"2024-01-02T00:00:00Z"},
		{"id":"dot","file":".","last_played":"2024-01-03T00:00:00Z"},
		{"id":"good","file":"good","last_played":"2024-01-04T00:00:00Z"}
	]`)
	writeCacheFile(t, dir, "good")
	cm, err := New(Options{Enabled: true, MaxEntries: 100, Dir: dir}, "/nonexistent")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if cm.idx.len() != 1 {
		t.Fatalf("idx len = %d, want 1（仅合法条目）", cm.idx.len())
	}
	if _, ok := cm.idx.get("good"); !ok {
		t.Error("valid entry good missing")
	}
	for _, id := range []string{"evil", "abs", "dot"} {
		if _, ok := cm.idx.get(id); ok {
			t.Errorf("非法条目 %s 仍在索引中，应被丢弃", id)
		}
	}
	// 目录外文件未被删除
	if _, err := os.Stat(outside); err != nil {
		t.Errorf("目录外文件被删除: %v", err)
	}
	// 清理结果已持久化：重读索引无非法条目
	ix, err := load(filepath.Join(dir, "index.json"))
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if _, ok := ix.get("evil"); ok {
		t.Error("穿越条目仍被持久化")
	}
	if _, ok := ix.get("abs"); ok {
		t.Error("绝对路径条目仍被持久化")
	}
}

func TestNewCleansPartFiles(t *testing.T) {
	dir := t.TempDir()
	for _, f := range []string{"a.part", "b.part", "sub.part"} {
		writeCacheFile(t, dir, f)
	}
	writeCacheFile(t, dir, "song.m4a") // 正常文件不受影响
	cm, err := New(Options{Enabled: true, MaxEntries: 100, Dir: dir}, "/nonexistent")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if cm.idx.len() != 0 {
		t.Errorf("idx len = %d, want 0", cm.idx.len())
	}
	for _, f := range []string{"a.part", "b.part", "sub.part"} {
		if _, err := os.Stat(filepath.Join(dir, f)); !os.IsNotExist(err) {
			t.Errorf("残留 %s 未被清理: %v", f, err)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "song.m4a")); err != nil {
		t.Errorf("正常缓存文件被误删: %v", err)
	}
}

func TestLookupMissThenHitThenFileGone(t *testing.T) {
	dir := t.TempDir()
	cm := newTestManager(t, Options{Enabled: true, MaxEntries: 100, Dir: dir})
	id := "dQw4w9WgXcQ"
	if _, ok := cm.Lookup(id); ok {
		t.Fatal("Lookup before register = hit, want miss")
	}
	writeCacheFile(t, dir, SafeName(id))
	if err := cm.Register(id); err != nil {
		t.Fatalf("Register: %v", err)
	}
	path, ok := cm.Lookup(id)
	if !ok {
		t.Fatal("Lookup after register = miss, want hit")
	}
	if want := filepath.Join(dir, SafeName(id)); path != want {
		t.Errorf("path = %q, want %q", path, want)
	}
	// 文件被外部删除 → miss 且条目移除
	if err := os.Remove(filepath.Join(dir, SafeName(id))); err != nil {
		t.Fatal(err)
	}
	if _, ok := cm.Lookup(id); ok {
		t.Fatal("Lookup after file removed = hit, want miss")
	}
	if _, ok := cm.idx.get(id); ok {
		t.Error("entry not removed after file gone")
	}
}

func TestRegisterEvictsOldest(t *testing.T) {
	dir := t.TempDir()
	cm := newTestManager(t, Options{Enabled: true, MaxEntries: 2, Dir: dir})
	for _, id := range []string{"aaaa", "bbbb", "cccc"} {
		writeCacheFile(t, dir, SafeName(id))
		if err := cm.Register(id); err != nil {
			t.Fatalf("Register(%s): %v", id, err)
		}
	}
	if _, ok := cm.Lookup("aaaa"); ok {
		t.Error("aaaa (oldest) still cached, want evicted")
	}
	if _, err := os.Stat(filepath.Join(dir, SafeName("aaaa"))); !os.IsNotExist(err) {
		t.Errorf("aaaa file not deleted: %v", err)
	}
	for _, id := range []string{"bbbb", "cccc"} {
		if _, ok := cm.Lookup(id); !ok {
			t.Errorf("%s missing, want hit", id)
		}
	}
}

func TestRegisterSameIDRefreshes(t *testing.T) {
	dir := t.TempDir()
	cm := newTestManager(t, Options{Enabled: true, MaxEntries: 2, Dir: dir})
	for _, id := range []string{"aaaa", "bbbb"} {
		writeCacheFile(t, dir, SafeName(id))
		if err := cm.Register(id); err != nil {
			t.Fatalf("Register(%s): %v", id, err)
		}
	}
	time.Sleep(5 * time.Millisecond) // 保证 LastPlayed 有先后
	writeCacheFile(t, dir, SafeName("aaaa"))
	if err := cm.Register("aaaa"); err != nil { // aaaa 刷新为最新
		t.Fatalf("re-Register: %v", err)
	}
	writeCacheFile(t, dir, SafeName("cccc"))
	if err := cm.Register("cccc"); err != nil { // 超限 → 应淘汰 bbbb（现在最旧）
		t.Fatalf("Register(cccc): %v", err)
	}
	if _, ok := cm.Lookup("bbbb"); ok {
		t.Error("bbbb still cached, want evicted (aaaa refreshed)")
	}
	for _, id := range []string{"aaaa", "cccc"} {
		if _, ok := cm.Lookup(id); !ok {
			t.Errorf("%s missing, want hit", id)
		}
	}
}

func TestManagerRemove(t *testing.T) {
	dir := t.TempDir()
	cm := newTestManager(t, Options{Enabled: true, MaxEntries: 100, Dir: dir})
	id := "abc123xyz"
	writeCacheFile(t, dir, SafeName(id))
	if err := cm.Register(id); err != nil {
		t.Fatalf("Register: %v", err)
	}
	if err := cm.Remove(id); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, SafeName(id))); !os.IsNotExist(err) {
		t.Errorf("file not deleted: %v", err)
	}
	if _, ok := cm.idx.get(id); ok {
		t.Error("entry still in index")
	}
	if err := cm.Remove(id); err != nil { // 不存在 → nil
		t.Errorf("Remove nonexistent = %v, want nil", err)
	}
}

func TestDisabledManager(t *testing.T) {
	cm := Disabled()
	if cm.Enabled() {
		t.Error("Disabled().Enabled() = true, want false")
	}
	if _, ok := cm.Lookup("x"); ok {
		t.Error("Disabled Lookup = hit, want miss")
	}
	if err := cm.Register("x"); err != nil {
		t.Errorf("Disabled Register = %v, want nil", err)
	}
	if err := cm.Remove("x"); err != nil {
		t.Errorf("Disabled Remove = %v, want nil", err)
	}
	// CacheAsync no-op：不会起 goroutine（无 panic 即通过，extract 无法注入——直接调用内部 download 也无副作用）
	cm.CacheAsync(model.Track{ID: "x", URL: "https://youtube.com/watch?v=x"})
	cm.download(model.Track{ID: "x", URL: "https://youtube.com/watch?v=x"})
	if cm.idx.len() != 0 {
		t.Error("Disabled manager mutated index")
	}
}

func TestCacheAsyncDedupSameID(t *testing.T) {
	cm := newTestManager(t, testOpts(t))
	var calls int32
	called := make(chan struct{}, 16)
	cm.extract = func(ctx context.Context, url string) (string, string, error) {
		atomic.AddInt32(&calls, 1)
		called <- struct{}{}
		return "", "", errors.New("stop-here")
	}
	const id = "dedup-id-1"
	for i := 0; i < 10; i++ {
		cm.CacheAsync(model.Track{ID: id, URL: "https://youtube.com/watch?v=" + id})
	}
	select {
	case <-called:
	case <-time.After(5 * time.Second):
		t.Fatal("extract never called")
	}
	time.Sleep(100 * time.Millisecond) // 让其余 goroutine 有机会跑（应全部 no-op）
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Errorf("extract calls = %d, want 1", got)
	}
}

func TestCacheAsyncDifferentIDs(t *testing.T) {
	cm := newTestManager(t, testOpts(t))
	var calls int32
	done := make(chan struct{}, 16)
	cm.extract = func(ctx context.Context, url string) (string, string, error) {
		atomic.AddInt32(&calls, 1)
		done <- struct{}{}
		return "", "", errors.New("stop-here")
	}
	for _, id := range []string{"id-1", "id-2", "id-3"} {
		cm.CacheAsync(model.Track{ID: id, URL: "https://youtube.com/watch?v=" + id})
	}
	for i := 0; i < 3; i++ {
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatal("extract call timeout")
		}
	}
	if got := atomic.LoadInt32(&calls); got != 3 {
		t.Errorf("extract calls = %d, want 3", got)
	}
}

func TestCacheAsyncSkipsExistingEntry(t *testing.T) {
	dir := t.TempDir()
	cm := newTestManager(t, Options{Enabled: true, MaxEntries: 100, Dir: dir})
	writeCacheFile(t, dir, SafeName("already"))
	if err := cm.Register("already"); err != nil {
		t.Fatal(err)
	}
	var calls int32
	cm.extract = func(ctx context.Context, url string) (string, string, error) {
		atomic.AddInt32(&calls, 1)
		return "", "", errors.New("stop-here")
	}
	cm.CacheAsync(model.Track{ID: "already", URL: "https://youtube.com/watch?v=already"})
	time.Sleep(100 * time.Millisecond)
	if got := atomic.LoadInt32(&calls); got != 0 {
		t.Errorf("extract calls = %d, want 0 (entry exists)", got)
	}
}

func TestCacheAsyncDownloadsAndRegisters(t *testing.T) {
	dir := t.TempDir()
	cm := newTestManager(t, Options{Enabled: true, MaxEntries: 100, Dir: dir})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, "stream-audio-bytes")
	}))
	defer srv.Close()
	cm.extract = func(ctx context.Context, url string) (string, string, error) {
		return srv.URL + "/stream", "m4a", nil
	}
	id := "dl-abcd1234"
	cm.CacheAsync(model.Track{ID: id, URL: "https://youtube.com/watch?v=" + id})

	// 等待异步完成：Lookup 命中
	deadline := time.Now().Add(5 * time.Second)
	var path string
	for {
		var ok bool
		if path, ok = cm.Lookup(id); ok {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("CacheAsync 下载未在超时内完成")
		}
		time.Sleep(10 * time.Millisecond)
	}
	// ext 拼接：文件名为 SafeName(id)+".m4a"
	wantFile := SafeName(id) + ".m4a"
	if want := filepath.Join(dir, wantFile); path != want {
		t.Errorf("path = %q, want %q", path, want)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read cached file: %v", err)
	}
	if string(data) != "stream-audio-bytes" {
		t.Errorf("cached content = %q", data)
	}
	// inflight 已清除：再次 CacheAsync 应 no-op（条目已存在）
	var calls int32
	cm.extract = func(ctx context.Context, url string) (string, string, error) {
		atomic.AddInt32(&calls, 1)
		return "", "", errors.New("stop-here")
	}
	cm.CacheAsync(model.Track{ID: id, URL: "https://youtube.com/watch?v=" + id})
	time.Sleep(50 * time.Millisecond)
	if got := atomic.LoadInt32(&calls); got != 0 {
		t.Errorf("second CacheAsync extract calls = %d, want 0", got)
	}
}

func TestCacheAsyncDisabledNoGoroutine(t *testing.T) {
	cm := Disabled()
	var calls int32
	cm.extract = func(ctx context.Context, url string) (string, string, error) {
		atomic.AddInt32(&calls, 1)
		return "", "", nil
	}
	cm.CacheAsync(model.Track{ID: "x", URL: "https://youtube.com/watch?v=x"})
	time.Sleep(50 * time.Millisecond)
	if got := atomic.LoadInt32(&calls); got != 0 {
		t.Errorf("extract calls = %d, want 0", got)
	}
}

func TestCacheAsyncFailureOnlyLogs(t *testing.T) {
	// extract 失败不应 panic（log.Printf 输出即可）
	cm := newTestManager(t, testOpts(t))
	cm.extract = func(ctx context.Context, url string) (string, string, error) {
		return "", "", fmt.Errorf("boom")
	}
	cm.CacheAsync(model.Track{ID: "fail-id", URL: "https://youtube.com/watch?v=fail"})
	// 等待 goroutine 结束：inflight 清除
	deadline := time.Now().Add(5 * time.Second)
	for {
		cm.mu.Lock()
		inFlight := cm.inflight["fail-id"]
		cm.mu.Unlock()
		if !inFlight {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("inflight 未清除")
		}
		time.Sleep(10 * time.Millisecond)
	}
}
