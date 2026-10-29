package desktop

import (
	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/w32"
)

type nativeAppearance struct{ small, large map[bool]w32.HICON }

func (n *nativeAppearance) apply(window *application.WebviewWindow, image []byte, dark bool) error {
	hwnd := w32.HWND(window.NativeWindow())
	if hwnd == 0 {
		return nil
	}
	if n.small == nil {
		n.small = map[bool]w32.HICON{}
		n.large = map[bool]w32.HICON{}
	}
	if n.small[dark] == 0 {
		small, err := w32.CreateSmallHIconFromImage(image)
		if err != nil {
			return err
		}
		large, err := w32.CreateLargeHIconFromImage(image)
		if err != nil {
			w32.DestroyIcon(small)
			return err
		}
		n.small[dark], n.large[dark] = small, large
	}
	w32.SetTheme(uintptr(hwnd), dark)
	w32.SendMessage(hwnd, w32.WM_SETICON, w32.ICON_SMALL, uintptr(n.small[dark]))
	w32.SendMessage(hwnd, w32.WM_SETICON, w32.ICON_BIG, uintptr(n.large[dark]))
	return nil
}

func (n *nativeAppearance) close() {
	for _, image := range n.small {
		w32.DestroyIcon(image)
	}
	for _, image := range n.large {
		w32.DestroyIcon(image)
	}
}
