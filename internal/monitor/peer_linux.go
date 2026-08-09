//go:build linux

package monitor

import (
	"fmt"
	"net"
	"os"

	"golang.org/x/sys/unix"
)

func validatePeer(conn *net.UnixConn) error {
	raw, err := conn.SyscallConn()
	if err != nil {
		return err
	}
	var uid uint32
	var peerErr error
	if err := raw.Control(func(fd uintptr) {
		var cred *unix.Ucred
		cred, peerErr = unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED)
		if peerErr == nil {
			uid = cred.Uid
		}
	}); err != nil {
		return err
	}
	if peerErr != nil {
		return peerErr
	}
	if uid != uint32(os.Geteuid()) {
		return fmt.Errorf("peer uid %d does not match monitor uid", uid)
	}
	return nil
}
