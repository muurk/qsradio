// SPDX-License-Identifier: Apache-2.0

package subcmd

import (
	"flag"
	"fmt"
	"os"

	"github.com/muurk/qsradio/pkg/qsradio/radio"
)

const (
	eepromSize      = 0x2000 // 8192 bytes, full UV-K5 configuration EEPROM
	eepromBlockSize = 0x80   // 128 bytes per read (hardware maximum)
)

// DumpEEPROM implements "qsradio dump-eeprom": reads the full EEPROM from the
// radio and writes the raw bytes to stdout. Progress is reported on stderr so
// that stdout can be piped directly to a file.
func DumpEEPROM(args []string) error {
	fs := flag.NewFlagSet("dump-eeprom", flag.ContinueOnError)
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
	fmt.Fprintf(os.Stderr, "connected: %s\n", ver)
	fmt.Fprintf(os.Stderr, "reading %d bytes in %d-byte blocks...\n", eepromSize, eepromBlockSize)

	total := 0
	blocks := eepromSize / eepromBlockSize
	for i := 0; i < blocks; i++ {
		offset := uint16(i * eepromBlockSize)
		data, err := r.ReadEEPROM(offset, eepromBlockSize)
		if err != nil {
			return fmt.Errorf("read EEPROM at offset 0x%04x: %w", offset, err)
		}
		if _, err := os.Stdout.Write(data); err != nil {
			return fmt.Errorf("write stdout: %w", err)
		}
		total += len(data)
		fmt.Fprintf(os.Stderr, "\r  %d / %d bytes", total, eepromSize)
	}

	fmt.Fprintf(os.Stderr, "\ndone\n")
	return nil
}
