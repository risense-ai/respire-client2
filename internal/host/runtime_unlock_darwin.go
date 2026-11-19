//go:build darwin && cgo

package host

/*
#cgo LDFLAGS: -framework Security -framework CoreFoundation
#include <Security/Security.h>
#include <stdlib.h>
*/
import "C"

import (
	"context"
	"errors"
	"fmt"
	"unicode/utf8"
	"unsafe"
)

func (m *Manager) unlockAccountRuntime(ctx context.Context, health HealthInfo) error {
	return m.unlockNativeRuntime(ctx, health, readMacCredential, nil)
}

func readMacCredential(service, account string) (*string, error) {
	serviceName, accountName := C.CString(service), C.CString(account)
	defer C.free(unsafe.Pointer(serviceName))
	defer C.free(unsafe.Pointer(accountName))
	var length C.UInt32
	var data unsafe.Pointer
	status := C.SecKeychainFindGenericPassword(0, C.UInt32(len(service)), serviceName,
		C.UInt32(len(account)), accountName, &length, &data, nil)
	if status == C.errSecItemNotFound {
		return nil, nil
	}
	if status != C.errSecSuccess {
		return nil, fmt.Errorf("macOS Keychain read: OSStatus %d", status)
	}
	if length > 65536 {
		if data != nil {
			C.SecKeychainItemFreeContent(nil, data)
		}
		return nil, errors.New("macOS Keychain credential exceeds the Runtime protocol limit")
	}
	var raw []byte
	if data != nil {
		raw = C.GoBytes(data, C.int(length))
		status = C.SecKeychainItemFreeContent(nil, data)
		if status != C.errSecSuccess {
			clear(raw)
			return nil, fmt.Errorf("macOS Keychain release: OSStatus %d", status)
		}
	} else if length != 0 {
		return nil, errors.New("macOS Keychain did not return the requested credential")
	}
	defer clear(raw)
	if !utf8.Valid(raw) {
		return nil, errors.New("macOS Keychain credential is not valid UTF-8")
	}
	value := string(raw)
	return &value, nil
}
