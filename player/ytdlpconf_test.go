package player

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// testConfPath 返回 buildYtdlpConf 的写入路径（os.TempDir()/music-tui-ytdlp-<pid>.conf）。
func testConfPath() string {
	return filepath.Join(os.TempDir(), fmt.Sprintf("music-tui-ytdlp-%d.conf", os.Getpid()))
}

// noUserConfPaths 返回注入用的固定路径列表：指向不存在的文件（彻底隔离
// 本机 /etc/yt-dlp.conf 等用户配置，保证断言只看 add-header 行）。
func noUserConfPaths(t *testing.T) []string {
	t.Helper()
	return []string{filepath.Join(t.TempDir(), "no-such-user-config")}
}

// cleanupTestConf 兜底清理临时配置文件（buildYtdlpConf 写全局固定路径，
// 失败路径也保证不残留污染后续测试）。
func cleanupTestConf(t *testing.T) {
	t.Helper()
	t.Cleanup(func() { os.Remove(testConfPath()) })
}

func buildConfForTest(t *testing.T, headers map[string]string, paths ...string) string {
	t.Helper()
	if len(paths) == 0 {
		paths = noUserConfPaths(t) // 不传 paths 时也注入不存在的候选，避免依赖本机配置
	}
	path, err := buildYtdlpConf(headers, paths...)
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

// 2 个 header 按键排序输出；值含空格 → 双引号包裹（yt-dlp 配置 shlex POSIX）；
// 选项行带 -- 前缀（配置文件中与命令行 switch 相同，无前缀会被 optparse
// 当作 URL 位置参数 → headers 不生效）。
func TestBuildYtdlpConfSortedHeadersAndSpaceQuoted(t *testing.T) {
	cleanupTestConf(t)
	path := buildConfForTest(t, map[string]string{
		"X-B": "b value", // 值含空格 → "X-B:b value"
		"X-A": "plain",
	})
	want := "--add-header X-A:plain\n--add-header \"X-B:b value\"\n"
	if got := readConf(t, path); got != want {
		t.Errorf("conf 内容 = %q, want %q", got, want)
	}
}

// 值含双引号/反斜杠 → 双引号包裹并转义（\ → \\，" → \"）。
func TestBuildYtdlpConfEscapesQuotesAndBackslashes(t *testing.T) {
	cleanupTestConf(t)
	path := buildConfForTest(t, map[string]string{
		"X-Q": `a"b`,
		"X-S": `a\b`,
	})
	want := "--add-header \"X-Q:a\\\"b\"\n--add-header \"X-S:a\\\\b\"\n"
	if got := readConf(t, path); got != want {
		t.Errorf("conf 内容 = %q, want %q", got, want)
	}
}

// 值含 # → 双引号包裹（防 shlex 注释截断）。
func TestBuildYtdlpConfHashValueQuoted(t *testing.T) {
	cleanupTestConf(t)
	path := buildConfForTest(t, map[string]string{"X-H": "a#b"})
	want := "--add-header \"X-H:a#b\"\n"
	if got := readConf(t, path); got != want {
		t.Errorf("conf 内容 = %q, want %q", got, want)
	}
}

// 值含换行 → 双引号包裹（防配置行结构被拆散）。
func TestBuildYtdlpConfNewlineValueQuoted(t *testing.T) {
	cleanupTestConf(t)
	path := buildConfForTest(t, map[string]string{"X-N": "a\nb"})
	want := "--add-header \"X-N:a\nb\"\n"
	if got := readConf(t, path); got != want {
		t.Errorf("conf 内容 = %q, want %q", got, want)
	}
}

// 用户默认配置原文合并在前（注入固定路径），过滤 config-location(s) 行，
// 排序 --add-header 行在后。
func TestBuildYtdlpConfMergesUserConfigFirst(t *testing.T) {
	confDir := t.TempDir()
	cleanupTestConf(t)
	userConf := "# 用户默认配置\nformat bestaudio\n"
	confPath := filepath.Join(confDir, "config")
	if err := os.WriteFile(confPath, []byte(userConf), 0o644); err != nil {
		t.Fatal(err)
	}
	path := buildConfForTest(t, map[string]string{
		"X-Z": "z",
		"X-A": "a",
	}, confPath)
	want := "# 用户默认配置\nformat bestaudio\n--add-header X-A:a\n--add-header X-Z:z\n"
	if got := readConf(t, path); got != want {
		t.Errorf("conf 内容 = %q, want %q", got, want)
	}
}

// 用户配置内嵌套的 --config-locations 相对路径会被 yt-dlp 解析为相对该配置
// 文件目录（合并副本在 os.TempDir()）→ 文件不存在 → parser.error 致命退出
// → mpv 播放全挂。合并时必须过滤 config-location(s) 行（含带值行、单/双
// 短横线、缩进）。
func TestBuildYtdlpConfFiltersConfigLocations(t *testing.T) {
	confDir := t.TempDir()
	cleanupTestConf(t)
	userConf := strings.Join([]string{
		"--config-locations nested.conf", // 带值行：必须过滤
		"format bestaudio",
		"# 注释保留",
		"--config-location=other.conf",     // = 形式：必须过滤
		"  -config-locations  spaced.conf", // 单横线 + 缩进：必须过滤
	}, "\n") + "\n"
	confPath := filepath.Join(confDir, "config")
	if err := os.WriteFile(confPath, []byte(userConf), 0o644); err != nil {
		t.Fatal(err)
	}
	path := buildConfForTest(t, map[string]string{"X-A": "a"}, confPath)
	want := "format bestaudio\n# 注释保留\n--add-header X-A:a\n"
	if got := readConf(t, path); got != want {
		t.Errorf("conf 内容 = %q, want %q", got, want)
	}
}

// 临时配置文件权限 0600（含 cookie 头，仅当前用户可读）。
func TestBuildYtdlpConfFilePerm0600(t *testing.T) {
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
	cleanupTestConf(t)
	buildConfForTest(t, map[string]string{"X-A": "a"})
	path := buildConfForTest(t, map[string]string{"X-B": "b"})
	want := "--add-header X-B:b\n"
	if got := readConf(t, path); got != want {
		t.Errorf("conf 内容 = %q, want %q（覆盖写）", got, want)
	}
}

// quoteMpvOptionValue：mpv 列表值语法——值含逗号时双引号包裹；内部 `"`
// 无法转义表示（read_subparam 引号内不支持反斜杠转义，含 `,`+`"` 的值
// 不可用），无逗号时原样返回（含裸 `"` 原样透传）。
func TestQuoteMpvOptionValue(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"/tmp/plain", "/tmp/plain"},   // 无特殊字符 → 原样
		{"/tmp/a,b", `"/tmp/a,b"`},     // 含逗号 → 引号包裹
		{`/tmp/a"b`, `/tmp/a"b`},       // 含双引号无逗号 → 原样（不转义）
		{`/tmp/a"b,c`, `"/tmp/a"b,c"`}, // 逗号+双引号：引号包裹但内部 " 无法表示（不可用）
		{"", ""},                       // 空串 → 原样
	}
	for _, tc := range cases {
		if got := quoteMpvOptionValue(tc.in); got != tc.want {
			t.Errorf("quoteMpvOptionValue(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
