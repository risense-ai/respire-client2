package desktop

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"time"
)

func platformFonts() ([]string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "/usr/bin/osascript", "-l", "JavaScript", "-e", "ObjC.import('AppKit'); JSON.stringify(ObjC.deepUnwrap($.NSFontManager.sharedFontManager.availableFontFamilies))").Output()
	if err != nil {
		return nil, fmt.Errorf("读取系统字体: %w", err)
	}
	var names []string
	if err = json.Unmarshal(out, &names); err != nil {
		return nil, err
	}
	return names, nil
}
