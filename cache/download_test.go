package cache

import (
	"context"
	"os"
	"path/filepath"
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

func writeFakeYtDlp(t *testing.T, body string) string {
	t.Helper()
	script := filepath.Join(t.TempDir(), "yt-dlp")
	if err := os.WriteFile(script, []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return script
}

func TestRealDownloadFakeScript(t *testing.T) {
	script := writeFakeYtDlp(t, fakeYtDlpBody(`printf 'fake-audio-bytes' > "$out"`))
	destBase := filepath.Join(t.TempDir(), "song")
	file, err := realDownload(context.Background(), script, "https://youtube.com/watch?v=abc", destBase)
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
	if string(data) != "fake-audio-bytes" {
		t.Errorf("content = %q, want fake-audio-bytes", data)
	}
}

func TestRealDownloadScriptFails(t *testing.T) {
	script := writeFakeYtDlp(t, fakeYtDlpBody(`echo "HTTP Error 403" >&2; exit 1`))
	destBase := filepath.Join(t.TempDir(), "song")
	_, err := realDownload(context.Background(), script, "https://youtube.com/watch?v=abc", destBase)
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
	_, err := realDownload(context.Background(), script, "https://youtube.com/watch?v=abc", destBase)
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
	_, err := realDownload(context.Background(), script, "https://youtube.com/watch?v=abc", destBase)
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
	_, err := realDownload(context.Background(), script, "https://youtube.com/watch?v=abc", destBase)
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
	if _, err := realDownload(context.Background(), script, "https://youtube.com/watch?v=abc", destBase); err == nil {
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
	if err := os.WriteFile(destBase+".webm", []byte("stale"), 0o644); err != nil { // 陈旧：先写、字典序靠后
		t.Fatal(err)
	}
	time.Sleep(10 * time.Millisecond) // 保证 mtime 有先后
	if err := os.WriteFile(destBase+".m4a", []byte("new"), 0o644); err != nil { // 本次产物：后写
		t.Fatal(err)
	}
	file, err := realDownload(context.Background(), script, "https://youtube.com/watch?v=abc", destBase)
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
	if _, err := realDownload(context.Background(), script, "https://youtube.com/watch?v=abc", destBase); err == nil {
		t.Fatal("realDownload exit 1 = nil error, want error")
	}
	if _, err := os.Stat(destBase + ".part"); !os.IsNotExist(err) {
		t.Errorf(".part 残留未被清理: %v", err)
	}
	if _, err := os.Stat(destBase + ".webm"); !os.IsNotExist(err) {
		t.Errorf("webm 残留未被清理: %v", err)
	}
}
