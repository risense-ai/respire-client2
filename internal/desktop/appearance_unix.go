//go:build darwin || linux

package desktop

import "github.com/wailsapp/wails/v3/pkg/application"

type nativeAppearance struct{}

func (*nativeAppearance) apply(*application.WebviewWindow, []byte, bool) error { return nil }
func (*nativeAppearance) close()                                               {}
