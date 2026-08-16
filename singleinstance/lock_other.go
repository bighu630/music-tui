//go:build !linux && !darwin && !freebsd && !openbsd && !netbsd && !dragonfly && !illumos

package singleinstance

// Acquire 获取单实例锁。非 flock 平台（windows、solaris、aix、plan9
// 等）用 pid 文件方案：崩溃残留的锁文件由下次启动的陈旧检测清理。
func Acquire(lockPath string) (*Lock, error) {
	return acquirePidLock(lockPath)
}

// releaseLock 释放 pid 锁：删除前校验 pid 是自己的（防 pid 重用误删）；
// 崩溃残留由下次启动的陈旧检测清理。O_EXCL 创建的锁无 flock 语义，
// 无需解锁。
func releaseLock(l *Lock) {
	releasePidLock(l)
}
