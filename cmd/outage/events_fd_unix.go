//go:build aix || android || darwin || dragonfly || freebsd || illumos || ios || linux || netbsd || openbsd || solaris

package main

// This file duplicates Unix event descriptors and performs no-wait writes.

import (
	"fmt"
	"os"
	"syscall"

	"golang.org/x/sys/unix"
)

func validateEventDescriptor(fd int) error {
	flags, err := unix.FcntlInt(uintptr(fd), unix.F_GETFL, 0)
	if err != nil {
		return err
	}
	if flags&unix.O_ACCMODE == unix.O_RDONLY {
		return fmt.Errorf("descriptor is not writable")
	}
	if flags&unix.O_NONBLOCK == 0 {
		return fmt.Errorf("descriptor must have O_NONBLOCK set")
	}
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		return err
	}
	switch stat.Mode & unix.S_IFMT {
	case unix.S_IFIFO, unix.S_IFSOCK:
		return nil
	default:
		return fmt.Errorf("descriptor must be a pipe, FIFO, or socket")
	}
}

func setEventDescriptorNonInheritable(fd int) error {
	_, err := unix.FcntlInt(uintptr(fd), unix.F_SETFD, unix.FD_CLOEXEC)
	return err
}

func duplicateEventFile(fd int) (*os.File, error) {
	// Dup creates an inheritable descriptor. Hold ForkLock until CLOEXEC is set
	// so a concurrent hook spawn cannot inherit the temporary descriptor.
	syscall.ForkLock.RLock()
	ownedFD, err := unix.Dup(fd)
	if err == nil {
		err = setEventDescriptorNonInheritable(ownedFD)
		if err != nil {
			_ = unix.Close(ownedFD)
		}
	}
	syscall.ForkLock.RUnlock()
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(ownedFD), "outage events")
	if file == nil {
		_ = unix.Close(ownedFD)
		return nil, fmt.Errorf("invalid duplicated file descriptor %d", ownedFD)
	}
	return file, nil
}

func writeEvent(file *os.File, data []byte) (int, error) {
	connection, err := file.SyscallConn()
	if err != nil {
		return 0, err
	}
	var written int
	var writeErr error
	if err := connection.Control(func(raw uintptr) {
		fd := int(raw)
		flags, err := unix.FcntlInt(uintptr(fd), unix.F_GETFL, 0)
		if err != nil {
			writeErr = err
			return
		}
		if flags&unix.O_NONBLOCK == 0 {
			writeErr = fmt.Errorf("event descriptor no longer has O_NONBLOCK set")
			return
		}
		written, writeErr = unix.Write(fd, data)
	}); err != nil {
		return 0, err
	}
	return written, writeErr
}
