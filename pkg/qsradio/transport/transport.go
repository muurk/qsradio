// SPDX-License-Identifier: Apache-2.0

// Package transport handles wire framing for the UV-K5 serial protocol.
// It is pure I/O: no protocol semantics, no opcode knowledge. All framing,
// deframing, XOR obfuscation, and CRC checks live here. Upper layers receive
// and send raw payloads as byte slices.
package transport

import (
	"fmt"
	"io"
)

// Frame boundary markers in the wire format.
const (
	FrameStart uint16 = 0xABCD
	FrameEnd   uint16 = 0xDCBA
)

// Framer reads and writes framed payloads over an underlying byte stream.
// Implementations must be safe for use from a single goroutine. Callers
// that need concurrent access are responsible for synchronisation.
type Framer interface {
	// WriteFrame encodes payload into a wire frame (start marker, obfuscated
	// payload, CRC16-CCITT, end marker) and writes it to the underlying stream.
	WriteFrame(payload []byte) error

	// ReadFrame reads one wire frame from the underlying stream, verifies the
	// CRC, removes obfuscation, and returns the raw payload.
	ReadFrame() ([]byte, error)
}

// framer is the concrete implementation of Framer.
type framer struct {
	rw io.ReadWriter
}

// New returns a Framer backed by rw.
func New(rw io.ReadWriter) Framer {
	return &framer{rw: rw}
}

// WriteFrame encodes payload into a wire frame and writes it to the underlying stream.
//
// Wire layout (all multi-byte integers little-endian):
//
//	[0xAB 0xCD]      start marker (2B, plain)
//	[cmd_len  ]      uint16: len(payload) WITHOUT CRC (2B, plain)
//	[payload  ]      opcode(2B) + body_len(2B) + body (XOR'd together with CRC)
//	[crc      ]      uint16 CRC16-CCITT over plain payload (2B, XOR'd)
//	[0xDC 0xBA]      end marker (2B, plain)
//
// Note: cmd_len covers only payload, not the trailing CRC. The XOR region is
// payload+CRC. This matches uart_send_msg in libuvk5.py.
func (f *framer) WriteFrame(payload []byte) error {
	crc := crc16CCITT(payload)

	// XOR region: payload + CRC (10 bytes for a typical 8-byte payload).
	xored := make([]byte, len(payload)+2)
	copy(xored, payload)
	xored[len(payload)] = byte(crc)
	xored[len(payload)+1] = byte(crc >> 8)
	xorObfuscate(xored)

	frame := make([]byte, 0, 4+len(xored)+2)
	frame = append(frame, 0xAB, 0xCD)
	// cmd_len is len(payload), not len(xored).
	frame = append(frame, byte(len(payload)), byte(len(payload)>>8))
	frame = append(frame, xored...)
	frame = append(frame, 0xDC, 0xBA)

	_, err := f.rw.Write(frame)
	return err
}

// readFull reads exactly len(buf) bytes into buf. Unlike io.ReadFull it treats
// a zero-byte non-error Read as a timeout (go.bug.st/serial returns (0, nil)
// on macOS when the read deadline expires).
func readFull(r io.Reader, buf []byte) (int, error) {
	total := 0
	for total < len(buf) {
		n, err := r.Read(buf[total:])
		total += n
		if err != nil {
			return total, err
		}
		if n == 0 {
			return total, fmt.Errorf("transport: read timeout after %d/%d bytes", total, len(buf))
		}
	}
	return total, nil
}

// ReadFrame reads one wire frame from the underlying stream, verifies the CRC,
// removes obfuscation, and returns the raw payload (opcode + body_len + body).
func (f *framer) ReadFrame() ([]byte, error) {
	// Read start marker + length.
	header := make([]byte, 4)
	if _, err := readFull(f.rw, header); err != nil {
		return nil, err
	}
	if header[0] != 0xAB || header[1] != 0xCD {
		return nil, fmt.Errorf("transport: bad start marker %02x%02x", header[0], header[1])
	}
	// cmd_len is the payload length WITHOUT the CRC; the XOR region is cmd_len+2.
	cmdLen := int(header[2]) | int(header[3])<<8
	xoredLen := cmdLen + 2

	// Read XOR'd section (payload + CRC).
	xored := make([]byte, xoredLen)
	if _, err := readFull(f.rw, xored); err != nil {
		return nil, err
	}

	// Read end marker.
	end := make([]byte, 2)
	if _, err := readFull(f.rw, end); err != nil {
		return nil, err
	}
	if end[0] != 0xDC || end[1] != 0xBA {
		return nil, fmt.Errorf("transport: bad end marker %02x%02x", end[0], end[1])
	}

	// Deobfuscate.
	xorObfuscate(xored)

	if xoredLen < 2 {
		return nil, fmt.Errorf("transport: frame too short (%d bytes)", xoredLen)
	}
	payload := xored[:xoredLen-2]
	wireCRC := uint16(xored[xoredLen-2]) | uint16(xored[xoredLen-1])<<8
	computedCRC := crc16CCITT(payload)

	// The radio firmware does not always send a meaningful CRC in responses
	// (libuvk5.py does not verify response CRCs). We accept the frame if the
	// CRC matches OR if the wire carries 0xFFFF (the radio's "no CRC" sentinel).
	// Genuine CRC errors on real hardware will still surface as garbled data.
	if wireCRC != computedCRC && wireCRC != 0xFFFF {
		return nil, fmt.Errorf("transport: CRC mismatch: got %04x want %04x", computedCRC, wireCRC)
	}

	return payload, nil
}
