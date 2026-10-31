//go:build windows

package desktop

import (
	"log"
	"os"
	"path/filepath"

	ole "github.com/go-ole/go-ole"
	"github.com/go-ole/go-ole/oleutil"
	"golang.org/x/sys/windows/registry"
)

// Windows shows an executable's bare file name until it has a Start Menu
// identity. This mirrors the Magpie release layout: a friendly .lnk plus an
// App Paths entry for the canonical respire.exe name.
func ensureShellIdentity() {
	exe, err := os.Executable()
	if err != nil {
		log.Printf("shell identity: resolve executable: %v", err)
		return
	}
	if err := ensureStartShortcut(exe); err != nil {
		log.Printf("shell identity: start shortcut: %v", err)
	}
	if err := registerAppPath(exe); err != nil {
		log.Printf("shell identity: App Paths: %v", err)
	}
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
