//go:build darwin

package daemonprocess

import (
	"fmt"
	"sync"
	"syscall"
	"unsafe"

	"github.com/ebitengine/purego"
)

const taskAuditTokenFlavor = 15

type darwinAuditToken [8]uint32

type darwinProcessSignalHandle struct {
	token darwinAuditToken
}

type darwinProcessSignalAPI struct {
	machTaskSelf       func() uint32
	taskNameForPID     func(uint32, int32, *uint32) int32
	taskInfo           func(uint32, int32, *uint32, *uint32) int32
	machPortDeallocate func(uint32, uint32) int32
	procSignal         uintptr
}

var (
	darwinSignalAPIOnce sync.Once
	darwinSignalAPI     darwinProcessSignalAPI
	darwinSignalAPIErr  error
)

func loadDarwinProcessSignalAPI() (darwinProcessSignalAPI, error) {
	darwinSignalAPIOnce.Do(func() {
		handle, err := purego.Dlopen("/usr/lib/libSystem.B.dylib", purego.RTLD_LAZY|purego.RTLD_LOCAL)
		if err != nil {
			darwinSignalAPIErr = err
			return
		}
		for _, symbol := range []struct {
			name   string
			handle uintptr
			target any
		}{
			{name: "mach_task_self", handle: handle, target: &darwinSignalAPI.machTaskSelf},
			{name: "task_name_for_pid", handle: handle, target: &darwinSignalAPI.taskNameForPID},
			{name: "task_info", handle: handle, target: &darwinSignalAPI.taskInfo},
			{name: "mach_port_deallocate", handle: handle, target: &darwinSignalAPI.machPortDeallocate},
		} {
			address, lookupErr := purego.Dlsym(symbol.handle, symbol.name)
			if lookupErr != nil {
				darwinSignalAPIErr = fmt.Errorf("load %s: %w", symbol.name, lookupErr)
				return
			}
			purego.RegisterFunc(symbol.target, address)
		}
		darwinSignalAPI.procSignal, err = purego.Dlsym(handle, "proc_signal_with_audittoken")
		if err != nil {
			darwinSignalAPIErr = fmt.Errorf("load proc_signal_with_audittoken: %w", err)
		}
	})
	return darwinSignalAPI, darwinSignalAPIErr
}

func openPlatformProcessSignalHandle(pid int) (processSignalHandle, error) {
	token, err := captureDarwinAuditToken(pid)
	if err != nil {
		return nil, err
	}
	return &darwinProcessSignalHandle{token: token}, nil
}

func captureDarwinAuditToken(pid int) (darwinAuditToken, error) {
	api, err := loadDarwinProcessSignalAPI()
	if err != nil {
		return darwinAuditToken{}, err
	}
	self := api.machTaskSelf()
	var taskName uint32
	if code := api.taskNameForPID(self, int32(pid), &taskName); code != 0 {
		return darwinAuditToken{}, fmt.Errorf("task_name_for_pid returned Mach error %d", code)
	}
	defer func() { _ = api.machPortDeallocate(self, taskName) }()

	var token darwinAuditToken
	count := uint32(len(token))
	if code := api.taskInfo(taskName, taskAuditTokenFlavor, &token[0], &count); code != 0 {
		return darwinAuditToken{}, fmt.Errorf("read process audit token: Mach error %d", code)
	}
	if count != uint32(len(token)) {
		return darwinAuditToken{}, fmt.Errorf("read process audit token: got %d words, want %d", count, len(token))
	}
	return token, nil
}

func (h *darwinProcessSignalHandle) Signal(signal syscall.Signal) error {
	api, err := loadDarwinProcessSignalAPI()
	if err != nil {
		return err
	}
	result, _, errno := purego.SyscallN(
		api.procSignal,
		uintptr(unsafe.Pointer(&h.token[0])),
		uintptr(signal),
	)
	if result != 0 {
		// libproc returns an errno value directly. Retain the conventional -1
		// fallback for older implementations that set thread-local errno.
		if int32(result) == -1 && errno != 0 {
			return syscall.Errno(errno)
		}
		return syscall.Errno(result)
	}
	return nil
}

func (h *darwinProcessSignalHandle) Close() error { return nil }
