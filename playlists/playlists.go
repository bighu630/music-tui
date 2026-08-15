// Package playlists 负责命名播放列表的 JSON 持久化：多列表 CRUD、
// 歌曲增删、原子写盘。存储模式与 history 包一致（.tmp + rename）。
package playlists

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"music-tui/model"
)

// List 是一个命名播放列表。
type List struct {
	Name      string        // 列表名（同名列表不允许，重名创建/改名报错）
	Tracks    []model.Track // 歌曲列表（保序；允许同一首歌重复添加）
	CreatedAt time.Time     // 创建时间（展示用）
}

// Store 读写播放列表 JSON 文件；所有方法并发安全。
// 文件损坏时 NewStore 返回错误，由调用方（main）备份重建，与 history 降级一致。
type Store struct {
	mu    sync.Mutex
	path  string
	lists []List
}

// NewStore 加载播放列表文件（不存在则视为空）；父目录自动创建。
func NewStore(path string) (*Store, error) {
	s := &Store{path: path}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("创建播放列表目录: %w", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return s, nil
		}
		return nil, fmt.Errorf("读取播放列表文件: %w", err)
	}
	if len(data) == 0 {
		return s, nil
	}
	if err := json.Unmarshal(data, &s.lists); err != nil {
		return nil, fmt.Errorf("解析播放列表文件: %w", err)
	}
	return s, nil
}

// Lists 返回全部列表副本（保持创建顺序）。
func (s *Store) Lists() []List {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]List, len(s.lists))
	for i, l := range s.lists {
		out[i] = cloneList(l)
	}
	return out
}

// Create 新建命名播放列表；空白名或重名返回错误。
func (s *Store) Create(name string) (List, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	name = strings.TrimSpace(name)
	if name == "" {
		return List{}, fmt.Errorf("列表名不能为空")
	}
	for _, l := range s.lists {
		if l.Name == name {
			return List{}, fmt.Errorf("已存在同名列表「%s」", name)
		}
	}
	l := List{Name: name, Tracks: []model.Track{}, CreatedAt: time.Now()}
	s.lists = append(s.lists, l)
	if err := s.saveLocked(); err != nil {
		return List{}, err
	}
	return cloneList(l), nil
}

// Rename 重命名列表；旧名不存在、新名空白或与既有列表重名返回错误。
func (s *Store) Rename(oldName, newName string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	newName = strings.TrimSpace(newName)
	if newName == "" {
		return fmt.Errorf("列表名不能为空")
	}
	idx := s.indexLocked(oldName)
	if idx < 0 {
		return fmt.Errorf("列表「%s」不存在", oldName)
	}
	for i, l := range s.lists {
		if i != idx && l.Name == newName {
			return fmt.Errorf("已存在同名列表「%s」", newName)
		}
	}
	s.lists[idx].Name = newName
	return s.saveLocked()
}

// Delete 删除指定列表；不存在时返回 nil。
func (s *Store) Delete(name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	idx := s.indexLocked(name)
	if idx < 0 {
		return nil
	}
	s.lists = append(s.lists[:idx], s.lists[idx+1:]...)
	return s.saveLocked()
}

// AddTrack 把歌曲追加到指定列表；列表不存在返回错误。
func (s *Store) AddTrack(name string, track model.Track) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	idx := s.indexLocked(name)
	if idx < 0 {
		return fmt.Errorf("列表「%s」不存在", name)
	}
	s.lists[idx].Tracks = append(s.lists[idx].Tracks, track)
	return s.saveLocked()
}

// AddTracks 把整批歌曲一次性追加到指定列表（单次原子写盘）；
// 列表不存在返回错误（存在性优先，空切片同样报错）；空切片是 no-op（返回 nil，不写盘）。
func (s *Store) AddTracks(name string, tracks []model.Track) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	idx := s.indexLocked(name)
	if idx < 0 {
		return fmt.Errorf("列表「%s」不存在", name)
	}
	if len(tracks) == 0 {
		return nil // no-op：不触发写盘
	}
	s.lists[idx].Tracks = append(s.lists[idx].Tracks, tracks...)
	return s.saveLocked()
}

// RemoveTrack 从指定列表移除第 index 首歌曲（0 基）；
// 列表不存在或下标越界返回错误。
func (s *Store) RemoveTrack(name string, index int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	idx := s.indexLocked(name)
	if idx < 0 {
		return fmt.Errorf("列表「%s」不存在", name)
	}
	if index < 0 || index >= len(s.lists[idx].Tracks) {
		return fmt.Errorf("下标 %d 越界（列表共 %d 首）", index, len(s.lists[idx].Tracks))
	}
	trs := s.lists[idx].Tracks
	s.lists[idx].Tracks = append(trs[:index], trs[index+1:]...)
	return s.saveLocked()
}

// Tracks 返回指定列表的歌曲副本；列表不存在返回 nil。
func (s *Store) Tracks(name string) []model.Track {
	s.mu.Lock()
	defer s.mu.Unlock()
	idx := s.indexLocked(name)
	if idx < 0 {
		return nil
	}
	return append([]model.Track(nil), s.lists[idx].Tracks...)
}

// indexLocked 返回列表下标；不存在返回 -1（调用方须持锁）。
func (s *Store) indexLocked(name string) int {
	for i, l := range s.lists {
		if l.Name == name {
			return i
		}
	}
	return -1
}

// cloneList 深拷贝一个 List（Tracks 切片隔离，防外部修改污染存储）。
func cloneList(l List) List {
	return List{Name: l.Name, Tracks: append([]model.Track(nil), l.Tracks...), CreatedAt: l.CreatedAt}
}

// saveLocked 原子写盘：先写临时文件再重命名（调用方须持锁）。
func (s *Store) saveLocked() error {
	data, err := json.MarshalIndent(s.lists, "", "  ")
	if err != nil {
		return fmt.Errorf("序列化播放列表: %w", err)
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return fmt.Errorf("写入播放列表文件: %w", err)
	}
	if err := os.Rename(tmp, s.path); err != nil {
		return fmt.Errorf("写入播放列表文件: %w", err)
	}
	return nil
}
