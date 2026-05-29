// SPDX-License-Identifier: Apache-2.0

package transport

import (
	"bytes"
	"testing"
)

// TestCRC16KnownVector verifies our implementation against the standard
// CRC-16/XMODEM check value: crc16("123456789") == 0x31C3.
func TestCRC16KnownVector(t *testing.T) {
	got := crc16CCITT([]byte("123456789"))
	if got != 0x31C3 {
		t.Errorf("crc16CCITT(\"123456789\") = %04x, want 31c3", got)
	}
}

// TestCRC16Empty verifies the zero-input case (initial value is 0).
func TestCRC16Empty(t *testing.T) {
	if got := crc16CCITT(nil); got != 0 {
		t.Errorf("crc16CCITT(nil) = %04x, want 0000", got)
	}
}

// TestCRC16VersionRequestPayload pins the CRC for the firmware version
// request payload so that future changes to the key or algorithm are caught.
func TestCRC16VersionRequestPayload(t *testing.T) {
	payload := []byte{0x14, 0x05, 0x04, 0x00, 0x46, 0x9c, 0x6f, 0x64}
	got := crc16CCITT(payload)
	if got != 0x3eb4 {
		t.Errorf("crc16CCITT(version request) = %04x, want 3eb4", got)
	}
}

// TestXORObfuscateSymmetric verifies the symmetric property: applying XOR
// twice returns the original data.
func TestXORObfuscateSymmetric(t *testing.T) {
	original := []byte{0x14, 0x05, 0x04, 0x00, 0x46, 0x9c, 0x6f, 0x64, 0xb4, 0x3e}
	data := make([]byte, len(original))
	copy(data, original)
	xorObfuscate(data)
	xorObfuscate(data)
	if !bytes.Equal(data, original) {
		t.Errorf("double XOR not identity: got %x, want %x", data, original)
	}
}

// TestXORObfuscateKeyByte verifies the first key byte against the reference
// value from libuvk5.py: payload_xor([0x00]) == [0x16].
func TestXORObfuscateKeyByte(t *testing.T) {
	data := []byte{0x00}
	xorObfuscate(data)
	if data[0] != 0x16 {
		t.Errorf("xorObfuscate([0x00])[0] = %02x, want 16 (first byte of obfuscation key)", data[0])
	}
}

// TestWriteFrameVersionRequest pins the exact wire bytes for the firmware
// version request. This vector is derived from the reference algorithm and
// verified by successful communication with real hardware.
//
// Frame layout:
//
//	[AB CD]      start (plain)
//	[08 00]      cmd_len = 8 (plain)
//	[10 bytes]   XOR(payload + CRC)
//	[DC BA]      end (plain)
func TestWriteFrameVersionRequest(t *testing.T) {
	payload := []byte{0x14, 0x05, 0x04, 0x00, 0x46, 0x9c, 0x6f, 0x64}

	want := []byte{
		0xAB, 0xCD, // start
		0x08, 0x00, // cmd_len = 8 (payload, no CRC)
		0x02, 0x69, 0x10, 0xe6, 0x68, 0x0d, 0x62, 0x24, 0x95, 0x0b, // XOR(payload+CRC)
		0xDC, 0xBA, // end
	}

	var buf bytes.Buffer
	f := New(&buf)
	if err := f.WriteFrame(payload); err != nil {
		t.Fatalf("WriteFrame: %v", err)
	}
	if !bytes.Equal(buf.Bytes(), want) {
		t.Errorf("WriteFrame bytes:\n got  %x\n want %x", buf.Bytes(), want)
	}
}

// TestReadFrameHardwareCapture verifies ReadFrame against bytes captured live
// from a UV-K5 running F4HWN v4.3 in response to a firmware version request.
func TestReadFrameHardwareCapture(t *testing.T) {
	// 48-byte response frame captured on 2026-05-16 from F4HWN v4.3.
	raw := []byte{
		0xab, 0xcd, 0x28, 0x00, // start + cmd_len=40
		0x03, 0x69, 0x30, 0xe6, 0x68, 0xa5, 0x45, 0x17, 0x6f, 0x15, 0xa3, 0x74,
		0x3d, 0x30, 0xe9, 0x9f, 0x00, 0x01, 0x01, 0xe2, 0x2e, 0x91, 0x16, 0x04,
		0x21, 0x35, 0xd5, 0x40, 0x13, 0x03, 0xe9, 0x80, 0x16, 0x6c, 0x14, 0xe6,
		0x2e, 0x91, 0x0d, 0x40, 0xde, 0xca, // XOR'd payload (40B) + CRC (2B)
		0xdc, 0xba, // end
	}

	// Expected payload after deobfuscation (opcode + body_len + body, 40 bytes).
	want := []byte{
		0x15, 0x05, // opcode 0x0515 (firmware version response)
		0x24, 0x00, // body_len = 36
		0x46, 0x34, 0x48, 0x57, 0x4e, 0x20, 0x76, 0x34, 0x2e, 0x33, 0x00, // "F4HWN v4.3\0"
		0x1f, 0x16, 0x6d, 0x15, 0x04, 0x00, 0x00, 0x1b, 0x44, // remaining body fields
		0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, // (padding / reserved)
		0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
	}

	f := New(bytes.NewBuffer(raw))
	got, err := f.ReadFrame()
	if err != nil {
		t.Fatalf("ReadFrame: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("ReadFrame payload:\n got  %x\n want %x", got, want)
	}
}

// TestFrameRoundTrip writes a frame then reads it back and checks that the
// payload is preserved exactly.
func TestFrameRoundTrip(t *testing.T) {
	payloads := [][]byte{
		{0x14, 0x05, 0x04, 0x00, 0x46, 0x9c, 0x6f, 0x64},
		{0x1b, 0x05, 0x08, 0x00, 0x00, 0x00, 0x40, 0x00, 0x46, 0x9c, 0x6f, 0x64},
		make([]byte, 64), // all-zero payload
	}

	for _, payload := range payloads {
		var buf bytes.Buffer
		f := New(&buf)
		if err := f.WriteFrame(payload); err != nil {
			t.Fatalf("WriteFrame: %v", err)
		}
		got, err := f.ReadFrame()
		if err != nil {
			t.Fatalf("ReadFrame: %v", err)
		}
		if !bytes.Equal(got, payload) {
			t.Errorf("round-trip mismatch:\n got  %x\n want %x", got, payload)
		}
	}
}
