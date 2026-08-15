package lyrics

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

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

// aiIdentifyBudget AI 识别子预算：大模型首 token 慢（实测 qwen3.7-plus
// 11.4s，高峰期可达 20s+；识别失败时终端会打印原因）。独立限时防止
// 吃光总预算饿死严格重查/确定性兜底；包级变量（测试可调小）。
var aiIdentifyBudget = 25 * time.Second

// Fetch 执行 AI 优先的增强匹配流程（用户确认：所有歌词请求都走 AI 判断）：
//  1. AI 未配置 → 纯确定性匹配（行为与无增强一致）；
//  2. AI 识别（结果缓存优先 + single-flight，子预算 aiIdentifyBudget）
//     失败/非歌曲/空标题 → 确定性匹配兜底（Title/Artist 空）；
//  3. AI 识别成功 → 歌词缓存命中直接返回；否则严格重查 lrclib
//     （get 优先，search ≤3s）→ 命中入缓存并返回；
//  4. 严格重查未命中（ErrNotFound）→ 确定性多候选兜底（30s 阈值）
//     → 命中入缓存（AI title-artist 键）并返回；
//  5. lrclib 服务端错误（非 ErrNotFound）原样透传，不做兜底。
//
// AI 识别成功时 FetchResult.Title/Artist 携带清洗后歌名/歌手（展示层
// 覆盖原始标题），Source 标 ai；纯确定性兜底（2 步）不携带。
func (e *EnhancedClient) Fetch(ctx context.Context, track model.Track) (FetchResult, error) {
	if e.ai == nil {
		return e.lrclib.Fetch(ctx, track)
	}
	aiCtx, cancel := context.WithTimeout(ctx, aiIdentifyBudget)
	defer cancel()
	res, ok := e.identify(aiCtx, track)
	if !ok || !res.IsSong || strings.TrimSpace(res.Title) == "" {
		// AI 失败/非歌曲/空标题：确定性匹配兜底（与无 AI 行为一致）
		return e.lrclib.Fetch(ctx, track)
	}
	if cached, ok := e.lrcCache.Get(res.Title, res.Artist); ok {
		cached.Source = LyricsSourceAI
		return FetchResult{Lyrics: cached, Title: res.Title, Artist: res.Artist}, nil
	}
	ly, err := e.lrclib.FetchForQuery(ctx, res.Title, res.Artist, track.Duration)
	if err == nil {
		e.lrcCache.Put(res.Title, res.Artist, ly)
		ly.Source = LyricsSourceAI
		return FetchResult{Lyrics: ly, Title: res.Title, Artist: res.Artist}, nil
	}
	if !errors.Is(err, ErrNotFound) {
		return FetchResult{}, err // 服务端故障透传，兜底只会再撞一次
	}
	// 严格重查未命中 → 确定性多候选兜底（30s 阈值，命中率互补）
	det, err := e.lrclib.Fetch(ctx, track)
	if err != nil {
		return FetchResult{}, err
	}
	ly = det.Lyrics
	e.lrcCache.Put(res.Title, res.Artist, ly)
	ly.Source = LyricsSourceAI
	return FetchResult{Lyrics: ly, Title: res.Title, Artist: res.Artist}, nil
}

// identify 识别标题（AI 结果缓存优先，single-flight 合并并发重复识别）；
// 识别成功即缓存（含负缓存），调用失败不缓存（瞬时错误下次重试）。
func (e *EnhancedClient) identify(ctx context.Context, track model.Track) (AIResult, bool) {
	key := aiCacheKey(track.Title, track.Artist)
	r, ok, wait := e.aiCache.Begin(key)
	if ok {
		return r, true
	}
	if wait != nil {
		// 已有执行者（并发播放同一标题）：等其完成并复用结果；
		// 执行者失败时不重复尝试（避免惊群，下次播放自然重试）。
		select {
		case <-wait:
			r, ok := e.aiCache.Get(key)
			return r, ok
		case <-ctx.Done():
			return AIResult{}, false
		}
	}
	defer e.aiCache.End(key)
	r, err := e.ai.Identify(ctx, track.Title, track.Artist)
	if err != nil {
		// 失败不缓存（瞬时错误下次重试）；打日志便于诊断——
		// 曾静默降级导致无法区分「AI 未配置/超时/key 无效/网络」
		log.Printf("AI 歌词识别失败（降级确定性结果）: %v", err)
		return AIResult{}, false
	}
	e.aiCache.Put(key, r)
	return r, true
}
