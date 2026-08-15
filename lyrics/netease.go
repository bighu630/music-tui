package lyrics

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// cnSong 中文歌词源的搜索结果条目。
type cnSong struct {
	ID       string  // 源内 ID（网易云 song id / QQ songmid），取歌词用
	Title    string  // 歌名
	Artist   string  // 歌手
	Duration float64 // 时长（秒）；未知 = 0
}

// cnLyricSource 中文歌词源抽象（网易云/QQ 音乐），匿名接口无需登录。
type cnLyricSource interface {
	Search(ctx context.Context, title, artist string) ([]cnSong, error)
	Lyric(ctx context.Context, id string) (*Lyrics, error)
}

// cnUA 网易云/QQ 音乐接口要求的浏览器 UA。
const cnUA = "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36"

// ── 网易云音乐 ────────────────────────────────────────────────────

// neteaseBaseURL 网易云官方接口基地址。
const neteaseBaseURL = "https://music.163.com"

// NeteaseClient 是网易云歌词客户端（匿名接口）：搜索 + 歌词均无需登录
// （实测 music.163.com/api/song/lyric 匿名可用；社区 LrcAPI 同结论）。
type NeteaseClient struct {
	baseURL string
	client  *http.Client
}

// NewNeteaseClient 创建网易云客户端（官方基地址）。
func NewNeteaseClient() *NeteaseClient {
	return NewNeteaseClientWithBaseURL(neteaseBaseURL)
}

// NewNeteaseClientWithBaseURL 创建指向自定义基地址的客户端（测试用）。
func NewNeteaseClientWithBaseURL(baseURL string) *NeteaseClient {
	return &NeteaseClient{
		baseURL: strings.TrimSuffix(baseURL, "/"),
		client:  &http.Client{Timeout: 10 * time.Second},
	}
}

// neteaseSearchResp 网易云搜索响应结构。
type neteaseSearchResp struct {
	Code   int `json:"code"`
	Result struct {
		Songs []struct {
			ID      int64  `json:"id"`
			Name    string `json:"name"`
			Artists []struct {
				Name string `json:"name"`
			} `json:"artists"`
			Duration int64 `json:"duration"` // 毫秒
		} `json:"songs"`
	} `json:"result"`
}

// Search 搜索歌曲（关键词 = title + artist，AI 清洗后精确匹配）。
func (c *NeteaseClient) Search(ctx context.Context, title, artist string) ([]cnSong, error) {
	q := url.Values{}
	q.Set("s", strings.TrimSpace(title+" "+artist))
	q.Set("type", "1")
	q.Set("limit", "5")
	u := c.baseURL + "/api/search/get?" + q.Encode()
	var resp neteaseSearchResp
	if err := c.do(ctx, u, &resp); err != nil {
		return nil, err
	}
	if resp.Code != 200 {
		return nil, fmt.Errorf("网易云搜索失败: code=%d", resp.Code)
	}
	songs := make([]cnSong, 0, len(resp.Result.Songs))
	for _, s := range resp.Result.Songs {
		song := cnSong{
			ID:       fmt.Sprintf("%d", s.ID),
			Title:    s.Name,
			Duration: float64(s.Duration) / 1000, // 毫秒 → 秒
		}
		if len(s.Artists) > 0 {
			song.Artist = s.Artists[0].Name
		}
		songs = append(songs, song)
	}
	return songs, nil
}

// Lyric 按 song id 取歌词：LRC 无时间轴（纯文本）或为空 → 返回 nil
// （sync-only 规则）。
func (c *NeteaseClient) Lyric(ctx context.Context, id string) (*Lyrics, error) {
	q := url.Values{}
	q.Set("id", id)
	q.Set("lv", "1")
	q.Set("kv", "1")
	q.Set("tv", "-1")
	u := c.baseURL + "/api/song/lyric?" + q.Encode()
	var resp struct {
		Code int `json:"code"`
		Lrc  struct {
			Lyric string `json:"lyric"`
		} `json:"lrc"`
	}
	if err := c.do(ctx, u, &resp); err != nil {
		return nil, err
	}
	if resp.Code != 200 {
		return nil, fmt.Errorf("网易云歌词失败: code=%d", resp.Code)
	}
	return lrcFromText(resp.Lrc.Lyric), nil
}

// do 发起 GET 并解码 JSON；非 200 报错。
func (c *NeteaseClient) do(ctx context.Context, u string, out interface{}) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", cnUA)
	resp, err := c.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("网易云请求失败: %s", resp.Status)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

// ── QQ 音乐 ───────────────────────────────────────────────────────

// qqmusicBaseURL QQ 音乐官方接口基地址。
const qqmusicBaseURL = "https://c.y.qq.com"

// QQMusicClient 是 QQ 音乐歌词客户端（匿名接口）：搜索 + 歌词均无需
// 登录（歌词接口需 Referer: y.qq.com）。
type QQMusicClient struct {
	baseURL string
	client  *http.Client
}

// NewQQMusicClient 创建 QQ 音乐客户端（官方基地址）。
func NewQQMusicClient() *QQMusicClient {
	return NewQQMusicClientWithBaseURL(qqmusicBaseURL)
}

// NewQQMusicClientWithBaseURL 创建指向自定义基地址的客户端（测试用）。
func NewQQMusicClientWithBaseURL(baseURL string) *QQMusicClient {
	return &QQMusicClient{
		baseURL: strings.TrimSuffix(baseURL, "/"),
		client:  &http.Client{Timeout: 10 * time.Second},
	}
}

// qqSearchResp QQ 音乐搜索响应结构。
type qqSearchResp struct {
	Code int `json:"code"`
	Data struct {
		Song struct {
			List []struct {
				Songmid  string `json:"songmid"`
				Songname string `json:"songname"`
				Singer   []struct {
					Name string `json:"name"`
				} `json:"singer"`
				Interval int `json:"interval"` // 秒
			} `json:"list"`
		} `json:"song"`
	} `json:"data"`
}

// Search 搜索歌曲（关键词 = title + artist）。
func (c *QQMusicClient) Search(ctx context.Context, title, artist string) ([]cnSong, error) {
	q := url.Values{}
	q.Set("format", "json")
	q.Set("w", strings.TrimSpace(title+" "+artist))
	q.Set("n", "5")
	u := c.baseURL + "/soso/fcgi-bin/client_search_cp?" + q.Encode()
	var resp qqSearchResp
	if err := c.do(ctx, u, &resp); err != nil {
		return nil, err
	}
	if resp.Code != 0 {
		return nil, fmt.Errorf("QQ 音乐搜索失败: code=%d", resp.Code)
	}
	songs := make([]cnSong, 0, len(resp.Data.Song.List))
	for _, s := range resp.Data.Song.List {
		song := cnSong{
			ID:       s.Songmid,
			Title:    s.Songname,
			Duration: float64(s.Interval),
		}
		if len(s.Singer) > 0 {
			song.Artist = s.Singer[0].Name
		}
		songs = append(songs, song)
	}
	return songs, nil
}

// Lyric 按 songmid 取歌词（需 Referer）；LRC 无时间轴或为空 → nil。
func (c *QQMusicClient) Lyric(ctx context.Context, mid string) (*Lyrics, error) {
	q := url.Values{}
	q.Set("songmid", mid)
	q.Set("format", "json")
	q.Set("nobase64", "1")
	u := c.baseURL + "/lyric/fcgi-bin/fcg_query_lyric_new.fcg?" + q.Encode()
	var resp struct {
		Code  int    `json:"code"`
		Lyric string `json:"lyric"`
	}
	if err := c.do(ctx, u, &resp); err != nil {
		return nil, err
	}
	if resp.Code != 0 {
		return nil, fmt.Errorf("QQ 音乐歌词失败: code=%d", resp.Code)
	}
	return lrcFromText(resp.Lyric), nil
}

// do 发起 GET 并解码 JSON；非 200 报错。
func (c *QQMusicClient) do(ctx context.Context, u string, out interface{}) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", cnUA)
	req.Header.Set("Referer", "https://y.qq.com/")
	resp, err := c.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("QQ 音乐请求失败: %s", resp.Status)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

// lrcFromText 把源返回的 LRC 文本转 *Lyrics：无时间轴（纯文本）或
// 空文本返回 nil（sync-only 规则）。
func lrcFromText(text string) *Lyrics {
	if strings.TrimSpace(text) == "" {
		return nil
	}
	ly, err := ParseLRC([]byte(text))
	if err != nil || len(ly.Lines) == 0 {
		return nil
	}
	return ly
}
