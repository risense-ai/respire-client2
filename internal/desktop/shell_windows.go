//go:build windows

package desktop

import (
	"log"
	"os"
	"path/filepath"
	"syscall"
	"unsafe"

	ole "github.com/go-ole/go-ole"
	"github.com/go-ole/go-ole/oleutil"
	"golang.org/x/sys/windows/registry"
)

// Windows shows an executable's bare file name until it has a Start Menu
// identity. This uses the same shell identity ideas: a friendly .lnk, App
// Paths, and a stable process AppUserModelID for notification grouping.
func ensureShellIdentity() {
	exe, err := os.Executable()
	if err != nil {
		log.Printf("shell identity: resolve executable: %v", err)
		return
	}
	if err := setProcessAppUserModelID("ai.risense.respire.client2"); err != nil {
		log.Printf("shell identity: AUMID: %v", err)
	}
	if err := ensureStartShortcut(exe); err != nil {
		log.Printf("shell identity: start shortcut: %v", err)
	}
	if err := registerAppPath(exe); err != nil {
		log.Printf("shell identity: App Paths: %v", err)
	}
}

func setProcessAppUserModelID(value string) error {
	valuePtr, err := syscall.UTF16PtrFromString(value)
	if err != nil {
		return err
	}
	result, _, callErr := syscall.NewLazyDLL("shell32.dll").NewProc("SetCurrentProcessExplicitAppUserModelID").Call(uintptr(unsafe.Pointer(valuePtr)))
	if result != 0 {
		return callErr
	}
	return nil
}

func ensureStartShortcut(exe string) error {
	startDir := filepath.Join(os.Getenv("APPDATA"), "Microsoft", "Windows", "Start Menu", "Programs")
	if err := os.MkdirAll(startDir, 0700); err != nil {
		return err
	}
	if err := ole.CoInitializeEx(0, ole.COINIT_APARTMENTTHREADED); err != nil {
		return err
	}
	defer ole.CoUninitialize()

	unknown, err := oleutil.CreateObject("WScript.Shell")
	if err != nil {
		return err
	}
	defer unknown.Release()

	shell, err := unknown.QueryInterface(ole.IID_IDispatch)
	if err != nil {
		return err
	}
	defer shell.Release()

	shortcutValue, err := oleutil.CallMethod(shell, "CreateShortcut", filepath.Join(startDir, "Respire.lnk"))
	if err != nil {
		return err
	}
	shortcut := shortcutValue.ToIDispatch()
	defer shortcutValue.Clear()
	if shortcut == nil {
		return ole.NewError(ole.E_FAIL)
	}

	if _, err = oleutil.PutProperty(shortcut, "TargetPath", exe); err != nil {
		return err
	}
	if _, err = oleutil.PutProperty(shortcut, "WorkingDirectory", filepath.Dir(exe)); err != nil {
		return err
	}
	if _, err = oleutil.PutProperty(shortcut, "Description", "Respire"); err != nil {
		return err
	}
	if _, err = oleutil.PutProperty(shortcut, "IconLocation", exe+",0"); err != nil {
		return err
	}
	if _, err = oleutil.CallMethod(shortcut, "Save"); err != nil {
		return err
	}
	return nil
}

func registerAppPath(exe string) error {
	keyPath := `Software\Microsoft\Windows\CurrentVersion\App Paths\respire.exe`
	key, _, err := registry.CreateKey(registry.CURRENT_USER, keyPath, registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer key.Close()
	if err := key.SetStringValue("", exe); err != nil {
		return err
	}
	return key.SetStringValue("Path", filepath.Dir(exe))
}
