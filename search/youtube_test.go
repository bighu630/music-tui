package search

import (
	"context"
	"fmt"
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

// ---- FetchPlaylist ----

// fakePlaylistJSON 模拟 yt-dlp --flat-playlist -J 输出的顶层歌单 JSON：
// 条目含 url（已是 music.youtube.com/watch?v=...）与 thumbnails 数组；
// 第二条无 thumbnails（flat 模式下字段可缺省）。
const fakePlaylistJSON = `{"_type":"playlist","id":"PL123","title":"我的最爱","entries":[{"id":"vid1","title":"晴天","channel":"周杰倫","duration":269.0,"url":"https://music.youtube.com/watch?v=vid1","thumbnails":[{"url":"https://i.ytimg.com/vi/vid1/hqdefault.jpg"}]},{"id":"vid2","title":"七里香","channel":"周杰倫","duration":311.0,"url":"https://music.youtube.com/watch?v=vid2"}]}`

func TestParseYTDLPPlaylist(t *testing.T) {
	pl, err := parseYTDLPPlaylist([]byte(fakePlaylistJSON))
	if err != nil {
		t.Fatal(err)
	}
	if pl.ID != "PL123" || pl.Title != "我的最爱" {
		t.Errorf("pl.ID/Title = %q/%q, want PL123/我的最爱", pl.ID, pl.Title)
	}
	if len(pl.Tracks) != 2 {
		t.Fatalf("len(tracks) = %d, want 2", len(pl.Tracks))
	}
	want := model.Track{
		ID:       "vid1",
		Title:    "晴天",
		Artist:   "周杰倫",
		Duration: 269.0,
		URL:      "https://music.youtube.com/watch?v=vid1",
		Source:   "youtube",
		CoverURL: "https://i.ytimg.com/vi/vid1/hqdefault.jpg",
	}
	if pl.Tracks[0] != want {
		t.Errorf("tracks[0] = %+v, want %+v（title/artist/duration/url/thumbnails[0] 映射）", pl.Tracks[0], want)
	}
	if pl.Tracks[1].ID != "vid2" || pl.Tracks[1].CoverURL != "" {
		t.Errorf("tracks[1] = %+v（无 thumbnails 时 CoverURL 应为空）", pl.Tracks[1])
	}
}

func TestParseYTDLPPlaylistEmptyEntries(t *testing.T) {
	pl, err := parseYTDLPPlaylist([]byte(`{"_type":"playlist","id":"PL0","title":"空歌单","entries":[]}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(pl.Tracks) != 0 {
		t.Errorf("len(tracks) = %d, want 0（空歌单不是错误）", len(pl.Tracks))
	}
}

func TestParseYTDLPPlaylistInvalidJSON(t *testing.T) {
	if _, err := parseYTDLPPlaylist([]byte("not json at all")); err == nil {
		t.Error("非法顶层 JSON 应报错")
	}
	if _, err := parseYTDLPPlaylist([]byte(`{"_type":"video","id":"v1","title":"单曲"}`)); err == nil {
		t.Error("非 playlist 类型输出应报错")
	}
}

func TestParseYTDLPPlaylistURLFallback(t *testing.T) {
	out := `{"_type":"playlist","id":"PL9","title":"T","entries":[{"id":"nourl","title":"无URL","channel":"某频道","duration":1.0}]}`
	pl, err := parseYTDLPPlaylist([]byte(out))
	if err != nil {
		t.Fatal(err)
	}
	if len(pl.Tracks) != 1 {
		t.Fatalf("len(tracks) = %d, want 1", len(pl.Tracks))
	}
	want := "https://music.youtube.com/watch?v=nourl"
	if pl.Tracks[0].URL != want {
		t.Errorf("URL = %q, want %q（url 为空时用 videoId 兜底）", pl.Tracks[0].URL, want)
	}
}

func TestParseYTDLPPlaylistSkipsEmptyID(t *testing.T) {
	out := `{"_type":"playlist","id":"P","title":"T","entries":[{"id":"","title":"无ID"},{"id":"ok1","title":"正常"}]}`
	pl, err := parseYTDLPPlaylist([]byte(out))
	if err != nil {
		t.Fatal(err)
	}
	if len(pl.Tracks) != 1 || pl.Tracks[0].ID != "ok1" {
		t.Errorf("len = %d, tracks[0].ID = %q, want 仅保留 ok1（空 ID 条目跳过）", len(pl.Tracks), pl.Tracks[0].ID)
	}
}

// argsCaptureScript 生成假 yt-dlp：把收到的参数写入 argsFile，然后输出空歌单 JSON。
func argsCaptureScript(t *testing.T, argsFile string) string {
	t.Helper()
	return writeScript(t, fmt.Sprintf(
		"#!/bin/sh\necho \"$@\" > %q\nprintf '{\"_type\":\"playlist\",\"id\":\"PL1\",\"title\":\"T\",\"entries\":[]}'\n",
		argsFile))
}

func TestFetchPlaylistCookieArgs(t *testing.T) {
	cases := []struct {
		name    string
		cookies CookieArgs
		expect  []string // 期望出现在 args 中的片段
		not     []string // 不应出现在 args 中的片段
	}{
		{"file", CookieArgs{File: "/tmp/c.txt"}, []string{"--cookies", "/tmp/c.txt"}, []string{"--cookies-from-browser"}},
		{"from-browser", CookieArgs{FromBrowser: "chrome"}, []string{"--cookies-from-browser", "chrome"}, []string{"--cookies "}},
		{"file-preferred", CookieArgs{FromBrowser: "chrome", File: "/tmp/c.txt"}, []string{"--cookies", "/tmp/c.txt"}, []string{"--cookies-from-browser"}},
		{"none", CookieArgs{}, nil, []string{"--cookies"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			argsFile := filepath.Join(t.TempDir(), "args.txt")
			a := NewYouTubeAdapter(argsCaptureScript(t, argsFile))
			const url = "https://music.youtube.com/playlist?list=PL1"
			if _, err := a.FetchPlaylist(context.Background(), url, tc.cookies); err != nil {
				t.Fatal(err)
			}
			got, err := os.ReadFile(argsFile)
			if err != nil {
				t.Fatal(err)
			}
			s := strings.TrimSpace(string(got))
			if !strings.HasPrefix(s, "--flat-playlist -J --no-warnings ") {
				t.Errorf("args = %q, want 以 --flat-playlist -J --no-warnings 开头", s)
			}
			if !strings.HasSuffix(s, url) {
				t.Errorf("args = %q, want 以歌单 URL 结尾", s)
			}
			for _, want := range tc.expect {
				if !strings.Contains(s, want) {
					t.Errorf("args = %q, want 包含 %q", s, want)
				}
			}
			for _, n := range tc.not {
				if strings.Contains(s, n) {
					t.Errorf("args = %q, 不应包含 %q", s, n)
				}
			}
		})
	}
}

func TestFetchPlaylistSubprocessOutput(t *testing.T) {
	jsonFile := filepath.Join(t.TempDir(), "pl.json")
	if err := os.WriteFile(jsonFile, []byte(fakePlaylistJSON), 0o644); err != nil {
		t.Fatal(err)
	}
	a := NewYouTubeAdapter(writeScript(t, fmt.Sprintf("#!/bin/sh\ncat %q\n", jsonFile)))
	pl, err := a.FetchPlaylist(context.Background(), "https://music.youtube.com/playlist?list=PL123", CookieArgs{})
	if err != nil {
		t.Fatal(err)
	}
	if pl.Title != "我的最爱" || len(pl.Tracks) != 2 {
		t.Errorf("pl = %+v（子进程输出解析）", pl)
	}
}

func TestFetchPlaylistTimeout(t *testing.T) {
	a := NewYouTubeAdapter(writeScript(t, "#!/bin/sh\nwhile :; do :; done\n"))
	a.plTimeout = 200 * time.Millisecond
	start := time.Now()
	_, err := a.FetchPlaylist(context.Background(), "https://music.youtube.com/playlist?list=PL1", CookieArgs{})
	if err == nil {
		t.Fatal("超时应报错")
	} else if !strings.Contains(err.Error(), "歌单拉取超时") {
		t.Errorf("err = %v, want 超时消息", err)
	}
	if time.Since(start) > 5*time.Second {
		t.Errorf("超时未及时生效: %v", time.Since(start))
	}
}

func TestFetchPlaylistCanceled(t *testing.T) {
	a := NewYouTubeAdapter(writeScript(t, "#!/bin/sh\nwhile :; do :; done\n"))
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // 立即取消，非超时
	_, err := a.FetchPlaylist(ctx, "https://music.youtube.com/playlist?list=PL1", CookieArgs{})
	if err == nil {
		t.Fatal("取消后应报错")
	} else if !strings.Contains(err.Error(), "已取消") {
		t.Errorf("err = %v, want 已取消消息（而非超时）", err)
	}
}

func TestFetchPlaylistErrorIncludesStderr(t *testing.T) {
	a := NewYouTubeAdapter(writeScript(t, "#!/bin/sh\necho 'ERROR: [youtube] Playlist unavailable' >&2\nexit 1\n"))
	_, err := a.FetchPlaylist(context.Background(), "https://music.youtube.com/playlist?list=PL1", CookieArgs{})
	if err == nil {
		t.Fatal("yt-dlp 失败应报错")
	} else if !strings.Contains(err.Error(), "Playlist unavailable") {
		t.Errorf("err = %v, want 包含 stderr 诊断", err)
	}
}
