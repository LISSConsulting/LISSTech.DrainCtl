//go:build windows

package perfmon

import (
	"unsafe"

	"golang.org/x/sys/windows"
)

// MEMORYSTATUSEX for GlobalMemoryStatusEx.
type memoryStatusEx struct {
	Length               uint32
	MemoryLoad           uint32
	TotalPhys            uint64
	AvailPhys            uint64
	TotalPageFile        uint64
	AvailPageFile        uint64
	TotalVirtual         uint64
	AvailVirtual         uint64
	AvailExtendedVirtual uint64
}

var procGlobalMemoryStatusEx = windows.NewLazySystemDLL("kernel32.dll").NewProc("GlobalMemoryStatusEx")

// totalPhysicalMemoryMB returns the total installed physical memory in MB.
// Returns 0 on failure.
func totalPhysicalMemoryMB() float64 {
	var ms memoryStatusEx
	ms.Length = uint32(unsafe.Sizeof(ms))
	ret, _, _ := procGlobalMemoryStatusEx.Call(uintptr(unsafe.Pointer(&ms)))
	if ret == 0 {
		return 0
	}
	return float64(ms.TotalPhys) / (1024 * 1024)
}
