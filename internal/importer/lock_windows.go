//go:build windows

package importer

import (
	"errors"
	"fmt"
	"io"
	"os"
	"syscall"
)

// errorSharingViolation is the Windows error for a file another process holds open.
const errorSharingViolation syscall.Errno = 32

// acquireLock opens path without sharing, so a second process cannot open it until this one closes it or exits.
func acquireLock(path string) (io.Closer, error) {
	name, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	h, err := syscall.CreateFile(name, syscall.GENERIC_READ|syscall.GENERIC_WRITE, 0, nil,
		syscall.OPEN_ALWAYS, syscall.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		if errors.Is(err, errorSharingViolation) {
			return nil, fmt.Errorf("another import is running with %s", path)
		}
		return nil, err
	}
	return os.NewFile(uintptr(h), path), nil
}
