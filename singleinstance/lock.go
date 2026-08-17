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
// 先关闭句柄再调用平台释放：Windows 上不能删除仍被打开的文件的句柄
// （os.Remove 对打开文件返回 Access denied 且被忽略，锁文件会残留）；
// unix 的 flock 随文件描述符关闭由内核自动释放（紧随的显式 LOCK_UN
// 对已关闭的 fd 返回 EBADF，被忽略，语义等价）。
func (l *Lock) Close() error {
	if l == nil || l.f == nil {
		return nil
	}
	err := l.f.Close()
	l.f = nil
	releaseLock(l) // 平台实现；使用 l.path/l.pid（l.f 已关闭，unix flock 已随 fd 释放）
	return err
}
