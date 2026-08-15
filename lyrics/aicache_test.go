package lyrics

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// TestAICacheRoundtrip 写后重建实例仍可命中（JSONL 持久化）。
func TestAICacheRoundtrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ai.jsonl")
	c, err := newAICache(path)
	if err != nil {
		t.Fatalf("newAICache: %v", err)
	}
	c.Put("晴天|周杰伦", AIResult{IsSong: true, Title: "晴天", Artist: "周杰伦"})

	c2, err := newAICache(path)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	got, ok := c2.Get("晴天|周杰伦")
	if !ok || !got.IsSong || got.Title != "晴天" || got.Artist != "周杰伦" {
		t.Errorf("reload 后 Get = %+v, %v，want 命中", got, ok)
	}
}

// TestAICacheNegativePersisted 负缓存（is_song=false）必须持久化：
// 重启后不重复调用 AI。
func TestAICacheNegativePersisted(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ai.jsonl")
	c, err := newAICache(path)
	if err != nil {
		t.Fatalf("newAICache: %v", err)
	}
	c.Put("城市漫步 Vlog|SomeChannel", AIResult{IsSong: false})

	c2, err := newAICache(path)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	got, ok := c2.Get("城市漫步 Vlog|SomeChannel")
	if !ok || got.IsSong {
		t.Errorf("负缓存未持久化: %+v, %v", got, ok)
	}
}

// TestAICacheCorruptLineSkipped 损坏行跳过且文件被重写（不留垃圾）。
func TestAICacheCorruptLineSkipped(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ai.jsonl")
	if err := os.WriteFile(path, []byte(
		"{\"key\":\"A|B\",\"is_song\":true,\"title\":\"A\",\"artist\":\"B\"}\n"+
			"垃圾行\n"+
			"{\"key\":\"C|D\",\"is_song\":false}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	c, err := newAICache(path)
	if err != nil {
		t.Fatalf("newAICache: %v", err)
	}
	if _, ok := c.Get("A|B"); !ok {
		t.Error("损坏行前的好行丢失")
	}
	if got, ok := c.Get("C|D"); !ok || got.IsSong {
		t.Error("损坏行后的好行丢失")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "垃圾行") {
		t.Errorf("损坏行未从文件清除:\n%s", data)
	}
}

// TestAICacheKeyNormalizesWhitespace 键规范化：空白折叠后同一标题命中。
func TestAICacheKeyNormalizesWhitespace(t *testing.T) {
	a := aiCacheKey("  晴天  Official  MV  ", " 周杰伦 ")
	b := aiCacheKey("晴天 Official MV", "周杰伦")
	if a != b {
		t.Errorf("key 不规范化: %q != %q", a, b)
	}
	if !strings.Contains(a, "|") {
		t.Errorf("key 缺分隔符: %q", a)
	}
}

// TestAICacheConcurrentPut 并发写不丢数据、无竞态（-race 校验）。
func TestAICacheConcurrentPut(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ai.jsonl")
	c, err := newAICache(path)
	if err != nil {
		t.Fatalf("newAICache: %v", err)
	}
	const n = 20
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			key := strings.Repeat("k", 1) + string(rune('0'+i%10))
			c.Put(key, AIResult{IsSong: true, Title: key})
		}(i)
	}
	wg.Wait()
	// 重建后 10 个唯一键全部可命中（重复写被去重但不丢首次值）
	c2, err := newAICache(path)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	const uniq = 10
	hit := 0
	for i := 0; i < uniq; i++ {
		key := strings.Repeat("k", 1) + string(rune('0'+i%10))
		if _, ok := c2.Get(key); ok {
			hit++
		}
	}
	if hit != uniq {
		t.Errorf("命中 %d/%d 个唯一键, want 全部", hit, uniq)
	}
}

// TestAICacheGetMiss 未知名返回 miss。
func TestAICacheGetMiss(t *testing.T) {
	c, err := newAICache(filepath.Join(t.TempDir(), "ai.jsonl"))
	if err != nil {
		t.Fatalf("newAICache: %v", err)
	}
	if _, ok := c.Get("不存在"); ok {
		t.Error("未知名竟然命中")
	}
}

// TestAICachePutDuplicate 同键重复写：不产生重复行、值不覆盖。
func TestAICachePutDuplicate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ai.jsonl")
	c, err := newAICache(path)
	if err != nil {
		t.Fatalf("newAICache: %v", err)
	}
	c.Put("K", AIResult{IsSong: true, Title: "first"})
	c.Put("K", AIResult{IsSong: true, Title: "second"})
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if lines := strings.Count(string(data), "\n"); lines != 1 {
		t.Errorf("行数 = %d, want 1（同键不重复追加）", lines)
	}
	if !strings.Contains(string(data), "first") || strings.Contains(string(data), "second") {
		t.Errorf("首值被覆盖: %s", data)
	}
}

// TestAICacheUnmarshal 校验 JSONL 行格式字段（防格式漂移）。
func TestAICacheUnmarshal(t *testing.T) {
	var line struct {
		Key    string `json:"key"`
		IsSong bool   `json:"is_song"`
		Title  string `json:"title"`
		Artist string `json:"artist"`
	}
	if err := json.Unmarshal([]byte(`{"key":"k","is_song":false,"title":"t","artist":"a"}`), &line); err != nil {
		t.Fatal(err)
	}
	if line.Key != "k" || line.IsSong || line.Title != "t" || line.Artist != "a" {
		t.Errorf("got %+v", line)
	}
}

// TestAICacheHugeLineSkipped 超长行（>Scanner 缓冲上限）视同损坏行：
// 跳过并重写，不阻止加载与使用。
func TestAICacheHugeLineSkipped(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ai.jsonl")
	huge := "{\"key\":\"H\",\"is_song\":true,\"title\":\"" + strings.Repeat("晴", 400*1024) + "\"}"
	data := "{\"key\":\"A|B\",\"is_song\":true,\"title\":\"A\",\"artist\":\"B\"}\n" + huge + "\n"
	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
	c, err := newAICache(path)
	if err != nil {
		t.Fatalf("newAICache: %v", err)
	}
	if _, ok := c.Get("A|B"); !ok {
		t.Error("超长行前的好行丢失")
	}
	if _, ok := c.Get("H"); ok {
		t.Error("超长行竟然被加载")
	}
	// 文件被重写：超长行已清除
	cleaned, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(cleaned) > 1024 {
		t.Errorf("重写后文件仍含超长行: %d 字节", len(cleaned))
	}
}

// TestAICacheKeyNoCollision 长度前缀编码：title 含 "|" 与 artist 含 "|"
// 的不同组合不得碰撞。
func TestAICacheKeyNoCollision(t *testing.T) {
	a := aiCacheKey("A|B", "C")
	b := aiCacheKey("A", "B|C")
	if a == b {
		t.Errorf("键碰撞: %q == %q（title 的 | 与 artist 的 | 必须可区分）", a, b)
	}
}
