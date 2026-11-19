package host

import (
	"crypto/rand"
	"encoding/hex"
	"testing"
	"unicode/utf16"
	"unsafe"

	"golang.org/x/sys/windows"
)

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
	if value, err := readWindowsCredential(service, account); err != nil || value != nil {
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
		readback, err := readWindowsCredential(service, account)
		if err != nil || readback == nil || *readback != value {
			t.Fatalf("credential readback changed the synthetic value: %v", err)
		}
		if err := preserveWindowsCredential(service, account, value); err != nil {
			t.Fatal(err)
		}
		if err := preserveWindowsCredential(service, account, "unrelated value"); err == nil {
			t.Fatal("would overwrite an existing credential with different material")
		}
		readback, err = readWindowsCredential(service, account)
		if err != nil || readback == nil || *readback != value {
			t.Fatalf("existing credential changed after rejecting a conflict: %v", err)
		}
	}
}
