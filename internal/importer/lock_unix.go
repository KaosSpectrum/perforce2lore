//go:build !windows

package importer

import (
	"errors"
	"fmt"
	"io"
	"os"
	"syscall"
)

// acquireLock holds an exclusive lock on path until the returned closer is closed or the process exits.
func acquireLock(path string) (io.Closer, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, fmt.Errorf("another import is running with %s", path)
		}
		return nil, err
	}
	return f, nil
}
