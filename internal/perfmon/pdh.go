//go:build windows

package perfmon

import (
	"fmt"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// PDH status codes.
const (
	pdhCStatusValidData   = 0x00000000
	pdhCStatusNewData     = 0x00000001
	pdhCStatusNoInstance  = 0x800007D1
	pdhMoreData           = 0x800007D2
	pdhNoData             = 0x800007D5
	pdhCalcNegativeDenom  = 0x800007D6
	pdhCalcNegativeValue  = 0x800007D8
	pdhCStatusNoObject    = 0xC0000BB8
	pdhCStatusNoCounter   = 0xC0000BB9
	pdhCStatusInvalidData = 0xC0000BBA
	pdhInvalidData        = 0xC0000BC6
)

// pdhFmtDouble requests double-precision formatted values.
const pdhFmtDouble = 0x00000200

// pdhFmtCountervalueDouble maps PDH_FMT_COUNTERVALUE for double format.
type pdhFmtCountervalueDouble struct {
	CStatus uint32
	_       [4]byte // alignment padding
	Value   float64
}

// pdhFmtCountervalueItemDouble maps PDH_FMT_COUNTERVALUE_ITEM_DOUBLE.
type pdhFmtCountervalueItemDouble struct {
	Name  *uint16
	Value pdhFmtCountervalueDouble
}

var (
	modPdh = windows.NewLazySystemDLL("pdh.dll")

	procPdhOpenQueryW               = modPdh.NewProc("PdhOpenQueryW")
	procPdhAddCounterW              = modPdh.NewProc("PdhAddCounterW")
	procPdhCollectQueryData         = modPdh.NewProc("PdhCollectQueryData")
	procPdhGetFormattedCounterValue = modPdh.NewProc("PdhGetFormattedCounterValue")
	procPdhGetFormattedCounterArray = modPdh.NewProc("PdhGetFormattedCounterArrayW")
	procPdhCloseQuery               = modPdh.NewProc("PdhCloseQuery")
)

func pdhOpenQuery() (syscall.Handle, error) {
	var query syscall.Handle
	ret, _, _ := procPdhOpenQueryW.Call(0, 0, uintptr(unsafe.Pointer(&query)))
	if ret != 0 {
		return 0, fmt.Errorf("PdhOpenQueryW: 0x%08X", ret)
	}
	return query, nil
}

func pdhAddCounter(query syscall.Handle, path string) (syscall.Handle, error) {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return 0, fmt.Errorf("invalid counter path %q: %w", path, err)
	}
	var counter syscall.Handle
	ret, _, _ := procPdhAddCounterW.Call(
		uintptr(query),
		uintptr(unsafe.Pointer(p)),
		0,
		uintptr(unsafe.Pointer(&counter)),
	)
	if ret != 0 {
		return 0, fmt.Errorf("PdhAddCounterW(%s): 0x%08X", path, ret)
	}
	return counter, nil
}

func pdhCollectQueryData(query syscall.Handle) error {
	ret, _, _ := procPdhCollectQueryData.Call(uintptr(query))
	if ret != 0 {
		return fmt.Errorf("PdhCollectQueryData: 0x%08X", ret)
	}
	return nil
}

// isTransientError returns true for PDH codes that indicate a counter is
// temporarily unavailable (rate counter still priming, instance disappeared,
// counter overflow). The counter should be silently skipped for this cycle.
func isTransientError(code uint32) bool {
	switch code {
	case pdhInvalidData, pdhCalcNegativeDenom, pdhCalcNegativeValue,
		pdhCStatusInvalidData, pdhCStatusNoInstance, pdhNoData:
		return true
	}
	return false
}

// errCounterNotReady signals a transient PDH condition. Callers skip the value.
var errCounterNotReady = fmt.Errorf("counter data not yet available")

func pdhGetDouble(counter syscall.Handle) (float64, error) {
	var val pdhFmtCountervalueDouble
	ret, _, _ := procPdhGetFormattedCounterValue.Call(
		uintptr(counter), pdhFmtDouble, 0,
		uintptr(unsafe.Pointer(&val)),
	)
	if ret != 0 {
		if isTransientError(uint32(ret)) {
			return 0, errCounterNotReady
		}
		return 0, fmt.Errorf("PdhGetFormattedCounterValue: 0x%08X", ret)
	}
	if val.CStatus != pdhCStatusValidData && val.CStatus != pdhCStatusNewData {
		if isTransientError(val.CStatus) {
			return 0, errCounterNotReady
		}
		return 0, fmt.Errorf("counter CStatus: 0x%08X", val.CStatus)
	}
	return val.Value, nil
}

func pdhGetDoubleArray(counter syscall.Handle) ([]float64, error) {
	var bufSize, itemCount uint32
	ret, _, _ := procPdhGetFormattedCounterArray.Call(
		uintptr(counter), pdhFmtDouble,
		uintptr(unsafe.Pointer(&bufSize)),
		uintptr(unsafe.Pointer(&itemCount)),
		0,
	)
	if ret != pdhMoreData && ret != 0 {
		return nil, fmt.Errorf("PdhGetFormattedCounterArrayW size: 0x%08X", ret)
	}
	if itemCount == 0 || bufSize == 0 {
		return nil, nil
	}

	buf := make([]byte, bufSize)
	ret, _, _ = procPdhGetFormattedCounterArray.Call(
		uintptr(counter), pdhFmtDouble,
		uintptr(unsafe.Pointer(&bufSize)),
		uintptr(unsafe.Pointer(&itemCount)),
		uintptr(unsafe.Pointer(&buf[0])),
	)
	if ret != 0 {
		return nil, fmt.Errorf("PdhGetFormattedCounterArrayW: 0x%08X", ret)
	}

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

func pdhCloseQuery(query syscall.Handle) {
	if query != 0 {
		_, _, _ = procPdhCloseQuery.Call(uintptr(query))
	}
}
