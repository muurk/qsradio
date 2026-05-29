// SPDX-License-Identifier: Apache-2.0

//go:build windows

package radio

import (
	"time"

	"go.bug.st/serial"
)

// pttAssert drives DTR=1, RTS=0 to key the radio.
// On Windows, go.bug.st/serial's SetDTR/SetRTS use EscapeCommFunction which
// does not have the CDC-ACM TIOCMGET+TIOCMSET race condition present on Linux
// and macOS. Direct ioctl access is not needed.
func pttAssert(sp serial.Port) error {
	_ = sp.SetDTR(false)
	time.Sleep(20 * time.Millisecond)
	_ = sp.SetDTR(true)
	time.Sleep(20 * time.Millisecond)
	return sp.SetRTS(false)
}

// pttRelease drives DTR=0, RTS=1 to unkey the radio.
func pttRelease(sp serial.Port) {
	_ = sp.SetDTR(false)
	_ = sp.SetRTS(true)
}

// ensureRTSHigh sets RTS=1 (PTT inactive) on an open port during cleanup.
func ensureRTSHigh(sp serial.Port) {
	_ = sp.SetRTS(true)
}
