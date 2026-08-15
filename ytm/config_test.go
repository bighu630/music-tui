package ytm

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func newTestStore(t *testing.T) (*Store, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "ytm.json")
	s, err := NewStore(path)
	if err != nil {
		t.Fatal(err)
	}
	return s, path
}

func TestNewStoreCreatesDirAndEmpty(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "dir", "ytm.json")
	s, err := NewStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if s.Login().Method != MethodNone {
		t.Errorf("初始应未登录, got %v", s.Login())
	}
	if got := s.SyncEntries(); len(got) != 0 {
		t.Errorf("初始 SyncEntries = %v, want 空", got)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("空 store 不应写盘, err = %v", err)
	}
}

func TestNewStoreEmptyAndCorruptFile(t *testing.T) {
	dir := t.TempDir()
	empty := filepath.Join(dir, "empty.json")
	if err := os.WriteFile(empty, []byte("  \n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewStore(empty); err != nil {
		t.Errorf("空白文件应视为空, err = %v", err)
	}
	corrupt := filepath.Join(dir, "corrupt.json")
	if err := os.WriteFile(corrupt, []byte("{corrupt!!!"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewStore(corrupt); err == nil {
		t.Error("损坏文件应报错")
	}
}

func TestSetLoginRoundtrip(t *testing.T) {
	s, _ := newTestStore(t)
	cfg := LoginConfig{Method: MethodCookiesFile, CookiesPath: " /tmp/cookies.txt "}
	if err := s.SetLogin(cfg); err != nil {
		t.Fatal(err)
	}
	got := s.Login()
	if got.Method != MethodCookiesFile || got.CookiesPath != "/tmp/cookies.txt" {
		t.Errorf("SetLogin roundtrip = %+v", got)
	}
	if got.UpdatedAt.IsZero() {
		t.Error("UpdatedAt 应自动填充")
	}
}

func TestClearLogin(t *testing.T) {
	s, _ := newTestStore(t)
	if err := s.SetLogin(LoginConfig{Method: MethodBrowser, Browser: "chrome"}); err != nil {
		t.Fatal(err)
	}
	if err := s.UpsertSync(SyncEntry{PlaylistID: "PL1", ListName: "YT: A"}); err != nil {
		t.Fatal(err)
	}
	if err := s.ClearLogin(); err != nil {
		t.Fatal(err)
	}
	if s.Login().Method != MethodNone {
		t.Error("ClearLogin 后应未登录")
	}
	if got := s.SyncEntries(); len(got) != 1 {
		t.Error("ClearLogin 不应清除 SyncEntry 映射")
	}
}

func TestStoreFilePermissionsAndAtomic(t *testing.T) {
	s, path := newTestStore(t)
	if err := s.SetLogin(LoginConfig{Method: MethodPasted, CookiesPath: "p"}); err != nil {
		t.Fatal(err)
	}
	if err := s.UpsertSync(SyncEntry{PlaylistID: "PL1", ListName: "YT: A", Count: 1}); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("ytm.json 权限 = %o, want 600", fi.Mode().Perm())
	}
	if _, err := os.Stat(path + ".tmp"); !os.IsNotExist(err) {
		t.Error("原子写后不应残留 .tmp 文件")
	}
}

func TestSyncEntryUpsertFindRemove(t *testing.T) {
	s, _ := newTestStore(t)
	e := SyncEntry{PlaylistID: "PL1", ListName: "YT: A", Count: 3, SyncedAt: time.Now()}
	if err := s.UpsertSync(e); err != nil {
		t.Fatal(err)
	}
	got, ok := s.FindSync("PL1")
	if !ok || got.ListName != "YT: A" || got.Count != 3 {
		t.Errorf("FindSync = %+v, %v", got, ok)
	}
	if _, ok := s.FindSync("PLNOPE"); ok {
		t.Error("不存在的映射不应命中")
	}
	// upsert 更新
	if err := s.UpsertSync(SyncEntry{PlaylistID: "PL1", ListName: "YT: A", Count: 5}); err != nil {
		t.Fatal(err)
	}
	got, _ = s.FindSync("PL1")
	if got.Count != 5 {
		t.Errorf("upsert 后 Count = %d, want 5", got.Count)
	}
	if n := len(s.SyncEntries()); n != 1 {
		t.Errorf("upsert 不应新增条目, n = %d", n)
	}
	// 多条目
	if err := s.UpsertSync(SyncEntry{PlaylistID: "PL2", ListName: "YT: B"}); err != nil {
		t.Fatal(err)
	}
	if n := len(s.SyncEntries()); n != 2 {
		t.Errorf("SyncEntries = %d, want 2", n)
	}
	// 删除
	if err := s.RemoveSync("PL1"); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.FindSync("PL1"); ok {
		t.Error("删除后不应命中")
	}
	if err := s.RemoveSync("PLNOPE"); err != nil {
		t.Errorf("删除不存在的映射应返回 nil, err = %v", err)
	}
}

func TestCookieHeaderNoLogin(t *testing.T) {
	s, _ := newTestStore(t)
	if _, err := s.CookieHeader(); !errors.Is(err, ErrNoLogin) {
		t.Errorf("未登录应返回 ErrNoLogin, got %v", err)
	}
}

func TestCookieHeaderPasted(t *testing.T) {
	s, _ := newTestStore(t)
	p := filepath.Join(filepath.Dir(s.path), "ytm-cookies.txt")
	raw := "SAPISID=pasted-sap; __Secure-3PAPISID=3p; SID=xxx"
	if err := os.WriteFile(p, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := s.SetLogin(LoginConfig{Method: MethodPasted, CookiesPath: p}); err != nil {
		t.Fatal(err)
	}
	header, err := s.CookieHeader()
	if err != nil {
		t.Fatal(err)
	}
	// 保序 roundtrip
	if header != raw {
		t.Errorf("CookieHeader = %q, want %q", header, raw)
	}
	// 粘贴 header 应被转为 Netscape 格式落盘（供 yt-dlp）
	data, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "\t") || !strings.HasPrefix(string(data), "#") {
		t.Error("粘贴文件应转为 Netscape 格式")
	}
	if fi, err := os.Stat(p); err != nil || fi.Mode().Perm() != 0o600 {
		t.Errorf("转换后的文件权限 = %v, err = %v", fi.Mode().Perm(), err)
	}
}

func TestCookieHeaderPastedMissingFile(t *testing.T) {
	s, _ := newTestStore(t)
	if err := s.SetLogin(LoginConfig{Method: MethodPasted, CookiesPath: filepath.Join(t.TempDir(), "nope.txt")}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CookieHeader(); err == nil {
		t.Error("粘贴文件缺失应报错")
	}
}

func TestCookieHeaderCookiesFileFiltersDomains(t *testing.T) {
	s, _ := newTestStore(t)
	p := filepath.Join(t.TempDir(), "full.txt")
	if err := WriteNetscape(p, []Cookie{
		{Domain: ".youtube.com", IncludeSubdomains: true, Path: "/", Secure: true, Name: "SAPISID", Value: "a"},
		{Domain: ".google.com", IncludeSubdomains: true, Path: "/", Secure: true, Name: "NID", Value: "b"},
		{Domain: "music.youtube.com", Path: "/", Name: "PREF", Value: "c"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetLogin(LoginConfig{Method: MethodCookiesFile, CookiesPath: p}); err != nil {
		t.Fatal(err)
	}
	header, err := s.CookieHeader()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(header, "NID=b") {
		t.Errorf("非 youtube 域不应进入 header: %q", header)
	}
	if !strings.Contains(header, "SAPISID=a") || !strings.Contains(header, "PREF=c") {
		t.Errorf("youtube 域 cookie 应保留: %q", header)
	}
}

func TestCookieHeaderCookiesFileMissing(t *testing.T) {
	s, _ := newTestStore(t)
	if err := s.SetLogin(LoginConfig{Method: MethodCookiesFile, CookiesPath: filepath.Join(t.TempDir(), "nope.txt")}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CookieHeader(); err == nil {
		t.Error("cookies.txt 缺失应报错")
	}
}

func TestCookieHeaderCookiesFileNoYTCookies(t *testing.T) {
	s, _ := newTestStore(t)
	p := filepath.Join(t.TempDir(), "other.txt")
	if err := WriteNetscape(p, []Cookie{
		{Domain: ".google.com", IncludeSubdomains: true, Path: "/", Name: "NID", Value: "b"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetLogin(LoginConfig{Method: MethodCookiesFile, CookiesPath: p}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CookieHeader(); !errors.Is(err, ErrNoYTCookies) {
		t.Errorf("无 youtube cookie 应返回 ErrNoYTCookies, got %v", err)
	}
}

func TestCookieHeaderBrowserLazyExport(t *testing.T) {
	home, _ := fakeBrowserHome(t)
	profileDir := filepath.Join(home, "google-chrome", "Default")
	key := deriveChromeKey([]byte("peanuts"), linuxKeyIterations)
	writeFakeBrowserProfile(t, profileDir, 10, []fakeCookieRow{
		{hostKey: ".youtube.com", name: "SAPISID", encryptedValue: encryptV10ForTest(t, key, []byte("browser-sap"), false), path: "/", expiresUtc: 0},
	})
	s, _ := newTestStore(t)
	if err := s.SetLogin(LoginConfig{Method: MethodBrowser, Browser: "chrome"}); err != nil {
		t.Fatal(err)
	}
	header, err := s.CookieHeader()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(header, "SAPISID=browser-sap") {
		t.Errorf("浏览器懒导出 header = %q", header)
	}
	// CookiesPath 覆盖式更新为默认落盘路径，文件 0600
	cfg := s.Login()
	if cfg.CookiesPath == "" {
		t.Fatal("浏览器导出后应记录 CookiesPath")
	}
	fi, err := os.Stat(cfg.CookiesPath)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("导出文件权限 = %o, want 600", fi.Mode().Perm())
	}
	// CookieFile 返回同一路径
	p, err := s.CookieFile()
	if err != nil || p != cfg.CookiesPath {
		t.Errorf("CookieFile = %q, %v", p, err)
	}
}

// SetPastedLogin：落盘 cookies 文件（0600）+ 保存配置 + CookieHeader 可派生。
func TestSetPastedLogin(t *testing.T) {
	s, path := newTestStore(t)
	p, err := s.SetPastedLogin("SAPISID=abc; __Secure-3PAPISID=xyz")
	if err != nil {
		t.Fatal(err)
	}
	if p != filepath.Join(filepath.Dir(path), "ytm-cookies.txt") {
		t.Errorf("cookies 路径 = %q, want 默认 ytm-cookies.txt", p)
	}
	if cfg := s.Login(); cfg.Method != MethodPasted || cfg.CookiesPath != p {
		t.Errorf("Login = %+v, want MethodPasted", cfg)
	}
	fi, err := os.Stat(p)
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("cookies 文件应 0600: %v %v", fi, err)
	}
	h, err := s.CookieHeader()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(h, "SAPISID=abc") || !strings.Contains(h, "__Secure-3PAPISID=xyz") {
		t.Errorf("CookieHeader = %q, want 含两个 cookie", h)
	}
	// 再次粘贴覆盖同一文件（幂等）
	p2, err := s.SetPastedLogin("SAPISID=new")
	if err != nil {
		t.Fatal(err)
	}
	if p2 != p {
		t.Errorf("二次粘贴路径 = %q, want %q", p2, p)
	}
	if _, err := s.CookieHeader(); err != nil {
		t.Errorf("覆盖后 CookieHeader 应可用: %v", err)
	}
	// 空文本 → 错误
	if _, err := s.SetPastedLogin("   "); err == nil {
		t.Error("空文本应报错")
	}
}

// M4 回归：非法粘贴文本不破坏既有登录——先有有效登录（文件+配置），
// 再粘贴非法文本应报错且文件原样、配置原样（旧实现先覆盖文件再校验）。
func TestSetPastedLoginInvalidDoesNotClobber(t *testing.T) {
	s, _ := newTestStore(t)
	p, err := s.SetPastedLogin("SAPISID=good; __Secure-3PAPISID=3p")
	if err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}

	// 非法粘贴：无 name=value 结构
	if _, err := s.SetPastedLogin("garbage without equals"); err == nil {
		t.Fatal("非法粘贴应报错")
	}
	// 文件原样（仍含有效 cookie，未写入垃圾文本）
	after, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Errorf("非法粘贴不应覆盖 cookie 文件:\nbefore %q\nafter  %q", before, after)
	}
	// 配置原样：仍是第一次粘贴的登录，CookieHeader 可派生
	cfg := s.Login()
	if cfg.Method != MethodPasted || cfg.CookiesPath != p {
		t.Errorf("非法粘贴不应改配置: %+v", cfg)
	}
	h, err := s.CookieHeader()
	if err != nil {
		t.Fatalf("既有登录应保持可用: %v", err)
	}
	if !strings.Contains(h, "SAPISID=good") {
		t.Errorf("CookieHeader = %q, want 仍含原 cookie", h)
	}
	// Netscape 格式粘贴的非法内容同样拒绝（无 cookie 行的纯注释文件除外……）
	// 注："# comment" 会被 looksLikeNetscape 判为 Netscape 原样保留，属既有行为
}
