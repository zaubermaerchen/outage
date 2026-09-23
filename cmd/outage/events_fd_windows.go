//go:build windows

package main

// This file duplicates Windows event handles and performs no-wait pipe writes.

import (
	"fmt"
	"os"

	"golang.org/x/sys/windows"
)

func validateEventDescriptor(fd int) error {
	handle := windows.Handle(fd)
	fileType, err := windows.GetFileType(handle)
	if err != nil {
		return err
	}
	if fileType != windows.FILE_TYPE_PIPE {
		return fmt.Errorf("handle must be a pipe")
	}
	if err := validateEventPipeMode(handle); err != nil {
		return err
	}
	return checkWindowsPipeWritable(handle)
}

func validateEventPipeMode(handle windows.Handle) error {
	var mode uint32
	if err := windows.GetNamedPipeHandleState(handle, &mode, nil, nil, nil, nil, 0); err != nil {
		return fmt.Errorf("verify event pipe mode: %w", err)
	}
	if mode&windows.PIPE_NOWAIT == 0 {
		return fmt.Errorf("event pipe must have PIPE_NOWAIT set")
	}
	return nil
}

func checkWindowsPipeWritable(handle windows.Handle) error {
	var duplicate windows.Handle
	if err := windows.DuplicateHandle(
		windows.CurrentProcess(),
		handle,
		windows.CurrentProcess(),
		&duplicate,
		windows.FILE_WRITE_DATA,
		false,
		0,
	); err != nil {
		return err
	}
	return windows.CloseHandle(duplicate)
}

func duplicateEventFile(fd int) (*os.File, error) {
	var owned windows.Handle
	if err := windows.DuplicateHandle(
		windows.CurrentProcess(),
		windows.Handle(fd),
		windows.CurrentProcess(),
		&owned,
		0,
		false,
		windows.DUPLICATE_SAME_ACCESS,
	); err != nil {
		return nil, err
	}
	closeOwnedHandle := true
	defer func() {
		if closeOwnedHandle {
			_ = windows.CloseHandle(owned)
		}
	}()

	file := os.NewFile(uintptr(owned), "outage events")
	if file == nil {
		return nil, fmt.Errorf("invalid duplicated event handle %v", owned)
	}
	closeOwnedHandle = false
	return file, nil
}

func writeEvent(file *os.File, data []byte) (int, error) {
	connection, err := file.SyscallConn()
	if err != nil {
		return 0, err
	}
	var written uint32
	var writeErr error
	if err := connection.Control(func(raw uintptr) {
		handle := windows.Handle(raw)
		if err := validateEventPipeMode(handle); err != nil {
			writeErr = err
			return
		}
		writeErr = windows.WriteFile(handle, data, &written, nil)
	}); err != nil {
		return 0, err
	}
	return int(written), writeErr
}
