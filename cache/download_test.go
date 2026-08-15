package cache

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
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
