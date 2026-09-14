//go:build windows

package main

// This file duplicates Windows event handles and performs no-wait pipe writes.

import (
	"fmt"
	"os"

	"golang.org/x/sys/windows"
)

func setEventDescriptorNonInheritable(fd int) error {
	return windows.SetHandleInformation(windows.Handle(fd), windows.HANDLE_FLAG_INHERIT, 0)
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
		fileType, err := windows.GetFileType(handle)
		if err != nil {
			writeErr = err
			return
		}
		if fileType != windows.FILE_TYPE_PIPE {
			// Disk handles do not expose a named-pipe wait mode and can be
			// written directly.
			writeErr = windows.WriteFile(handle, data, &written, nil)
			return
		}
		writeErr = writeEventPipe(handle, data, &written)
	}); err != nil {
		return 0, err
	}
	return int(written), writeErr
}

func writeEventPipe(handle windows.Handle, data []byte, written *uint32) error {
	var originalMode uint32
	if err := windows.GetNamedPipeHandleState(handle, &originalMode, nil, nil, nil, nil, 0); err != nil {
		return fmt.Errorf("get event pipe mode: %w", err)
	}
	nowaitMode := originalMode | windows.PIPE_NOWAIT
	if err := windows.SetNamedPipeHandleState(handle, &nowaitMode, nil, nil); err != nil {
		return fmt.Errorf("set event pipe no-wait mode: %w", err)
	}

	writeErr := windows.WriteFile(handle, data, written, nil)
	if err := windows.SetNamedPipeHandleState(handle, &originalMode, nil, nil); err != nil {
		if writeErr != nil {
			return fmt.Errorf("%w (restore event pipe mode: %v)", writeErr, err)
		}
		return fmt.Errorf("restore event pipe mode: %w", err)
	}
	return writeErr
}
