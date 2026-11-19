package host

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
	"unicode/utf16"
	"unicode/utf8"
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	windowsGenericCredential  = 1
	windowsCredentialLimit    = 2560
	windowsLoginCustodyPrefix = "rsrs-login-control:utf8:v1\x00"
)

type windowsCredential struct {
	flags       uint32
	kind        uint32
	target      *uint16
	comment     *uint16
	lastWritten windows.Filetime
	blobSize    uint32
	blob        *uint16
	persist     uint32
	attributes  uint32
	attributesP uintptr
	targetAlias *uint16
	username    *uint16
}

var (
	credentialDLL = windows.NewLazySystemDLL("advapi32.dll")
	credRead      = credentialDLL.NewProc("CredReadW")
	credWrite     = credentialDLL.NewProc("CredWriteW")
	credDelete    = credentialDLL.NewProc("CredDeleteW")
	credFree      = credentialDLL.NewProc("CredFree")
)

func isWindowsLoginCustody(service, account string) bool {
	return service == "rsrs" && strings.HasPrefix(account, "login-control:login-")
}

func removeSystemCredential(service, account, expected string) error {
	existing, err := readSystemCredential(service, account)
	if err != nil || existing == nil {
		return err
	}
	if *existing != expected {
		return errors.New("Windows credential changed before cleanup")
	}
	target, err := windows.UTF16PtrFromString(account + "." + service)
	if err != nil {
		return err
	}
	ok, _, deleteErr := credDelete.Call(uintptr(unsafe.Pointer(target)), windowsGenericCredential, 0)
	if ok == 0 && !errors.Is(deleteErr, windows.ERROR_NOT_FOUND) {
		return fmt.Errorf("Windows credential cleanup: %w", deleteErr)
	}
	remaining, err := readSystemCredential(service, account)
	if err != nil {
		return err
	}
	if remaining != nil {
		return errors.New("Windows credential cleanup could not be verified")
	}
	return nil
}

// Keep existing password slots in UTF-16 and share CLI's encoding for private login state.
func readSystemCredential(service, account string) (password *string, err error) {
	target, err := windows.UTF16PtrFromString(account + "." + service)
	if err != nil {
		return nil, errors.New("invalid Windows credential target")
	}
	var credential *windowsCredential
	ok, _, readErr := credRead.Call(uintptr(unsafe.Pointer(target)), windowsGenericCredential, 0,
		uintptr(unsafe.Pointer(&credential)))
	if ok == 0 {
		if errors.Is(readErr, windows.ERROR_NOT_FOUND) {
			return nil, nil
		}
		return nil, fmt.Errorf("Windows credential read: %w", readErr)
	}
	if credential == nil {
		return nil, errors.New("Windows credential response is empty")
	}
	defer credFree.Call(uintptr(unsafe.Pointer(credential)))
	if credential.kind != windowsGenericCredential || credential.blobSize > windowsCredentialLimit ||
		(credential.blobSize != 0 && credential.blob == nil) {
		return nil, errors.New("invalid Windows credential blob")
	}
	if isWindowsLoginCustody(service, account) {
		raw := unsafe.Slice((*byte)(unsafe.Pointer(credential.blob)), int(credential.blobSize))
		if payload, ok := bytes.CutPrefix(raw, []byte(windowsLoginCustodyPrefix)); ok {
			if !utf8.Valid(payload) {
				return nil, errors.New("invalid UTF-8 Windows login custody")
			}
			value := string(payload)
			return &value, nil
		}
		// Older CLI operations stored their JSON as a Windows password.
		if !bytes.HasPrefix(raw, []byte{'{', 0}) {
			return nil, errors.New("unsupported Windows login custody encoding")
		}
	}
	if credential.blobSize%2 != 0 {
		return nil, errors.New("invalid UTF-16 Windows credential length")
	}
	wordCount := int(credential.blobSize / 2)
	words := unsafe.Slice(credential.blob, wordCount)
	for index := 0; index < wordCount; index++ {
		word := words[index]
		if word >= 0xD800 && word <= 0xDBFF {
			index++
			if index >= wordCount || words[index] < 0xDC00 || words[index] > 0xDFFF {
				return nil, errors.New("invalid UTF-16 Windows credential")
			}
		} else if word >= 0xDC00 && word <= 0xDFFF {
			return nil, errors.New("invalid UTF-16 Windows credential")
		}
	}
	value := string(utf16.Decode(words))
	return &value, nil
}

func preserveSystemCredential(service, account, value string) error {
	return replaceSystemCredential(service, account, nil, value)
}

// The authentication coordinator holds the installation takeover lock while replacing custody.
func replaceSystemCredential(service, account string, expected *string, value string) error {
	existing, err := readSystemCredential(service, account)
	if err != nil {
		return err
	}
	if existing != nil && *existing == value {
		return nil
	}
	if (existing == nil) != (expected == nil) || existing != nil && *existing != *expected {
		return errors.New("Windows credential slot differs; original session retained")
	}
	target, err := windows.UTF16PtrFromString(account + "." + service)
	if err != nil {
		return errors.New("invalid Windows credential target")
	}
	record := windowsCredential{kind: windowsGenericCredential, target: target, persist: 3}
	if isWindowsLoginCustody(service, account) {
		if !utf8.ValidString(value) {
			return errors.New("invalid UTF-8 Windows login custody")
		}
		if len(windowsLoginCustodyPrefix)+len(value) > windowsCredentialLimit {
			return errors.New("Windows login custody exceeds its storage limit")
		}
		raw := append([]byte(windowsLoginCustodyPrefix), value...)
		defer clear(raw)
		record.blob, record.blobSize = (*uint16)(unsafe.Pointer(&raw[0])), uint32(len(raw))
	} else {
		words := utf16.Encode([]rune(value))
		defer clear(words)
		if len(words)*2 > windowsCredentialLimit {
			return errors.New("original Windows credential exceeds its storage limit")
		}
		if len(words) != 0 {
			record.blob, record.blobSize = &words[0], uint32(len(words)*2)
		}
	}
	ok, _, err := credWrite.Call(uintptr(unsafe.Pointer(&record)), 0)
	if ok == 0 {
		return fmt.Errorf("Windows credential write: %w", err)
	}
	written, err := readSystemCredential(service, account)
	if err != nil {
		return err
	}
	if written == nil || *written != value {
		return errors.New("Windows credential readback differs")
	}
	return nil
}
