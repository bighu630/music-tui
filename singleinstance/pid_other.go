//go:build !unix && !windows

package singleinstance

// defaultProcessAlive 无可靠进程探测 API，保守视为存活：
// 陈旧锁只能等 pid 重用或人工删除，但绝不误删正在运行的实例的锁。
func defaultProcessAlive(pid int) bool {
	return true
}
