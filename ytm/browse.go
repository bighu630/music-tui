package ytm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
)

var (
	// ytmBrowseURL 是 InnerTube browse 端点（无 key 参数，实测有效）。
	// 声明为变量以便测试注入 httptest 地址。
	ytmBrowseURL = "https://music.youtube.com/youtubei/v1/browse?prettyPrint=false"
	// likedPlaylistsBrowseID 是"歌单库"browseId（FEmusic_library_playlists
	// 已废弃返回 400；ytmusicapi/yutemal 均用 liked_playlists，实测 200）。
	likedPlaylistsBrowseID = "FEmusic_liked_playlists"
	// ytmUserAgent 模拟 Chrome 桌面浏览器 UA。
	ytmUserAgent = "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36"
)

var (
	// ErrNotLoggedIn 未登录：无有效登录配置，或响应 logged_in=0 / 无歌单条目。
	ErrNotLoggedIn = errors.New("未登录：请先配置 YouTube Music 登录")
	// ErrSessionInvalid 登录已失效（HTTP >= 400，需重新导出 cookie）。
	ErrSessionInvalid = errors.New("登录已失效，请重新导出 cookie")
)

// innerTube 是 YouTube Music InnerTube browse 客户端。
type innerTube struct {
	http         *http.Client
	cookieHeader string
}

func newInnerTube(hc *http.Client, cookieHeader string) *innerTube {
	return &innerTube{http: hc, cookieHeader: cookieHeader}
}

// browse 发送 browse 请求并返回解析后的 JSON（任意结构）。
// 错误分类：HTTP>=400 → ErrSessionInvalid；网络错误透传（包装）。
func (it *innerTube) browse(ctx context.Context, browseID string) (any, error) {
	sapisid, err := extractSAPISID(it.cookieHeader)
	if err != nil {
		return nil, fmt.Errorf("%w（cookie 缺少 SAPISID）", ErrNotLoggedIn)
	}
	// clientVersion 动态日期：1.YYYYMMDD.01.00（ytmusicapi 同款，实测有效）
	body, err := json.Marshal(map[string]any{
		"context": map[string]any{
			"client": map[string]any{
				"clientName":    "WEB_REMIX",
				"clientVersion": "1." + time.Now().UTC().Format("20060102") + ".01.00",
			},
		},
		"browseId": browseID,
	})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, ytmBrowseURL, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	ts := time.Now().Unix()
	req.Header.Set("Cookie", it.cookieHeader)
	req.Header.Set("Authorization", "SAPISIDHASH "+sapisidHash(sapisid, ts))
	req.Header.Set("Origin", ytmOrigin)
	req.Header.Set("X-Origin", ytmOrigin)
	req.Header.Set("X-Goog-AuthUser", "0")
	req.Header.Set("Referer", ytmOrigin+"/")
	req.Header.Set("User-Agent", ytmUserAgent)
	req.Header.Set("Content-Type", "application/json")

	resp, err := it.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("请求 YouTube Music 失败: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("%w（HTTP %d）", ErrSessionInvalid, resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if err != nil {
		return nil, fmt.Errorf("读取响应失败: %w", err)
	}
	var v any
	if err := json.Unmarshal(data, &v); err != nil {
		return nil, fmt.Errorf("解析响应失败: %w", err)
	}
	return v, nil
}

// loggedInParam 提取 serviceTrackingParams 中 logged_in 参数值
// （"1"/"0"/""）；缺失返回 ""。
func loggedInParam(v any) string {
	obj, ok := v.(map[string]any)
	if !ok {
		return ""
	}
	stp, ok := obj["serviceTrackingParams"].([]any)
	if !ok {
		return ""
	}
	for _, e := range stp {
		svc, ok := e.(map[string]any)
		if !ok {
			continue
		}
		params, ok := svc["params"].([]any)
		if !ok {
			continue
		}
		for _, p := range params {
			pair, ok := p.(map[string]any)
			if !ok {
				continue
			}
			if k, _ := pair["key"].(string); strings.EqualFold(k, "logged_in") {
				if val, ok := pair["value"].(string); ok {
					return val
				}
			}
		}
	}
	return ""
}

// extractPlaylists 容错递归扫描响应，提取全部 musicTwoRowItemRenderer
// 歌单条目（browseId 去重，保序）。不依赖固定 JSON 路径，结构变化也能解析
// （参考 yutemal extractor.go fromJSON 思路）。
func extractPlaylists(v any) []RemotePlaylist {
	var out []RemotePlaylist
	seen := make(map[string]bool)
	var walk func(any)
	walk = func(v any) {
		switch val := v.(type) {
		case map[string]any:
			if r, ok := val["musicTwoRowItemRenderer"].(map[string]any); ok {
				if p := remotePlaylistFromItem(r); p != nil && !seen[p.ID] {
					seen[p.ID] = true
					out = append(out, *p)
				}
				return // 命中 renderer 不再深入
			}
			// 键排序保证同一层多个候选项的发现顺序稳定（歌单保序）
			keys := make([]string, 0, len(val))
			for k := range val {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			for _, k := range keys {
				walk(val[k])
			}
		case []any:
			for _, item := range val {
				walk(item)
			}
		}
	}
	walk(v)
	return out
}

// remotePlaylistFromItem 从 musicTwoRowItemRenderer 对象提取歌单；
// 缺少 browseId 返回 nil。
func remotePlaylistFromItem(r map[string]any) *RemotePlaylist {
	id := getStringPath(r, "navigationEndpoint", "browseEndpoint", "browseId")
	if id == "" {
		return nil
	}
	return &RemotePlaylist{
		ID:    id,
		Title: firstRunText(r["title"]),
		Count: subtitleCount(r["subtitle"]),
	}
}

// getStringPath 按 key 路径取字符串值；任一层缺失返回 ""。
func getStringPath(m map[string]any, keys ...string) string {
	var cur any = m
	for _, k := range keys {
		mm, ok := cur.(map[string]any)
		if !ok {
			return ""
		}
		cur = mm[k]
	}
	s, _ := cur.(string)
	return s
}

// firstRunText 取 title 类节点文本：runs[0].text 优先，simpleText 兜底。
func firstRunText(v any) string {
	m, ok := v.(map[string]any)
	if !ok {
		return ""
	}
	if runs, ok := m["runs"].([]any); ok && len(runs) > 0 {
		if r0, ok := runs[0].(map[string]any); ok {
			if t, ok := r0["text"].(string); ok {
				return t
			}
		}
	}
	if t, ok := m["simpleText"].(string); ok {
		return t
	}
	return ""
}

// subtitleCount 从 subtitle runs 提取歌单数量（如 "5 首"/"5 songs" → 5）；
// 取第一个含数字的 run 的连续数字段；无数字返回 0。
func subtitleCount(v any) int {
	m, ok := v.(map[string]any)
	if !ok {
		return 0
	}
	runs, ok := m["runs"].([]any)
	if !ok {
		return 0
	}
	for _, r := range runs {
		run, ok := r.(map[string]any)
		if !ok {
			continue
		}
		text, _ := run["text"].(string)
		for i := 0; i < len(text); i++ {
			if text[i] < '0' || text[i] > '9' {
				continue
			}
			j := i
			for j < len(text) && text[j] >= '0' && text[j] <= '9' {
				j++
			}
			if n, err := strconv.Atoi(text[i:j]); err == nil {
				return n
			}
		}
	}
	return 0
}
