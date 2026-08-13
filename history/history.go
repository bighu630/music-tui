// Package history 负责播放历史的 JSON 持久化：去重置顶、上限裁剪。
package history

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"music-tui/model"
)

// MaxEntries 是历史记录条数上限，超出时裁剪最旧记录。
const MaxEntries = 100

// Entry 是一条播放记录，完整嵌入 Track，重播无需重新搜索。
type Entry struct {
	Track    model.Track
	PlayedAt time.Time
}

// Store 读写历史 JSON 文件；所有方法并发安全。
type Store struct {
	mu      sync.Mutex
	path    string
	entries []Entry
}

// NewStore 加载历史文件（不存在则视为空历史）；父目录自动创建。
func NewStore(path string) (*Store, error) {
	s := &Store{path: path}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("创建历史目录: %w", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return s, nil
		}
		return nil, fmt.Errorf("读取历史文件: %w", err)
	}
	if len(data) == 0 {
		return s, nil
	}
	if err := json.Unmarshal(data, &s.entries); err != nil {
		return nil, fmt.Errorf("解析历史文件: %w", err)
	}
	return s, nil
}

// Add 记录一次播放：同 ID+Source 的旧记录移除后置顶；超出上限裁剪。
func (s *Store) Add(track model.Track) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now()
	s.removeLocked(track.ID, track.Source)
	s.entries = append([]Entry{{Track: track, PlayedAt: now}}, s.entries...)
	if len(s.entries) > MaxEntries {
		s.entries = s.entries[:MaxEntries]
	}
	return s.saveLocked()
}

// Remove 删除指定 ID+Source 的记录；不存在时返回 nil。
func (s *Store) Remove(id, source string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.removeLocked(id, source) {
		return nil
	}
	return s.saveLocked()
}

// Clear 清空全部历史（写盘为空数组 []，而非 null）。
func (s *Store) Clear() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.entries = []Entry{}
	return s.saveLocked()
}

// Entries 返回历史副本（新记录在前）。
func (s *Store) Entries() []Entry {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Entry(nil), s.entries...)
}

// removeLocked 移除同 ID+Source 的记录，返回是否移除过（调用方须持锁）。
func (s *Store) removeLocked(id, source string) bool {
	removed := false
	out := make([]Entry, 0, len(s.entries))
	for _, e := range s.entries {
		if e.Track.ID == id && e.Track.Source == source {
			removed = true
			continue
		}
		out = append(out, e)
	}
	s.entries = out
	return removed
}

// saveLocked 原子写盘：先写临时文件再重命名（调用方须持锁）。
func (s *Store) saveLocked() error {
	data, err := json.MarshalIndent(s.entries, "", "  ")
	if err != nil {
		return fmt.Errorf("序列化历史: %w", err)
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return fmt.Errorf("写入历史文件: %w", err)
	}
	if err := os.Rename(tmp, s.path); err != nil {
		return fmt.Errorf("写入历史文件: %w", err)
	}
	return nil
}
