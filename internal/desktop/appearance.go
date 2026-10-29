package desktop

import (
	"log"

	"github.com/wailsapp/wails/v3/pkg/application"
)

func (d *desktop) applyAppearance() {
	application.InvokeAsync(func() {
		theme := d.m.Preferences().Theme
		dark := theme == "dark" || theme == "system" && d.app.Env.IsDarkMode()
		image := lightIcon
		resolved := "light"
		if dark {
			image = icon
			resolved = "dark"
		}
		d.app.SetIcon(image)
		if d.tray != nil {
			d.tray.SetIcon(image)
		}
		if d.window != nil {
			if err := d.native.apply(d.window, image, dark); err != nil {
				log.Printf("window appearance: %v", err)
			}
		}
		d.mu.Lock()
		d.resolvedTheme = resolved
		d.mu.Unlock()
	})
}
