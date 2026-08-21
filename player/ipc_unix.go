//go:build !windows

package player

import (
	"net"
	"os"
)

// dialIPC 连接 IPC socket（Unix 平台为 Unix domain socket）。
func dialIPC(path string) (net.Conn, error) {
	return net.Dial("unix", path)
}

// removeSocket 清理 IPC socket 文件（仅 Unix 平台需要）。
func removeSocket(path string) error {
	return os.Remove(path)
}
