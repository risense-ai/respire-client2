package host

import (
	"errors"
	"fmt"
	"unicode/utf16"
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	windowsGenericCredential = 1
	windowsCredentialLimit   = 2560
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
	credFree      = credentialDLL.NewProc("CredFree")
)

// Keep the same target and UTF-16 blob encoding as Respire's existing keyring entries.
func readWindowsCredential(service, account string) (password *string, err error) {
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
		credential.blobSize%2 != 0 || (credential.blobSize != 0 && credential.blob == nil) {
		return nil, errors.New("invalid Windows credential blob")
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
