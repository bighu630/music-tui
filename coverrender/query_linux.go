//go:build linux

package coverrender

import "golang.org/x/sys/unix"

const (
	tioGet = unix.TCGETS
	tioSet = unix.TCSETS
)
