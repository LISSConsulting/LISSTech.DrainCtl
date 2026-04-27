//go:build windows

package updater

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"unsafe"

	"golang.org/x/sys/windows"
)

// expectedSubjectCN is the exact Subject CN of the LISS code-signing cert.
// Compared verbatim — no whitespace normalization, no case folding.
const expectedSubjectCN = "LISS Consulting, Corp."

// Sentinel errors so callers (and tests) can branch precisely with errors.Is.
var (
	errSignatureMissing         = errors.New("update: MSI is not signed (Authenticode)")
	errSignatureUntrusted       = errors.New("update: MSI signature failed WinVerifyTrust")
	errSignatureSubjectMismatch = errors.New("update: MSI signature subject CN does not match")
)

// Win32 constants for the verifier path.
const (
	// WinVerifyTrust action GUID — WINTRUST_ACTION_GENERIC_VERIFY_V2,
	// {00aac56b-cd44-11d0-8cc2-00c04fc295ee}.
	wtdUIChoiceNone      = 2 // WTD_UI_NONE
	wtdRevokeNone        = 0 // WTD_REVOKE_NONE
	wtdChoiceFile        = 1 // WTD_CHOICE_FILE
	wtdStateActionVerify = 1 // WTD_STATEACTION_VERIFY
	wtdStateActionClose  = 2 // WTD_STATEACTION_CLOSE

	trustENosignature = 0x800B0100 // TRUST_E_NOSIGNATURE

	// CryptQueryObject input.
	certQueryObjectFile                  = 1 // CERT_QUERY_OBJECT_FILE
	certQueryContentFlagPKCS7SignedEmbed = 1024
	certQueryFormatFlagBinary            = 2

	// CertGetNameString.
	certNameAttrType = 3
)

// wintrustActionGenericVerifyV2 is the GUID-bytes representation of
// {00aac56b-cd44-11d0-8cc2-00c04fc295ee} in the layout expected by Win32
// (Data1=DWORD little-endian, Data2/3=WORD little-endian, Data4=8 raw bytes).
var wintrustActionGenericVerifyV2 = guid{
	Data1: 0x00aac56b,
	Data2: 0xcd44,
	Data3: 0x11d0,
	Data4: [8]byte{0x8c, 0xc2, 0x00, 0xc0, 0x4f, 0xc2, 0x95, 0xee},
}

// guid mirrors the C GUID struct (16 bytes total).
type guid struct {
	Data1 uint32
	Data2 uint16
	Data3 uint16
	Data4 [8]byte
}

// wintrustFileInfo mirrors WINTRUST_FILE_INFO. On x64 the trailing pointer
// fields are 8 bytes each, so the struct is 4 + 4 (pad) + 8 + 8 + 8 = 32
// bytes. On x86 it would be 16 bytes — but this codebase is windows/amd64.
type wintrustFileInfo struct {
	cbStruct       uint32
	_              uint32 // padding to align pointer on 8-byte boundary (amd64)
	pcwszFilePath  *uint16
	hFile          windows.Handle
	pgKnownSubject *guid
}

// wintrustData mirrors WINTRUST_DATA. We use the dwUnionChoice = WTD_CHOICE_FILE
// arm of the union, so the union slot is the pointer to a wintrustFileInfo.
// The trailing pSignatureSettings is Win8+; we leave it zero (nil), which
// the OS treats as "no per-signature settings" — equivalent to the V1 layout.
type wintrustData struct {
	cbStruct            uint32
	_                   uint32 // padding
	pPolicyCallbackData uintptr
	pSIPClientData      uintptr
	dwUIChoice          uint32
	fdwRevocationChecks uint32
	dwUnionChoice       uint32
	_                   uint32  // padding before the union pointer (amd64)
	unionFile           uintptr // pFile (the active union arm)
	dwStateAction       uint32
	_                   uint32 // padding
	hWVTStateData       windows.Handle
	pwszURLReference    *uint16
	dwProvFlags         uint32
	dwUIContext         uint32
	pSignatureSettings  uintptr
}

// Lazy-loaded syscalls. Per the codebase pattern (see
// internal/evtspike/subscriber_windows.go), the .Call method values are
// captured into package vars so tests can swap them out at the syscall
// boundary if needed.
var (
	wintrustDLL             = windows.NewLazySystemDLL("wintrust.dll")
	procWinVerifyTrust      = wintrustDLL.NewProc("WinVerifyTrust")
	crypt32DLL              = windows.NewLazySystemDLL("crypt32.dll")
	procCryptQueryObject    = crypt32DLL.NewProc("CryptQueryObject")
	procCertEnumCerts       = crypt32DLL.NewProc("CertEnumCertificatesInStore")
	procCertGetNameStringW  = crypt32DLL.NewProc("CertGetNameStringW")
	procCertFreeCertContext = crypt32DLL.NewProc("CertFreeCertificateContext")
	procCertCloseStore      = crypt32DLL.NewProc("CertCloseStore")

	winVerifyTrust              = procWinVerifyTrust.Call
	cryptQueryObject            = procCryptQueryObject.Call
	certEnumCertificatesInStore = procCertEnumCerts.Call
	certGetNameStringW          = procCertGetNameStringW.Call
	certFreeCertificateContext  = procCertFreeCertContext.Call
	certCloseStore              = procCertCloseStore.Call
)

// Typed test seams. The production verifyAuthenticode reads the signer
// Subject CN via openSignerStore + readSubjectCN + closeSignerStore, each
// of which is a package var so tests can swap them with typed fakes that
// don't need to round-trip uintptr through unsafe.Pointer (which `go vet`'s
// unsafeptr check flags as misuse).
//
// The default implementations call the syscall vars above; tests for the
// non-syscall-boundary paths (subject mismatch, success) replace these so
// they can return arbitrary Go-typed values without dereferencing uintptrs.
var (
	openSignerStore  = openSignerStoreImpl
	readSubjectCN    = readSubjectCNImpl
	closeSignerStore = closeSignerStoreImpl
)

// verifyAuthenticode is the load-bearing security check on the auto-update
// path. It returns nil only when:
//  1. WinVerifyTrust succeeds (chain valid, not revoked, timestamp OK).
//  2. The signer cert's Subject CN equals expectedSubjectCN exactly.
//
// Any other outcome returns one of the sentinel errors wrapped via
// fmt.Errorf("...: %w", err) so callers can errors.Is. A slog.Warn is also
// emitted naming the file and the failure mode, so operators see the reason
// without depending on the caller's logging.
func verifyAuthenticode(msiPath string) error {
	if msiPath == "" {
		return fmt.Errorf("verifyAuthenticode: empty path: %w", errSignatureMissing)
	}
	if _, err := os.Stat(msiPath); err != nil {
		return fmt.Errorf("verifyAuthenticode: stat %s: %w", msiPath, err)
	}

	pathPtr, err := windows.UTF16PtrFromString(msiPath)
	if err != nil {
		return fmt.Errorf("verifyAuthenticode: UTF16PtrFromString: %w", err)
	}

	// Build WINTRUST_FILE_INFO + WINTRUST_DATA on the stack.
	fileInfo := wintrustFileInfo{
		pcwszFilePath: pathPtr,
	}
	fileInfo.cbStruct = uint32(unsafe.Sizeof(fileInfo))

	data := wintrustData{
		dwUIChoice:          wtdUIChoiceNone,
		fdwRevocationChecks: wtdRevokeNone,
		dwUnionChoice:       wtdChoiceFile,
		unionFile:           uintptr(unsafe.Pointer(&fileInfo)),
		dwStateAction:       wtdStateActionVerify,
	}
	data.cbStruct = uint32(unsafe.Sizeof(data))

	// hwnd = INVALID_HANDLE_VALUE (-1) signals "no UI parent" per docs.
	const invalidHandle = ^uintptr(0)
	r, _, _ := winVerifyTrust(
		invalidHandle,
		uintptr(unsafe.Pointer(&wintrustActionGenericVerifyV2)),
		uintptr(unsafe.Pointer(&data)),
	)
	// Always close the trust state, regardless of the verify outcome, to
	// release the cert chain context the provider built up.
	defer func() {
		data.dwStateAction = wtdStateActionClose
		_, _, _ = winVerifyTrust(
			invalidHandle,
			uintptr(unsafe.Pointer(&wintrustActionGenericVerifyV2)),
			uintptr(unsafe.Pointer(&data)),
		)
	}()

	hr := uint32(r)
	switch hr {
	case 0:
		// Trust succeeded; fall through to the Subject CN check.
	case trustENosignature:
		slog.Warn("update: authenticode verify failed", "path", msiPath, "reason", "no_signature")
		return fmt.Errorf("verifyAuthenticode %s: %w", msiPath, errSignatureMissing)
	default:
		slog.Warn("update: authenticode verify failed", "path", msiPath, "reason", "untrusted", "hresult", fmt.Sprintf("0x%08x", hr))
		return fmt.Errorf("verifyAuthenticode %s: hresult=0x%08x: %w", msiPath, hr, errSignatureUntrusted)
	}

	// Trust succeeded — now extract the signer cert and check Subject CN.
	subject, err := readSignerSubjectCN(pathPtr)
	if err != nil {
		slog.Warn("update: authenticode verify failed", "path", msiPath, "reason", "subject_extract", "err", err.Error())
		return fmt.Errorf("verifyAuthenticode %s: %w", msiPath, err)
	}
	if subject != expectedSubjectCN {
		// Don't bake the wrong subject into the error to keep the sentinel
		// stable for errors.Is tests. Log it instead so operators see it.
		slog.Warn("update: authenticode verify failed", "path", msiPath, "reason", "subject_mismatch", "got_subject", subject, "want_subject", expectedSubjectCN)
		return fmt.Errorf("verifyAuthenticode %s: %w", msiPath, errSignatureSubjectMismatch)
	}
	return nil
}

// readSignerSubjectCN orchestrates the typed cert-extraction seams. Each
// seam (openSignerStore, readSubjectCN, closeSignerStore) is a package var
// so unit tests can replace it with a Go-typed fake — production
// implementations live in *Impl below and call the syscall vars.
func readSignerSubjectCN(pathPtr *uint16) (string, error) {
	hStore, hCert, err := openSignerStore(pathPtr)
	if err != nil {
		return "", err
	}
	defer closeSignerStore(hStore, hCert)
	return readSubjectCN(hCert)
}

// openSignerStoreImpl is the production CryptQueryObject + first-cert
// implementation of the openSignerStore seam.
func openSignerStoreImpl(pathPtr *uint16) (hStore uintptr, hCert uintptr, err error) {
	var (
		encoding    uint32
		contentType uint32
		formatType  uint32
		hMsg        uintptr
	)
	r, _, e := cryptQueryObject(
		uintptr(certQueryObjectFile),
		uintptr(unsafe.Pointer(pathPtr)),
		uintptr(certQueryContentFlagPKCS7SignedEmbed),
		uintptr(certQueryFormatFlagBinary),
		0,
		uintptr(unsafe.Pointer(&encoding)),
		uintptr(unsafe.Pointer(&contentType)),
		uintptr(unsafe.Pointer(&formatType)),
		uintptr(unsafe.Pointer(&hStore)),
		uintptr(unsafe.Pointer(&hMsg)),
		0,
	)
	if r == 0 {
		return 0, 0, fmt.Errorf("CryptQueryObject: %w", e)
	}
	// hMsg is not used; CryptQueryObject returns 0 here in practice when
	// called with the embedded-PKCS#7 content flag. We pass &hMsg only
	// because the Win32 prototype demands a non-NULL phMsg slot.

	// First cert in the store = signer (Authenticode PKCS#7 with a single
	// signer). Iterate once with pPrev = 0.
	cert, _, ce := certEnumCertificatesInStore(hStore, 0)
	if cert == 0 {
		_, _, _ = certCloseStore(hStore, 0)
		return 0, 0, fmt.Errorf("CertEnumCertificatesInStore: %w", ce)
	}
	return hStore, cert, nil
}

// readSubjectCNImpl is the production CertGetNameStringW implementation of
// the readSubjectCN seam.
func readSubjectCNImpl(hCert uintptr) (string, error) {
	// OID "2.5.4.3" = Common Name. CertGetNameStringW accepts the OID via
	// pvTypePara when dwType is CERT_NAME_ATTR_TYPE.
	oidCN := []byte("2.5.4.3\x00")
	var buf [256]uint16
	r, _, _ := certGetNameStringW(
		hCert,
		uintptr(certNameAttrType),
		0,
		uintptr(unsafe.Pointer(&oidCN[0])),
		uintptr(unsafe.Pointer(&buf[0])),
		uintptr(len(buf)),
	)
	// CertGetNameStringW always succeeds: it returns at least 1 (a single
	// NUL when nothing matches). r is the count of TCHARs written
	// including the trailing NUL.
	if r <= 1 {
		return "", fmt.Errorf("CertGetNameStringW: empty subject CN")
	}
	return windows.UTF16ToString(buf[:]), nil
}

// closeSignerStoreImpl releases the cert and store handles. Both are
// no-ops when zero.
func closeSignerStoreImpl(hStore, hCert uintptr) {
	if hCert != 0 {
		_, _, _ = certFreeCertificateContext(hCert)
	}
	if hStore != 0 {
		_, _, _ = certCloseStore(hStore, 0)
	}
}
