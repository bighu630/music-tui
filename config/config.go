// Package config 负责 music-tui 的 JSON 配置文件读写：首次运行自动生成默认配置，
// 文件损坏时返回错误（由 main 层备份重建），字段缺失逐项回落默认值。
package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"music-tui/cache"
	"music-tui/logger"
)

// DefaultMaxEntries 是缓存歌曲数上限默认值。
const DefaultMaxEntries = 100

// DefaultOpenAIModel 是 OpenAI 识别默认模型（openai.model 缺失时回落；
// 与 lyrics 包 DefaultAIModel 保持一致）。
const DefaultOpenAIModel = "gpt-4o-mini"

// OpenAI 是 OpenAI 增强歌词匹配配置：api_key 为空 = 整个 AI 路径禁用
// （行为与未启用增强完全一致）；model 为空回落 DefaultOpenAIModel；
// base_url 为空 = OpenAI 官方 API（可填任何 OpenAI 协议兼容服务，
// 如 DeepSeek/自托管网关）。
type OpenAI struct {
	APIKey  string `json:"api_key"`
	Model   string `json:"model"`
	BaseURL string `json:"base_url"`
}

// Ytdlp 是 yt-dlp 全局附加参数配置：Headers 非空时以 --add-header 附加到
// 每次 yt-dlp 调用（cookie 文件由 ytm.Store 管理，不在配置文件中）。
// Headers 为 nil/空 = 未配置（行为与不附加完全一致）。
// header 名须匹配 [\w-]+（字母数字与连字符）：非法键（如含空格）会让
// 整次 yt-dlp 调用报错。
type Ytdlp struct {
	Headers map[string]string `json:"headers"`
}

// Log 是日志配置：Level 为 "debug"/"info"/"warn"/"error"（大小写不敏感），
// 缺失/空/非法回落 "info"（默认级别）。
type Log struct {
	Level string `json:"level"`
}

// Config 是顶层配置：缓存设置（嵌入 cache.Options，json tag 即文件格式）
// + OpenAI 增强歌词配置 + yt-dlp 附加参数配置 + 日志配置。
type Config struct {
	Cache  cache.Options `json:"cache"`
	OpenAI OpenAI        `json:"openai"`
	Ytdlp  Ytdlp         `json:"ytdlp"`
	Log    Log           `json:"log"`
}

// Default 返回默认配置：缓存开启、上限 DefaultMaxEntries、
// 目录为 os.UserCacheDir()/music-tui（UserCacheDir 失败返回错误）。
func Default() (*Config, error) {
	dir, err := os.UserCacheDir()
	if err != nil {
		return nil, fmt.Errorf("获取用户缓存目录: %w", err)
	}
	return &Config{Cache: cache.Options{
		Enabled:    true,
		MaxEntries: DefaultMaxEntries,
		Dir:        filepath.Join(dir, "music-tui"),
	}, OpenAI: OpenAI{
		Model: DefaultOpenAIModel,
	}, Log: Log{
		Level: "info",
	}}, nil
}

// Load 加载配置文件：
//   - 文件不存在 → 创建父目录 + 写入默认配置（首次运行生成）+ 返回默认值
//   - 空文件 → 返回默认值（不写盘）
//   - JSON 损坏 → 返回错误（原文件保持不动，由 main 层备份重建）
//   - 字段缺失/非法 → 逐项回落默认：Enabled 缺失 → true（显式 false 保留）、
//     MaxEntries<1 → DefaultMaxEntries、Dir=="" → 默认目录；
//     ytdlp.headers 缺失/null → nil，显式 {} → 空 map（均为未配置）
//
// 返回的 Config 始终是规范化后的完整值。
func Load(path string) (*Config, error) {
	d, err := Default()
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			if err := d.Save(path); err != nil {
				return nil, err
			}
			return d, nil
		}
		return nil, fmt.Errorf("读取配置文件: %w", err)
	}
	if len(data) == 0 {
		return d, nil
	}

	// 指针字段区分「缺失」与「零值」：缺失回落默认，显式值原样保留。
	var raw struct {
		Cache struct {
			Enabled    *bool   `json:"enabled"`
			MaxEntries *int    `json:"max_entries"`
			Dir        *string `json:"dir"`
		} `json:"cache"`
		OpenAI struct {
			APIKey  *string `json:"api_key"`
			Model   *string `json:"model"`
			BaseURL *string `json:"base_url"`
		} `json:"openai"`
		Ytdlp struct {
			Headers map[string]string `json:"headers"`
		} `json:"ytdlp"`
		Log struct {
			Level *string `json:"level"`
		} `json:"log"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("解析配置文件: %w", err)
	}

	c := &Config{Cache: cache.Options{
		Enabled:    true,
		MaxEntries: DefaultMaxEntries,
		Dir:        d.Cache.Dir,
	}, OpenAI: OpenAI{
		Model: DefaultOpenAIModel,
	}, Log: Log{
		Level: "info",
	}}
	if raw.Cache.Enabled != nil {
		c.Cache.Enabled = *raw.Cache.Enabled
	}
	if raw.Cache.MaxEntries != nil && *raw.Cache.MaxEntries >= 1 {
		c.Cache.MaxEntries = *raw.Cache.MaxEntries
	}
	if raw.Cache.Dir != nil && *raw.Cache.Dir != "" {
		c.Cache.Dir = *raw.Cache.Dir
	}
	// OpenAI：api_key 缺失/空 → 禁用（保持零值）；model 缺失/空 → 默认；
	// base_url 缺失/空 → 官方默认（客户端回落）
	if raw.OpenAI.APIKey != nil {
		c.OpenAI.APIKey = *raw.OpenAI.APIKey
	}
	if raw.OpenAI.Model != nil && *raw.OpenAI.Model != "" {
		c.OpenAI.Model = *raw.OpenAI.Model
	}
	if raw.OpenAI.BaseURL != nil {
		c.OpenAI.BaseURL = *raw.OpenAI.BaseURL
	}
	// Log：level 缺失 → 默认 "info"；显式值经 logger 规范化（非法回落 "info"）
	if raw.Log.Level != nil {
		c.Log.Level = logger.NormalizeLevel(*raw.Log.Level)
	}
	// Ytdlp：map 字段天然区分「缺失/null → nil」与「显式 {} → 空 map」；
	// nil 与空 map 语义一致，均为未配置（不附加任何参数）。
	if raw.Ytdlp.Headers != nil {
		c.Ytdlp.Headers = raw.Ytdlp.Headers
	}
	return c, nil
}

// Save 原子写盘：MarshalIndent("", "  ") 后写临时文件再 rename；父目录自动创建。
func (c *Config) Save(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("创建配置目录: %w", err)
	}
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return fmt.Errorf("序列化配置: %w", err)
	}
	tmp := path + ".tmp"
	// 清陈旧 tmp（可能为崩溃残留的旧权限文件；os.WriteFile 不 chmod
	// 已存在文件，必须先移除才能保证 0600 生效）
	_ = os.Remove(tmp)
	// 0600：配置文件含 OpenAI API key，禁止其他本地用户读取
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return fmt.Errorf("写入配置文件: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("写入配置文件: %w", err)
	}
	return nil
}
