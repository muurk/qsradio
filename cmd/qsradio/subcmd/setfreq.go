// SPDX-License-Identifier: Apache-2.0

package subcmd

import (
	"flag"
	"fmt"
	"time"

	"github.com/muurk/qsradio/pkg/qsradio/radio"
)

// SetFreq implements "qsradio set-freq": tunes VFO A to the given frequency,
// measures the latency of each step, and reports which control path was used
// (live BK4819 register write or EEPROM fallback).
func SetFreq(args []string) error {
	fs := flag.NewFlagSet("set-freq", flag.ContinueOnError)
	port := fs.String("port", "", "serial port (e.g. /dev/ttyACM0 on Linux, /dev/cu.usbmodem... on macOS, COM5 on Windows)")
	freqHz := fs.Uint64("freq", 0, "target frequency in Hz (e.g. 145500000)")
	reboot := fs.Bool("reboot", false, "send CMD_REBOOT after write so the radio applies the new frequency")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *port == "" {
		return fmt.Errorf("--port is required")
	}
	if *freqHz == 0 {
		return fmt.Errorf("--freq is required")
	}

	// --- open ---
	t0 := time.Now()
	r, err := radio.Open(*port)
	if err != nil {
		return err
	}
	defer r.Close()
	fmt.Printf("open+handshake:  %v\n", time.Since(t0))

	// --- read current state ---
	t1 := time.Now()
	freqBefore, err := r.GetFrequency()
	if err != nil {
		return fmt.Errorf("get frequency: %w", err)
	}
	modeBefore, bwBefore, _ := r.GetMode()
	fmt.Printf("get freq+mode:   %v\n", time.Since(t1))
	fmt.Printf("  before: %.6f MHz  mode=%s  bw=%d Hz\n",
		float64(freqBefore)/1e6, modeBefore, int(bwBefore))

	// --- write new frequency ---
	caps, hasCaps := r.(radio.Capable)

	t2 := time.Now()
	if err := r.SetFrequency(*freqHz); err != nil {
		return fmt.Errorf("set frequency: %w", err)
	}
	writeLatency := time.Since(t2)

	livePath := hasCaps && caps.Capabilities().BK4819RegAccess
	if livePath {
		fmt.Printf("live BK4819 write: %v  (two CMD_0602 register writes, no reboot needed)\n", writeLatency)
	} else {
		fmt.Printf("EEPROM write:      %v  (reboot required to apply)\n", writeLatency)
	}

	// --- read back to verify ---
	t3 := time.Now()
	freqAfter, err := r.GetFrequency()
	if err != nil {
		return fmt.Errorf("read-back: %w", err)
	}
	readback := time.Since(t3)
	if livePath {
		fmt.Printf("live BK4819 read:  %v  (two CMD_0601 register reads)\n", readback)
	} else {
		fmt.Printf("EEPROM read-back:  %v\n", readback)
	}
	fmt.Printf("  after:  %.6f MHz  (target %.6f MHz)\n",
		float64(freqAfter)/1e6, float64(*freqHz)/1e6)

	if freqAfter != *freqHz {
		fmt.Printf("  WARNING: read-back mismatch\n")
	} else if livePath {
		fmt.Printf("  radio tuned OK (no reboot needed)\n")
	} else {
		fmt.Printf("  EEPROM verified OK (reboot required to apply)\n")
	}

	if livePath || !*reboot {
		if !livePath {
			fmt.Printf("\nRun with --reboot to trigger CMD_REBOOT and apply the EEPROM change.\n")
		}
		return nil
	}

	// --- reboot ---
	rb, ok := r.(radio.Rebooter)
	if !ok {
		return fmt.Errorf("this radio implementation does not support reboot")
	}
	fmt.Printf("\nSending CMD_REBOOT...\n")
	t4 := time.Now()
	if err := rb.Reboot(); err != nil {
		return fmt.Errorf("reboot: %w", err)
	}
	fmt.Printf("reboot command sent in %v\n", time.Since(t4))
	fmt.Printf("Radio is restarting. Wait 4-5 s, then verify the display shows %.6f MHz.\n",
		float64(*freqHz)/1e6)
	fmt.Printf("\nTotal time (open -> write -> reboot): %v\n", time.Since(t0))

	return nil
}
