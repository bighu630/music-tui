// Package singleinstance 提供单实例检测：确保同一时刻只有一个
// music-tui 实例在运行。
//
// 平台策略：Unix 上用 flock（进程退出/崩溃时内核自动释放锁，无需
// 清理锁文件）；非 Unix 平台用 pid 文件 + 陈旧检测（崩溃残留的锁
// 文件由下次启动时检测 pid 是否存活来清理）。
package singleinstance

import (
	"errors"
	"os"
)

// ErrInstanceRunning 表示已有另一个 music-tui 实例在运行。
var ErrInstanceRunning = errors.New("已有 music-tui 实例在运行")

// Lock 持有单实例锁；Close 释放。Unix 上进程退出（含崩溃）内核自动释放；
// 非 Unix 平台由陈旧检测兜底。
type Lock struct {
	f    *os.File
	path string // pid 方案用于退出清理校验；unix 恒空
	pid  int    // pid 方案本实例 pid；unix 恒 0
}

// Close 释放单实例锁。重复调用与 nil 接收者均为安全 no-op。
func (l *Lock) Close() error {
	if l == nil || l.f == nil {
		return nil
	}
	releaseLock(l) // 平台实现；使用 l.f/l.path/l.pid，必须在置 nil 前调用
	err := l.f.Close()
	l.f = nil // 无论 Close 成败都置 nil，保证二次调用安全 no-op
	return err
}
