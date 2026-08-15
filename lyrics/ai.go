package lyrics

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// DefaultAIModel 是 OpenAI 识别默认模型（配置未指定时使用）。
const DefaultAIModel = "gpt-4o-mini"

// defaultAIBaseURL OpenAI 兼容 API 默认基地址。
const defaultAIBaseURL = "https://api.openai.com/v1"

// maxAIAttempts 单次识别最多请求次数（仅对瞬时错误重试一次）。
const maxAIAttempts = 2

// OpenAIClient 是 OpenAI Chat Completions 的 REST 客户端（无需 SDK，
// 兼容任何 OpenAI 协议服务）。temperature 固定 0.2（参考项目取值）：
// 低随机性保证 JSON 输出形状稳定。
type OpenAIClient struct {
	apiKey  string
	model   string
	baseURL string
	client  *http.Client
}

// NewOpenAIClient 创建指向 OpenAI 官方的客户端；model 为空用 DefaultAIModel。
func NewOpenAIClient(apiKey, model string) *OpenAIClient {
	return NewOpenAIClientWithBaseURL(apiKey, model, defaultAIBaseURL)
}

// NewOpenAIClientWithBaseURL 创建指向自定义基地址的客户端（测试/自托管
// 兼容服务）；baseURL 为空回落官方默认（defaultAIBaseURL），
// 尾部斜杠自动去除。
func NewOpenAIClientWithBaseURL(apiKey, model, baseURL string) *OpenAIClient {
	if model == "" {
		model = DefaultAIModel
	}
	if baseURL == "" {
		baseURL = defaultAIBaseURL
	}
	return &OpenAIClient{
		apiKey:  apiKey,
		model:   model,
		baseURL: strings.TrimSuffix(baseURL, "/"),
		client:  &http.Client{Timeout: 30 * time.Second},
	}
}

// Identify 识别媒体标题是否为歌曲并提取歌名/歌手。仅瞬时错误（网络
// 传输失败、429、5xx）重试一次；ctx 取消/超时、4xx、响应解析失败等
// 确定性错误不重试（不白付调用费）。
func (c *OpenAIClient) Identify(ctx context.Context, title, artist string) (AIResult, error) {
	var lastErr error
	for attempt := 0; attempt < maxAIAttempts; attempt++ {
		res, err := c.identifyOnce(ctx, title, artist)
		if err == nil {
			return res, nil
		}
		lastErr = err
		if !retryableAIErr(ctx, err) {
			return AIResult{}, err
		}
	}
	return AIResult{}, lastErr
}

// identifyOnce 发起一次 chat/completions 请求并解析识别结果。
func (c *OpenAIClient) identifyOnce(ctx context.Context, title, artist string) (AIResult, error) {
	body, err := json.Marshal(map[string]interface{}{
		"model":       c.model,
		"temperature": 0.2,
		"messages": []map[string]string{
			{"role": "user", "content": songPrompt(title, artist)},
		},
	})
	if err != nil {
		return AIResult{}, fmt.Errorf("构造 OpenAI 请求体: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return AIResult{}, fmt.Errorf("构造 OpenAI 请求: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.client.Do(req)
	if err != nil {
		return AIResult{}, fmt.Errorf("OpenAI 请求失败: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return AIResult{}, &aiStatusError{code: resp.StatusCode}
	}
	var parsed struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return AIResult{}, fmt.Errorf("解析 OpenAI 响应: %w", err)
	}
	if len(parsed.Choices) == 0 || parsed.Choices[0].Message.Content == "" {
		return AIResult{}, fmt.Errorf("OpenAI 响应缺少 choices[0].message.content")
	}
	res, err := parseAIResponse(parsed.Choices[0].Message.Content)
	if err != nil {
		return AIResult{}, fmt.Errorf("解析 OpenAI 内容: %w", err)
	}
	return res, nil
}

// aiStatusError OpenAI 非 200 状态码错误。
type aiStatusError struct {
	code int
}

func (e *aiStatusError) Error() string {
	return fmt.Sprintf("OpenAI API 错误: HTTP %d", e.code)
}

// retryableAIErr 判断错误是否值得重试：仅网络传输错误（*url.Error，
// 排除 ctx 取消）与 429/5xx 重试；ctx 取消/超时、4xx、响应解析失败等
// 确定性错误不重试。
func retryableAIErr(ctx context.Context, err error) bool {
	if ctx.Err() != nil {
		return false // 取消/超时：重试只会立即再失败
	}
	var se *aiStatusError
	if errors.As(err, &se) {
		return se.code == http.StatusTooManyRequests || se.code >= 500
	}
	var ue *url.Error
	return errors.As(err, &ue)
}

// AIResult 是 AI 对媒体标题的识别结果。
type AIResult struct {
	IsSong bool   // 标题是否为歌曲（false = 播客/影视/教程等，负缓存依据）
	Title  string // 清洗后的歌曲名
	Artist string // 歌手（可能为空：AI 无法确定时不阻塞查询）
}

// parseAIResponse 从 AI 响应文本中提取识别结果：
// 支持 ```json 代码围栏与前后杂文（LLM 常附加解释），取首个平衡的
// JSON 对象解析；is_song 字段缺失时按 true 处理（与参考项目一致，
// 向后兼容），title 缺失留空由上层判断。截断/无 JSON/形状错误返回错误。
func parseAIResponse(raw string) (AIResult, error) {
	obj := extractJSONObject(raw)
	if obj == "" {
		return AIResult{}, fmt.Errorf("AI 响应中未找到 JSON 对象")
	}
	var r struct {
		IsSong *bool  `json:"is_song"`
		Title  string `json:"title"`
		Artist string `json:"artist"`
	}
	if err := json.Unmarshal([]byte(obj), &r); err != nil {
		return AIResult{}, fmt.Errorf("解析 AI JSON: %w", err)
	}
	res := AIResult{Title: strings.TrimSpace(r.Title), Artist: strings.TrimSpace(r.Artist), IsSong: true}
	if r.IsSong != nil {
		res.IsSong = *r.IsSong
	}
	return res, nil
}

// extractJSONObject 返回输入中首个括号平衡的 JSON 对象（含字符串与转义
// 感知），未找到或括号不平衡（AI 截断）返回空串。
func extractJSONObject(s string) string {
	start := strings.Index(s, "{")
	if start < 0 {
		return ""
	}
	depth := 0
	inStr := false
	esc := false
	for i := start; i < len(s); i++ {
		ch := s[i]
		if inStr {
			if esc {
				esc = false
				continue
			}
			switch ch {
			case '\\':
				esc = true
			case '"':
				inStr = false
			}
			continue
		}
		switch ch {
		case '"':
			inStr = true
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return s[start : i+1]
			}
		}
	}
	return ""
}

// songPrompt 构造歌曲识别 prompt：要求 AI 返回固定 JSON 形状，
// 含繁→简转换与 feat./版本描述剥离规则与示例。
func songPrompt(title, artist string) string {
	var sb strings.Builder
	sb.WriteString("You are a song identification assistant. Extract song information from a media title (e.g. a YouTube video title).\n")
	sb.WriteString("Return ONLY a JSON object, no explanation:\n")
	sb.WriteString(`{"is_song": true, "title": "Song Title", "artist": "Artist Name"}` + "\n")
	sb.WriteString(`or {"is_song": false}` + "\n\n")
	sb.WriteString("Rules:\n")
	sb.WriteString("- If the title contains song info, set is_song to true and extract the accurate song title and artist.\n")
	sb.WriteString("- If the title is a podcast, vlog, documentary, news, tutorial, gameplay, or not a song at all, set is_song to false.\n")
	sb.WriteString("- Convert Traditional Chinese text to Simplified Chinese.\n")
	sb.WriteString("- \"feat.\"/\"ft.\" artists are NOT the main artist: extract the main artist only.\n")
	sb.WriteString("- Remove version descriptors such as (Live), [Official MV], 现场版 from the title.\n\n")
	sb.WriteString("Examples:\n")
	sb.WriteString(`"山吹菌 - 『絶美戲腔』少年霜/提糯-非李" -> {"is_song": true, "title": "非李", "artist": "少年霜"}` + "\n")
	sb.WriteString(`"【周杰倫】晴天 Official Music Video" -> {"is_song": true, "title": "晴天", "artist": "周杰伦"}` + "\n")
	sb.WriteString(`"DOLLA - DASH (Official Music Video)" -> {"is_song": true, "title": "DASH", "artist": "DOLLA"}` + "\n")
	sb.WriteString(`"Travis Scott - FE!N ft. Playboi Carti" -> {"is_song": true, "title": "FE!N", "artist": "Travis Scott"}` + "\n")
	sb.WriteString(`"城市漫步 Vlog #12" -> {"is_song": false}` + "\n\n")
	sb.WriteString("Input media title: " + title)
	if artist != "" {
		sb.WriteString("\nMedia channel name (hint only, may be unrelated): " + artist)
	}
	return sb.String()
}
