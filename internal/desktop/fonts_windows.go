package desktop

import (
	"errors"
	"golang.org/x/sys/windows/registry"
	"strings"
)

func platformFonts() ([]string, error) {
	var fonts []string
	for _, root := range []registry.Key{registry.LOCAL_MACHINE, registry.CURRENT_USER} {
		key, err := registry.OpenKey(root, `SOFTWARE\Microsoft\Windows NT\CurrentVersion\Fonts`, registry.QUERY_VALUE)
		if errors.Is(err, registry.ErrNotExist) && root == registry.CURRENT_USER {
			continue
		}
		if err != nil {
			return nil, err
		}
		names, readErr := key.ReadValueNames(-1)
		closeErr := key.Close()
		if err := errors.Join(readErr, closeErr); err != nil {
			return nil, err
		}
		for _, name := range names {
			for _, suffix := range []string{" (TrueType)", " (OpenType)", " (TrueType italic)"} {
				name = strings.TrimSuffix(name, suffix)
			}
			fonts = append(fonts, strings.Split(name, " & ")...)
		}
	}
	return fonts, nil
}
