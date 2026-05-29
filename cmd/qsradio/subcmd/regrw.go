// SPDX-License-Identifier: Apache-2.0

package subcmd

import (
	"encoding/hex"
	"flag"
	"fmt"
	"time"

	"go.bug.st/serial"

	"github.com/muurk/qsradio/pkg/qsradio/protocol"
	"github.com/muurk/qsradio/pkg/qsradio/transport"
)

// RegRead implements "qsradio reg-read": sends CMD_0601 to read a BK4819
// register directly. If the firmware responds, ENABLE_UART_RW_BK_REGS is
// compiled in. If it times out, the flag is absent.
func RegRead(args []string) error {
	fs := flag.NewFlagSet("reg-read", flag.ContinueOnError)
	port := fs.String("port", "", "serial device")
	reg := fs.Uint("reg", 0x38, "BK4819 register number (hex, e.g. 0x38 for freq-low)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *port == "" {
		return fmt.Errorf("--port is required")
	}

	sp, err := serial.Open(*port, &serial.Mode{
		BaudRate: 38400,
		DataBits: 8,
		Parity:   serial.NoParity,
		StopBits: serial.OneStopBit,
	})
	if err != nil {
		return fmt.Errorf("open %s: %w", *port, err)
	}
	defer sp.Close()

	// Drain stale bytes.
	sp.SetReadTimeout(100 * time.Millisecond)
	drain := make([]byte, 512)
	sp.Read(drain)

	// Long enough to see the firmware respond if CMD_0601 is compiled in.
	sp.SetReadTimeout(1 * time.Second)
	f := transport.New(sp)

	regByte := uint8(*reg & 0xFF)
	payload := protocol.ReadBK4819RegRequest(regByte)
	fmt.Printf("sending CMD_0601 (read BK4819 reg 0x%02x)\n", regByte)
	fmt.Printf("  payload: %s\n", hex.EncodeToString(payload))

	if err := f.WriteFrame(payload); err != nil {
		return fmt.Errorf("write: %w", err)
	}

	resp, err := f.ReadFrame()
	if err != nil {
		fmt.Printf("no response: %v\n", err)
		fmt.Printf("\nConclusion: ENABLE_UART_RW_BK_REGS is NOT compiled into this firmware.\n")
		fmt.Printf("CMD_0601/CMD_0602 (direct BK4819 register access) are unavailable.\n")
		fmt.Printf("To enable them, rebuild F4HWN with ENABLE_UART_RW_BK_REGS=1.\n")
		return nil
	}

	_, val, err := protocol.ParseReadBK4819RegResponse(resp)
	if err != nil {
		fmt.Printf("response parse error: %v\n", err)
		fmt.Printf("  raw payload: %s\n", hex.EncodeToString(resp))
		return nil
	}

	fmt.Printf("  response: reg=0x%02x value=0x%04x (%d)\n", regByte, val, val)
	fmt.Printf("\nConclusion: ENABLE_UART_RW_BK_REGS IS compiled in. CMD_0601/CMD_0602 available.\n")

	// For the frequency registers, show the decoded frequency.
	if regByte == protocol.BK4819RegFreqLow {
		// Need REG_39 to complete the picture.
		payload39 := protocol.ReadBK4819RegRequest(protocol.BK4819RegFreqHigh)
		if err := f.WriteFrame(payload39); err == nil {
			if resp, err := f.ReadFrame(); err == nil {
				if _, val39, err := protocol.ParseReadBK4819RegResponse(resp); err == nil {
					freq10 := uint64(val39)<<16 | uint64(val)
					fmt.Printf("  live BK4819 freq: %.6f MHz\n", float64(freq10*10)/1e6)
				}
			}
		}
	}

	return nil
}
