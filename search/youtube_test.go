package search

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"music-tui/model"
)

// fakeYTDLPOutput 模拟 yt-dlp --dump-json 的逐行 JSON 输出（每行一条结果）。
const fakeYTDLPOutput = `{"id":"abc123","title":"晴天","channel":"周杰倫","duration":269.0,"webpage_url":"https://www.youtube.com/watch?v=abc123","thumbnail":"https://i.ytimg.com/vi/abc123/maxresdefault.jpg"}
{"id":"def456","title":"七里香","channel":"周杰倫","duration":311.0,"webpage_url":"https://www.youtube.com/watch?v=def456","thumbnail":"https://i.ytimg.com/vi/def456/hqdefault.jpg"}`

func TestParseYTDLPOutput(t *testing.T) {
	tracks, err := parseYTDLPOutput([]byte(fakeYTDLPOutput))
	if err != nil {
		t.Fatal(err)
	}
	if len(tracks) != 2 {
		t.Fatalf("len = %d, want 2", len(tracks))
	}
	want := model.Track{
		ID:       "abc123",
		Title:    "晴天",
		Artist:   "周杰倫",
		Duration: 269.0,
		URL:      "https://www.youtube.com/watch?v=abc123",
		Source:   "youtube",
		CoverURL: "https://i.ytimg.com/vi/abc123/maxresdefault.jpg",
	}
	if tracks[0] != want {
		t.Errorf("tracks[0] = %+v, want %+v", tracks[0], want)
	}
	if tracks[1].ID != "def456" || tracks[1].Source != "youtube" {
		t.Errorf("tracks[1] = %+v", tracks[1])
	}
}

func TestParseYTDLPOutputSkipsGarbageLines(t *testing.T) {
	out := "WARNING: [youtube] something\n" + fakeYTDLPOutput + "\nnot json at all\n"
	tracks, err := parseYTDLPOutput([]byte(out))
	if err != nil {
		t.Fatal(err)
	}
	if len(tracks) != 2 {
		t.Fatalf("len = %d, want 2（非 JSON 行应跳过）", len(tracks))
	}
}

func TestParseYTDLPOutputEmpty(t *testing.T) {
	tracks, err := parseYTDLPOutput(nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(tracks) != 0 {
		t.Errorf("len = %d, want 0", len(tracks))
	}
}

func TestParseYTDLPOutputSkipsEmptyID(t *testing.T) {
	out := `{"id":"","title":"无ID"}`
	tracks, err := parseYTDLPOutput([]byte(out))
	if err != nil {
		t.Fatal(err)
	}
	if len(tracks) != 0 {
		t.Errorf("len = %d, want 0（空 ID 条目应跳过）", len(tracks))
	}
}

func TestSearchBinaryMissing(t *testing.T) {
	a := NewYouTubeAdapter("/nonexistent/yt-dlp")
	if _, err := a.Search(context.Background(), "test"); err == nil {
		t.Error("yt-dlp 不存在时应报错")
	}
}

// fakeYTDLPFlatOutput 模拟 yt-dlp --flat-playlist 的实际输出：
// 无 singular thumbnail 字段，仅 thumbnails 数组（实测含 hqdefault URL）。
const fakeYTDLPFlatOutput = `{"id":"ghi789","title":"稻香","channel":"周杰倫","duration":223.0,"webpage_url":"https://www.youtube.com/watch?v=ghi789","thumbnails":[{"url":"https://i.ytimg.com/vi/ghi789/hqdefault.jpg?sqp=xyz","id":"hqdefault"}]}`

func TestParseYTDLPOutputFlatThumbnailsFallback(t *testing.T) {
	tracks, err := parseYTDLPOutput([]byte(fakeYTDLPFlatOutput))
	if err != nil {
		t.Fatal(err)
	}
	if len(tracks) != 1 {
		t.Fatalf("len = %d, want 1", len(tracks))
	}
	want := "https://i.ytimg.com/vi/ghi789/hqdefault.jpg?sqp=xyz"
	if tracks[0].CoverURL != want {
		t.Errorf("CoverURL = %q, want %q（flat 模式无 thumbnail 字段时应取 thumbnails[0].url）", tracks[0].CoverURL, want)
	}
	if tracks[0].ID != "ghi789" || tracks[0].Duration != 223.0 {
		t.Errorf("tracks[0] = %+v", tracks[0])
	}
}

func TestParseYTDLPOutputSkipsLongGarbageLine(t *testing.T) {
	// 单行 100KB，超过 bufio.Scanner 默认 64KB token 上限；
	// 应被跳过且不破坏后续解析。
	longLine := strings.Repeat("x", 100*1024)
	out := longLine + "\n" + fakeYTDLPOutput
	tracks, err := parseYTDLPOutput([]byte(out))
	if err != nil {
		t.Fatal(err)
	}
	if len(tracks) != 2 {
		t.Fatalf("len = %d, want 2（超长行应被跳过）", len(tracks))
	}
}

func TestSearchTimeout(t *testing.T) {
	a := NewYouTubeAdapter(writeScript(t, "#!/bin/sh\nwhile :; do :; done\n"))
	a.timeout = 200 * time.Millisecond
	start := time.Now()
	_, err := a.Search(context.Background(), "q")
	if err == nil {
		t.Fatal("超时应报错")
	} else if !strings.Contains(err.Error(), "搜索超时") {
		t.Errorf("err = %v, want 超时消息", err)
	}
	if time.Since(start) > 5*time.Second {
		t.Errorf("超时未及时生效: %v", time.Since(start))
	}
}

func TestSearchCanceled(t *testing.T) {
	a := NewYouTubeAdapter(writeScript(t, "#!/bin/sh\nwhile :; do :; done\n"))
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // 立即取消，非超时
	_, err := a.Search(ctx, "q")
	if err == nil {
		t.Fatal("取消后应报错")
	} else if !strings.Contains(err.Error(), "已取消") {
		t.Errorf("err = %v, want 已取消消息（而非超时）", err)
	}
}

func TestSearchErrorIncludesStderr(t *testing.T) {
	a := NewYouTubeAdapter(writeScript(t, "#!/bin/sh\necho 'ERROR: [youtube] Unable to download API page' >&2\nexit 1\n"))
	_, err := a.Search(context.Background(), "q")
	if err == nil {
		t.Fatal("yt-dlp 失败应报错")
	} else if !strings.Contains(err.Error(), "Unable to download API page") {
		t.Errorf("err = %v, want 包含 stderr 诊断", err)
	}
}

// writeScript 在临时目录写一个可执行 shell 脚本并返回路径。
func writeScript(t *testing.T, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "fake-ytdlp.sh")
	if err := os.WriteFile(p, []byte(content), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}
