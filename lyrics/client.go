package lyrics

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"music-tui/model"
)

const lrclibBaseURL = "https://lrclib.net"

// ErrNotFound 表示 lrclib 中找不到对应歌词。
var ErrNotFound = errors.New("lyrics not found")

// Fetcher 是歌词获取服务抽象：*Client（确定性匹配）与 *EnhancedClient
// （AI 增强）均实现；ui/main 层按配置选择具体实现。
type Fetcher interface {
	Fetch(ctx context.Context, track model.Track) (*Lyrics, error)
}

// Client 是 lrclib 的 HTTP 客户端。
type Client struct {
	baseURL   string
	client    *http.Client
	userAgent string
}

// NewClient 创建 lrclib 客户端。userAgent 需符合 lrclib 要求的
// "应用名 版本 (主页/邮箱)" 格式。
func NewClient(userAgent string) *Client {
	return NewClientWithBaseURL(lrclibBaseURL, userAgent)
}

// NewClientWithBaseURL 创建指向自定义基地址的客户端，用于测试或自托管 lrclib。
func NewClientWithBaseURL(baseURL, userAgent string) *Client {
	return &Client{
		baseURL:   baseURL,
		client:    &http.Client{Timeout: 10 * time.Second},
		userAgent: userAgent,
	}
}

// lrclibSong 对应 lrclib /api/get 与 /api/search 返回的歌曲对象。
type lrclibSong struct {
	TrackName    string  `json:"trackName"`
	ArtistName   string  `json:"artistName"`
	AlbumName    string  `json:"albumName"`
	Duration     float64 `json:"duration"`
	Instrumental bool    `json:"instrumental"`
	PlainLyrics  string  `json:"plainLyrics"`
	SyncedLyrics string  `json:"syncedLyrics"`
}

// Fetch 获取歌曲歌词：按 cleanCandidates 生成的候选标题依次查询，命中即停。
// 候选 0 为原始标题，带 artist 走 /api/get（lrclib 内部按 ±2s 精确匹配）
// → 404 或空歌词降级 /api/search；派生候选（去噪/切分/CJK 词元）不带
// artist 直接走 /api/search（lrclib 对 track_name 精确匹配，噪声词会致
// 0 结果）。未找到歌词返回 ErrNotFound；网络或服务端错误原样返回。
func (c *Client) Fetch(ctx context.Context, track model.Track) (*Lyrics, error) {
	for i, cand := range cleanCandidates(track.Title) {
		// 派生候选恰为歌手名（如 CJK 词元"周杰倫"）是最常见的浪费请求，跳过。
		if i > 0 && strings.EqualFold(cand, track.Artist) {
			continue
		}
		ly, err := c.fetchOne(ctx, track, cand, i == 0, maxDurationDelta)
		if err == nil {
			return ly, nil
		}
		if !errors.Is(err, ErrNotFound) {
			return nil, err
		}
	}
	return nil, ErrNotFound
}

// FetchForQuery 用外部清洗后的 title/artist 重查 lrclib（AI 增强路径）：
// /api/get 优先（lrclib 服务端 ±2s 匹配，天然满足严格阈值），降级
// /api/search 并以 maxAIDurationDelta 严格筛选（差距最小者优先，
// 全部超限视为无歌词）。artist 为空时跳过 get 直接 search。
func (c *Client) FetchForQuery(ctx context.Context, title, artist string, duration float64) (*Lyrics, error) {
	track := model.Track{Title: title, Artist: artist, Duration: duration}
	return c.fetchOne(ctx, track, title, artist != "", maxAIDurationDelta)
}

// fetchOne 对单个候选标题查询：withArtist=true 时先 /api/get
// 精确命中，404 或歌词为空时降级 /api/search 并选择时长最接近的匹配
// （阈值 maxDelta）；withArtist=false 时跳过 get 直接 search（不带
// artist 参数）。未命中返回 ErrNotFound，由 Fetch 决定是否尝试下一候选。
func (c *Client) fetchOne(ctx context.Context, track model.Track, cand string, withArtist bool, maxDelta float64) (*Lyrics, error) {
	base := strings.TrimSuffix(c.baseURL, "/")
	if withArtist && track.Artist != "" {
		// /api/get 必须带 artist_name，否则 lrclib 返回 400 中断整条链；
		// artist 为空时跳过 get 直接走 search。
		var song lrclibSong
		q := url.Values{}
		q.Set("track_name", cand)
		q.Set("artist_name", track.Artist)
		q.Set("duration", fmt.Sprintf("%.2f", track.Duration))
		u := base + "/api/get?" + q.Encode()
		err := c.do(ctx, u, &song)
		if err == nil {
			// get 命中同样受 maxDelta 约束（确定性路径 30s 与 lrclib ±2s
			// 契约兼容；AI 路径 3s 防御非标准服务端不遵守契约的情况）。
			if ly := songToLyrics(song); ly != nil && math.Abs(song.Duration-track.Duration) <= maxDelta {
				return ly, nil
			}
			err = ErrNotFound // 200 但歌词为空或时长超限：视同未命中，降级 search
		}
		if !errors.Is(err, ErrNotFound) {
			return nil, err
		}
	}

	var songs []lrclibSong
	q2 := url.Values{}
	q2.Set("track_name", cand)
	if withArtist && track.Artist != "" {
		q2.Set("artist_name", track.Artist)
	}
	u2 := base + "/api/search?" + q2.Encode()
	if err := c.do(ctx, u2, &songs); err != nil {
		return nil, err
	}
	if ly := chooseBestWithin(songs, track, maxDelta); ly != nil {
		return ly, nil
	}
	return nil, ErrNotFound
}

// do 发起 GET 并解码 JSON 到 out；404 → ErrNotFound；
// 429 按 Retry-After 等待后重试一次（最多两次请求）。
func (c *Client) do(ctx context.Context, u string, out interface{}) error {
	for attempt := 0; attempt < 2; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		if err != nil {
			return err
		}
		req.Header.Set("User-Agent", c.userAgent)
		resp, err := c.client.Do(req)
		if err != nil {
			return err
		}
		switch resp.StatusCode {
		case http.StatusOK:
			err := json.NewDecoder(resp.Body).Decode(out)
			resp.Body.Close()
			if err != nil {
				return fmt.Errorf("解析 lrclib 响应: %w", err)
			}
			return nil
		case http.StatusNotFound:
			resp.Body.Close()
			return ErrNotFound
		case http.StatusTooManyRequests:
			wait := retryAfter(resp)
			resp.Body.Close()
			timer := time.NewTimer(wait)
			select {
			case <-timer.C:
				continue
			case <-ctx.Done():
				timer.Stop()
				return ctx.Err()
			}
		default:
			status := resp.Status
			resp.Body.Close()
			return fmt.Errorf("lrclib 请求失败: %s", status)
		}
	}
	return fmt.Errorf("lrclib 限流，重试后仍失败")
}

// retryAfter 解析 Retry-After 响应头（秒数或 HTTP 日期），缺省 1 秒，
// 结果 clamp 到 [0, maxRetryWait]：候选链请求翻倍后防止单候选长时间
// 拖死整条链。
func retryAfter(resp *http.Response) time.Duration {
	v := resp.Header.Get("Retry-After")
	if v == "" {
		return time.Second
	}
	if secs, err := strconv.Atoi(v); err == nil {
		return clampWait(time.Duration(secs) * time.Second)
	}
	if t, err := http.ParseTime(v); err == nil {
		return clampWait(time.Until(t))
	}
	return time.Second
}

// maxRetryWait 单次 429 等待上限（5s）。
const maxRetryWait = 5 * time.Second

// clampWait 将等待时长限制在 [0, maxRetryWait]。
func clampWait(d time.Duration) time.Duration {
	if d < 0 {
		return 0
	}
	if d > maxRetryWait {
		return maxRetryWait
	}
	return d
}

// songToLyrics 将 lrclib 歌曲对象转为 Lyrics：同步歌词优先，退回纯文本；
// 两者皆空返回 nil。
func songToLyrics(s lrclibSong) *Lyrics {
	if s.SyncedLyrics != "" {
		if ly, err := ParseLRC([]byte(s.SyncedLyrics)); err == nil && len(ly.Lines) > 0 {
			return ly
		}
	}
	if s.PlainLyrics != "" {
		return &Lyrics{Plain: s.PlainLyrics}
	}
	return nil
}

// maxDurationDelta 确定性路径时长匹配阈值（秒）：偏差超过即视为不同
// 曲目，排除。音频/MV 片头偏移通常 ≤30s（实测晴天 MV 319s vs lrclib
// 300s，Δ=19s 属同曲应命中）；现场版/错歌偏差通常 >60s，应排除。
const maxDurationDelta = 30.0

// maxAIDurationDelta AI 增强路径时长匹配阈值（秒，用户明确要求）：
// AI 清洗后的查询结果必须与目标时长差距 ≤3s 才采用——AI 结果本身
// 可能张冠李戴（同名翻唱/现场版），时长是最可靠的判别信号，
// 差距过大宁可不显示歌词。
const maxAIDurationDelta = 3.0

// chooseBest 在搜索结果中选最佳：跳过纯器乐与时长偏差超过阈值的条目，
// 选时长与目标最接近的（确定性路径阈值 30s）。
func chooseBest(songs []lrclibSong, track model.Track) *Lyrics {
	return chooseBestWithin(songs, track, maxDurationDelta)
}

// chooseBestWithin 以给定阈值选最佳匹配：跳过纯器乐与时长偏差超过
// maxDelta 的条目，选时长与目标最接近的。
func chooseBestWithin(songs []lrclibSong, track model.Track, maxDelta float64) *Lyrics {
	var best *lrclibSong
	bestDelta := math.MaxFloat64
	for i := range songs {
		s := &songs[i]
		if s.Instrumental {
			continue
		}
		delta := math.Abs(s.Duration - track.Duration)
		if delta > maxDelta {
			continue
		}
		if delta < bestDelta {
			best, bestDelta = s, delta
		}
	}
	if best == nil {
		return nil
	}
	return songToLyrics(*best)
}
