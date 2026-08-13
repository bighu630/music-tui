package search

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"music-tui/model"
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

// YouTubeAdapter 通过 yt-dlp 子进程搜索 YouTube（ytsearch: 前缀），
// 使用 --flat-playlist 直接读取搜索结果元数据，避免逐个全量提取。
type YouTubeAdapter struct {
	ytdlpPath string
	timeout   time.Duration
	limit     int
}

// NewYouTubeAdapter 创建 YouTube 搜索适配器，limit 固定 20 条。
func NewYouTubeAdapter(ytdlpPath string) *YouTubeAdapter {
	return &YouTubeAdapter{
		ytdlpPath: ytdlpPath,
		timeout:   10 * time.Second,
		limit:     20,
	}
}

// Search 执行 ytsearch 搜索并解析输出为 []model.Track。
// 超时（10s）与 yt-dlp 报错均返回错误。
func (a *YouTubeAdapter) Search(ctx context.Context, query string) ([]model.Track, error) {
	ctx, cancel := context.WithTimeout(ctx, a.timeout)
	defer cancel()
	arg := fmt.Sprintf("ytsearch%d:%s", a.limit, query)
	cmd := exec.CommandContext(ctx, a.ytdlpPath,
		"--dump-json", "--no-warnings", "--flat-playlist", arg,
	)
	out, err := cmd.Output()
	if err != nil {
		if ctx.Err() != nil {
			return nil, fmt.Errorf("搜索超时（%s）: %w", a.timeout, ctx.Err())
		}
		return nil, fmt.Errorf("yt-dlp 搜索失败: %w", err)
	}
	return parseYTDLPOutput(out)
}

// parseYTDLPOutput 将 yt-dlp 的逐行 JSON 输出解析为歌曲列表；
// 非 JSON 行（警告等）与空 ID 条目跳过。与子进程解耦，可独立单测。
func parseYTDLPOutput(out []byte) ([]model.Track, error) {
	var tracks []model.Track
	sc := bufio.NewScanner(bytes.NewReader(out))
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
