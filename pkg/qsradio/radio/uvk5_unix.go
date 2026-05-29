// SPDX-License-Identifier: Apache-2.0

//go:build !windows

package radio

import (
	"reflect"
	"time"
	"unsafe"

	"go.bug.st/serial"
	"golang.org/x/sys/unix"
)

// serialPortFD extracts the raw file descriptor from a go.bug.st/serial Port.
// The library's unixPort struct has handle int as its first field (confirmed
// from go.bug.st/serial v1.6.4 source). This fd is required for direct TIOCMSET
// ioctl calls because go.bug.st/serial's SetDTR/SetRTS are not reliable on USB
// CDC ACM devices due to a TIOCMGET+TIOCMSET race condition confirmed with the
// AIOC on Linux.
func serialPortFD(p serial.Port) int {
	type portHandle struct{ handle int }
	return (*portHandle)(unsafe.Pointer(reflect.ValueOf(p).Pointer())).handle
}

// pttAssert drives DTR=1, RTS=0 to key the radio via direct ioctl calls.
// The DTR toggle sequence (low then high before asserting RTS low) matches
// the confirmed pyserial behaviour on the AIOC.
func pttAssert(sp serial.Port) error {
	fd := serialPortFD(sp)
	unix.IoctlSetPointerInt(fd, unix.TIOCMBIC, unix.TIOCM_DTR)
	time.Sleep(20 * time.Millisecond)
	unix.IoctlSetPointerInt(fd, unix.TIOCMBIS, unix.TIOCM_DTR)
	time.Sleep(20 * time.Millisecond)
	return unix.IoctlSetPointerInt(fd, unix.TIOCMBIC, unix.TIOCM_RTS)
}

// pttRelease drives DTR=0, RTS=1 to unkey the radio.
func pttRelease(sp serial.Port) {
	fd := serialPortFD(sp)
	unix.IoctlSetPointerInt(fd, unix.TIOCMSET, unix.TIOCM_RTS)
}

// ensureRTSHigh sets RTS=1 (PTT inactive) on an open port during cleanup.
func ensureRTSHigh(sp serial.Port) {
	fd := serialPortFD(sp)
	unix.IoctlSetPointerInt(fd, unix.TIOCMSET, unix.TIOCM_RTS)
}
