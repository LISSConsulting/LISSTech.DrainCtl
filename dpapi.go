//go:build windows

package drainctl

import (
	"fmt"
	"unsafe"

	"golang.org/x/sys/windows"
)

// DPAPI flags.
const cryptprotectLocalMachine = 0x04 // any process on this machine can decrypt

// dataBlob mirrors the Windows DATA_BLOB structure.
type dataBlob struct {
	cbData uint32
	pbData *byte
}

var (
	crypt32                = windows.NewLazySystemDLL("crypt32.dll")
	procCryptProtectData   = crypt32.NewProc("CryptProtectData")
	procCryptUnprotectData = crypt32.NewProc("CryptUnprotectData")
	dpapiEntropy           = []byte("LISSTech.DrainCtl/v1/notify-secret")
)

// DPAPIEncrypt encrypts plaintext using DPAPI with machine-scope protection.
// The ciphertext can be decrypted by any process running on the same machine.
func DPAPIEncrypt(plaintext []byte) ([]byte, error) {
	if len(plaintext) == 0 {
		return nil, nil
	}

	in := dataBlob{
		cbData: uint32(len(plaintext)),
		pbData: &plaintext[0],
	}
	entropy := dataBlob{
		cbData: uint32(len(dpapiEntropy)),
		pbData: &dpapiEntropy[0],
	}
	var out dataBlob

	r, _, err := procCryptProtectData.Call(
		uintptr(unsafe.Pointer(&in)),
		0, // szDataDescr
		uintptr(unsafe.Pointer(&entropy)),
		0, // pvReserved
		0, // pPromptStruct
		cryptprotectLocalMachine,
		uintptr(unsafe.Pointer(&out)),
	)
	if r == 0 {
		return nil, fmt.Errorf("CryptProtectData: %w", err)
	}
	defer func() { _, _ = windows.LocalFree(windows.Handle(unsafe.Pointer(out.pbData))) }()

	result := make([]byte, out.cbData)
	copy(result, unsafe.Slice(out.pbData, out.cbData))
	return result, nil
}

// DPAPIDecrypt decrypts ciphertext that was encrypted with DPAPIEncrypt.
func DPAPIDecrypt(ciphertext []byte) ([]byte, error) {
	if len(ciphertext) == 0 {
		return nil, nil
	}

	in := dataBlob{
		cbData: uint32(len(ciphertext)),
		pbData: &ciphertext[0],
	}
	entropy := dataBlob{
		cbData: uint32(len(dpapiEntropy)),
		pbData: &dpapiEntropy[0],
	}
	var out dataBlob

	r, _, err := procCryptUnprotectData.Call(
		uintptr(unsafe.Pointer(&in)),
		0, // ppszDataDescr
		uintptr(unsafe.Pointer(&entropy)),
		0, // pvReserved
		0, // pPromptStruct
		cryptprotectLocalMachine,
		uintptr(unsafe.Pointer(&out)),
	)
	if r == 0 {
		return nil, fmt.Errorf("CryptUnprotectData: %w", err)
	}
	defer func() { _, _ = windows.LocalFree(windows.Handle(unsafe.Pointer(out.pbData))) }()

	result := make([]byte, out.cbData)
	copy(result, unsafe.Slice(out.pbData, out.cbData))
	return result, nil
}
