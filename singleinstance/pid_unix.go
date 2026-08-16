//go:build unix

package singleinstance

import "syscall"

// defaultProcessAlive 用 kill(pid, 0) 探测进程存活：
// 返回 nil → 进程存在；ESRCH → 无此进程（false）；
// EPERM → 进程存在但无权限（true，保守视为存活）。
// 该文件只用于在 Unix 上给 processAlive 提供默认值（供 pidlock
// 测试注入基准）；unix 的 Acquire 走 flock，不经过 pid 方案。
func defaultProcessAlive(pid int) bool {
	return syscall.Kill(pid, 0) == nil
}
