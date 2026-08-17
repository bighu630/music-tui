//go:build !windows

package coverrender

import (
	"os"
	"time"

	"golang.org/x/sys/unix"
)

// readResponse unix 实现：临时 raw 模式（canonical 行缓冲读不到无换行的转义应答）
// + select(2) 超时（SetReadDeadline 在 raw 模式 fd 上失效，实测永久阻塞——见
// third_party/go-termimg/pkg/csi 同款修复）。读一次即返回，恢复原终端模式。
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

	var rfds unix.FdSet
	rfds.Set(fd)
	tv := unix.NsecToTimeval(int64(timeout))
	n, err := unix.Select(fd+1, &rfds, nil, nil, &tv)
	if err != nil || n <= 0 {
		return nil
	}
	buf := make([]byte, 256)
	n, _ = os.Stdin.Read(buf)
	if n <= 0 {
		return nil
	}
	return buf[:n]
}