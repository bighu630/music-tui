//go:build windows

package coverrender

import (
	"os"
	"time"
)

// readResponse windows 实现：best-effort——控制台读取无法可靠设置 raw 模式，
// 用 goroutine + 超时。Windows 终端（如 Windows Terminal）对 DA1/kitty 查询的
// 应答可能受控制台行缓冲影响，读不到则回落（查询失败不影响任何功能）。
func readResponse(timeout time.Duration) []byte {
	type res struct {
		buf []byte
	}
	ch := make(chan res, 1)
	go func() {
		var buf [256]byte
		n, _ := os.Stdin.Read(buf[:])
		ch <- res{buf: buf[:n]}
	}()
	select {
	case r := <-ch:
		return r.buf
	case <-time.After(timeout):
		return nil
	}
}