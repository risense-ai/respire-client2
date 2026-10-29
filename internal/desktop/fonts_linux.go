package desktop

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

func platformFonts() ([]string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "fc-list", ":", "family").Output()
	if err != nil {
		return nil, fmt.Errorf("读取系统字体（需要 fontconfig）: %w", err)
	}
	return strings.FieldsFunc(string(out), func(c rune) bool { return c == ',' || c == '\n' }), nil
}
