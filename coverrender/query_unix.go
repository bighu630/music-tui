//go:build !windows

package coverrender

import (
	"os"
	"time"

	"golang.org/x/sys/unix"
)

// readResponse unix 实现：临时 raw 模式（canonical 行缓冲读不到无换行的转义应答）
// + select(2) 超时（SetReadDeadline 在 raw 模式 fd 上失效，实测永久阻塞——见
// third_party/go-termimg/pkg/csi 同款修复）。**持续读取直到 timeout 到期或连续
// 无新数据**：多个查询（DA1/kitty/16t）的应答分多条到达，只读一次会漏掉后续
// 应答（16t 的 cell 尺寸应答晚于 DA1 到达）。读完后恢复原终端模式。
func readResponse(timeout time.Duration) []byte {
	fd := int(os.Stdin.Fd())
	old, err := unix.IoctlGetTermios(fd, tioGet)
	if err != nil {
		return nil
	}
	raw := *old
	raw.Lflag &^= unix.ICANON | unix.ECHO | unix.ISIG
	raw.Iflag &^= unix.IXON | unix.ICRNL
	if err := unix.IoctlSetTermios(fd, tioSet, &raw); err != nil {
		return nil
	}
	defer unix.IoctlSetTermios(fd, tioSet, old)

	deadline := time.Now().Add(timeout)
	const idle = 30 * time.Millisecond // 连续无新数据即认为应答流结束
	var out []byte
	for {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			break
		}
		var rfds unix.FdSet
		rfds.Set(fd)
		tv := unix.NsecToTimeval(int64(minDur(idle, remaining)))
		n, err := unix.Select(fd+1, &rfds, nil, nil, &tv)
		if err != nil || n <= 0 {
			break // 超时无新数据：应答流结束
		}
		buf := make([]byte, 4096)
		n, _ = os.Stdin.Read(buf)
		if n <= 0 {
			break
		}
		out = append(out, buf[:n]...)
		if len(out) > 16384 {
			break // 防御：应答异常膨胀
		}
	}
	return out
}

func minDur(a, b time.Duration) time.Duration {
	if a < b {
		return a
	}
	return b
}