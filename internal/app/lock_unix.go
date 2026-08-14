//go:build unix

package app

import (
	"os"

	"golang.org/x/sys/unix"
)

// lockFile は排他ロックを取る。取れるまで待つ。
func lockFile(f *os.File) error {
	return unix.Flock(int(f.Fd()), unix.LOCK_EX)
}

// unlockFile は排他ロックを外す。
func unlockFile(f *os.File) error {
	return unix.Flock(int(f.Fd()), unix.LOCK_UN)
}
