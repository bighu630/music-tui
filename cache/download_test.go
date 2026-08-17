//go:build !windows

// 本文件全部用例经由 writeFakeYtDlp 假 yt-dlp 脚本（POSIX sh）驱动真实
// exec 流程，Windows 无法执行该脚本，整文件跳过（见 cache_test.go 头部
// 注释）。
package cache

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// fakeYtDlpBody 返回假 yt-dlp 脚本体：先遍历参数解析 -o 模板得到输出路径 $out
// （%(ext)s → webm，与真实 yt-dlp -o 落盘同语义），再执行 extra（其中可用 $out
// 写文件）。与真实命令 `yt-dlp ... -o <destBase>.%(ext)s <url>` 对齐。
func fakeYtDlpBody(extra string) string {
	return `
out=""
prev=""
for a in "$@"; do
  if [ "$prev" = "-o" ]; then
    out=$(printf '%s' "$a" | sed 's/%(ext)s/webm/')
  fi
  prev="$a"
done
[ -n "$out" ] || exit 9
` + extra
}

// fakeAudioOut 是假 yt-dlp 脚本的合法音频产物写法：EBML/WebM 魔数（八进制
// 转义，POSIX sh 兼容）+ 零填充到 2048 字节（≥ MinAudioSize）——内容校验
// 通过，下载→注册全链路真实走通（与真实 yt-dlp 落盘音频对齐）。
const fakeAudioOut = `printf '\032\105\337\243' > "$out"
head -c 2044 /dev/zero >> "$out"`

// argsLogBody 返回假 yt-dlp 脚本体：把收到的全部参数逐行写入 <缓存目录>/args
//（配合 fakeYtDlpBody 的 -o 解析），用于断言启动参数（--cookies/--add-header）。
// printf '%s\n' "$@" 保证参数边界不丢（值含空格时仍是一行一个参数）。
func argsLogBody(extra string) string {
	return fakeYtDlpBody(`printf '%s\n' "$@" > "$(dirname "$out")/args"
` + extra)
}

// readArgsFile 读取假脚本写入的逐行参数文件，返回参数切片。
func readArgsFile(t *testing.T, dir string) []string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, "args"))
	if err != nil {
		t.Fatalf("读 args 文件: %v", err)
	}
	s := strings.TrimRight(string(data), "\n")
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}

func writeFakeYtDlp(t *testing.T, body string) string {
	t.Helper()
	script := filepath.Join(t.TempDir(), "yt-dlp")
	if err := os.WriteFile(script, []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return script
}

func TestRealDownloadFakeScript(t *testing.T) {
	script := writeFakeYtDlp(t, fakeYtDlpBody(fakeAudioOut))
	destBase := filepath.Join(t.TempDir(), "song")
	file, err := realDownload(context.Background(), script, "https://youtube.com/watch?v=abc", destBase, "", nil)
	if err != nil {
		t.Fatalf("realDownload: %v", err)
	}
	if want := "song.webm"; file != want {
		t.Errorf("file = %q, want %q", file, want)
	}
	if filepath.Base(file) != file {
		t.Errorf("返回值应为 basename（register 用）: %q", file)
	}
	data, err := os.ReadFile(destBase + ".webm")
	if err != nil {
		t.Fatalf("read output: %v", err)
	}
	if string(data) != string(validAudioBytes()) {
		t.Errorf("content = %q, want 合法音频字节（EBML 魔数 + 2048 字节）", data)
	}
}

func TestRealDownloadScriptFails(t *testing.T) {
	script := writeFakeYtDlp(t, fakeYtDlpBody(`echo "HTTP Error 403" >&2; exit 1`))
	destBase := filepath.Join(t.TempDir(), "song")
	_, err := realDownload(context.Background(), script, "https://youtube.com/watch?v=abc", destBase, "", nil)
	if err == nil {
		t.Fatal("realDownload exit 1 = nil error, want error")
	}
	if !strings.Contains(err.Error(), "HTTP Error 403") {
		t.Errorf("error %q 缺少 stderr 诊断信息", err)
	}
}

func TestRealDownloadNoOutputFile(t *testing.T) {
	script := writeFakeYtDlp(t, fakeYtDlpBody(`exit 0`)) // 成功退出但什么都没写
	destBase := filepath.Join(t.TempDir(), "song")
	_, err := realDownload(context.Background(), script, "https://youtube.com/watch?v=abc", destBase, "", nil)
	if err == nil {
		t.Fatal("realDownload 未产出文件 = nil error, want error")
	}
	if !strings.Contains(err.Error(), "未产出文件") {
		t.Errorf("error = %q, want 含未产出文件", err)
	}
}

func TestRealDownloadZeroByteFile(t *testing.T) {
	script := writeFakeYtDlp(t, fakeYtDlpBody(`: > "$out"`)) // 写 0 字节产物
	destBase := filepath.Join(t.TempDir(), "song")
	_, err := realDownload(context.Background(), script, "https://youtube.com/watch?v=abc", destBase, "", nil)
	if err == nil {
		t.Fatal("realDownload 0 字节 = nil error, want error")
	}
	if !strings.Contains(err.Error(), "0 字节") {
		t.Errorf("error = %q, want 含 0 字节", err)
	}
	if _, err := os.Stat(destBase + ".webm"); !os.IsNotExist(err) {
		t.Errorf("0 字节产物残留未被清理: %v", err)
	}
}

func TestRealDownloadPartOnly(t *testing.T) {
	script := writeFakeYtDlp(t, fakeYtDlpBody(`: > "$out.part"`)) // 只产出 .part 临时文件
	destBase := filepath.Join(t.TempDir(), "song")
	_, err := realDownload(context.Background(), script, "https://youtube.com/watch?v=abc", destBase, "", nil)
	if err == nil {
		t.Fatal("realDownload 仅 .part = nil error, want error")
	}
	if !strings.Contains(err.Error(), "未产出文件") {
		t.Errorf("error = %q, want 含未产出文件", err)
	}
}

// 缓存目录名含 glob 元字符（用户配置的 m.dir 如 "cache[x]"）时，失败清理
// 不得依赖 glob（旧实现 `cache[x]/song.*` 静默不匹配 → .part 永久滞留）：
// os.ReadDir + 前缀匹配下 .part 与产物均被清理。
func TestRealDownloadCleansPartWithMetacharDir(t *testing.T) {
	script := writeFakeYtDlp(t, fakeYtDlpBody(`
mkdir -p "$(dirname "$out")"
: > "$out.part"
echo "HTTP Error 403" >&2
exit 1
`))
	destBase := filepath.Join(t.TempDir(), "cache[x]", "song")
	if _, err := realDownload(context.Background(), script, "https://youtube.com/watch?v=abc", destBase, "", nil); err == nil {
		t.Fatal("realDownload exit 1 = nil error, want error")
	}
	if _, err := os.Stat(destBase + ".webm.part"); !os.IsNotExist(err) {
		t.Errorf(".part 残留未被清理: %v", err)
	}
}

// 多匹配（陈旧残留 + 本次新产物）时按 ModTime 取最新，避免陈旧文件胜出。
// 注意：旧实现按字典序取最后一个非 .part 文件——这里让陈旧文件 .webm
// 排在 .m4a 之后，旧实现会选到陈旧文件（红），新实现按 mtime 选 .m4a（绿）。
func TestRealDownloadPicksLatestOnMultipleMatches(t *testing.T) {
	script := writeFakeYtDlp(t, fakeYtDlpBody(`exit 0`)) // 不写文件：预置两个产物模拟残留
	dir := t.TempDir()
	destBase := filepath.Join(dir, "song")
	if err := os.WriteFile(destBase+".webm", validAudioBytes(), 0o644); err != nil { // 陈旧：先写、字典序靠后
		t.Fatal(err)
	}
	time.Sleep(10 * time.Millisecond) // 保证 mtime 有先后
	if err := os.WriteFile(destBase+".m4a", validAudioBytes(), 0o644); err != nil { // 本次产物：后写
		t.Fatal(err)
	}
	file, err := realDownload(context.Background(), script, "https://youtube.com/watch?v=abc", destBase, "", nil)
	if err != nil {
		t.Fatalf("realDownload: %v", err)
	}
	if want := "song.m4a"; file != want {
		t.Errorf("file = %q, want %q（最新 mtime 应胜出）", file, want)
	}
}

func TestRealDownloadCleansPartOnFailure(t *testing.T) {
	script := writeFakeYtDlp(t, fakeYtDlpBody(`
: > "$out.part"
echo "HTTP Error 403" >&2
exit 1
`))
	destBase := filepath.Join(t.TempDir(), "song")
	if _, err := realDownload(context.Background(), script, "https://youtube.com/watch?v=abc", destBase, "", nil); err == nil {
		t.Fatal("realDownload exit 1 = nil error, want error")
	}
	if _, err := os.Stat(destBase + ".part"); !os.IsNotExist(err) {
		t.Errorf(".part 残留未被清理: %v", err)
	}
	if _, err := os.Stat(destBase + ".webm"); !os.IsNotExist(err) {
		t.Errorf("webm 残留未被清理: %v", err)
	}
}

// 配置 cookie + headers 时：args 必须含 --cookies <file> 与按键排序的
// --add-header 对（格式 "Name:"+TrimSpace(value)，value 空跳过），且位于
// -o 之前、url 最后；与真实命令顺序完全一致（逐项 DeepEqual）。
// 缓存目录名含空格：验证参数逐行写入不丢边界（printf '%s\n' "$@"）。
func TestRealDownloadAddsCookieAndHeaders(t *testing.T) {
	script := writeFakeYtDlp(t, argsLogBody(fakeAudioOut))
	base := filepath.Join(t.TempDir(), "cache dir")
	if err := os.MkdirAll(base, 0o755); err != nil {
		t.Fatal(err)
	}
	destBase := filepath.Join(base, "song")
	const url = "https://youtube.com/watch?v=abc"
	cookieFile := filepath.Join(base, "cookies.txt")
	headers := map[string]string{
		"X-Zeta":  "z-value",
		"X-Alpha": "  alpha  ", // 值 TrimSpace 后再拼
		"X-Skip":  "   ",       // TrimSpace 后为空 → 跳过
	}
	if _, err := realDownload(context.Background(), script, url, destBase, cookieFile, headers); err != nil {
		t.Fatalf("realDownload: %v", err)
	}
	want := []string{
		"--no-playlist", "--no-warnings", "-f", "bestaudio",
		"--cookies", cookieFile,
		"--add-header", "X-Alpha:alpha", // 按键排序，值已 TrimSpace
		"--add-header", "X-Zeta:z-value",
		"-o", destBase + ".%(ext)s", url,
	}
	if got := readArgsFile(t, base); !reflect.DeepEqual(got, want) {
		t.Errorf("args = %q\nwant %q", got, want)
	}
}

// 未配置 cookie/headers（空串 + nil）时 args 与旧版完全一致：
// --no-playlist --no-warnings -f bestaudio -o <destBase>.%(ext)s <url>（无回归）。
func TestRealDownloadNoConfigArgsUnchanged(t *testing.T) {
	script := writeFakeYtDlp(t, argsLogBody(fakeAudioOut))
	destBase := filepath.Join(t.TempDir(), "song")
	const url = "https://youtube.com/watch?v=abc"
	if _, err := realDownload(context.Background(), script, url, destBase, "", nil); err != nil {
		t.Fatalf("realDownload: %v", err)
	}
	want := []string{
		"--no-playlist", "--no-warnings", "-f", "bestaudio",
		"-o", destBase + ".%(ext)s", url,
	}
	if got := readArgsFile(t, filepath.Dir(destBase)); !reflect.DeepEqual(got, want) {
		t.Errorf("args = %q\nwant %q", got, want)
	}
}

// 产物内容非音频（yt-dlp 被代理/中间页劫持返回 HTML 错误页，≥MinAudioSize
// 且魔数不匹配）→ realDownload 返回错误并清理 destBase.* 残留；外层重试
// 循环重新跑 yt-dlp 重新提取新 URL（HTML 不注册入库）。
func TestRealDownloadRejectsHtmlOutput(t *testing.T) {
	script := writeFakeYtDlp(t, fakeYtDlpBody(`
printf '<!DOCTYPE html><html>err</html>' > "$out"
head -c 2048 /dev/zero >> "$out"
`))
	destBase := filepath.Join(t.TempDir(), "song")
	_, err := realDownload(context.Background(), script, "https://youtube.com/watch?v=abc", destBase, "", nil)
	if err == nil {
		t.Fatal("realDownload HTML 产物 = nil error, want error")
	}
	if !strings.Contains(err.Error(), "非音频") {
		t.Errorf("error = %q, want 含非音频", err)
	}
	if _, err := os.Stat(destBase + ".webm"); !os.IsNotExist(err) {
		t.Errorf("HTML 产物残留未被清理: %v", err)
	}
}
