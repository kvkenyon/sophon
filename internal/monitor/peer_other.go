//go:build !darwin && !linux

package monitor

import (
	"errors"
	"net"
)

func validatePeer(_ *net.UnixConn) error {
	return errors.New("same-user Unix peer validation is unsupported on this platform")
}
