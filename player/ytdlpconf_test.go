package player

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// testConfPath 返回 buildYtdlpConf 的写入路径（os.TempDir()/music-tui-ytdlp-<pid>.conf）。
func testConfPath() string {
	return filepath.Join(os.TempDir(), fmt.Sprintf("music-tui-ytdlp-%d.conf", os.Getpid()))
}

// isolateYtdlpEnv 隔离用户默认配置候选：XDG_CONFIG_HOME/HOME 指向空目录
// （~/.config/yt-dlp/config 等候选不存在），保证测试只看 add-header 行。
func isolateYtdlpEnv(t *testing.T) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(t.TempDir(), "xdg"))
	t.Setenv("HOME", t.TempDir())
}

// cleanupTestConf 兜底清理临时配置文件（buildYtdlpConf 写全局固定路径，
// 失败路径也保证不残留污染后续测试）。
func cleanupTestConf(t *testing.T) {
	t.Helper()
	t.Cleanup(func() { os.Remove(testConfPath()) })
}

func buildConfForTest(t *testing.T, headers map[string]string) string {
	t.Helper()
	path, err := buildYtdlpConf(headers)
	if err != nil {
		t.Fatalf("buildYtdlpConf: %v", err)
	}
	if path != testConfPath() {
		t.Errorf("conf 路径 = %q, want %q", path, testConfPath())
	}
	return path
}

func readConf(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读临时配置: %v", err)
	}
	return string(data)
}

// 2 个 header 按键排序输出；值含空格 → 双引号包裹（yt-dlp 配置 shlex POSIX）。
func TestBuildYtdlpConfSortedHeadersAndSpaceQuoted(t *testing.T) {
	isolateYtdlpEnv(t)
	cleanupTestConf(t)
	path := buildConfForTest(t, map[string]string{
		"X-B": "b value", // 值含空格 → "X-B:b value"
		"X-A": "plain",
	})
	want := "add-header X-A:plain\nadd-header \"X-B:b value\"\n"
	if got := readConf(t, path); got != want {
		t.Errorf("conf 内容 = %q, want %q", got, want)
	}
}

// 值含双引号/反斜杠 → 双引号包裹并转义（\ → \\，" → \"）。
func TestBuildYtdlpConfEscapesQuotesAndBackslashes(t *testing.T) {
	isolateYtdlpEnv(t)
	cleanupTestConf(t)
	path := buildConfForTest(t, map[string]string{
		"X-Q": `a"b`,
		"X-S": `a\b`,
	})
	want := "add-header \"X-Q:a\\\"b\"\nadd-header \"X-S:a\\\\b\"\n"
	if got := readConf(t, path); got != want {
		t.Errorf("conf 内容 = %q, want %q", got, want)
	}
}

// 值含 # → 双引号包裹（防 shlex 注释截断）。
func TestBuildYtdlpConfHashValueQuoted(t *testing.T) {
	isolateYtdlpEnv(t)
	cleanupTestConf(t)
	path := buildConfForTest(t, map[string]string{"X-H": "a#b"})
	want := "add-header \"X-H:a#b\"\n"
	if got := readConf(t, path); got != want {
		t.Errorf("conf 内容 = %q, want %q", got, want)
	}
}

// 用户默认配置原文合并在前（XDG_CONFIG_HOME/yt-dlp/config），排序 add-header 行在后。
func TestBuildYtdlpConfMergesUserConfigFirst(t *testing.T) {
	xdg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdg)
	t.Setenv("HOME", t.TempDir())
	cleanupTestConf(t)
	confDir := filepath.Join(xdg, "yt-dlp")
	if err := os.MkdirAll(confDir, 0o755); err != nil {
		t.Fatal(err)
	}
	userConf := "# 用户默认配置\nformat bestaudio\n"
	if err := os.WriteFile(filepath.Join(confDir, "config"), []byte(userConf), 0o644); err != nil {
		t.Fatal(err)
	}
	path := buildConfForTest(t, map[string]string{
		"X-Z": "z",
		"X-A": "a",
	})
	want := "# 用户默认配置\nformat bestaudio\nadd-header X-A:a\nadd-header X-Z:z\n"
	if got := readConf(t, path); got != want {
		t.Errorf("conf 内容 = %q, want %q", got, want)
	}
}

// 临时配置文件权限 0600（含 cookie 头，仅当前用户可读）。
func TestBuildYtdlpConfFilePerm0600(t *testing.T) {
	isolateYtdlpEnv(t)
	cleanupTestConf(t)
	path := buildConfForTest(t, map[string]string{"X-A": "a"})
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if perm := fi.Mode().Perm(); perm != 0o600 {
		t.Errorf("conf 权限 = %o, want 600", perm)
	}
}

// 覆盖写幂等：同路径再次生成（重连场景 startProcess 每次重新生成）。
func TestBuildYtdlpConfOverwrites(t *testing.T) {
	isolateYtdlpEnv(t)
	cleanupTestConf(t)
	buildConfForTest(t, map[string]string{"X-A": "a"})
	path := buildConfForTest(t, map[string]string{"X-B": "b"})
	want := "add-header X-B:b\n"
	if got := readConf(t, path); got != want {
		t.Errorf("conf 内容 = %q, want %q（覆盖写）", got, want)
	}
}

// quoteMpvOptionValue：mpv 选项值含逗号/双引号时双引号包裹并转义内部双引号。
func TestQuoteMpvOptionValue(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"/tmp/plain", "/tmp/plain"},    // 无特殊字符 → 原样
		{"/tmp/a,b", `"/tmp/a,b"`},      // 含逗号 → 引号包裹
		{`/tmp/a"b`, `"/tmp/a\"b"`},     // 含双引号 → 引号包裹 + 转义
		{`/tmp/a"b,c`, `"/tmp/a\"b,c"`}, // 逗号+双引号
		{"", ""},                        // 空串 → 原样
	}
	for _, tc := range cases {
		if got := quoteMpvOptionValue(tc.in); got != tc.want {
			t.Errorf("quoteMpvOptionValue(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
