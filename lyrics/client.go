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
		ly, err := c.fetchOne(ctx, track, cand, i == 0)
		if err == nil {
			return ly, nil
		}
		if !errors.Is(err, ErrNotFound) {
			return nil, err
		}
	}
	return nil, ErrNotFound
}

// fetchOne 对单个候选标题查询：withArtist=true（仅候选 0）时先 /api/get
// 精确命中，404 或歌词为空时降级 /api/search 并选择时长最接近的匹配；
// withArtist=false 时跳过 get 直接 search（不带 artist 参数）。
// 未命中返回 ErrNotFound，由 Fetch 决定是否尝试下一候选。
func (c *Client) fetchOne(ctx context.Context, track model.Track, cand string, withArtist bool) (*Lyrics, error) {
	base := strings.TrimSuffix(c.baseURL, "/")
	if withArtist {
		var song lrclibSong
		q := url.Values{}
		q.Set("track_name", cand)
		q.Set("artist_name", track.Artist)
		q.Set("duration", fmt.Sprintf("%.2f", track.Duration))
		u := base + "/api/get?" + q.Encode()
		err := c.do(ctx, u, &song)
		if err == nil {
			if ly := songToLyrics(song); ly != nil {
				return ly, nil
			}
			err = ErrNotFound // 200 但歌词为空：视同未命中，降级 search
		}
		if !errors.Is(err, ErrNotFound) {
			return nil, err
		}
	}

	var songs []lrclibSong
	q2 := url.Values{}
	q2.Set("track_name", cand)
	if withArtist {
		q2.Set("artist_name", track.Artist)
	}
	u2 := base + "/api/search?" + q2.Encode()
	if err := c.do(ctx, u2, &songs); err != nil {
		return nil, err
	}
	if ly := chooseBest(songs, track); ly != nil {
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

// retryAfter 解析 Retry-After 响应头（秒数或 HTTP 日期），缺省 1 秒。
func retryAfter(resp *http.Response) time.Duration {
	v := resp.Header.Get("Retry-After")
	if v == "" {
		return time.Second
	}
	if secs, err := strconv.Atoi(v); err == nil {
		return time.Duration(secs) * time.Second
	}
	if t, err := http.ParseTime(v); err == nil {
		return time.Until(t)
	}
	return time.Second
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

// chooseBest 在搜索结果中选最佳：跳过纯器乐，选时长与目标最接近的条目。
func chooseBest(songs []lrclibSong, track model.Track) *Lyrics {
	var best *lrclibSong
	bestDelta := math.MaxFloat64
	for i := range songs {
		s := &songs[i]
		if s.Instrumental {
			continue
		}
		delta := math.Abs(s.Duration - track.Duration)
		if delta < bestDelta {
			best, bestDelta = s, delta
		}
	}
	if best == nil {
		return nil
	}
	return songToLyrics(*best)
}
