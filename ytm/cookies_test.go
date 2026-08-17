package ytm

import (
	"runtime"
	"database/sql"
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// ---- Netscape 解析/写出 ----

func TestParseNetscape(t *testing.T) {
	data := `# Netscape HTTP Cookie File
# comment line

.youtube.com	TRUE	/	TRUE	1750000000	SAPISID	abc123
youtube.com	FALSE	/	FALSE	0	NID	plain-value
#HttpOnly_.youtube.com	TRUE	/	TRUE	1800000000	SID	secret
bad line without tabs
.youtube.com	TRUE	/	TRUE	1600000000	OK
`
	cookies, err := ParseNetscape([]byte(data))
	if err != nil {
		t.Fatal(err)
	}
	if len(cookies) != 3 {
		t.Fatalf("len = %d, want 3（注释/空行/畸形行应跳过）", len(cookies))
	}
	c0 := cookies[0]
	if c0.Domain != ".youtube.com" || c0.Name != "SAPISID" || c0.Value != "abc123" {
		t.Errorf("c0 = %+v", c0)
	}
	if !c0.IncludeSubdomains || !c0.Secure || c0.Expires != 1750000000 || c0.HttpOnly {
		t.Errorf("c0 标志位错误: %+v", c0)
	}
	c1 := cookies[1]
	if c1.HttpOnly || c1.IncludeSubdomains || c1.Secure || c1.Expires != 0 {
		t.Errorf("c1 标志位错误: %+v", c1)
	}
	c2 := cookies[2]
	if !c2.HttpOnly || c2.Domain != ".youtube.com" || c2.Name != "SID" || c2.Value != "secret" {
		t.Errorf("c2 HTTPONLY 标记处理错误: %+v", c2)
	}
}

func TestParseNetscapeEmpty(t *testing.T) {
	cookies, err := ParseNetscape(nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(cookies) != 0 {
		t.Errorf("空输入应返回空列表, got %d", len(cookies))
	}
}

func TestWriteNetscapePermissionsAndRoundtrip(t *testing.T) {
    if runtime.GOOS == "windows" {
        t.Skip("Windows 无 POSIX 权限位语义；浏览器 cookie 导出仅 linux/darwin 支持（ErrUnsupportedOS），skip")
    }
	path := filepath.Join(t.TempDir(), "cookies.txt")
	cookies := []Cookie{
		{Domain: ".youtube.com", IncludeSubdomains: true, Path: "/", Secure: true, Expires: 1750000000, Name: "SAPISID", Value: "abc"},
		{Domain: "youtube.com", Path: "/", Name: "NID", Value: "v", HttpOnly: true},
	}
	if err := WriteNetscape(path, cookies); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("权限 = %o, want 600", fi.Mode().Perm())
	}
	// 回读 roundtrip
	back, err := ParseNetscape(mustRead(t, path))
	if err != nil {
		t.Fatal(err)
	}
	if len(back) != 2 {
		t.Fatalf("roundtrip len = %d", len(back))
	}
	if back[0] != cookies[0] {
		t.Errorf("c0 roundtrip = %+v, want %+v", back[0], cookies[0])
	}
	if back[1].HttpOnly != true || back[1].Domain != "youtube.com" {
		t.Errorf("c1 roundtrip = %+v", back[1])
	}
}

func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// ---- 域过滤 ----

func TestIsYouTubeDomain(t *testing.T) {
	ok := []string{"youtube.com", ".youtube.com", "www.youtube.com", "music.youtube.com", ".music.youtube.com"}
	bad := []string{"google.com", "example.com", "youtube.com.evil.com", "myoutube.com", "", "accounts.google.com"}
	for _, d := range ok {
		if !isYouTubeDomain(d) {
			t.Errorf("%q 应视为 youtube 域", d)
		}
	}
	for _, d := range bad {
		if isYouTubeDomain(d) {
			t.Errorf("%q 不应视为 youtube 域", d)
		}
	}
}

// ---- 浏览器导出 ----

// fakeCookieRow 是构造假 Cookies 数据库的一行。
type fakeCookieRow struct {
	hostKey        string
	name           string
	value          string
	encryptedValue []byte
	path           string
	expiresUtc     int64
	secure         int
	httponly       int
}

// writeFakeBrowserProfile 构造一个假浏览器配置目录：
// 含 Cookies SQLite 数据库（meta 表 + cookies 表，列名与 Chrome 一致）。
func writeFakeBrowserProfile(t *testing.T, profileDir string, metaVersion int, rows []fakeCookieRow) {
	t.Helper()
	if err := os.MkdirAll(profileDir, 0o755); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", filepath.Join(profileDir, "Cookies"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE meta(key TEXT, value TEXT)`); err != nil {
		t.Fatal(err)
	}
	if metaVersion > 0 {
		if _, err := db.Exec(`INSERT INTO meta VALUES('version', ?)`, strconv.Itoa(metaVersion)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`CREATE TABLE cookies(
		host_key TEXT, name TEXT, value TEXT, encrypted_value BLOB,
		path TEXT, expires_utc INTEGER, is_secure INTEGER, is_httponly INTEGER)`); err != nil {
		t.Fatal(err)
	}
	for _, r := range rows {
		if _, err := db.Exec(`INSERT INTO cookies VALUES(?,?,?,?,?,?,?,?)`,
			r.hostKey, r.name, r.value, r.encryptedValue, r.path, r.expiresUtc, r.secure, r.httponly); err != nil {
			t.Fatal(err)
		}
	}
}

// fakeBrowserHome 把 XDG_CONFIG_HOME 指向临时目录并构造 chrome 配置。
func fakeBrowserHome(t *testing.T) (home string, profileDir string) {
	t.Helper()
	home = t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", home)
	t.Setenv("HOME", home)
	profileDir = filepath.Join(home, "google-chrome", "Default")
	return home, profileDir
}

func TestExportBrowserCookiesNotFound(t *testing.T) {
    if runtime.GOOS == "windows" {
        t.Skip("Windows 无 POSIX 权限位语义；浏览器 cookie 导出仅 linux/darwin 支持（ErrUnsupportedOS），skip")
    }
	fakeBrowserHome(t) // 空配置目录
	err := ExportBrowserCookies("chrome", filepath.Join(t.TempDir(), "out.txt"))
	if !errors.Is(err, ErrBrowserNotFound) {
		t.Errorf("未安装浏览器应返回 ErrBrowserNotFound, got %v", err)
	}
}

func TestExportBrowserCookiesUnsupportedBrowser(t *testing.T) {
    if runtime.GOOS == "windows" {
        t.Skip("Windows 无 POSIX 权限位语义；浏览器 cookie 导出仅 linux/darwin 支持（ErrUnsupportedOS），skip")
    }
	fakeBrowserHome(t)
	err := ExportBrowserCookies("firefox", filepath.Join(t.TempDir(), "out.txt"))
	if !errors.Is(err, ErrBrowserNotFound) && !strings.Contains(err.Error(), "不支持") {
		t.Errorf("未知浏览器应报错, got %v", err)
	}
}

func TestExportBrowserCookiesV10Decrypt(t *testing.T) {
    if runtime.GOOS == "windows" {
        t.Skip("Windows 无 POSIX 权限位语义；浏览器 cookie 导出仅 linux/darwin 支持（ErrUnsupportedOS），skip")
    }
	_, profileDir := fakeBrowserHome(t)
	key := deriveChromeKey([]byte("peanuts"), linuxKeyIterations)
	rows := []fakeCookieRow{
		// youtube 域 v10 加密 cookie（meta_version=10，无 SHA256 前缀）
		{hostKey: ".youtube.com", name: "SAPISID", encryptedValue: encryptV10ForTest(t, key, []byte("sap-value"), false), path: "/", expiresUtc: 13000000000000000, secure: 1},
		{hostKey: "music.youtube.com", name: "PREF", encryptedValue: encryptV10ForTest(t, key, []byte("pref-value"), false), path: "/", expiresUtc: 0},
		// 非 youtube 域：不应导出
		{hostKey: ".google.com", name: "NID", encryptedValue: encryptV10ForTest(t, key, []byte("nid-value"), false), path: "/", expiresUtc: 0},
		// 未加密明文 cookie：直接导出
		{hostKey: ".youtube.com", name: "VISITOR_INFO1_LIVE", value: "plain-visitor", path: "/", expiresUtc: 0},
	}
	writeFakeBrowserProfile(t, profileDir, 10, rows)

	out := filepath.Join(t.TempDir(), "ytm-cookies.txt")
	if err := ExportBrowserCookies("chrome", out); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(out)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("输出文件权限 = %o, want 600", fi.Mode().Perm())
	}
	cookies, err := ParseNetscape(mustRead(t, out))
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, c := range cookies {
		got[c.Name] = c.Value
		if !isYouTubeDomain(c.Domain) {
			t.Errorf("不应导出非 youtube 域 cookie: %+v", c)
		}
	}
	if got["SAPISID"] != "sap-value" {
		t.Errorf("SAPISID 解密 = %q, want %q", got["SAPISID"], "sap-value")
	}
	if got["PREF"] != "pref-value" {
		t.Errorf("PREF 解密 = %q", got["PREF"])
	}
	if got["VISITOR_INFO1_LIVE"] != "plain-visitor" {
		t.Errorf("明文 cookie 应原样导出, got %q", got["VISITOR_INFO1_LIVE"])
	}
	if _, ok := got["NID"]; ok {
		t.Error("google.com 域 cookie 不应导出")
	}
	// 过期时间：Chrome epoch 微秒 → unix 秒
	for _, c := range cookies {
		if c.Name == "SAPISID" && c.Expires != 1355526400 {
			t.Errorf("SAPISID expires = %d, want 1355526400", c.Expires)
		}
		if c.Name == "PREF" && c.Expires != 0 {
			t.Errorf("会话 cookie expires 应为 0, got %d", c.Expires)
		}
	}
}

func TestExportBrowserCookiesMeta24StripsPrefix(t *testing.T) {
    if runtime.GOOS == "windows" {
        t.Skip("Windows 无 POSIX 权限位语义；浏览器 cookie 导出仅 linux/darwin 支持（ErrUnsupportedOS），skip")
    }
	_, profileDir := fakeBrowserHome(t)
	key := deriveChromeKey([]byte("peanuts"), linuxKeyIterations)
	// meta_version=24：密文带 32 字节 SHA256 前缀，解密后应剥掉
	rows := []fakeCookieRow{
		{hostKey: ".youtube.com", name: "SAPISID", encryptedValue: encryptV10ForTest(t, key, []byte("meta24-value"), true), path: "/", expiresUtc: 0},
	}
	writeFakeBrowserProfile(t, profileDir, 24, rows)

	out := filepath.Join(t.TempDir(), "ytm-cookies.txt")
	if err := ExportBrowserCookies("chrome", out); err != nil {
		t.Fatal(err)
	}
	cookies, err := ParseNetscape(mustRead(t, out))
	if err != nil {
		t.Fatal(err)
	}
	if len(cookies) != 1 || cookies[0].Value != "meta24-value" {
		t.Errorf("meta24 前缀未剥离: %+v", cookies)
	}
}

func TestExportBrowserCookiesEmptyKeyFallback(t *testing.T) {
    if runtime.GOOS == "windows" {
        t.Skip("Windows 无 POSIX 权限位语义；浏览器 cookie 导出仅 linux/darwin 支持（ErrUnsupportedOS），skip")
    }
	_, profileDir := fakeBrowserHome(t)
	// Local State 无 encrypted_key → peanuts 优先；cookie 用 empty key 加密 → 降级成功
	emptyKey := deriveChromeKey(nil, linuxKeyIterations)
	rows := []fakeCookieRow{
		{hostKey: ".youtube.com", name: "SAPISID", encryptedValue: encryptV10ForTest(t, emptyKey, []byte("empty-key-value"), false), path: "/", expiresUtc: 0},
	}
	writeFakeBrowserProfile(t, profileDir, 10, rows)

	out := filepath.Join(t.TempDir(), "ytm-cookies.txt")
	if err := ExportBrowserCookies("chrome", out); err != nil {
		t.Fatal(err)
	}
	cookies, err := ParseNetscape(mustRead(t, out))
	if err != nil {
		t.Fatal(err)
	}
	if len(cookies) != 1 || cookies[0].Value != "empty-key-value" {
		t.Errorf("empty key 降级失败: %+v", cookies)
	}
}

// M3 回归：Linux 忽略 Local State 的 encrypted_key（对齐 yt-dlp，该字段仅
// Windows/DPAPI 使用）——Local State 存在时 peanuts 仍被尝试（旧实现用
// encrypted_key 替换 primary，peanuts 永不被尝试导致解密失败）。
func TestExportBrowserCookiesIgnoresLocalStateKey(t *testing.T) {
    if runtime.GOOS == "windows" {
        t.Skip("Windows 无 POSIX 权限位语义；浏览器 cookie 导出仅 linux/darwin 支持（ErrUnsupportedOS），skip")
    }
	t.Run("peanuts-still-tried", func(t *testing.T) {
		home, profileDir := fakeBrowserHome(t)
		_ = home
		// Local State 携带 os_crypt.encrypted_key（base64("DPAPI"+password)）
		password := []byte("local-state-password")
		if err := os.MkdirAll(profileDir, 0o755); err != nil {
			t.Fatal(err)
		}
		ls := `{"os_crypt":{"encrypted_key":"` + base64.StdEncoding.EncodeToString(append([]byte("DPAPI"), password...)) + `"}}`
		if err := os.WriteFile(filepath.Join(profileDir, "Local State"), []byte(ls), 0o644); err != nil {
			t.Fatal(err)
		}
		// cookie 用 peanuts 加密（Linux 真实密钥）
		key := deriveChromeKey([]byte("peanuts"), linuxKeyIterations)
		rows := []fakeCookieRow{
			{hostKey: ".youtube.com", name: "SAPISID", encryptedValue: encryptV10ForTest(t, key, []byte("peanuts-value"), false), path: "/", expiresUtc: 0},
		}
		writeFakeBrowserProfile(t, profileDir, 10, rows)

		out := filepath.Join(t.TempDir(), "ytm-cookies.txt")
		if err := ExportBrowserCookies("chrome", out); err != nil {
			t.Fatalf("encrypted_key 存在时 peanuts 应仍可解密: %v", err)
		}
		cookies, err := ParseNetscape(mustRead(t, out))
		if err != nil {
			t.Fatal(err)
		}
		if len(cookies) != 1 || cookies[0].Value != "peanuts-value" {
			t.Errorf("解密结果 = %+v, want peanuts-value", cookies)
		}
	})
	t.Run("local-state-key-not-used", func(t *testing.T) {
		home, profileDir := fakeBrowserHome(t)
		_ = home
		password := []byte("local-state-password")
		if err := os.MkdirAll(profileDir, 0o755); err != nil {
			t.Fatal(err)
		}
		ls := `{"os_crypt":{"encrypted_key":"` + base64.StdEncoding.EncodeToString(append([]byte("DPAPI"), password...)) + `"}}`
		if err := os.WriteFile(filepath.Join(profileDir, "Local State"), []byte(ls), 0o644); err != nil {
			t.Fatal(err)
		}
		// cookie 只用 Local State 密钥加密（旧实现能解，新实现应失败）
		key := deriveChromeKey(password, linuxKeyIterations)
		rows := []fakeCookieRow{
			{hostKey: ".youtube.com", name: "SAPISID", encryptedValue: encryptV10ForTest(t, key, []byte("ls-value"), false), path: "/", expiresUtc: 0},
		}
		writeFakeBrowserProfile(t, profileDir, 10, rows)

		err := ExportBrowserCookies("chrome", filepath.Join(t.TempDir(), "out.txt"))
		if !errors.Is(err, ErrDecryptFailed) {
			t.Errorf("Local State 密钥不应在 Linux 使用（应解密失败）, got %v", err)
		}
	})
}

func TestExportBrowserCookiesNoYouTubeCookies(t *testing.T) {
    if runtime.GOOS == "windows" {
        t.Skip("Windows 无 POSIX 权限位语义；浏览器 cookie 导出仅 linux/darwin 支持（ErrUnsupportedOS），skip")
    }
	_, profileDir := fakeBrowserHome(t)
	key := deriveChromeKey([]byte("peanuts"), linuxKeyIterations)
	rows := []fakeCookieRow{
		{hostKey: ".google.com", name: "NID", encryptedValue: encryptV10ForTest(t, key, []byte("nid"), false), path: "/", expiresUtc: 0},
	}
	writeFakeBrowserProfile(t, profileDir, 10, rows)

	err := ExportBrowserCookies("chrome", filepath.Join(t.TempDir(), "out.txt"))
	if !errors.Is(err, ErrNoYTCookies) {
		t.Errorf("无 youtube cookie 应返回 ErrNoYTCookies, got %v", err)
	}
}

func TestExportBrowserCookiesDecryptFailure(t *testing.T) {
    if runtime.GOOS == "windows" {
        t.Skip("Windows 无 POSIX 权限位语义；浏览器 cookie 导出仅 linux/darwin 支持（ErrUnsupportedOS），skip")
    }
	_, profileDir := fakeBrowserHome(t)
	rows := []fakeCookieRow{
		// 无法解密的垃圾密文（填充校验失败）
		{hostKey: ".youtube.com", name: "SAPISID", encryptedValue: []byte("v10" + string(make([]byte, 16))), path: "/", expiresUtc: 0},
	}
	writeFakeBrowserProfile(t, profileDir, 10, rows)

	err := ExportBrowserCookies("chrome", filepath.Join(t.TempDir(), "out.txt"))
	if !errors.Is(err, ErrDecryptFailed) {
		t.Errorf("全量解密失败应返回 ErrDecryptFailed, got %v", err)
	}
}

func TestExportBrowserCookiesBraveAlternativeDir(t *testing.T) {
    if runtime.GOOS == "windows" {
        t.Skip("Windows 无 POSIX 权限位语义；浏览器 cookie 导出仅 linux/darwin 支持（ErrUnsupportedOS），skip")
    }
	home, _ := fakeBrowserHome(t)
	// Brave Linux 实际目录：~/.config/BraveSoftware/Brave-Browser
	profileDir := filepath.Join(home, "BraveSoftware", "Brave-Browser", "Default")
	key := deriveChromeKey([]byte("peanuts"), linuxKeyIterations)
	rows := []fakeCookieRow{
		{hostKey: ".youtube.com", name: "SAPISID", encryptedValue: encryptV10ForTest(t, key, []byte("brave-value"), false), path: "/", expiresUtc: 0},
	}
	writeFakeBrowserProfile(t, profileDir, 10, rows)

	out := filepath.Join(t.TempDir(), "ytm-cookies.txt")
	if err := ExportBrowserCookies("brave", out); err != nil {
		t.Fatal(err)
	}
	cookies, err := ParseNetscape(mustRead(t, out))
	if err != nil {
		t.Fatal(err)
	}
	if len(cookies) != 1 || cookies[0].Value != "brave-value" {
		t.Errorf("brave 目录探测失败: %+v", cookies)
	}
}

// m6：macOS 非 v10/v11 前缀的旧明文 cookie 原样返回（yt-dlp Mac 分支同款）；
// 非 macOS（macLegacyPlain=false）无法处理。
func TestDecryptValueMacLegacyPlaintext(t *testing.T) {
	// macOS：无前缀旧明文 → 原样返回
	macKeys := &chromeKeys{empty: deriveChromeKey(nil, linuxKeyIterations), macLegacyPlain: true}
	if got, ok := decryptValue([]byte("legacy-plain-value"), macKeys); !ok || got != "legacy-plain-value" {
		t.Errorf("macOS 旧明文应原样返回, got %q, %v", got, ok)
	}
	// 非 macOS：未知前缀 → 无法处理
	linuxKeys := &chromeKeys{empty: deriveChromeKey(nil, linuxKeyIterations)}
	if _, ok := decryptValue([]byte("legacy-plain-value"), linuxKeys); ok {
		t.Error("Linux 未知前缀不应返回明文")
	}
	// 空/过短输入两类平台都不处理
	if _, ok := decryptValue(nil, macKeys); ok {
		t.Error("空输入不应处理")
	}
}

func TestChromeEpochToUnix(t *testing.T) {
	cases := []struct {
		us   int64
		want int64
	}{
		{0, 0},
		{13000000000000000, 1355526400}, // 1601-epoch 微秒 → unix 秒
		{1750000000, 1750000000},        // 已经是 unix 秒（小值直通）
		{-1, 0},
	}
	for _, c := range cases {
		if got := chromeEpochToUnix(c.us); got != c.want {
			t.Errorf("chromeEpochToUnix(%d) = %d, want %d", c.us, got, c.want)
		}
	}
}
