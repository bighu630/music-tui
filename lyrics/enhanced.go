package lyrics

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"music-tui/model"
)

// LyricsSourceAI 标记歌词来自 AI 增强路径（UI 据此显示来源标识）；
// 确定性路径与缓存文件本身不携带该标记，读取时按路径重新标注。
const LyricsSourceAI = "ai"

// EnhancedClient 是 AI 增强歌词客户端：先跑确定性匹配（*Client），
// 未命中且配置了 OpenAI 时，用 AI 清洗标题后重查 lrclib（严格时长
// 规则），并维护双缓存：
//   - AI 结果缓存（JSONL）：识别结果与负缓存（is_song=false），
//     同一标题不重复调用 AI；
//   - 歌词缓存（LRC 文件）：拉取到的歌词落盘，命中免 lrclib 请求。
//
// AI 未配置（nil）或调用失败时整体降级为确定性结果，行为与
// 未启用增强一致。ai 为 nil 时可用（等价纯确定性客户端）。
type EnhancedClient struct {
	lrclib   *Client
	ai       *OpenAIClient // nil = AI 禁用
	aiCache  *aiCache
	lrcCache *lrcCache
}

// NewEnhancedClient 组装增强客户端；cacheDir 存放 ai.jsonl 与 LRC 文件
// （建议 <缓存根>/lyrics）。缓存初始化失败返回错误（由调用方降级）。
func NewEnhancedClient(l *Client, ai *OpenAIClient, cacheDir string) (*EnhancedClient, error) {
	if l == nil {
		return nil, fmt.Errorf("lrclib 客户端不能为 nil")
	}
	aiCache, err := newAICache(cacheDir + "/ai.jsonl")
	if err != nil {
		return nil, err
	}
	lrcCache, err := newLRCCache(cacheDir + "/lrc")
	if err != nil {
		return nil, err
	}
	return &EnhancedClient{lrclib: l, ai: ai, aiCache: aiCache, lrcCache: lrcCache}, nil
}

// Fetch 执行增强匹配流程：
//  1. 确定性匹配命中 → 直接返回（Source 为空，UI 不标 AI）；
//  2. 未命中且 AI 禁用/调用失败 → 原样返回确定性结果（ErrNotFound）；
//  3. AI 识别（结果缓存优先）→ is_song=false 或空标题 → ErrNotFound；
//  4. 歌词缓存命中 → 返回（Source=ai）；
//  5. 用清洗后 title/artist 重查 lrclib（get 优先，search 严格 ≤3s）
//     → 命中入歌词缓存并返回；未命中 → ErrNotFound（与确定性结果一致）。
func (e *EnhancedClient) Fetch(ctx context.Context, track model.Track) (*Lyrics, error) {
	ly, err := e.lrclib.Fetch(ctx, track)
	if err == nil || !errors.Is(err, ErrNotFound) {
		return ly, err
	}
	if e.ai == nil {
		return nil, err
	}
	res, ok := e.identify(ctx, track)
	if !ok || !res.IsSong || strings.TrimSpace(res.Title) == "" {
		return nil, ErrNotFound
	}
	if ly, ok := e.lrcCache.Get(res.Title, res.Artist); ok {
		ly.Source = LyricsSourceAI
		return ly, nil
	}
	ly, err = e.lrclib.FetchForQuery(ctx, res.Title, res.Artist, track.Duration)
	if err != nil {
		return nil, ErrNotFound
	}
	e.lrcCache.Put(res.Title, res.Artist, ly)
	ly.Source = LyricsSourceAI
	return ly, nil
}

// identify 识别标题（AI 结果缓存优先）；识别成功即缓存（含负缓存），
// 调用失败不缓存（瞬时错误下次重试）。
func (e *EnhancedClient) identify(ctx context.Context, track model.Track) (AIResult, bool) {
	key := aiCacheKey(track.Title, track.Artist)
	if r, ok := e.aiCache.Get(key); ok {
		return r, true
	}
	r, err := e.ai.Identify(ctx, track.Title, track.Artist)
	if err != nil {
		return AIResult{}, false
	}
	e.aiCache.Put(key, r)
	return r, true
}
