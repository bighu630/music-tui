//go:build unix

package singleinstance

import (
	"errors"
	"fmt"
	"os"
	"syscall"
)

// Acquire 获取单实例锁。Unix 实现：flock 独占非阻塞锁，
// 进程退出（含崩溃）时内核自动释放，无需清理锁文件。
func Acquire(lockPath string) (*Lock, error) {
	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, fmt.Errorf("打开锁文件失败: %w", err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN) {
			return nil, fmt.Errorf("%w（锁文件: %s）", ErrInstanceRunning, lockPath)
		}
		return nil, fmt.Errorf("获取单实例锁失败: %w", err)
	}
	return &Lock{f: f}, nil
}

// releaseLock 释放锁。unix 的 Acquire 恒为 flock 锁（path 为空），
// 不删除锁文件：删除会产生"解锁-删除"窗口期 inode 竞态（另一进程
// 可能已打开并锁定旧 inode），残留文件无害——每次启动 O_CREATE 复用。
// pid 锁（path 非空；仅测试路径与非 unix 平台产生）走共享清理。
func releaseLock(l *Lock) {
	_ = syscall.Flock(int(l.f.Fd()), syscall.LOCK_UN)
	if l.path != "" {
		releasePidLock(l)
	}
}
