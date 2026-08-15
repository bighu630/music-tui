package search

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"sort"
	"strings"
	"time"

	"music-tui/model"
)

const (
	// searchTimeout 是单次搜索的总超时（覆盖 yt-dlp 子进程执行时间）。
	searchTimeout = 10 * time.Second
	// playlistTimeout 是歌单拉取的总超时：歌单条目数远多于单次搜索，单独给 30s。
	playlistTimeout = 30 * time.Second
	// searchLimit 是 ytsearch 返回的最大结果数。
	searchLimit = 20
	// maxStderrTail 是错误分支拼入错误消息的 stderr 诊断文本最大长度。
	maxStderrTail = 512
)

// ytdlpEntry 对应 yt-dlp --dump-json 输出中单条结果的字段。
// -J（--dump-single-json）模式下 entries 数组的元素结构与此一致：
// id/title/channel/duration/url/webpage_url/thumbnails 均同 flat 逐行模式。
type ytdlpEntry struct {
	ID         string  `json:"id"`
	Title      string  `json:"title"`
	Channel    string  `json:"channel"`
	Duration   float64 `json:"duration"`
	URL        string  `json:"url"`
	WebpageURL string  `json:"webpage_url"`
	Thumbnail  string  `json:"thumbnail"`
	// Thumbnails 是 --flat-playlist 模式下的缩略图数组：
	// flat 输出无 singular thumbnail 字段，须用 thumbnails[0].url 兜底。
	Thumbnails []struct {
		URL string `json:"url"`
	} `json:"thumbnails"`
}

// ytdlpPlaylist 对应 yt-dlp --flat-playlist -J 输出的顶层歌单 JSON。
type ytdlpPlaylist struct {
	Type    string       `json:"_type"`
	ID      string       `json:"id"`
	Title   string       `json:"title"`
	Entries []ytdlpEntry `json:"entries"`
}

// YouTubeAdapter 通过 yt-dlp 子进程搜索 YouTube（ytsearch: 前缀）。
// 使用 --flat-playlist 直接读取搜索结果元数据，避免逐个全量提取。
// Task 10 验收项 4 实测：flat 输出缺 singular thumbnail 字段但有
// thumbnails 数组（duration 正常）；全量提取 ytsearch20 实测需 90s，
// 远超 searchTimeout 不可行——故保留 flat，CoverURL 以 thumbnail 优先、
// thumbnails[0].url 兜底（实测为 hqdefault URL）。
type YouTubeAdapter struct {
	ytdlpPath string
	timeout   time.Duration
	plTimeout time.Duration
	limit     int
	// cookieFile/headers 是全局附加的 yt-dlp 参数（SetGlobalYTDlp 设置）：
	// cookieFile 非空 → 每次调用附加 --cookies <file>；headers 非空 →
	// 按键排序附加 --add-header。未设置（零值）时行为与现状完全一致。
	cookieFile string
	headers    map[string]string
}

// NewYouTubeAdapter 创建 YouTube 搜索适配器，limit 固定 searchLimit 条。
func NewYouTubeAdapter(ytdlpPath string) *YouTubeAdapter {
	return &YouTubeAdapter{
		ytdlpPath: ytdlpPath,
		timeout:   searchTimeout,
		plTimeout: playlistTimeout,
		limit:     searchLimit,
	}
}

// SetGlobalYTDlp 设置全局附加的 yt-dlp 参数，对 Search / FetchPlaylist 均生效：
// cookieFile 非空时附加 --cookies <file>（FetchPlaylist 的 CookieArgs 参数优先，
// 参数全空才回落全局）；headers 按键排序附加 --add-header "Name:Value"
// （Value 先 TrimSpace，为空则跳过该条）。cookieFile 为空 / headers 为 nil
// 或空 = 不附加，行为与不设置完全一致。
func (a *YouTubeAdapter) SetGlobalYTDlp(cookieFile string, headers map[string]string) {
	a.cookieFile = cookieFile
	a.headers = headers
}

// headerArgs 返回按键排序的 --add-header 参数序列；值 TrimSpace 后为空的键
// 跳过。无有效 header 时返回 nil（不附加任何参数）。
func (a *YouTubeAdapter) headerArgs() []string {
	if len(a.headers) == 0 {
		return nil
	}
	keys := make([]string, 0, len(a.headers))
	for k := range a.headers {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var args []string
	for _, k := range keys {
		v := strings.TrimSpace(a.headers[k])
		if v == "" {
			continue
		}
		// 统一 "Name:Value" 格式：冒号后无空格
		args = append(args, "--add-header", k+":"+v)
	}
	return args
}

// Search 执行 ytsearch 搜索并解析输出为 []model.Track。
// 超时与 yt-dlp 报错均返回错误：超时（DeadlineExceeded）与父 ctx
// 取消（Canceled）分别给出不同消息；非超时失败携带截断的 stderr
// 诊断文本，便于用户看到 yt-dlp 的真实报错。
// 参数 = 全局附加参数（--cookies + 排序后的 --add-header）+ 原有搜索参数。
func (a *YouTubeAdapter) Search(ctx context.Context, query string) ([]model.Track, error) {
	ctx, cancel := context.WithTimeout(ctx, a.timeout)
	defer cancel()
	arg := fmt.Sprintf("ytsearch%d:%s", a.limit, query)
	args := []string{}
	if a.cookieFile != "" {
		args = append(args, "--cookies", a.cookieFile)
	}
	args = append(args, a.headerArgs()...)
	args = append(args, "--dump-json", "--no-warnings", "--flat-playlist", arg)
	cmd := exec.CommandContext(ctx, a.ytdlpPath, args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		// 先查 ctx.Err()：超时/取消时 Output 返回的 err 可能是
		// *ExitError（非零退出）或 signal: killed，须以 ctx 状态为准。
		if ctx.Err() != nil {
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				return nil, fmt.Errorf("搜索超时（%s）: %w", a.timeout, ctx.Err())
			}
			return nil, fmt.Errorf("搜索已取消: %w", ctx.Err())
		}
		msg := tail(stderr.String(), maxStderrTail)
		if msg == "" {
			msg = "<无输出>"
		}
		return nil, fmt.Errorf("yt-dlp 搜索失败: %w（stderr: %s）", err, msg)
	}
	return parseYTDLPOutput(out)
}

// tail 返回 s 末尾最多 max 字节；用于截取错误诊断文本。
func tail(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[len(s)-max:]
}

// FetchPlaylist 用 yt-dlp --flat-playlist -J 拉取歌单标题与条目元数据。
// cookies 参数可空：File 非空时加 --cookies <file>；否则 FromBrowser 非空时加
// --cookies-from-browser <browser>；两者都空则回落全局 cookieFile（非空时加
// --cookies <file>），全局也未设置则不加 cookie 参数。全局 headers 总是附加。
// 错误分支与 Search 一致：超时（DeadlineExceeded）与父 ctx 取消（Canceled）
// 分别给出不同消息；非超时失败携带截断的 stderr 诊断文本。
func (a *YouTubeAdapter) FetchPlaylist(ctx context.Context, playlistURL string, cookies CookieArgs) (model.Playlist, error) {
	ctx, cancel := context.WithTimeout(ctx, a.plTimeout)
	defer cancel()
	args := []string{"--flat-playlist", "-J", "--no-warnings"}
	switch {
	case cookies.File != "":
		args = append(args, "--cookies", cookies.File)
	case cookies.FromBrowser != "":
		args = append(args, "--cookies-from-browser", cookies.FromBrowser)
	case a.cookieFile != "":
		// 参数全空：回落全局 cookie 文件
		args = append(args, "--cookies", a.cookieFile)
	}
	// 全局 headers 总是附加（与 CookieArgs 是否指定无关）
	args = append(args, a.headerArgs()...)
	// yt-dlp 2026.07+ 对私有歌单默认执行网页 authcheck 验证，失败即报错并建议
	// 跳过；这里显式跳过（youtubetab:skip=authcheck），私有歌单（如 LM）
	// 直接凭 cookie 拉取，公开歌单不受影响。
	args = append(args, "--extractor-args", "youtubetab:skip=authcheck")
	args = append(args, playlistURL)
	cmd := exec.CommandContext(ctx, a.ytdlpPath, args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		// 先查 ctx.Err()：超时/取消时 Output 返回的 err 可能是
		// *ExitError（非零退出）或 signal: killed，须以 ctx 状态为准。
		if ctx.Err() != nil {
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				return model.Playlist{}, fmt.Errorf("歌单拉取超时（%s）: %w", a.plTimeout, ctx.Err())
			}
			return model.Playlist{}, fmt.Errorf("歌单拉取已取消: %w", ctx.Err())
		}
		msg := tail(stderr.String(), maxStderrTail)
		if msg == "" {
			msg = "<无输出>"
		}
		return model.Playlist{}, fmt.Errorf("yt-dlp 歌单拉取失败: %w（stderr: %s）", err, msg)
	}
	return parseYTDLPPlaylist(out)
}

// entryToTrack 将 yt-dlp 单条条目映射为 Track；ID 为空返回 ok=false。
// CoverURL 以 thumbnail 优先、thumbnails[0].url 兜底（flat 模式无 singular
// thumbnail 字段）；URL 以 url 优先、webpage_url 次之，最后用 videoId 构造
// music.youtube.com 地址兜底。
func entryToTrack(e ytdlpEntry) (model.Track, bool) {
	if e.ID == "" {
		return model.Track{}, false
	}
	cover := e.Thumbnail
	if cover == "" && len(e.Thumbnails) > 0 {
		cover = e.Thumbnails[0].URL
	}
	u := e.URL
	if u == "" {
		u = e.WebpageURL
	}
	if u == "" {
		u = "https://music.youtube.com/watch?v=" + e.ID
	}
	return model.Track{
		ID:       e.ID,
		Title:    e.Title,
		Artist:   e.Channel,
		Duration: e.Duration,
		URL:      u,
		Source:   "youtube",
		CoverURL: cover,
	}, true
}

// parseYTDLPOutput 将 yt-dlp 的逐行 JSON 输出解析为歌曲列表；
// 非 JSON 行（警告等）与空 ID 条目跳过；空输出（或无有效行）
// 返回空列表，不是错误。与子进程解耦，可独立单测。
func parseYTDLPOutput(out []byte) ([]model.Track, error) {
	var tracks []model.Track
	sc := bufio.NewScanner(bytes.NewReader(out))
	// 放宽单行上限：单条结果 JSON 可能超过默认 64KB，
	// 避免 ErrTooLong 导致整个解析失败；上限 1MB 仍防止异常巨行吃满内存。
	sc.Buffer(make([]byte, 64*1024), 1<<20)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var e ytdlpEntry
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			continue
		}
		if t, ok := entryToTrack(e); ok {
			tracks = append(tracks, t)
		}
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	return tracks, nil
}

// parseYTDLPPlaylist 解析 yt-dlp --flat-playlist -J 输出的顶层歌单 JSON。
// 顶层非法 JSON（或非 playlist 类型输出，如误传单曲 URL）返回错误；
// 合法歌单 JSON 但无条目返回空 Tracks（不是错误）。与子进程解耦，可独立单测。
func parseYTDLPPlaylist(out []byte) (model.Playlist, error) {
	var pl ytdlpPlaylist
	if err := json.Unmarshal(out, &pl); err != nil {
		return model.Playlist{}, fmt.Errorf("解析歌单 JSON 失败: %w", err)
	}
	if pl.Type != "" && pl.Type != "playlist" {
		return model.Playlist{}, fmt.Errorf("yt-dlp 输出类型 %q，期望 playlist（URL 可能不是歌单链接）", pl.Type)
	}
	p := model.Playlist{ID: pl.ID, Title: pl.Title}
	for _, e := range pl.Entries {
		if t, ok := entryToTrack(e); ok {
			p.Tracks = append(p.Tracks, t)
		}
	}
	return p, nil
}
