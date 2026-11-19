package host

import (
	"errors"
	"os"

	"golang.org/x/sys/windows"
)

func canonicalSessionDirectory(path string) (string, error) {
	directory, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer directory.Close()
	var buffer [32768]uint16
	n, err := windows.GetFinalPathNameByHandle(windows.Handle(directory.Fd()), &buffer[0], uint32(len(buffer)), 0)
	if err != nil {
		return "", err
	}
	if n == 0 || n >= uint32(len(buffer)) {
		return "", errors.New("historical profile has no canonical Windows identity")
	}
	return windows.UTF16ToString(buffer[:n]), nil
}

func publishSessionFile(from, to string) error {
	return moveDurableRuntimeFile(from, to, windows.MOVEFILE_REPLACE_EXISTING)
}

func publishFreshRuntimeFile(from, to string) error {
	return moveDurableRuntimeFile(from, to, 0)
}

func moveDurableRuntimeFile(from, to string, flags uint32) error {
	source, err := windows.UTF16PtrFromString(from)
	if err != nil {
		return err
	}
	destination, err := windows.UTF16PtrFromString(to)
	if err != nil {
		return err
	}
	return windows.MoveFileEx(source, destination, flags|windows.MOVEFILE_WRITE_THROUGH)
}
