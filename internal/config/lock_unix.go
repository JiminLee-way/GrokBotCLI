//go:build unix

package config

import (
	"os"
	"syscall"
)

// Lock takes an exclusive cross-process lock on the profile (used around token refresh).
func (p *Profile) Lock() (unlock func(), err error) {
	f, err := os.OpenFile(p.path(".lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		f.Close()
		return nil, err
	}
	return func() { _ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN); f.Close() }, nil
}
