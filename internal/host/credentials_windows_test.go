package host

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"strings"
	"testing"
	"unicode/utf16"
	"unsafe"

	"golang.org/x/sys/windows"
)

func TestWindowsLoginCustodyInteroperability(t *testing.T) {
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		t.Fatal(err)
	}
	const service = "rsrs"
	for _, version := range []string{"utf16", "utf8"} {
		t.Run(version, func(t *testing.T) {
			account := "login-control:login-" + hex.EncodeToString(nonce[:]) + "-" + version
			target, err := windows.UTF16PtrFromString(account + "." + service)
			if err != nil {
				t.Fatal(err)
			}
			deleteEntry := credentialDLL.NewProc("CredDeleteW")
			t.Cleanup(func() {
				ok, _, err := deleteEntry.Call(uintptr(unsafe.Pointer(target)), windowsGenericCredential, 0)
				if ok == 0 && err != windows.ERROR_NOT_FOUND {
					t.Errorf("remove synthetic login custody: %v", err)
				}
			})
			value := `{"fixture":"密钥🔑"}`
			if version == "utf16" {
				words := utf16.Encode([]rune(value))
				record := windowsCredential{kind: windowsGenericCredential, target: target, persist: 1,
					blob: &words[0], blobSize: uint32(len(words) * 2)}
				if ok, _, err := credWrite.Call(uintptr(unsafe.Pointer(&record)), 0); ok == 0 {
					t.Fatalf("write older CLI custody: %v", err)
				}
			} else {
				value = `{"fixture":"` + strings.Repeat("a", 1400) + `密钥🔑"}`
				if err := preserveSystemCredential(service, account, value); err != nil {
					t.Fatal(err)
				}
				var record *windowsCredential
				if ok, _, err := credRead.Call(uintptr(unsafe.Pointer(target)), windowsGenericCredential, 0,
					uintptr(unsafe.Pointer(&record))); ok == 0 || record == nil {
					t.Fatalf("read compact custody blob: %v", err)
				}
				defer credFree.Call(uintptr(unsafe.Pointer(record)))
				raw := unsafe.Slice((*byte)(unsafe.Pointer(record.blob)), int(record.blobSize))
				if !bytes.Equal(raw, []byte("rsrs-login-control:utf8:v1\x00"+value)) {
					t.Fatal("Client2 did not store the exact CLI UTF-8 format")
				}
			}
			if err := preserveSystemCredential(service, account, "different"); err == nil {
				t.Fatal("would overwrite the original login custody")
			}
			readback, err := readSystemCredential(service, account)
			if err != nil || readback == nil || *readback != value {
				t.Fatalf("login custody readback changed the synthetic value: %v", err)
			}
			next := `{"fixture":"verified"}`
			if err := replaceSystemCredential(service, account, &value, next); err != nil {
				t.Fatal(err)
			}
			if err := replaceSystemCredential(service, account, &value, "stale"); err == nil {
				t.Fatal("stale custody update would replace the verified value")
			}
			readback, err = readSystemCredential(service, account)
			if err != nil || readback == nil || *readback != next {
				t.Fatalf("verified custody changed after stale update: %v", err)
			}
		})
	}
}

func TestWindowsCredentialManagerReadsRespireSlots(t *testing.T) {
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		t.Fatal(err)
	}
	const service = "rsrs"
	account := "pass:client2-test-" + hex.EncodeToString(nonce[:])
	target, err := windows.UTF16PtrFromString(account + "." + service)
	if err != nil {
		t.Fatal(err)
	}
	if value, err := readSystemCredential(service, account); err != nil || value != nil {
		t.Fatalf("fresh synthetic slot must be absent: %v", err)
	}
	deleteEntry := credentialDLL.NewProc("CredDeleteW")
	t.Cleanup(func() {
		ok, _, err := deleteEntry.Call(uintptr(unsafe.Pointer(target)), windowsGenericCredential, 0)
		if ok == 0 && err != windows.ERROR_NOT_FOUND {
			t.Errorf("remove synthetic credential: %v", err)
		}
	})
	writeEntry := credentialDLL.NewProc("CredWriteW")
	for _, value := range []string{"synthetic-密钥-🔑", ""} {
		words := utf16.Encode([]rune(value))
		credential := windowsCredential{kind: windowsGenericCredential, target: target, persist: 1}
		if len(words) > 0 {
			credential.blob, credential.blobSize = &words[0], uint32(len(words)*2)
		}
		ok, _, err := writeEntry.Call(uintptr(unsafe.Pointer(&credential)), 0)
		if ok == 0 {
			t.Fatalf("write synthetic credential: %v", err)
		}
		readback, err := readSystemCredential(service, account)
		if err != nil || readback == nil || *readback != value {
			t.Fatalf("credential readback changed the synthetic value: %v", err)
		}
		if err := preserveSystemCredential(service, account, value); err != nil {
			t.Fatal(err)
		}
		if err := preserveSystemCredential(service, account, "unrelated value"); err == nil {
			t.Fatal("would overwrite an existing credential with different material")
		}
		readback, err = readSystemCredential(service, account)
		if err != nil || readback == nil || *readback != value {
			t.Fatalf("existing credential changed after rejecting a conflict: %v", err)
		}
	}
}
