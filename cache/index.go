package cache

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"time"
)

// Entry 是一条缓存记录。
type Entry struct {
	ID         string    `json:"id"`
	File       string    `json:"file"` // 缓存目录内的文件名（SafeName 结果，可含扩展名）
	LastPlayed time.Time `json:"last_played"`
}

// index 是 LRU 索引：entries 按 LastPlayed 升序排列，entries[0] 最旧。
// 内部结构体，Manager 私有使用；非并发安全（由 Manager 持锁调用）。
type index struct {
	entries []Entry
}

// get 返回指定 ID 的条目。
func (ix *index) get(id string) (Entry, bool) {
	for _, e := range ix.entries {
		if e.ID == id {
			return e, true
		}
	}
	return Entry{}, false
}

// upsert 插入或刷新条目：存在则更新 LastPlayed，不存在则追加；之后统一重排（升序）。
// 新插入条目的文件名为 SafeName(id)。
func (ix *index) upsert(id string, at time.Time) {
	ix.upsertFile(id, SafeName(id), at)
}

// upsertFile 同 upsert，但新插入时使用显式文件名（下载完成时含扩展名）。
func (ix *index) upsertFile(id, file string, at time.Time) {
	found := false
	for i := range ix.entries {
		if ix.entries[i].ID == id {
			ix.entries[i].LastPlayed = at
			found = true
			break
		}
	}
	if !found {
		ix.entries = append(ix.entries, Entry{ID: id, File: file, LastPlayed: at})
	}
	sort.SliceStable(ix.entries, func(i, j int) bool {
		return ix.entries[i].LastPlayed.Before(ix.entries[j].LastPlayed)
	})
}

// remove 移除指定 ID 的条目，返回是否移除过。
func (ix *index) remove(id string) bool {
	for i, e := range ix.entries {
		if e.ID == id {
			ix.entries = append(ix.entries[:i], ix.entries[i+1:]...)
			return true
		}
	}
	return false
}

// len 返回条目数。
func (ix *index) len() int { return len(ix.entries) }

// oldest 返回最旧（LastPlayed 最早）的条目，即淘汰候选。
func (ix *index) oldest() (Entry, bool) {
	if len(ix.entries) == 0 {
		return Entry{}, false
	}
	return ix.entries[0], true
}

// load 从磁盘读取索引：文件不存在或为空 → 空索引；JSON 损坏 → 错误。
func load(path string) (*index, error) {
	ix := &index{}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return ix, nil
		}
		return nil, fmt.Errorf("读取缓存索引: %w", err)
	}
	if len(data) == 0 {
		return ix, nil
	}
	if err := json.Unmarshal(data, &ix.entries); err != nil {
		return nil, fmt.Errorf("解析缓存索引: %w", err)
	}
	// 手写/乱序索引按 LastPlayed 升序重排（与 upsert 同规则），保证 oldest() 淘汰选对条目
	sort.SliceStable(ix.entries, func(i, j int) bool {
		return ix.entries[i].LastPlayed.Before(ix.entries[j].LastPlayed)
	})
	return ix, nil
}

// save 原子写盘：先写临时文件再重命名（照抄 history.saveLocked 模式）。
func (ix *index) save(path string) error {
	data, err := json.MarshalIndent(ix.entries, "", "  ")
	if err != nil {
		return fmt.Errorf("序列化缓存索引: %w", err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return fmt.Errorf("写入缓存索引: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("写入缓存索引: %w", err)
	}
	return nil
}
