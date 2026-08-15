package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
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
