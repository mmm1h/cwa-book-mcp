//go:build windows

package main

import (
	"golang.org/x/sys/windows"
	"unsafe"
)

var psapi = windows.NewLazySystemDLL("psapi.dll").NewProc("GetProcessMemoryInfo")
var handleCount = windows.NewLazySystemDLL("kernel32.dll").NewProc("GetProcessHandleCount")

type counters struct {
	Size, PageFaults                                                                             uint32
	PeakWorkingSet, WorkingSet, PeakPaged, Paged, PeakNonPaged, NonPaged, Pagefile, PeakPagefile uintptr
}

func processStats(pid int) (uint64, uint64, int, error) {
	h, e := windows.OpenProcess(windows.PROCESS_QUERY_INFORMATION|windows.PROCESS_VM_READ, false, uint32(pid))
	if e != nil {
		return 0, 0, 0, e
	}
	defer windows.CloseHandle(h)
	var m counters
	m.Size = uint32(unsafe.Sizeof(m))
	r, _, e := psapi.Call(uintptr(h), uintptr(unsafe.Pointer(&m)), uintptr(m.Size))
	if r == 0 {
		return 0, 0, 0, e
	}
	var handles uint32
	_, _, _ = handleCount.Call(uintptr(h), uintptr(unsafe.Pointer(&handles)))
	return uint64(m.WorkingSet), uint64(m.PeakWorkingSet), int(handles), nil
}
func cgroupStats(pid int) map[string]uint64 { return map[string]uint64{} }
