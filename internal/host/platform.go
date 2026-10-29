package host

import (
	"debug/elf"
	"debug/macho"
	"debug/pe"
	"encoding/binary"
	"fmt"
	"path/filepath"
	"runtime"
	"strings"
)

func Platform() string { return runtime.GOOS + "-" + runtime.GOARCH }

func ClientAsset(goos, arch string) string {
	ext := map[string]string{"windows": ".exe", "darwin": ".zip", "linux": ".tar.gz"}[goos]
	if ext == "" {
		return ""
	}
	return "respire-" + goos + "-" + arch + ext
}

func binaryExtension() string {
	if runtime.GOOS == "windows" {
		return ".exe"
	}
	return ""
}

func runtimeBinary() string { return "rsrs" + binaryExtension() }

func runtimeTarget() (string, error) {
	target := map[string]string{
		"windows-amd64":  "x86_64-pc-windows-msvc",
		"windows-386":    "i686-pc-windows-msvc",
		"windows-arm64":  "aarch64-pc-windows-msvc",
		"darwin-amd64":   "x86_64-apple-darwin",
		"darwin-arm64":   "aarch64-apple-darwin",
		"linux-amd64":    "x86_64-unknown-linux-gnu",
		"linux-386":      "i686-unknown-linux-gnu",
		"linux-arm64":    "aarch64-unknown-linux-gnu",
		"linux-mipsle":   "mipsel-unknown-linux-gnu",
		"linux-mips64le": "mips64el-unknown-linux-gnuabi64",
		"linux-loong64":  "loongarch64-unknown-linux-gnu",
	}[Platform()]
	if target == "" {
		return "", fmt.Errorf("没有为 %s 配置 CLI 发布目标", Platform())
	}
	return target, nil
}

func samePath(a, b string) bool {
	a, b = filepath.Clean(a), filepath.Clean(b)
	if runtime.GOOS == "windows" {
		return strings.EqualFold(a, b)
	}
	return a == b
}

func verifyExecutable(path string) error {
	mismatch := fmt.Errorf("CLI 可执行文件与 %s 不匹配", Platform())
	switch runtime.GOOS {
	case "windows":
		image, err := pe.Open(path)
		if err != nil {
			return err
		}
		defer image.Close()
		machine := map[string]uint16{"amd64": pe.IMAGE_FILE_MACHINE_AMD64, "386": pe.IMAGE_FILE_MACHINE_I386, "arm64": pe.IMAGE_FILE_MACHINE_ARM64}[runtime.GOARCH]
		if machine == 0 || image.Machine != machine {
			return mismatch
		}
	case "darwin":
		image, err := macho.Open(path)
		if err != nil {
			return err
		}
		defer image.Close()
		cpu := map[string]macho.Cpu{"amd64": macho.CpuAmd64, "arm64": macho.CpuArm64}[runtime.GOARCH]
		if cpu == 0 || image.Cpu != cpu {
			return mismatch
		}
	case "linux":
		image, err := elf.Open(path)
		if err != nil {
			return err
		}
		defer image.Close()
		machine := map[string]elf.Machine{"amd64": elf.EM_X86_64, "386": elf.EM_386, "arm64": elf.EM_AARCH64, "mipsle": elf.EM_MIPS, "mips64le": elf.EM_MIPS, "loong64": elf.EM_LOONGARCH}[runtime.GOARCH]
		class := elf.ELFCLASS64
		if runtime.GOARCH == "386" || runtime.GOARCH == "mipsle" {
			class = elf.ELFCLASS32
		}
		if machine == 0 || image.Machine != machine || image.Class != class || image.ByteOrder != binary.LittleEndian {
			return mismatch
		}
	default:
		return mismatch
	}
	return nil
}
