//go:build windows

package singleinstance

import (
	"fmt"
	"os/exec"
	"strings"
)

// defaultProcessAlive 用 tasklist 查询 pid 对应的进程是否存在。
// 查询失败时保守返回 true（无法检测视为存活，宁可不启动也不误删
// 正在运行的实例的锁）。
func defaultProcessAlive(pid int) bool {
	out, err := exec.Command("tasklist", "/FI", fmt.Sprintf("PID eq %d", pid), "/NH").Output()
	if err != nil {
		return true
	}
	return strings.Contains(string(out), fmt.Sprintf("%d", pid))
}
