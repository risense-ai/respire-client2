//go:build linux || darwin

package host

import (
	"errors"
	"net"
	"os/exec"
	"syscall"
)

func configureCommand(*exec.Cmd) {}

func IsUnavailable(err error) bool {
	var op *net.OpError
	return errors.As(err, &op) && errors.Is(err, syscall.ECONNREFUSED)
}
