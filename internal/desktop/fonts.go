package desktop

import (
	"sort"
	"strings"
)

func SystemFonts() ([]string, error) {
	names, err := platformFonts()
	if err != nil {
		return nil, err
	}
	unique := map[string]bool{}
	fonts := []string{}
	for _, name := range names {
		name = strings.TrimSpace(name)
		if name != "" && !unique[name] {
			unique[name] = true
			fonts = append(fonts, name)
		}
	}
	sort.Strings(fonts)
	return fonts, nil
}
