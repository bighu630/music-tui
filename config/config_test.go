package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"music-tui/cache"
)

// defaultDir 返回默认缓存目录（UserCacheDir()/music-tui）。
func defaultDir(t *testing.T) string {
	t.Helper()
	dir, err := os.UserCacheDir()
	if err != nil {
		t.Fatalf("os.UserCacheDir: %v", err)
	}
	return filepath.Join(dir, "music-tui")
}

func TestDefault(t *testing.T) {
	cfg, err := Default()
	if err != nil {
		t.Fatalf("Default() error: %v", err)
	}
	if !cfg.Cache.Enabled {
		t.Errorf("Enabled = false, want true")
	}
	if cfg.Cache.MaxEntries != DefaultMaxEntries {
		t.Errorf("MaxEntries = %d, want %d", cfg.Cache.MaxEntries, DefaultMaxEntries)
	}
	if cfg.Cache.Dir != defaultDir(t) {
		t.Errorf("Dir = %q, want %q", cfg.Cache.Dir, defaultDir(t))
	}
	if !cfg.LyricFile.Enabled {
		t.Errorf("LyricFile.Enabled = false, want true")
	}
}

func TestLoadMissingCreatesDefault(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "config.json")
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	want := defaultDir(t)
	if !cfg.Cache.Enabled || cfg.Cache.MaxEntries != DefaultMaxEntries || cfg.Cache.Dir != want {
		t.Errorf("got Cache=%+v, want enabled=true max=%d dir=%q", cfg.Cache, DefaultMaxEntries, want)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("文件未生成: %v", err)
	}
	var back Config
	if err := json.Unmarshal(data, &back); err != nil {
		t.Fatalf("生成的配置文件无法解析: %v", err)
	}
	if back.Cache != cfg.Cache {
		t.Errorf("文件内容 %+v != 加载结果 %+v", back.Cache, cfg.Cache)
	}
}

func TestLoadExistingOverrides(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(`{"cache":{"enabled":false,"max_entries":7,"dir":"/tmp/xyz"}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Cache.Enabled {
		t.Errorf("Enabled = true, want false")
	}
	if cfg.Cache.MaxEntries != 7 {
		t.Errorf("MaxEntries = %d, want 7", cfg.Cache.MaxEntries)
	}
	if cfg.Cache.Dir != "/tmp/xyz" {
		t.Errorf("Dir = %q, want /tmp/xyz", cfg.Cache.Dir)
	}
}

func TestLoadPartialFallsBack(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(`{"cache":{"enabled":false}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Cache.Enabled {
		t.Errorf("Enabled = true, want false")
	}
	if cfg.Cache.MaxEntries != DefaultMaxEntries {
		t.Errorf("MaxEntries = %d, want %d", cfg.Cache.MaxEntries, DefaultMaxEntries)
	}
	if cfg.Cache.Dir != defaultDir(t) {
		t.Errorf("Dir = %q, want %q", cfg.Cache.Dir, defaultDir(t))
	}
}

func TestLoadMaxEntriesBelowOneClamps(t *testing.T) {
	for _, v := range []int{0, -5} {
		data := fmt.Sprintf(`{"cache":{"max_entries":%d}}`, v)
		path := filepath.Join(t.TempDir(), "config.json")
		if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
			t.Fatal(err)
		}
		cfg, err := Load(path)
		if err != nil {
			t.Fatalf("Load(max_entries=%d): %v", v, err)
		}
		if cfg.Cache.MaxEntries != DefaultMaxEntries {
			t.Errorf("max_entries=%d: MaxEntries = %d, want %d", v, cfg.Cache.MaxEntries, DefaultMaxEntries)
		}
	}
}

func TestLoadCorruptReturnsError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	corrupt := []byte(`{invalid`)
	if err := os.WriteFile(path, corrupt, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("Load(corrupt) = nil error, want error")
	}
	// 不写盘覆盖原文件：内容保持原样（留给 main 层备份重建）
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != string(corrupt) {
		t.Errorf("损坏文件被改写: %q", data)
	}
}

func TestLoadEmptyFileDefaults(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	want := defaultDir(t)
	if !cfg.Cache.Enabled || cfg.Cache.MaxEntries != DefaultMaxEntries || cfg.Cache.Dir != want {
		t.Errorf("got Cache=%+v, want 默认值", cfg.Cache)
	}
	// 空文件不写盘：内容保持为空
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(data) != 0 {
		t.Errorf("空文件被改写: %q", data)
	}
}

func TestSaveRoundtrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "config.json")
	cfg := &Config{Cache: cache.Options{Enabled: false, MaxEntries: 42, Dir: "/tmp/some-cache"}}
	if err := cfg.Save(path); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.Cache != cfg.Cache {
		t.Errorf("roundtrip 不一致: got %+v, want %+v", got.Cache, cfg.Cache)
	}
	if _, err := os.Stat(path + ".tmp"); !os.IsNotExist(err) {
		t.Errorf("tmp 文件残留: %v", err)
	}
}

// ── OpenAI 配置节 ─────────────────────────────────────────────────

func TestLoadOpenAIMissingDisabled(t *testing.T) {
	// 配置无 openai 节：APIKey 为空 = AI 路径完全禁用
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(`{"cache":{"enabled":false}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.OpenAI.APIKey != "" {
		t.Errorf("APIKey = %q, want 空（未配置 = 禁用）", cfg.OpenAI.APIKey)
	}
	if cfg.OpenAI.Model != DefaultOpenAIModel {
		t.Errorf("Model = %q, want 默认 %q", cfg.OpenAI.Model, DefaultOpenAIModel)
	}
}

func TestLoadOpenAIKeyEnablesModelDefaults(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(`{"openai":{"api_key":"sk-123"}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.OpenAI.APIKey != "sk-123" {
		t.Errorf("APIKey = %q, want sk-123", cfg.OpenAI.APIKey)
	}
	if cfg.OpenAI.Model != DefaultOpenAIModel {
		t.Errorf("Model = %q, want 默认 %q", cfg.OpenAI.Model, DefaultOpenAIModel)
	}
}

func TestLoadOpenAIExplicitEmptyKeyDisabled(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(`{"openai":{"api_key":"","model":"gpt-4o"}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.OpenAI.APIKey != "" {
		t.Errorf("APIKey = %q, want 空（显式空 key = 禁用）", cfg.OpenAI.APIKey)
	}
}

func TestLoadOpenAIExplicitValues(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(`{"openai":{"api_key":"sk-456","model":"gpt-4o-mini"}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.OpenAI.APIKey != "sk-456" || cfg.OpenAI.Model != "gpt-4o-mini" {
		t.Errorf("got %+v, want sk-456/gpt-4o-mini", cfg.OpenAI)
	}
}

func TestLoadOpenAIExplicitEmptyModelDefaults(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(`{"openai":{"api_key":"sk-456","model":""}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.OpenAI.Model != DefaultOpenAIModel {
		t.Errorf("Model = %q, want 默认 %q", cfg.OpenAI.Model, DefaultOpenAIModel)
	}
	if cfg.OpenAI.APIKey != "sk-456" {
		t.Errorf("APIKey = %q, want sk-456", cfg.OpenAI.APIKey)
	}
}

func TestSaveRoundtripOpenAI(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "config.json")
	cfg := &Config{
		Cache:  cache.Options{Enabled: false, MaxEntries: 42, Dir: "/tmp/some-cache"},
		OpenAI: OpenAI{APIKey: "sk-789", Model: "gpt-4o-mini"},
	}
	if err := cfg.Save(path); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.OpenAI != cfg.OpenAI {
		t.Errorf("openai roundtrip 不一致: got %+v, want %+v", got.OpenAI, cfg.OpenAI)
	}
}

// TestSavePerms0600 配置文件含 OpenAI API key：写盘权限必须 0600
// （其他本地用户不可读）。
func TestSavePerms0600(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	cfg := &Config{OpenAI: OpenAI{APIKey: "sk-secret", Model: "gpt-4o-mini"}}
	if err := cfg.Save(path); err != nil {
		t.Fatalf("Save: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("权限 = %o, want 600", perm)
	}
	// 加载后再存（Load→Save 路径）同样 0600
	got, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if err := got.Save(path); err != nil {
		t.Fatalf("Save2: %v", err)
	}
	info, err = os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("重存后权限 = %o, want 600", perm)
	}
}

// TestLoadOpenAIBaseURLMissing 三方 base_url 缺省：空 = 走 OpenAI 官方默认。
func TestLoadOpenAIBaseURLMissing(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(`{"openai":{"api_key":"sk-123"}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.OpenAI.BaseURL != "" {
		t.Errorf("BaseURL = %q, want 空（缺省 = 官方默认）", cfg.OpenAI.BaseURL)
	}
}

func TestLoadOpenAIBaseURLExplicit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(`{"openai":{"api_key":"sk-123","base_url":"https://api.deepseek.com/v1","model":"deepseek-chat"}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.OpenAI.BaseURL != "https://api.deepseek.com/v1" || cfg.OpenAI.Model != "deepseek-chat" {
		t.Errorf("got %+v, want base_url=https://api.deepseek.com/v1 model=deepseek-chat", cfg.OpenAI)
	}
}

func TestSaveRoundtripOpenAIBaseURL(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "config.json")
	cfg := &Config{OpenAI: OpenAI{APIKey: "sk-789", Model: "gpt-4o-mini", BaseURL: "https://api.thirdparty.com/v1"}}
	if err := cfg.Save(path); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.OpenAI != cfg.OpenAI {
		t.Errorf("openai roundtrip 不一致: got %+v, want %+v", got.OpenAI, cfg.OpenAI)
	}
}

// ── Ytdlp 配置节 ─────────────────────────────────────────────────

func TestDefaultYtdlpHeadersNil(t *testing.T) {
	cfg, err := Default()
	if err != nil {
		t.Fatalf("Default() error: %v", err)
	}
	if cfg.Ytdlp.Headers != nil {
		t.Errorf("Ytdlp.Headers = %v, want nil（默认不配置任何 header）", cfg.Ytdlp.Headers)
	}
}

func TestLoadYtdlpMissingSectionNil(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(`{"cache":{"enabled":false}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Ytdlp.Headers != nil {
		t.Errorf("Ytdlp.Headers = %v, want nil（无 ytdlp 节 = 未配置）", cfg.Ytdlp.Headers)
	}
}

func TestLoadYtdlpEmptyObjectNil(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(`{"ytdlp":{}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Ytdlp.Headers != nil {
		t.Errorf("Ytdlp.Headers = %v, want nil（ytdlp:{} = 未配置）", cfg.Ytdlp.Headers)
	}
}

func TestLoadYtdlpHeadersNullNil(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(`{"ytdlp":{"headers":null}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Ytdlp.Headers != nil {
		t.Errorf("Ytdlp.Headers = %v, want nil（headers:null = 未配置）", cfg.Ytdlp.Headers)
	}
}

func TestLoadYtdlpEmptyHeadersMap(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(`{"ytdlp":{"headers":{}}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(cfg.Ytdlp.Headers) != 0 {
		t.Errorf("len(Ytdlp.Headers) = %d, want 0（headers:{} = 空 map）", len(cfg.Ytdlp.Headers))
	}
}

func TestLoadYtdlpHeadersExplicit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(`{"ytdlp":{"headers":{"User-Agent":"Mozilla/5.0 (X11; Linux x86_64)","X-YouTube-Client-Name":"1"}}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	want := map[string]string{
		"User-Agent":            "Mozilla/5.0 (X11; Linux x86_64)",
		"X-YouTube-Client-Name": "1",
	}
	if !reflect.DeepEqual(cfg.Ytdlp.Headers, want) {
		t.Errorf("Ytdlp.Headers = %v, want %v", cfg.Ytdlp.Headers, want)
	}
}

func TestLoadYtdlpHeadersInvalidType(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(`{"ytdlp":{"headers":"oops"}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("headers 为字符串时应整体解析报错（与 openai 节行为一致）")
	}
}

func TestSaveRoundtripYtdlp(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "config.json")
	cfg := &Config{
		Cache:  cache.Options{Enabled: false, MaxEntries: 42, Dir: "/tmp/some-cache"},
		OpenAI: OpenAI{APIKey: "sk-789", Model: "gpt-4o-mini"},
		Ytdlp:  Ytdlp{Headers: map[string]string{"User-Agent": "Mozilla/5.0", "X-Custom": "v"}},
	}
	if err := cfg.Save(path); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !reflect.DeepEqual(got.Ytdlp, cfg.Ytdlp) {
		t.Errorf("ytdlp roundtrip 不一致: got %+v, want %+v", got.Ytdlp, cfg.Ytdlp)
	}
}

// ── Log 配置节 ─────────────────────────────────────────────────

func TestLogLevelDefault(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	cfg, err := Load(path) // 文件不存在 → 生成默认
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Log.Level != "info" {
		t.Errorf("默认 Log.Level = %q, want info", cfg.Log.Level)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"level": "info"`) {
		t.Errorf("默认配置文件应含 log.level: %s", data)
	}
}

// ── LyricFile 配置节 ─────────────────────────────────────────────

func TestLoadLyricFileEnabled(t *testing.T) {
	// 缺失 → 默认 true(开启)
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(`{"cache":{"enabled":false}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !cfg.LyricFile.Enabled {
		t.Error("lyric_file 缺失时应默认开启")
	}

	// 显式 false → 关闭
	path = filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(`{"lyric_file": {"enabled": false}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg2, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg2.LyricFile.Enabled {
		t.Error("lyric_file.enabled=false 时应关闭")
	}

	// 显式 true → 开启
	path = filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(`{"lyric_file": {"enabled": true}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg3, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !cfg3.LyricFile.Enabled {
		t.Error("lyric_file.enabled=true 时应开启")
	}
}

func TestLogLevelParse(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{`{"log": {"level": "debug"}}`, "debug"},
		{`{"log": {"level": "warn"}}`, "warn"},
		{`{"log": {"level": "error"}}`, "error"},
		{`{"log": {"level": "bogus"}}`, "info"},   // 非法回落
		{`{"log": {"level": ""}}`, "info"},        // 空回落
		{`{"cache": {"enabled": false}}`, "info"}, // 缺失回落（不破坏既有字段）
	}
	for _, c := range cases {
		path := filepath.Join(t.TempDir(), "config.json")
		if err := os.WriteFile(path, []byte(c.in), 0o600); err != nil {
			t.Fatal(err)
		}
		cfg, err := Load(path)
		if err != nil {
			t.Fatalf("Load(%s): %v", c.in, err)
		}
		if cfg.Log.Level != c.want {
			t.Errorf("Load(%s) Log.Level = %q, want %q", c.in, cfg.Log.Level, c.want)
		}
	}
}
