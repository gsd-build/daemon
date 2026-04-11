//go:build darwin

package session

import (
	"encoding/binary"
	"fmt"
	"syscall"
	"unsafe"
)

// hwMemsize is the MIB for hw.memsize on Darwin: CTL_HW=6, HW_MEMSIZE=24.
var hwMemsize = [2]int32{6, 24}

func systemMemory() (total, avail uint64, err error) {
	// Read hw.memsize (uint64) using the raw sysctl syscall.
	// syscall.SysctlUint32 only handles 32-bit values; we call SYS___SYSCTL directly.
	var n uintptr = 8
	buf := make([]byte, 8)
	_, _, errno := syscall.Syscall6(
		syscall.SYS___SYSCTL,
		uintptr(unsafe.Pointer(&hwMemsize[0])),
		2,
		uintptr(unsafe.Pointer(&buf[0])),
		uintptr(unsafe.Pointer(&n)),
		0,
		0,
	)
	if errno != 0 {
		return 0, 0, fmt.Errorf("sysctl hw.memsize: %w", errno)
	}
	if n < 8 {
		return 0, 0, nil
	}
	total = binary.LittleEndian.Uint64(buf[:8])
	// Darwin doesn't expose "available" easily via sysctl without vm_stat parsing.
	// Return total as avail so the memory check never rejects on Darwin.
	// The concurrency limit is the primary safety net on this platform.
	return total, total, nil
}
