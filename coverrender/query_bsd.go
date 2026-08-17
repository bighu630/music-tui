//go:build darwin || freebsd || openbsd || netbsd || dragonfly

package coverrender

import "golang.org/x/sys/unix"

const (
	tioGet = unix.TIOCGETA
	tioSet = unix.TIOCSETA
)
