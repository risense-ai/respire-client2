//go:build darwin && cgo

package host

/*
#cgo LDFLAGS: -framework Security -framework CoreFoundation
#include <Security/Security.h>
#include <stdlib.h>
#include <string.h>

static OSStatus read_password(UInt32 serviceLen, const char *service, UInt32 accountLen,
    const char *account, UInt32 *length, void **data) {
    return SecKeychainFindGenericPassword(NULL, serviceLen, service, accountLen, account, length, data, NULL);
}

static OSStatus add_password(UInt32 serviceLen, const char *service, UInt32 accountLen,
    const char *account, UInt32 length, void *data) {
    return SecKeychainAddGenericPassword(NULL, serviceLen, service, accountLen, account, length, data, NULL);
}
*/
import "C"

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"unicode/utf8"
	"unsafe"
)

func (m *Manager) unlockAccountRuntime(ctx context.Context, health HealthInfo) error {
	return m.unlockNativeRuntime(ctx, health, readSystemCredential, m.migrateNativeSession)
}

func readSystemCredential(service, account string) (*string, error) {
	serviceName, accountName := C.CString(service), C.CString(account)
	defer C.free(unsafe.Pointer(serviceName))
	defer C.free(unsafe.Pointer(accountName))
	var length C.UInt32
	var data unsafe.Pointer
	status := C.read_password(C.UInt32(len(service)), serviceName,
		C.UInt32(len(account)), accountName, &length, &data)
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

func preserveSystemCredential(service, account, value string) error {
	existing, err := readSystemCredential(service, account)
	if err != nil {
		return err
	}
	if existing == nil {
		serviceName, accountName := C.CString(service), C.CString(account)
		defer C.free(unsafe.Pointer(serviceName))
		defer C.free(unsafe.Pointer(accountName))
		password := C.CString(value)
		defer C.free(unsafe.Pointer(password))
		defer C.memset(unsafe.Pointer(password), 0, C.size_t(len(value)))
		status := C.add_password(C.UInt32(len(service)), serviceName, C.UInt32(len(account)),
			accountName, C.UInt32(len(value)), unsafe.Pointer(password))
		if status != C.errSecSuccess && status != C.errSecDuplicateItem {
			return fmt.Errorf("macOS Keychain save: OSStatus %d", status)
		}
		existing, err = readSystemCredential(service, account)
		if err != nil {
			return err
		}
	}
	if existing == nil || *existing != value {
		return errors.New("macOS Keychain credential differs from historical session; original session retained")
	}
	return nil
}

func canonicalSessionDirectory(path string) (string, error) {
	return filepath.EvalSymlinks(path)
}

func publishSessionFile(from, to string) error {
	directory, err := os.Open(filepath.Dir(to))
	if err != nil {
		return err
	}
	defer directory.Close()
	if err := os.Rename(from, to); err != nil {
		return err
	}
	return directory.Sync()
}
