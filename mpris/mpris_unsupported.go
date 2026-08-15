//go:build !linux

// Package mpris 在非 Linux 平台提供 no-op 桩：D-Bus 媒体控制仅 Linux 可用，
// 桩保持与 Linux 实现相同的 API，保证 main.go 无平台分支。
package mpris

import (
	"music-tui/model"
	"music-tui/player"
	"music-tui/queue"
)

// playerLike 与 mpris_linux.go 中定义保持一致（两文件互斥编译，
// 仅用于 NewServer 签名匹配；桩不使用该参数）。
type playerLike interface {
	Subscribe() (<-chan player.Event, func())
	Play(url string) error
	Pause() error
	Resume() error
	Seek(seconds float64) error
	SetVolume(percent float64) error
	Volume() (float64, error)
}

// controller 与 mpris_linux.go 中定义保持一致（两文件互斥编译，
// 仅用于 SetController 签名匹配；桩不使用该参数）。
type controller interface {
	PlayNext() error
	PlayPrevious() error
	SetMode(queue.Mode)
	Mode() queue.Mode
	Len() int
}

// Server 非 Linux 平台下的 no-op 桩。
type Server struct{}

// NewServer 创建 no-op 桩（不连接任何总线）。
func NewServer(p playerLike) *Server { return &Server{} }

// Start 非 Linux 平台直接成功（什么都不做）。
func (s *Server) Start() error { return nil }

// Close no-op。
func (s *Server) Close() error { return nil }

// SetTrack no-op。
func (s *Server) SetTrack(t *model.Track) {}

// SetController no-op（注入队列控制器）。
func (s *Server) SetController(ctrl controller) {}

// SyncMode no-op（同步播放模式投影）。
func (s *Server) SyncMode(m queue.Mode) {}
