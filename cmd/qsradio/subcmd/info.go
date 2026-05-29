// SPDX-License-Identifier: Apache-2.0

package subcmd

import (
	"flag"
	"fmt"

	"github.com/muurk/qsradio/pkg/qsradio/radio"
)

// Info implements "qsradio info": opens the radio, performs the firmware
// version handshake, and reports which optional firmware features are available.
func Info(args []string) error {
	fs := flag.NewFlagSet("info", flag.ContinueOnError)
	port := fs.String("port", "", "serial port (e.g. /dev/ttyACM0 on Linux, /dev/cu.usbmodem... on macOS, COM5 on Windows)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *port == "" {
		return fmt.Errorf("--port is required")
	}

	r, err := radio.Open(*port)
	if err != nil {
		return err
	}
	defer r.Close()

	ver, err := r.GetFirmwareVersion()
	if err != nil {
		return fmt.Errorf("get firmware version: %w", err)
	}
	fmt.Printf("firmware: %s\n", ver)

	// Report probed capabilities.
	if c, ok := r.(radio.Capable); ok {
		caps := c.Capabilities()
		if caps.BK4819RegAccess {
			freq, _ := r.GetFrequency()
			fmt.Printf("CMD_0601/0602 (BK4819 reg access): YES  live freq=%.6f MHz\n", float64(freq)/1e6)
		} else {
			fmt.Printf("CMD_0601/0602 (BK4819 reg access): NO   (need ENABLE_UART_RW_BK_REGS in firmware)\n")
		}
		if caps.RSSICommand || caps.BK4819RegAccess {
			if raw, err := r.GetSMeter(); err == nil {
				dbm := float32(raw)/2 - 160
				fmt.Printf("RSSI:                              raw=%d  (%.0f dBm)\n", raw, dbm)
			}
		}
		if caps.RSSICommand {
			fmt.Printf("CMD_0527      (RSSI):              YES\n")
		} else {
			fmt.Printf("CMD_0527      (RSSI):              NO   (need ENABLE_EXTRA_UART_CMD in firmware)\n")
		}
	}

	return nil
}
