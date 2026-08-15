package lyrics

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// aiCache 是 AI 识别结果的 JSONL 缓存（内存 map + 文件追加持久化）。
// 负缓存（is_song=false）同样入库：同一标题不再重复调用 AI 烧钱。
// 损坏行加载时跳过并原子重写文件；追加写失败仅影响持久化，不阻塞
// 本次会话命中（缓存是增强功能，绝不影响主流程）。
type aiCache struct {
	path string
	mu   sync.Mutex
	m    map[string]AIResult
}

// aiCacheLine 是 JSONL 单行格式。
type aiCacheLine struct {
	Key    string `json:"key"`
	IsSong bool   `json:"is_song"`
	Title  string `json:"title"`
	Artist string `json:"artist"`
}

// newAICache 创建并加载缓存：目录自动创建，损坏行跳过，若存在损坏行
// 则清理重写文件。
func newAICache(path string) (*aiCache, error) {
	c := &aiCache{path: path, m: make(map[string]AIResult)}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("创建 AI 缓存目录: %w", err)
	}
	dropped, err := c.load()
	if err != nil {
		return nil, err
	}
	if dropped > 0 {
		if err := c.rewrite(); err != nil {
			return nil, err
		}
	}
	return c, nil
}

// load 读入全部行，返回跳过的损坏行数。
func (c *aiCache) load() (int, error) {
	f, err := os.Open(c.path)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, fmt.Errorf("读取 AI 缓存: %w", err)
	}
	defer f.Close()
	dropped := 0
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var rec aiCacheLine
		if err := json.Unmarshal([]byte(line), &rec); err != nil || rec.Key == "" {
			dropped++
			continue
		}
		c.m[rec.Key] = AIResult{IsSong: rec.IsSong, Title: rec.Title, Artist: rec.Artist}
	}
	if err := sc.Err(); err != nil {
		return 0, fmt.Errorf("扫描 AI 缓存: %w", err)
	}
	return dropped, nil
}

// Get 按 key 查询；未命中返回 false。
func (c *aiCache) Get(key string) (AIResult, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	r, ok := c.m[key]
	return r, ok
}

// Put 存入结果（内存 + 追加文件一行）；同键已存在时不覆盖不追加
// （首次结果为准）。
func (c *aiCache) Put(key string, r AIResult) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, ok := c.m[key]; ok {
		return
	}
	c.m[key] = r
	// 持久化失败仅丢弃该行（下次启动重新识别），不影响本次运行
	_ = c.appendLine(key, r)
}

// appendLine 追加一行 JSONL。
func (c *aiCache) appendLine(key string, r AIResult) error {
	data, err := json.Marshal(aiCacheLine{Key: key, IsSong: r.IsSong, Title: r.Title, Artist: r.Artist})
	if err != nil {
		return err
	}
	f, err := os.OpenFile(c.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.Write(append(data, '\n'))
	return err
}

// rewrite 用内存内容原子重写文件（清理加载时发现的损坏行）。
func (c *aiCache) rewrite() error {
	f, err := os.CreateTemp(filepath.Dir(c.path), ".ai-cache-*")
	if err != nil {
		return fmt.Errorf("创建 AI 缓存临时文件: %w", err)
	}
	tmp := f.Name()
	for key, r := range c.m {
		data, err := json.Marshal(aiCacheLine{Key: key, IsSong: r.IsSong, Title: r.Title, Artist: r.Artist})
		if err != nil {
			f.Close()
			os.Remove(tmp)
			return err
		}
		if _, err := f.Write(append(data, '\n')); err != nil {
			f.Close()
			os.Remove(tmp)
			return err
		}
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, c.path)
}

// aiCacheKey 规范化 AI 缓存键：title/artist 折叠空白（换行/制表/连续
// 空格统一为单空格）后以 "|" 连接——同一视频标题的任何空白写法命中
// 同一缓存项。
func aiCacheKey(title, artist string) string {
	norm := func(s string) string {
		return strings.Join(strings.Fields(strings.TrimSpace(s)), " ")
	}
	return norm(title) + "|" + norm(artist)
}
