//go:build windows

package perfmon

import (
	"fmt"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// PDH error codes.
const (
	pdhCStatusValidData     = 0x00000000
	pdhCStatusNewData       = 0x00000001
	pdhCStatusNoInstance    = 0x800007D1
	pdhCStatusInvalidData   = 0xC0000BBA
	pdhCStatusNoObject      = 0xC0000BB8
	pdhCStatusNoCounter     = 0xC0000BB9
	pdhMoreData             = 0x800007D2
	pdhInvalidHandle        = 0xC0000BBC
	pdhNoData               = 0x800007D5
	pdhCalcNegativeDenom    = 0x800007D6
	pdhCalcNegativeValue    = 0x800007D8
	pdhInvalidArgument      = 0xC0000BBD
	pdhEntryNotInLogFile    = 0xC0000BCD
	pdhCStatusNoCountername = 0xC0000BBF
)

// PDH_FMT constants for PdhGetFormattedCounterValue.
const (
	pdhFmtDouble = 0x00000200
	pdhFmtLarge  = 0x00000400
)

// PDH_FMT_COUNTERVALUE for double values.
type pdhFmtCountervalueDouble struct {
	CStatus uint32
	_       [4]byte // padding
	Value   float64
}

// PDH_FMT_COUNTERVALUE_ITEM_DOUBLE for array queries.
type pdhFmtCountervalueItemDouble struct {
	Name  *uint16
	Value pdhFmtCountervalueDouble
}

var (
	modPdh = windows.NewLazySystemDLL("pdh.dll")

	procPdhOpenQueryW               = modPdh.NewProc("PdhOpenQueryW")
	procPdhAddEnglishCounterW       = modPdh.NewProc("PdhAddEnglishCounterW")
	procPdhCollectQueryData         = modPdh.NewProc("PdhCollectQueryData")
	procPdhGetFormattedCounterValue = modPdh.NewProc("PdhGetFormattedCounterValue")
	procPdhGetFormattedCounterArray = modPdh.NewProc("PdhGetFormattedCounterArrayW")
	procPdhCloseQuery               = modPdh.NewProc("PdhCloseQuery")
)

// pdhOpenQuery creates a new PDH query handle.
func pdhOpenQuery() (syscall.Handle, error) {
	var query syscall.Handle
	ret, _, _ := procPdhOpenQueryW.Call(0, 0, uintptr(unsafe.Pointer(&query)))
	if ret != 0 {
		return 0, fmt.Errorf("PdhOpenQueryW failed: 0x%08X", ret)
	}
	return query, nil
}

// pdhAddEnglishCounter adds a counter by its English name (locale-independent).
func pdhAddEnglishCounter(query syscall.Handle, counterPath string) (syscall.Handle, error) {
	path, err := windows.UTF16PtrFromString(counterPath)
	if err != nil {
		return 0, fmt.Errorf("invalid counter path %q: %w", counterPath, err)
	}
	var counter syscall.Handle
	ret, _, _ := procPdhAddEnglishCounterW.Call(
		uintptr(query),
		uintptr(unsafe.Pointer(path)),
		0,
		uintptr(unsafe.Pointer(&counter)),
	)
	if ret != 0 {
		return 0, fmt.Errorf("PdhAddEnglishCounterW(%s) failed: 0x%08X", counterPath, ret)
	}
	return counter, nil
}

// pdhCollectQueryData collects current data for all counters in the query.
func pdhCollectQueryData(query syscall.Handle) error {
	ret, _, _ := procPdhCollectQueryData.Call(uintptr(query))
	if ret != 0 {
		return fmt.Errorf("PdhCollectQueryData failed: 0x%08X", ret)
	}
	return nil
}

// pdhGetFormattedDouble reads a scalar counter value as float64.
// Returns 0 and a non-nil error if the counter has no valid data.
func pdhGetFormattedDouble(counter syscall.Handle) (float64, error) {
	var val pdhFmtCountervalueDouble
	ret, _, _ := procPdhGetFormattedCounterValue.Call(
		uintptr(counter),
		pdhFmtDouble,
		0,
		uintptr(unsafe.Pointer(&val)),
	)
	if ret != 0 {
		return 0, fmt.Errorf("PdhGetFormattedCounterValue failed: 0x%08X", ret)
	}
	if val.CStatus != pdhCStatusValidData && val.CStatus != pdhCStatusNewData {
		return 0, fmt.Errorf("counter status: 0x%08X", val.CStatus)
	}
	return val.Value, nil
}

// pdhGetFormattedDoubleArray reads a multi-instance counter and returns all
// instance values as float64 slices. Used for per-session counters.
func pdhGetFormattedDoubleArray(counter syscall.Handle) ([]float64, error) {
	var bufSize uint32
	var itemCount uint32

	// First call: determine required buffer size.
	ret, _, _ := procPdhGetFormattedCounterArray.Call(
		uintptr(counter),
		pdhFmtDouble,
		uintptr(unsafe.Pointer(&bufSize)),
		uintptr(unsafe.Pointer(&itemCount)),
		0,
	)
	if ret != pdhMoreData && ret != 0 {
		return nil, fmt.Errorf("PdhGetFormattedCounterArrayW size query failed: 0x%08X", ret)
	}
	if itemCount == 0 || bufSize == 0 {
		return nil, nil
	}

	// Allocate buffer and collect.
	buf := make([]byte, bufSize)
	ret, _, _ = procPdhGetFormattedCounterArray.Call(
		uintptr(counter),
		pdhFmtDouble,
		uintptr(unsafe.Pointer(&bufSize)),
		uintptr(unsafe.Pointer(&itemCount)),
		uintptr(unsafe.Pointer(&buf[0])),
	)
	if ret != 0 {
		return nil, fmt.Errorf("PdhGetFormattedCounterArrayW failed: 0x%08X", ret)
	}

	// Parse items from the buffer.
	itemSize := unsafe.Sizeof(pdhFmtCountervalueItemDouble{})
	values := make([]float64, 0, itemCount)
	for i := uint32(0); i < itemCount; i++ {
		item := (*pdhFmtCountervalueItemDouble)(unsafe.Pointer(&buf[uintptr(i)*itemSize]))
		if item.Value.CStatus == pdhCStatusValidData || item.Value.CStatus == pdhCStatusNewData {
			values = append(values, item.Value.Value)
		}
	}
	return values, nil
}

// pdhCloseQuery releases a PDH query handle and all associated counters.
func pdhCloseQuery(query syscall.Handle) {
	if query != 0 {
		_, _, _ = procPdhCloseQuery.Call(uintptr(query))
	}
}
