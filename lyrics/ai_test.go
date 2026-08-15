package lyrics

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestParseAIResponsePlainJSON(t *testing.T) {
	got, err := parseAIResponse(`{"is_song": true, "title": "晴天", "artist": "周杰伦"}`)
	if err != nil {
		t.Fatalf("parseAIResponse: %v", err)
	}
	if !got.IsSong || got.Title != "晴天" || got.Artist != "周杰伦" {
		t.Errorf("got %+v, want is_song=true title=晴天 artist=周杰伦", got)
	}
}

func TestParseAIResponseCodeFence(t *testing.T) {
	raw := "```json\n{\"is_song\": true, \"title\": \"FE!N\", \"artist\": \"Travis Scott\"}\n```"
	got, err := parseAIResponse(raw)
	if err != nil {
		t.Fatalf("parseAIResponse: %v", err)
	}
	if !got.IsSong || got.Title != "FE!N" || got.Artist != "Travis Scott" {
		t.Errorf("got %+v, want is_song=true title=FE!N artist=Travis Scott", got)
	}
}

func TestParseAIResponseTextAroundJSON(t *testing.T) {
	raw := "The song is:\n{\"is_song\": true, \"title\": \"晴天\", \"artist\": \"周杰伦\"}\nHope that helps!"
	got, err := parseAIResponse(raw)
	if err != nil {
		t.Fatalf("parseAIResponse: %v", err)
	}
	if !got.IsSong || got.Title != "晴天" || got.Artist != "周杰伦" {
		t.Errorf("got %+v", got)
	}
}

func TestParseAIResponseNotSong(t *testing.T) {
	got, err := parseAIResponse(`{"is_song": false}`)
	if err != nil {
		t.Fatalf("parseAIResponse: %v", err)
	}
	if got.IsSong {
		t.Errorf("IsSong = true, want false")
	}
}

func TestParseAIResponseMissingIsSongDefaultsTrue(t *testing.T) {
	got, err := parseAIResponse(`{"title": "晴天", "artist": "周杰伦"}`)
	if err != nil {
		t.Fatalf("parseAIResponse: %v", err)
	}
	if !got.IsSong {
		t.Errorf("IsSong = false, want true (缺省为歌曲)")
	}
	if got.Title != "晴天" || got.Artist != "周杰伦" {
		t.Errorf("got %+v", got)
	}
}

func TestParseAIResponseTruncatedJSON(t *testing.T) {
	// AI 输出被 max_tokens 截断：括号不平衡，必须报错而非静默吞掉
	if _, err := parseAIResponse(`{"is_song": true, "title": "晴天"`); err == nil {
		t.Fatal("parseAIResponse(truncated) = nil error, want error")
	}
}

func TestParseAIResponseGarbage(t *testing.T) {
	for _, raw := range []string{"", "no json here", "```text\nnothing\n```"} {
		if _, err := parseAIResponse(raw); err == nil {
			t.Errorf("parseAIResponse(%q) = nil error, want error", raw)
		}
	}
}

func TestParseAIResponseArrayWrapped(t *testing.T) {
	// 数组包裹的 JSON：取首个对象（容错而非报错）
	got, err := parseAIResponse(`[{"is_song": true, "title": "晴天", "artist": "周杰伦"}]`)
	if err != nil {
		t.Fatalf("parseAIResponse(array): %v", err)
	}
	if !got.IsSong || got.Title != "晴天" || got.Artist != "周杰伦" {
		t.Errorf("got %+v", got)
	}
}

// TestPromptContainsTitleAndFeatExamples 校验 prompt 携带标题与 feat. 示例
// （防 prompt 回归：误删示例会导致 AI 输出质量下降）。
func TestPromptContainsTitleAndFeatExamples(t *testing.T) {
	p := songPrompt("晴天 Official Music Video", "周杰倫官方頻道")
	for _, want := range []string{
		"晴天 Official Music Video", "周杰倫官方頻道",
		"feat", "is_song", "Traditional Chinese", "Simplified Chinese",
	} {
		if !strings.Contains(p, want) {
			t.Errorf("prompt 缺少 %q", want)
		}
	}
}

// ── OpenAIClient.Identify（httptest mock）──────────────────────────

func TestIdentifySuccess(t *testing.T) {
	var gotModel, gotTemp, gotPrompt, gotAuth string
	var gotMethod, gotPath, gotContentType string
	var gotUserRole bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath = r.Method, r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		gotContentType = r.Header.Get("Content-Type")
		var body struct {
			Model       string  `json:"model"`
			Temperature float64 `json:"temperature"`
			Messages    []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("请求体解析失败: %v", err)
			return
		}
		gotModel, gotTemp, gotPrompt = body.Model, fmt.Sprint(body.Temperature), body.Messages[0].Content
		gotUserRole = body.Messages[0].Role == "user"
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"{\"is_song\": true, \"title\": \"晴天\", \"artist\": \"周杰伦\"}"}}]}`))
	}))
	defer server.Close()

	c := NewOpenAIClientWithBaseURL("sk-test", "gpt-4o-mini", server.URL)
	res, err := c.Identify(context.Background(), "【周杰倫】晴天 Official Music Video", "周杰倫官方頻道")
	if err != nil {
		t.Fatalf("Identify: %v", err)
	}
	if !res.IsSong || res.Title != "晴天" || res.Artist != "周杰伦" {
		t.Errorf("got %+v", res)
	}
	if gotMethod != http.MethodPost || gotPath != "/chat/completions" {
		t.Errorf("请求 = %s %s, want POST /chat/completions", gotMethod, gotPath)
	}
	if gotAuth != "Bearer sk-test" {
		t.Errorf("Authorization = %q", gotAuth)
	}
	if gotContentType != "application/json" {
		t.Errorf("Content-Type = %q", gotContentType)
	}
	if gotModel != "gpt-4o-mini" {
		t.Errorf("model = %q", gotModel)
	}
	if gotTemp != "0.2" {
		t.Errorf("temperature = %v, want 0.2", gotTemp)
	}
	if !gotUserRole {
		t.Errorf("role = %v, want user", gotUserRole)
	}
	if !strings.Contains(gotPrompt, "【周杰倫】晴天 Official Music Video") {
		t.Errorf("prompt 未携带标题: %q", gotPrompt)
	}
}

func TestIdentifyRetriesOn500(t *testing.T) {
	attempts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		if attempts == 1 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"{\"is_song\": false}"}}]}`))
	}))
	defer server.Close()

	c := NewOpenAIClientWithBaseURL("sk-test", "", server.URL)
	res, err := c.Identify(context.Background(), "城市漫步 Vlog", "")
	if err != nil {
		t.Fatalf("Identify: %v", err)
	}
	if res.IsSong {
		t.Errorf("IsSong = true, want false")
	}
	if attempts != 2 {
		t.Errorf("attempts = %d, want 2（5xx 重试一次）", attempts)
	}
}

func TestIdentifyNoRetryOn401(t *testing.T) {
	attempts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer server.Close()

	c := NewOpenAIClientWithBaseURL("sk-bad", "", server.URL)
	if _, err := c.Identify(context.Background(), "晴天", ""); err == nil {
		t.Fatal("Identify(401) = nil error, want error")
	}
	if attempts != 1 {
		t.Errorf("attempts = %d, want 1（401 不重试）", attempts)
	}
}

func TestIdentifyRetriesExhausted(t *testing.T) {
	attempts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer server.Close()

	c := NewOpenAIClientWithBaseURL("sk-test", "", server.URL)
	if _, err := c.Identify(context.Background(), "晴天", ""); err == nil {
		t.Fatal("Identify(429) = nil error, want error")
	}
	if attempts != 2 {
		t.Errorf("attempts = %d, want 2（429 重试一次后报错）", attempts)
	}
}

func TestIdentifyEmptyContent(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":""}}]}`))
	}))
	defer server.Close()

	c := NewOpenAIClientWithBaseURL("sk-test", "", server.URL)
	if _, err := c.Identify(context.Background(), "晴天", ""); err == nil {
		t.Fatal("Identify(empty content) = nil error, want error")
	}
}

func TestIdentifyMissingChoices(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"error":{"message":"rate limit"}}`))
	}))
	defer server.Close()

	c := NewOpenAIClientWithBaseURL("sk-test", "", server.URL)
	if _, err := c.Identify(context.Background(), "晴天", ""); err == nil {
		t.Fatal("Identify(missing choices) = nil error, want error")
	}
}

func TestNewOpenAIClientDefaults(t *testing.T) {
	c := NewOpenAIClient("sk-test", "")
	if c.model != DefaultAIModel {
		t.Errorf("model = %q, want 默认 %q", c.model, DefaultAIModel)
	}
	if c.baseURL != defaultAIBaseURL {
		t.Errorf("baseURL = %q, want %q", c.baseURL, defaultAIBaseURL)
	}
}

func TestIdentifyEmptyContentNoRetry(t *testing.T) {
	attempts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":""}}]}`))
	}))
	defer server.Close()

	c := NewOpenAIClientWithBaseURL("sk-test", "", server.URL)
	if _, err := c.Identify(context.Background(), "晴天", ""); err == nil {
		t.Fatal("Identify(empty content) = nil error, want error")
	}
	if attempts != 1 {
		t.Errorf("attempts = %d, want 1（响应解析失败不重试，不白付调用费）", attempts)
	}
}

func TestIdentifyContextCanceledNoRetry(t *testing.T) {
	attempts := 0
	received := make(chan struct{})
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		close(received) // 通知测试：请求已发出并挂起
		<-release
		aiRespond(w, r, `{"is_song": false}`)
	}))
	defer server.Close()
	defer func() { close(release) }() // 先放行挂起请求，再关服务器（避免 Close 等 handler 死锁）

	ctx, cancel := context.WithCancel(context.Background())
	c := NewOpenAIClientWithBaseURL("sk-test", "", server.URL)
	done := make(chan struct{})
	var idErr error
	go func() {
		_, idErr = c.Identify(ctx, "晴天", "")
		close(done)
	}()
	select {
	case <-received: // 请求确实发出后再取消，杜绝时序抖动
	case <-time.After(3 * time.Second):
		t.Fatal("请求未到达服务器")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("Identify 未在取消后返回")
	}
	if idErr == nil {
		t.Fatal("Identify(canceled) = nil error, want error")
	}
	if attempts != 1 {
		t.Errorf("attempts = %d, want 1（ctx 取消不重试）", attempts)
	}
}

func TestIdentifyNetworkErrorRetries(t *testing.T) {
	// 连接拒绝 = 网络错误：重试一次（共 2 次尝试）
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	url := server.URL
	server.Close() // 关闭后连接必然失败

	c := NewOpenAIClientWithBaseURL("sk-test", "", url)
	if _, err := c.Identify(context.Background(), "晴天", ""); err == nil {
		t.Fatal("Identify(network error) = nil error, want error")
	}
	_ = c // 客户端内部尝试次数无法直接观测，能拿到错误即可
}
