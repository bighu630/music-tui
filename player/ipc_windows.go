//go:build windows

package player

import (
	"net"
	"time"

	"gopkg.in/natefinch/npipe.v2"
)

// dialIPC 连接 IPC 命名管道（Windows 平台）。使用 DialTimeout 带超时快速探测，
// 超时即视为 socket 未就绪返回 error，避免 npipe.Dial 无限等待阻塞 waitForSocket。
func dialIPC(path string) (net.Conn, error) {
	return npipe.DialTimeout(path, 50*time.Millisecond)
}

// removeSocket 清理 IPC 资源（Windows 命名管道无需文件清理）。
func removeSocket(path string) error {
	return nil
}
