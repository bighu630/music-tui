package search

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"music-tui/model"
)

const (
	// searchTimeout 是单次搜索的总超时（覆盖 yt-dlp 子进程执行时间）。
	searchTimeout = 10 * time.Second
	// searchLimit 是 ytsearch 返回的最大结果数。
	searchLimit = 20
	// maxStderrTail 是错误分支拼入错误消息的 stderr 诊断文本最大长度。
	maxStderrTail = 512
)

// ytdlpEntry 对应 yt-dlp --dump-json 输出中单条结果的字段。
type ytdlpEntry struct {
	ID         string  `json:"id"`
	Title      string  `json:"title"`
	Channel    string  `json:"channel"`
	Duration   float64 `json:"duration"`
	WebpageURL string  `json:"webpage_url"`
	Thumbnail  string  `json:"thumbnail"`
}

// YouTubeAdapter 通过 yt-dlp 子进程搜索 YouTube（ytsearch: 前缀）。
// 使用 --flat-playlist 直接读取搜索结果元数据，避免逐个全量提取；
// 代价是 duration/thumbnail 等字段可能缺失——Task 10 验收项 4 实测
// 确认，若缺失则去掉该参数改全量提取（变慢）。
type YouTubeAdapter struct {
	ytdlpPath string
	timeout   time.Duration
	limit     int
}

// NewYouTubeAdapter 创建 YouTube 搜索适配器，limit 固定 searchLimit 条。
func NewYouTubeAdapter(ytdlpPath string) *YouTubeAdapter {
	return &YouTubeAdapter{
		ytdlpPath: ytdlpPath,
		timeout:   searchTimeout,
		limit:     searchLimit,
	}
}

// Search 执行 ytsearch 搜索并解析输出为 []model.Track。
// 超时与 yt-dlp 报错均返回错误：超时（DeadlineExceeded）与父 ctx
// 取消（Canceled）分别给出不同消息；非超时失败携带截断的 stderr
// 诊断文本，便于用户看到 yt-dlp 的真实报错。
func (a *YouTubeAdapter) Search(ctx context.Context, query string) ([]model.Track, error) {
	ctx, cancel := context.WithTimeout(ctx, a.timeout)
	defer cancel()
	arg := fmt.Sprintf("ytsearch%d:%s", a.limit, query)
	cmd := exec.CommandContext(ctx, a.ytdlpPath,
		"--dump-json", "--no-warnings", "--flat-playlist", arg,
	)
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
		if e.ID == "" {
			continue
		}
		tracks = append(tracks, model.Track{
			ID:       e.ID,
			Title:    e.Title,
			Artist:   e.Channel,
			Duration: e.Duration,
			URL:      e.WebpageURL,
			Source:   "youtube",
			CoverURL: e.Thumbnail,
		})
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	return tracks, nil
}
