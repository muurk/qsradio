// SPDX-License-Identifier: Apache-2.0

package protocol_test

import (
	"bytes"
	"testing"

	"github.com/muurk/qsradio/pkg/qsradio/protocol"
)

// TestBuildCommandStructure verifies that BuildCommand produces a payload with
// the correct opcode and body-length prefix.
func TestBuildCommandStructure(t *testing.T) {
	body := []byte{0x01, 0x02, 0x03, 0x04}
	payload := protocol.BuildCommand(0x0514, body)

	if len(payload) != 8 {
		t.Fatalf("len(payload) = %d, want 8", len(payload))
	}
	// Opcode LE.
	if payload[0] != 0x14 || payload[1] != 0x05 {
		t.Errorf("opcode bytes = %02x %02x, want 14 05", payload[0], payload[1])
	}
	// Body length LE.
	if payload[2] != 0x04 || payload[3] != 0x00 {
		t.Errorf("body_len bytes = %02x %02x, want 04 00", payload[2], payload[3])
	}
	// Body.
	if !bytes.Equal(payload[4:], body) {
		t.Errorf("body = %x, want %x", payload[4:], body)
	}
}

// TestFirmwareVersionRequestBytes pins the exact payload bytes for the
// firmware version command so that session-timestamp or opcode changes are
// caught immediately.
func TestFirmwareVersionRequestBytes(t *testing.T) {
	want := []byte{
		0x14, 0x05, // opcode 0x0514 LE
		0x04, 0x00, // body_len = 4
		0x46, 0x9c, 0x6f, 0x64, // session timestamp
	}
	got := protocol.FirmwareVersionRequest()
	if !bytes.Equal(got, want) {
		t.Errorf("FirmwareVersionRequest:\n got  %x\n want %x", got, want)
	}
}

// TestParseFirmwareVersionFromHardwareCapture parses the payload bytes decoded
// from a live exchange with a UV-K5 running F4HWN v4.3 (captured 2026-05-16).
func TestParseFirmwareVersionFromHardwareCapture(t *testing.T) {
	// Payload is what transport.ReadFrame returns from the hardware capture:
	// opcode(2) + body_len(2) + body(36).
	payload := []byte{
		0x15, 0x05, // opcode 0x0515
		0x24, 0x00, // body_len = 36
		0x46, 0x34, 0x48, 0x57, 0x4e, 0x20, 0x76, 0x34, 0x2e, 0x33, 0x00, // "F4HWN v4.3\0"
		0x1f, 0x16, 0x6d, 0x15, 0x04, 0x00, 0x00, 0x1b, 0x44,
		0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
		0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
	}

	ver, err := protocol.ParseFirmwareVersion(payload)
	if err != nil {
		t.Fatalf("ParseFirmwareVersion: %v", err)
	}
	if ver != "F4HWN v4.3" {
		t.Errorf("ParseFirmwareVersion = %q, want %q", ver, "F4HWN v4.3")
	}
}

// TestParseBody verifies that ParseBody extracts the body slice correctly and
// does not include the opcode or body_len prefix.
func TestParseBody(t *testing.T) {
	body := []byte{0xAA, 0xBB, 0xCC, 0xDD}
	payload := protocol.BuildCommand(0x051B, body)

	got, err := protocol.ParseBody(payload)
	if err != nil {
		t.Fatalf("ParseBody: %v", err)
	}
	if !bytes.Equal(got, body) {
		t.Errorf("ParseBody = %x, want %x", got, body)
	}
}

// TestConfigMemRequestBytes verifies address and length encoding in the EEPROM
// read command payload.
func TestConfigMemRequestBytes(t *testing.T) {
	payload := protocol.ConfigMemRequest(0x0000, 0x0080)

	// Opcode 0x051B LE.
	if payload[0] != 0x1b || payload[1] != 0x05 {
		t.Errorf("opcode = %02x %02x, want 1b 05", payload[0], payload[1])
	}
	// body_len = 8 (address 2B + length 2B + timestamp 4B).
	if payload[2] != 0x08 || payload[3] != 0x00 {
		t.Errorf("body_len = %02x %02x, want 08 00", payload[2], payload[3])
	}
	// Address 0x0000 LE.
	if payload[4] != 0x00 || payload[5] != 0x00 {
		t.Errorf("address = %02x %02x, want 00 00", payload[4], payload[5])
	}
	// Length 0x0080 LE.
	if payload[6] != 0x80 || payload[7] != 0x00 {
		t.Errorf("length = %02x %02x, want 80 00", payload[6], payload[7])
	}
}
