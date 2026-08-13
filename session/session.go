// Package session 负责播放会话（队列 + 进度）的 JSON 持久化，
// 用于退出/崩溃后恢复续播。
package session

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"music-tui/queue"
)

// State 是保存的会话状态：播放队列快照 + 当前进度。
type State struct {
	Queue    queue.Snapshot `json:"queue"`
	Position float64        `json:"position"` // 当前曲目进度（秒）
	Ended    bool           `json:"ended"`    // 退出时当前曲目是否已播完（恢复时跳下一首/从头）
}

// Store 读写会话 JSON 文件；所有方法并发安全。State() 返回 nil 表示无会话。
type Store struct {
	mu    sync.Mutex
	path  string
	state *State
}

// NewStore 加载会话文件（不存在视为无会话）；父目录自动创建。
// 文件损坏（崩溃/断电截断）时返回错误，由调用方按 loadHistory
// 模式备份后重建。
func NewStore(path string) (*Store, error) {
	s := &Store{path: path}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("创建会话目录: %w", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return s, nil
		}
		return nil, fmt.Errorf("读取会话文件: %w", err)
	}
	if len(data) == 0 {
		return s, nil
	}
	var st State
	if err := json.Unmarshal(data, &st); err != nil {
		return nil, fmt.Errorf("解析会话文件: %w", err)
	}
	s.state = &st
	return s, nil
}

// Save 原子写盘保存会话状态（先写临时文件再重命名）。
func (s *Store) Save(st State) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	data, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return fmt.Errorf("序列化会话: %w", err)
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return fmt.Errorf("写入会话文件: %w", err)
	}
	if err := os.Rename(tmp, s.path); err != nil {
		return fmt.Errorf("写入会话文件: %w", err)
	}
	cp := st
	s.state = &cp
	return nil
}

// State 返回已保存会话状态的副本；无会话时返回 nil。
func (s *Store) State() *State {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state == nil {
		return nil
	}
	cp := *s.state
	return &cp
}

// Clear 删除会话文件（无进行中播放时退出调用，表示会话自然结束）。
func (s *Store) Clear() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.state = nil
	if err := os.Remove(s.path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}
