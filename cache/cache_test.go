package cache

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"music-tui/model"
)

// newTestManager 构造测试用 Manager（真实文件系统，yt-dlp 指向不存在的路径：
// 不触发下载的测试场景用）。
func newTestManager(t *testing.T, opts Options) *Manager {
	t.Helper()
	return newTestManagerWithYtdlp(t, opts, "/nonexistent/yt-dlp")
}

// newTestManagerWithYtdlp 同 newTestManager，但注入假 yt-dlp 脚本路径：
// 下载类测试用它真实走完 下载→注册 全链路。
func newTestManagerWithYtdlp(t *testing.T, opts Options, ytdlpPath string) *Manager {
	t.Helper()
	cm, err := New(opts, ytdlpPath, "", nil)
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
	if err := os.WriteFile(filepath.Join(dir, file), validAudioBytes(), 0o644); err != nil {
		t.Fatal(err)
	}
}

// validAudioBytes 返回合法音频内容（EBML/WebM 魔数 + 零填充到 2048 字节，
// 满足 MinAudioSize，内容校验通过）。
func validAudioBytes() []byte {
	return append([]byte{0x1A, 0x45, 0xDF, 0xA3}, make([]byte, 2044)...)
}

// writeHTMLCacheFile 写 HTML 内容文件（填充到 ≥MinAudioSize，魔数校验必
// 失败），模拟被中间页劫持/替换的损坏缓存文件。
func writeHTMLCacheFile(t *testing.T, dir, file string) {
	t.Helper()
	data := append([]byte("<!DOCTYPE html><html>err</html>"), make([]byte, 2048)...)
	if err := os.WriteFile(filepath.Join(dir, file), data, 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestNewCreatesDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "a", "b")
	cm, err := New(Options{Enabled: true, MaxEntries: 100, Dir: dir}, "/nonexistent", "", nil)
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
	if _, err := New(Options{Enabled: true, Dir: ""}, "/nonexistent", "", nil); err == nil {
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
	if _, err := New(Options{Enabled: true, MaxEntries: 100, Dir: dir}, "/nonexistent", "", nil); err == nil {
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
	cm, err := New(Options{Enabled: true, MaxEntries: 100, Dir: dir}, "/nonexistent", "", nil)
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

// 启动清理内容校验：索引条目文件被替换为 HTML（非音频，魔数不匹配且
// ≥MinAudioSize）→ 文件删除 + 条目丢弃，清理结果持久化（防损坏文件滞留）。
func TestNewPrunesInvalidAudioEntries(t *testing.T) {
	dir := t.TempDir()
	writeIndexFile(t, dir, `[
		{"id":"html","file":"html","last_played":"2024-01-01T00:00:00Z"},
		{"id":"audio","file":"audio","last_played":"2024-01-02T00:00:00Z"}
	]`)
	writeHTMLCacheFile(t, dir, "html")
	writeCacheFile(t, dir, "audio")
	cm, err := New(Options{Enabled: true, MaxEntries: 100, Dir: dir}, "/nonexistent", "", nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if cm.idx.len() != 1 {
		t.Fatalf("idx len = %d, want 1", cm.idx.len())
	}
	if _, ok := cm.idx.get("html"); ok {
		t.Error("HTML 内容条目 html 仍在索引中，应被丢弃")
	}
	if _, ok := cm.idx.get("audio"); !ok {
		t.Error("合法音频条目 audio 缺失")
	}
	if _, err := os.Stat(filepath.Join(dir, "html")); !os.IsNotExist(err) {
		t.Errorf("HTML 内容文件未被删除: %v", err)
	}
	// 清理结果已持久化：重读索引无 HTML 条目
	ix, err := load(filepath.Join(dir, "index.json"))
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if _, ok := ix.get("html"); ok {
		t.Error("HTML 条目仍被持久化")
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
	cm, err := New(Options{Enabled: true, MaxEntries: 2, Dir: dir}, "/nonexistent", "", nil)
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
	cm, err := New(Options{Enabled: true, MaxEntries: 100, Dir: dir}, "/nonexistent", "", nil)
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
	cm, err := New(Options{Enabled: true, MaxEntries: 100, Dir: dir}, "/nonexistent", "", nil)
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

// 命中前内容校验：条目文件被替换为 HTML（非音频）→ Lookup 返回 miss、
// 文件与条目被清理（UI 自动回退网络取流，损坏文件不滞留）。
func TestLookupInvalidAudioReturnsMissAndCleans(t *testing.T) {
	dir := t.TempDir()
	cm := newTestManager(t, Options{Enabled: true, MaxEntries: 100, Dir: dir})
	id := "html-id-1234"
	// 预置条目 + HTML 内容文件（直接操作索引绕过 Register 的写入前校验，
	// 模拟注册后被外部替换成损坏文件）
	cm.idx.upsertFile(id, SafeName(id), time.Now())
	if err := cm.idx.save(cm.indexPath()); err != nil {
		t.Fatal(err)
	}
	writeHTMLCacheFile(t, dir, SafeName(id))
	if _, ok := cm.Lookup(id); ok {
		t.Fatal("Lookup HTML 内容 = hit, want miss")
	}
	if _, ok := cm.idx.get(id); ok {
		t.Error("HTML 内容条目未被移除")
	}
	if _, err := os.Stat(filepath.Join(dir, SafeName(id))); !os.IsNotExist(err) {
		t.Errorf("HTML 内容文件未被删除: %v", err)
	}
}

// register 写入前内容校验：待注册文件内容为 HTML（非音频，魔数不匹配且
// ≥MinAudioSize）→ Register 返回错误、不产生索引条目（后续 Lookup miss），
// 损坏产物不入库。
func TestRegisterRejectsHtmlContent(t *testing.T) {
	dir := t.TempDir()
	cm := newTestManager(t, Options{Enabled: true, MaxEntries: 100, Dir: dir})
	id := "html-reg-1234"
	writeHTMLCacheFile(t, dir, SafeName(id))
	err := cm.Register(id)
	if err == nil {
		t.Fatal("Register HTML 内容文件 = nil error, want error")
	}
	if !strings.Contains(err.Error(), "非音频") {
		t.Errorf("错误 = %v, want 非音频内容拒绝（而非文件缺失等其他错误）", err)
	}
	if _, ok := cm.idx.get(id); ok {
		t.Error("HTML 内容条目被注册进索引")
	}
	if _, ok := cm.Lookup(id); ok {
		t.Error("Lookup HTML 内容 = hit, want miss")
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
	cm.download(model.Track{ID: "x", URL: "https://youtube.com/watch?v=x"}, make(chan struct{}))
	if cm.idx.len() != 0 {
		t.Error("Disabled manager mutated index")
	}
}

// callCounterBody 返回假脚本体：每次被调用向 <缓存目录>/calls 追加一行
// （<dirname(-o)> 即缓存目录），测试用行数断言调用次数。配合成功落盘
// $out 使用——下载一次即退出，不留跨测试存活的下载 goroutine
// （避免 -race 下与后续测试改包级变量竞争）。
func callCounterBody() string {
	return `mkdir -p "$(dirname "$out")"
echo x >> "$(dirname "$out")/calls"`
}

func TestCacheAsyncDedupSameID(t *testing.T) {
	dir := t.TempDir()
	callsFile := filepath.Join(dir, "calls")
	cm := newTestManagerWithYtdlp(t, Options{Enabled: true, MaxEntries: 100, Dir: dir},
		writeFakeYtDlp(t, fakeYtDlpBody(fakeAudioOut+"\n"+callCounterBody())))
	const id = "dedup-id-1"
	for i := 0; i < 10; i++ {
		cm.CacheAsync(model.Track{ID: id, URL: "https://youtube.com/watch?v=" + id})
	}
	// 等脚本被调用（calls 文件出现）
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(callsFile); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("假脚本从未被调用")
		}
		time.Sleep(10 * time.Millisecond)
	}
	time.Sleep(100 * time.Millisecond) // 让其余 goroutine 有机会跑（应全部 no-op）
	if got := callCount(callsFile); got != 1 {
		t.Errorf("脚本调用次数 = %d, want 1（同 ID 去重）", got)
	}
}

// callCount 读 calls 文件返回行数（不存在 = 0）。
func callCount(callsFile string) int {
	data, err := os.ReadFile(callsFile)
	if err != nil {
		return 0
	}
	return strings.Count(string(data), "\n")
}

func TestCacheAsyncDifferentIDs(t *testing.T) {
	dir := t.TempDir()
	callsFile := filepath.Join(dir, "calls")
	cm := newTestManagerWithYtdlp(t, Options{Enabled: true, MaxEntries: 100, Dir: dir},
		writeFakeYtDlp(t, fakeYtDlpBody(fakeAudioOut+"\n"+callCounterBody())))
	for _, id := range []string{"id-1", "id-2", "id-3"} {
		cm.CacheAsync(model.Track{ID: id, URL: "https://youtube.com/watch?v=" + id})
	}
	// 等 3 个脚本调用全部到达
	deadline := time.Now().Add(5 * time.Second)
	for callCount(callsFile) < 3 {
		if time.Now().After(deadline) {
			t.Fatal("脚本调用数未达 3（超时）")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if got := callCount(callsFile); got != 3 {
		t.Errorf("脚本调用次数 = %d, want 3", got)
	}
}

func TestCacheAsyncSkipsExistingEntry(t *testing.T) {
	dir := t.TempDir()
	callsFile := filepath.Join(dir, "calls")
	cm := newTestManagerWithYtdlp(t, Options{Enabled: true, MaxEntries: 100, Dir: dir},
		writeFakeYtDlp(t, fakeYtDlpBody(callCounterBody())))
	writeCacheFile(t, dir, SafeName("already"))
	if err := cm.Register("already"); err != nil {
		t.Fatal(err)
	}
	cm.CacheAsync(model.Track{ID: "already", URL: "https://youtube.com/watch?v=already"})
	time.Sleep(100 * time.Millisecond)
	if _, err := os.Stat(callsFile); !os.IsNotExist(err) {
		t.Errorf("条目已存在仍调用 yt-dlp（calls 文件被创建）")
	}
}

func TestCacheAsyncDownloadsAndRegisters(t *testing.T) {
	dir := t.TempDir()
	callsFile := filepath.Join(dir, "calls")
	// 脚本同时落盘音频字节并计数：全链路真实走完 下载→注册
	cm := newTestManagerWithYtdlp(t, Options{Enabled: true, MaxEntries: 100, Dir: dir},
		writeFakeYtDlp(t, fakeYtDlpBody(fakeAudioOut+"\n"+callCounterBody())))
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
	// 假脚本把 %(ext)s 替换为 webm：文件名为 SafeName(id)+".webm"
	wantFile := SafeName(id) + ".webm"
	if want := filepath.Join(dir, wantFile); path != want {
		t.Errorf("path = %q, want %q", path, want)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read cached file: %v", err)
	}
	if string(data) != string(validAudioBytes()) {
		t.Errorf("cached content = %q", data)
	}
	// inflight 已清除：再次 CacheAsync 应 no-op（条目已存在）→ 脚本调用次数不再增长
	if got := callCount(callsFile); got != 1 {
		t.Fatalf("前置：脚本调用次数 = %d, want 1", got)
	}
	cm.CacheAsync(model.Track{ID: id, URL: "https://youtube.com/watch?v=" + id})
	time.Sleep(50 * time.Millisecond)
	if got := callCount(callsFile); got != 1 {
		t.Errorf("再次 CacheAsync 后脚本调用次数 = %d, want 1（条目已存在应 no-op）", got)
	}
}

func TestCacheAsyncDisabledNoGoroutine(t *testing.T) {
	cm := Disabled()
	dir := t.TempDir()
	callsFile := filepath.Join(dir, "calls")
	// 假脚本路径不会传给 Disabled manager（ytdlpPath 为空）——直接调内部
	// download 也应被防御检查拦截，脚本不可能被调用
	writeFakeYtDlp(t, fakeYtDlpBody(`echo x >> "`+callsFile+`"`))
	cm.CacheAsync(model.Track{ID: "x", URL: "https://youtube.com/watch?v=x"})
	cm.download(model.Track{ID: "x", URL: "https://youtube.com/watch?v=x"}, make(chan struct{}))
	time.Sleep(50 * time.Millisecond)
	if _, err := os.Stat(callsFile); !os.IsNotExist(err) {
		t.Error("Disabled manager 不应调用 yt-dlp（calls 文件被创建）")
	}
	if cm.idx.len() != 0 {
		t.Error("Disabled manager mutated index")
	}
}

func TestCacheAsyncFailureOnlyLogs(t *testing.T) {
	// yt-dlp 下载失败不应 panic（log.Printf 输出即可）；调小预算与退避加速
	defer func(old time.Duration) { DownloadRetryBackoff = old }(DownloadRetryBackoff)
	defer func(old int) { MaxDownloadAttempts = old }(MaxDownloadAttempts)
	DownloadRetryBackoff = time.Millisecond
	MaxDownloadAttempts = 2

	dir := t.TempDir()
	cm := newTestManagerWithYtdlp(t, Options{Enabled: true, MaxEntries: 100, Dir: dir},
		writeFakeYtDlp(t, fakeYtDlpBody(callCounterBody()+"\necho \"HTTP Error 403\" >&2\nexit 1")))
	cm.CacheAsync(model.Track{ID: "fail-id", URL: "https://youtube.com/watch?v=fail"})
	// 等待 goroutine 结束：inflight 清除
	deadline := time.Now().Add(5 * time.Second)
	for {
		cm.mu.Lock()
		inFlight := cm.inflight["fail-id"]
		cm.mu.Unlock()
		if inFlight == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("inflight 未清除")
		}
		time.Sleep(10 * time.Millisecond)
	}
	// 预算耗尽才放弃：脚本调用次数 = MaxDownloadAttempts
	if got := callCount(filepath.Join(dir, "calls")); got != MaxDownloadAttempts {
		t.Errorf("脚本调用次数 = %d, want %d（预算耗尽）", got, MaxDownloadAttempts)
	}
}

// 403 概率性风控：下载失败整进程重跑（每次重新运行 yt-dlp = 重新提取新 URL），
// 预算内前 2 次失败、第 3 次成功 → 最终命中缓存。
func TestCacheAsyncRetriesThenSucceeds(t *testing.T) {
	defer func(old time.Duration) { DownloadRetryBackoff = old }(DownloadRetryBackoff)
	DownloadRetryBackoff = time.Millisecond

	dir := t.TempDir()
	body := `
callfile="$(dirname "$out")/calls"
mkdir -p "$(dirname "$out")"
n=0
[ -f "$callfile" ] && n=$(wc -l < "$callfile")
n=$((n + 1))
echo x >> "$callfile"
if [ "$n" -le 2 ]; then
  echo "HTTP Error 403" >&2
  exit 1
fi
printf '\032\105\337\243' > "$out"
head -c 2044 /dev/zero >> "$out"
`
	cm := newTestManagerWithYtdlp(t, Options{Enabled: true, MaxEntries: 100, Dir: dir},
		writeFakeYtDlp(t, fakeYtDlpBody(body)))
	id := "retry-403-1"
	cm.CacheAsync(model.Track{ID: id, URL: "https://youtube.com/watch?v=" + id})

	deadline := time.Now().Add(5 * time.Second)
	var path string
	for {
		var ok bool
		if path, ok = cm.Lookup(id); ok {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("403 重跑后仍未命中缓存（超时）")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if want := filepath.Join(dir, SafeName(id)+".webm"); path != want {
		t.Errorf("path = %q, want %q", path, want)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read cached file: %v", err)
	}
	if string(data) != string(validAudioBytes()) {
		t.Errorf("cached content = %q", data)
	}
	// 前 2 次失败 + 第 3 次成功 = 共 3 次调用
	if got := callCount(filepath.Join(dir, "calls")); got != 3 {
		t.Errorf("脚本调用次数 = %d, want 3（2 败 1 胜）", got)
	}
}

// 缓存目录名含 glob 元字符（如 "cache[x]"）时，New 的 .part 清理与
// realDownload 的产物发现均不得依赖 glob（旧实现静默不匹配/报错）：
// 下载→注册全链路仍正常命中。
func TestCacheAsyncDirWithGlobMetachar(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "cache[x]")
	cm := newTestManagerWithYtdlp(t, Options{Enabled: true, MaxEntries: 100, Dir: dir},
		writeFakeYtDlp(t, fakeYtDlpBody(fakeAudioOut)))
	id := "meta-1234567"
	cm.CacheAsync(model.Track{ID: id, URL: "https://youtube.com/watch?v=" + id})

	deadline := time.Now().Add(5 * time.Second)
	var path string
	for {
		var ok bool
		if path, ok = cm.Lookup(id); ok {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("glob 元字符目录下下载未在超时内注册")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if want := filepath.Join(dir, SafeName(id)+".webm"); path != want {
		t.Errorf("path = %q, want %q", path, want)
	}
}

// 超时杀进程清理：假脚本先写 .part 再 sleep 5（不产出最终文件），单次尝试
// 超时被 kill → 失败清理应删除 .part；预算耗尽才放弃（无 .part 残留、inflight 清除）。
func TestCacheAsyncTimeoutKillsAndCleans(t *testing.T) {
	defer func(old time.Duration) { DownloadAttemptTimeout = old }(DownloadAttemptTimeout)
	defer func(old time.Duration) { DownloadRetryBackoff = old }(DownloadRetryBackoff)
	defer func(old int) { MaxDownloadAttempts = old }(MaxDownloadAttempts)
	DownloadAttemptTimeout = 200 * time.Millisecond
	DownloadRetryBackoff = time.Millisecond
	MaxDownloadAttempts = 2

	dir := t.TempDir()
	// sleep 必须重定向（>/dev/null 2>&1）：否则 sleep 继承 stderr 管道，
	// sh 被 kill 后管道仍被 sleep 持有，cmd.Run 会阻塞到 sleep 自然结束（~5s），
	// 超时杀进程效果无法被测试观察到。
	body := `: > "$out.part"` + "\n" + callCounterBody() + "\n" + `sleep 5 >/dev/null 2>&1
`
	cm := newTestManagerWithYtdlp(t, Options{Enabled: true, MaxEntries: 100, Dir: dir},
		writeFakeYtDlp(t, fakeYtDlpBody(body)))
	id := "timeout-id-1"
	cm.CacheAsync(model.Track{ID: id, URL: "https://youtube.com/watch?v=" + id})

	// 等待 goroutine 结束：inflight 清除（在 realDownload 失败清理之后）
	deadline := time.Now().Add(5 * time.Second)
	for {
		cm.mu.Lock()
		inFlight := cm.inflight[id]
		cm.mu.Unlock()
		if inFlight == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("inflight 未清除")
		}
		time.Sleep(10 * time.Millisecond)
	}
	// 预算耗尽才放弃：脚本调用次数 = MaxDownloadAttempts
	if got := callCount(filepath.Join(dir, "calls")); got != MaxDownloadAttempts {
		t.Errorf("脚本调用次数 = %d, want %d（预算耗尽）", got, MaxDownloadAttempts)
	}
	// 超时被杀后 .part 残留被失败清理删除
	if _, err := os.Stat(filepath.Join(dir, SafeName(id)+".webm.part")); !os.IsNotExist(err) {
		t.Errorf(".part 残留未被清理: %v", err)
	}
}

// ---- CacheAsync 完成信号（preload 调度器串行化的依赖） ----

// doneClosed 非阻塞探测 channel 是否已关闭。
func doneClosed(done <-chan struct{}) bool {
	select {
	case <-done:
		return true
	default:
		return false
	}
}

// waitDone 轮询等待完成信号关闭（超时 fatal）；异步下载结束时刻不可精确预测，
// 测试只能轮询（同仓库既有测试的轮询模式）。
func waitDone(t *testing.T, done <-chan struct{}) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !doneClosed(done) {
		if time.Now().After(deadline) {
			t.Fatal("下载完成信号未在超时内关闭")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestCacheAsyncDisabledReturnsNil(t *testing.T) {
	cm := Disabled()
	if done := cm.CacheAsync(model.Track{ID: "x", URL: "https://youtube.com/watch?v=x"}); done != nil {
		t.Errorf("Disabled CacheAsync = %v, want nil（开关关 = no-op）", done)
	}
}

func TestCacheAsyncExistingEntryReturnsNil(t *testing.T) {
	dir := t.TempDir()
	cm := newTestManager(t, Options{Enabled: true, MaxEntries: 100, Dir: dir})
	writeCacheFile(t, dir, SafeName("already"))
	if err := cm.Register("already"); err != nil {
		t.Fatal(err)
	}
	if done := cm.CacheAsync(model.Track{ID: "already", URL: "https://youtube.com/watch?v=already"}); done != nil {
		t.Errorf("条目已存在 CacheAsync = %v, want nil（no-op）", done)
	}
}

// 同 ID 在途 → nil：inflight 标记在 CacheAsync 内同步置位（返回前），
// 紧跟的第二次调用必然看到在途（无时序敏感性）。
func TestCacheAsyncInFlightReturnsNil(t *testing.T) {
	dir := t.TempDir()
	cm := newTestManagerWithYtdlp(t, Options{Enabled: true, MaxEntries: 100, Dir: dir},
		writeFakeYtDlp(t, fakeYtDlpBody(fakeAudioOut)))
	id := "inflight-nil-1"
	done1 := cm.CacheAsync(model.Track{ID: id, URL: "https://youtube.com/watch?v=" + id})
	if done1 == nil {
		t.Fatal("首次 CacheAsync 应启动下载并返回完成信号")
	}
	if done2 := cm.CacheAsync(model.Track{ID: id, URL: "https://youtube.com/watch?v=" + id}); done2 != nil {
		t.Errorf("在途同 ID CacheAsync = %v, want nil（去重）", done2)
	}
	waitDone(t, done1) // 首次下载正常结束（成功注册）
}

// 真实下载成功（假 yt-dlp 脚本落盘合法音频）：完成信号在下载彻底结束
// （成功注册进索引）后关闭——调用方据此知道预下载已可用。
func TestCacheAsyncDoneClosedAfterSuccess(t *testing.T) {
	dir := t.TempDir()
	cm := newTestManagerWithYtdlp(t, Options{Enabled: true, MaxEntries: 100, Dir: dir},
		writeFakeYtDlp(t, fakeYtDlpBody(fakeAudioOut)))
	id := "done-close-1"
	done := cm.CacheAsync(model.Track{ID: id, URL: "https://youtube.com/watch?v=" + id})
	if done == nil {
		t.Fatal("CacheAsync 应启动下载并返回完成信号")
	}
	waitDone(t, done)
	// 信号关闭时注册已完成：Lookup 必然命中（defer 先清 inflight 再 close，
	// register 在 return 前已落盘索引）
	if _, ok := cm.Lookup(id); !ok {
		t.Error("完成信号关闭后 Lookup 应命中（成功注册）")
	}
}

// 下载失败预算耗尽：完成信号同样关闭（失败也是“彻底结束”）——调度器
// 不能因为失败而永久卡在 <-done 上。
func TestCacheAsyncDoneClosedAfterFailure(t *testing.T) {
	defer func(old time.Duration) { DownloadRetryBackoff = old }(DownloadRetryBackoff)
	defer func(old int) { MaxDownloadAttempts = old }(MaxDownloadAttempts)
	DownloadRetryBackoff = time.Millisecond
	MaxDownloadAttempts = 2

	dir := t.TempDir()
	cm := newTestManagerWithYtdlp(t, Options{Enabled: true, MaxEntries: 100, Dir: dir},
		writeFakeYtDlp(t, fakeYtDlpBody(`echo "HTTP Error 403" >&2; exit 1`)))
	id := "done-close-fail"
	done := cm.CacheAsync(model.Track{ID: id, URL: "https://youtube.com/watch?v=" + id})
	if done == nil {
		t.Fatal("CacheAsync 应启动下载并返回完成信号")
	}
	waitDone(t, done)
	if _, ok := cm.Lookup(id); ok {
		t.Error("失败后 Lookup 不应命中")
	}
	// 失败路径同样清除 inflight：再次 CacheAsync 重新启动下载并返回新信号
	done2 := cm.CacheAsync(model.Track{ID: id, URL: "https://youtube.com/watch?v=" + id})
	if done2 == nil {
		t.Error("失败清除 inflight 后 CacheAsync 应重新启动下载")
	}
	waitDone(t, done2)
}

// ---- WaitDone（缓存兜底播放的前置：监听在途下载的完成信号） ----

// blockingYtDlpBody 返回假 yt-dlp 脚本体：先阻塞轮询等待 release 文件出现，
// 再执行 extra（可用 $out 落盘）——下载在途窗口被拉长到确定可观测，
// 测试在窗口内做 WaitDone 断言（不依赖下载 goroutine 的调度时序）。
func blockingYtDlpBody(release, extra string) string {
	return fakeYtDlpBody(`while [ ! -f "` + release + `" ]; do sleep 0.05; done
` + extra)
}

// 在途下载期间 WaitDone 返回的 channel 与 CacheAsync 首次发起时返回的是
// 同一 channel（== 比较）；下载完成后两个句柄指向的信号都关闭。
func TestWaitDoneInflightSameSignal(t *testing.T) {
	dir := t.TempDir()
	release := filepath.Join(dir, "release")
	cm := newTestManagerWithYtdlp(t, Options{Enabled: true, MaxEntries: 100, Dir: dir},
		writeFakeYtDlp(t, blockingYtDlpBody(release, fakeAudioOut)))
	const id = "waitdone-same-1"
	done1 := cm.CacheAsync(model.Track{ID: id, URL: "https://youtube.com/watch?v=" + id})
	if done1 == nil {
		t.Fatal("首次 CacheAsync 应启动下载并返回完成信号")
	}
	// 在途窗口内：WaitDone 返回与 CacheAsync 首次发起返回的是同一 channel
	wd := cm.WaitDone(id)
	if wd == nil {
		t.Fatal("在途 WaitDone = nil, want 完成信号")
	}
	if wd != done1 {
		t.Fatalf("WaitDone = %v, want 与 CacheAsync 返回同一 channel %v", wd, done1)
	}
	// 释放下载 → 完成：两个句柄指向的信号都关闭
	if err := os.WriteFile(release, []byte("go"), 0o644); err != nil {
		t.Fatal(err)
	}
	waitDone(t, done1)
	if !doneClosed(done1) {
		t.Error("CacheAsync 返回的完成信号未关闭")
	}
	if !doneClosed(wd) {
		t.Error("WaitDone 返回的完成信号未关闭")
	}
	// 结束后 inflight 已清除：再查返回 nil
	if got := cm.WaitDone(id); got != nil {
		t.Errorf("下载结束后 WaitDone = %v, want nil", got)
	}
}

// 无在途下载返回 nil：未启动、已缓存（条目存在）、已完成（下载结束 inflight
// 清除）三种场景，以及 Disabled manager 恒 nil。
func TestWaitDoneNoInflightNil(t *testing.T) {
	dir := t.TempDir()
	release := filepath.Join(dir, "release")
	cm := newTestManagerWithYtdlp(t, Options{Enabled: true, MaxEntries: 100, Dir: dir},
		writeFakeYtDlp(t, blockingYtDlpBody(release, fakeAudioOut)))
	// 未启动
	if got := cm.WaitDone("never-started"); got != nil {
		t.Errorf("未启动 ID WaitDone = %v, want nil", got)
	}
	// 已缓存（条目存在，无在途）
	writeCacheFile(t, dir, SafeName("cached-1"))
	if err := cm.Register("cached-1"); err != nil {
		t.Fatal(err)
	}
	if got := cm.WaitDone("cached-1"); got != nil {
		t.Errorf("已缓存 ID WaitDone = %v, want nil", got)
	}
	// 已完成（下载结束、inflight 清除）
	const id = "waitdone-nil-1"
	done := cm.CacheAsync(model.Track{ID: id, URL: "https://youtube.com/watch?v=" + id})
	if done == nil {
		t.Fatal("CacheAsync 应启动下载")
	}
	if err := os.WriteFile(release, []byte("go"), 0o644); err != nil {
		t.Fatal(err)
	}
	waitDone(t, done)
	if got := cm.WaitDone(id); got != nil {
		t.Errorf("下载结束后 WaitDone = %v, want nil", got)
	}
	// Disabled manager 恒 nil
	if got := Disabled().WaitDone("x"); got != nil {
		t.Errorf("Disabled WaitDone = %v, want nil", got)
	}
}

// 下载彻底结束（成功注册 / 失败预算耗尽）时，WaitDone 拿到的信号关闭：
// 调用方 <-done 不会永久阻塞。
func TestWaitDoneClosesOnDownloadEnd(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		dir := t.TempDir()
		release := filepath.Join(dir, "release")
		cm := newTestManagerWithYtdlp(t, Options{Enabled: true, MaxEntries: 100, Dir: dir},
			writeFakeYtDlp(t, blockingYtDlpBody(release, fakeAudioOut)))
		const id = "waitdone-close-ok"
		if done := cm.CacheAsync(model.Track{ID: id, URL: "https://youtube.com/watch?v=" + id}); done == nil {
			t.Fatal("CacheAsync 应启动下载")
		}
		wd := cm.WaitDone(id)
		if wd == nil {
			t.Fatal("在途 WaitDone = nil, want 完成信号")
		}
		if err := os.WriteFile(release, []byte("go"), 0o644); err != nil {
			t.Fatal(err)
		}
		waitDone(t, wd)
		// 成功路径：信号关闭时注册已完成，Lookup 命中
		if _, ok := cm.Lookup(id); !ok {
			t.Error("完成信号关闭后 Lookup 应命中（成功注册）")
		}
	})
	t.Run("failure", func(t *testing.T) {
		defer func(old time.Duration) { DownloadRetryBackoff = old }(DownloadRetryBackoff)
		defer func(old int) { MaxDownloadAttempts = old }(MaxDownloadAttempts)
		DownloadRetryBackoff = time.Millisecond
		MaxDownloadAttempts = 2

		dir := t.TempDir()
		release := filepath.Join(dir, "release")
		cm := newTestManagerWithYtdlp(t, Options{Enabled: true, MaxEntries: 100, Dir: dir},
			writeFakeYtDlp(t, blockingYtDlpBody(release, `echo "HTTP Error 403" >&2; exit 1`)))
		const id = "waitdone-close-fail"
		if done := cm.CacheAsync(model.Track{ID: id, URL: "https://youtube.com/watch?v=" + id}); done == nil {
			t.Fatal("CacheAsync 应启动下载")
		}
		wd := cm.WaitDone(id)
		if wd == nil {
			t.Fatal("在途 WaitDone = nil, want 完成信号")
		}
		if err := os.WriteFile(release, []byte("go"), 0o644); err != nil {
			t.Fatal(err)
		}
		waitDone(t, wd)
		// 失败路径：信号同样关闭，但未注册
		if _, ok := cm.Lookup(id); ok {
			t.Error("失败后 Lookup 不应命中")
		}
	})
}

// 下载成功但 register 持久化失败（index.json 被占位成目录）→ 已下载文件应被
// 删除，避免孤儿滞留；inflight 清除。
func TestCacheAsyncRegisterFailureRemovesFile(t *testing.T) {
	dir := t.TempDir()
	cm := newTestManagerWithYtdlp(t, Options{Enabled: true, MaxEntries: 100, Dir: dir},
		writeFakeYtDlp(t, fakeYtDlpBody(fakeAudioOut)))
	// 让 register 的持久化失败：把 index.json 占位成目录（save 的 Rename 会失败）
	if err := os.Mkdir(filepath.Join(dir, "index.json"), 0o755); err != nil {
		t.Fatal(err)
	}
	id := "regfail-1234"
	cm.CacheAsync(model.Track{ID: id, URL: "https://youtube.com/watch?v=" + id})

	deadline := time.Now().Add(5 * time.Second)
	for {
		cm.mu.Lock()
		inFlight := cm.inflight[id]
		cm.mu.Unlock()
		if inFlight == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("inflight 未清除")
		}
		time.Sleep(10 * time.Millisecond)
	}
	// 已下载文件被删除，避免孤儿滞留
	if _, err := os.Stat(filepath.Join(dir, SafeName(id)+".webm")); !os.IsNotExist(err) {
		t.Errorf("注册失败后已下载文件残留: %v", err)
	}
}
