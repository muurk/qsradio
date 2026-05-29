// SPDX-License-Identifier: Apache-2.0

package subcmd

import (
	"encoding/hex"
	"flag"
	"fmt"
	"io"
	"time"

	"go.bug.st/serial"

	"github.com/muurk/qsradio/pkg/qsradio/protocol"
	"github.com/muurk/qsradio/pkg/qsradio/transport"
)

// RawDump sends a firmware version request and prints the raw bytes
// received in response, before any framing or XOR is applied.
// Used for diagnosing wire-level framing issues.
func RawDump(args []string) error {
	fs := flag.NewFlagSet("rawdump", flag.ContinueOnError)
	port := fs.String("port", "", "serial device")
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

	// Drain any stale bytes in the buffer before sending.
	sp.SetReadTimeout(100 * time.Millisecond)
	drain := make([]byte, 256)
	n, _ := sp.Read(drain)
	if n > 0 {
		fmt.Printf("drained %d stale bytes: %s\n", n, hex.EncodeToString(drain[:n]))
	}

	// Send the version request frame and show what we sent.
	payload := protocol.FirmwareVersionRequest()
	f := transport.New(sp)

	// Build the raw frame manually to display it before sending.
	if err := f.WriteFrame(payload); err != nil {
		return fmt.Errorf("write: %w", err)
	}
	fmt.Printf("sent payload (pre-frame): %s\n", hex.EncodeToString(payload))

	// Read up to 256 raw bytes with a generous timeout.
	sp.SetReadTimeout(2 * time.Second)
	buf := make([]byte, 256)
	total := 0
	for total < len(buf) {
		n, err := sp.Read(buf[total:])
		total += n
		if err == io.EOF || n == 0 {
			break
		}
		if err != nil {
			fmt.Printf("read error after %d bytes: %v\n", total, err)
			break
		}
		// Stop once we see the end marker.
		if total >= 2 && buf[total-2] == 0xDC && buf[total-1] == 0xBA {
			break
		}
	}

	fmt.Printf("received %d bytes: %s\n", total, hex.EncodeToString(buf[:total]))
	if total > 0 {
		fmt.Println("annotated:")
		for i, b := range buf[:total] {
			fmt.Printf("  [%02d] %02x\n", i, b)
		}
	}
	return nil
}
