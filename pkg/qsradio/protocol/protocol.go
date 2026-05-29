// SPDX-License-Identifier: Apache-2.0

// Package protocol defines opcodes, request/response types, and payload codecs
// for the UV-K5 serial protocol as used by F4HWN firmware.
// No I/O is performed here. All functions are pure encode/decode.
//
// Reference implementations:
//
//	amnemonic/Quansheng_UV-K5_Firmware python-utils/libuvk5.py
//	sq5bpf/k5prog
//	armel/uv-k5-chirp-driver
package protocol

import (
	"encoding/binary"
	"fmt"
)

// Opcodes. Values are the command opcode sent in the request; the radio responds
// with opcode+1 in all cases except CMD_Reboot which has no reply.
const (
	CmdGetFirmwareVersion uint16 = 0x0514 // reply 0x0515
	CmdReadFirmwareMem    uint16 = 0x0517 // reply 0x0518 (application firmware only)
	CmdReadConfigMem      uint16 = 0x051B // reply 0x051C
	CmdWriteConfigMem     uint16 = 0x051D // reply 0x051E
	CmdGetRSSI            uint16 = 0x0527 // reply 0x0528; returns rssi+noise+glitch
	CmdGetADC             uint16 = 0x0529 // reply 0x052A; returns battery ADC values
	CmdReboot             uint16 = 0x05DD // no reply
)

// SessionTimestamp is the fixed 4-byte session token used in every command body.
// All known implementations use this constant value; the radio does not validate it.
var SessionTimestamp = [4]byte{0x46, 0x9C, 0x6F, 0x64}

// BuildCommand constructs the framer payload for a command.
// The returned bytes are opcode(2B LE) + bodyLen(2B LE) + body,
// ready to pass directly to transport.Framer.WriteFrame.
func BuildCommand(opcode uint16, body []byte) []byte {
	payload := make([]byte, 4+len(body))
	binary.LittleEndian.PutUint16(payload[0:2], opcode)
	binary.LittleEndian.PutUint16(payload[2:4], uint16(len(body)))
	copy(payload[4:], body)
	return payload
}

// ParseResponseOpcode returns the opcode embedded in a raw framer payload.
func ParseResponseOpcode(payload []byte) (uint16, error) {
	if len(payload) < 4 {
		return 0, fmt.Errorf("protocol: payload too short (%d bytes)", len(payload))
	}
	return binary.LittleEndian.Uint16(payload[0:2]), nil
}

// ParseBody returns the body bytes from a raw framer payload, stripping the
// opcode and body-length prefix.
func ParseBody(payload []byte) ([]byte, error) {
	if len(payload) < 4 {
		return nil, fmt.Errorf("protocol: payload too short (%d bytes)", len(payload))
	}
	bodyLen := int(binary.LittleEndian.Uint16(payload[2:4]))
	if len(payload) < 4+bodyLen {
		return nil, fmt.Errorf("protocol: body truncated: need %d have %d", bodyLen, len(payload)-4)
	}
	return payload[4 : 4+bodyLen], nil
}

// FirmwareVersionRequest returns the payload for a CmdGetFirmwareVersion command.
func FirmwareVersionRequest() []byte {
	return BuildCommand(CmdGetFirmwareVersion, SessionTimestamp[:])
}

// ParseFirmwareVersion extracts the null-terminated version string from a
// CmdGetFirmwareVersion response payload.
func ParseFirmwareVersion(payload []byte) (string, error) {
	body, err := ParseBody(payload)
	if err != nil {
		return "", err
	}
	// Version is a null-terminated ASCII string at the start of the body.
	for i, b := range body {
		if b == 0 {
			return string(body[:i]), nil
		}
	}
	return string(body), nil
}

// RSSIRequest returns the payload for a CmdGetRSSI command.
func RSSIRequest() []byte {
	return BuildCommand(CmdGetRSSI, SessionTimestamp[:])
}

// RSSIResult holds the decoded signal-strength response.
type RSSIResult struct {
	// Raw is the 9-bit BK4819 REG_67 value (0-511).
	// dBm = Raw/2 - 160.
	Raw    uint16
	Noise  uint8
	Glitch uint8
}

// ParseRSSI decodes a CmdGetRSSI response payload.
func ParseRSSI(payload []byte) (RSSIResult, error) {
	body, err := ParseBody(payload)
	if err != nil {
		return RSSIResult{}, err
	}
	if len(body) < 4 {
		return RSSIResult{}, fmt.Errorf("protocol: RSSI body too short (%d bytes)", len(body))
	}
	return RSSIResult{
		Raw:    binary.LittleEndian.Uint16(body[0:2]) & 0x01FF,
		Noise:  body[2],
		Glitch: body[3],
	}, nil
}

// RSSIToDBm converts a raw BK4819 RSSI value to approximate dBm.
func RSSIToDBm(raw uint16) float32 {
	return float32(raw)/2 - 160
}

// ConfigMemRequest returns the payload for a CmdReadConfigMem command.
func ConfigMemRequest(address, length uint16) []byte {
	body := make([]byte, 8)
	binary.LittleEndian.PutUint16(body[0:2], address)
	binary.LittleEndian.PutUint16(body[2:4], length)
	copy(body[4:8], SessionTimestamp[:])
	return BuildCommand(CmdReadConfigMem, body)
}

// ParseConfigMem extracts the EEPROM data bytes from a CmdReadConfigMem response.
func ParseConfigMem(payload []byte) ([]byte, error) {
	body, err := ParseBody(payload)
	if err != nil {
		return nil, err
	}
	// Body layout: address(2B) + length(2B) + data.
	if len(body) < 4 {
		return nil, fmt.Errorf("protocol: config mem body too short (%d bytes)", len(body))
	}
	return body[4:], nil
}

// BK4819 direct register access opcodes.
// Available only when the firmware is built with ENABLE_UART_RW_BK_REGS.
const (
	CmdReadBK4819Reg  uint16 = 0x0601 // reply 0x0601; body: reg(1B)
	CmdWriteBK4819Reg uint16 = 0x0602 // no reply; body: reg(1B) + value(2B LE)
)

// BK4819 frequency registers. Two writes to these set the PLL frequency
// directly without touching EEPROM or rebooting.
const (
	BK4819RegFreqLow  = 0x38 // low 16 bits of frequency in 10 Hz units
	BK4819RegFreqHigh = 0x39 // high 16 bits of frequency in 10 Hz units
	BK4819RegRSSI     = 0x67 // RSSI, bits 8:0 (9-bit value)
	BK4819RegNoise    = 0x65 // noise indicator, bits 6:0
	BK4819RegGlitch   = 0x63 // glitch indicator, low byte
	BK4819RegAFGain   = 0x48 // AF output gain (see BuildAFGainReg)
	BK4819RegAGC0     = 0x13 // AGC table entry 0; LNA gain fields in bits [12:8]

	// Flat audio control registers (from Mobilinkd UV-K6 digital mod research).
	BK4819RegAudioFilter = 0x2B // audio filter enable/disable (see FlatAudio* consts)
	BK4819RegDCFilter    = 0x7E // DC filter bandwidth for TX/RX paths
	BK4819RegALC         = 0x4B // ALC (limiter) control
	BK4819RegMICAGC      = 0x19 // MIC automatic gain control

	// Compander control registers.
	// The firmware enables a 1:2 RX expander by default (REG_28 bits [15:14] = 01).
	// This causes "pumping" distortion on FSK signals and must be disabled for
	// packet/digital modes. REG_31 bit 3 is the compander global enable.
	BK4819RegCompander = 0x28 // bits [15:14] RX ratio (00=off), bits [13:7] threshold, etc.
	BK4819RegControl   = 0x31 // global enables: bit3=compander, bit1=scrambler, bit4=VOX
	BK4819RegMic       = 0x7D // bits[3:0] = MIC sensitivity (0=min, 15=max); default 0xE940 (=0)
)

// Bit masks for BK4819RegAudioFilter (REG_0x2B).
// Each bit disables a specific audio filter stage when set to 1.
// Default value is 0x0000 (all filters enabled, correct for FM voice).
// Source: BK4819V3Registers_List_20201218.pdf via Mobilinkd UV-K6 digital mod.
const (
	// FlatAudioRXBits disables the three RX audio filter stages:
	//   bit 10: AFRxHPF300: 300 Hz high-pass (sub-audio / CTCSS path)
	//   bit  9: AFRxLPF3K: 3 kHz low-pass (was attenuating 2200 Hz AFSK space tone)
	//   bit  8: RX de-emphasis
	FlatAudioRXBits uint16 = 0x0700

	// FlatAudioTXBits disables the three TX audio filter stages:
	//   bit  2: AFTxHPF300: 300 Hz high-pass
	//   bit  1: AFTxLPF1: ~3 kHz low-pass voice LPF
	//   bit  0: TX pre-emphasis (was boosting high-frequency AFSK tones unevenly)
	FlatAudioTXBits uint16 = 0x0007

	// FlatAudioAllBits disables all six voice-oriented filter stages.
	FlatAudioAllBits uint16 = FlatAudioRXBits | FlatAudioTXBits // 0x0707

	// FlatAudioClearMask clears only the six filter bits, preserving all others.
	FlatAudioClearMask uint16 = 0xF8F8
)

// BuildAFGainReg constructs the REG_48 value for the BK4819 AF output stage.
// afRxGain2 is the AF Rx Gain-2 field (0-63): range -26 dB to +5.5 dB in
// 0.5 dB steps. The other fields are held at firmware-default values.
//
// REG_48 layout:
//
//	bits [15:12] = 11  (fixed)
//	bits [11:10] =  0  AF Rx Gain-1: 0 = 0 dB
//	bits  [9:4]  = afRxGain2  (0-63, 0.5 dB/step, 58 = firmware default ≈ 0 dB ref)
//	bits  [3:0]  =  8  AF DAC Gain: ~2 dB/step, 8 = firmware default
func BuildAFGainReg(afRxGain2 uint8) uint16 {
	if afRxGain2 > 63 {
		afRxGain2 = 63
	}
	return uint16(11<<12) | uint16(0<<10) | uint16(afRxGain2<<4) | uint16(8)
}

// ReadBK4819RegRequest returns the payload for a CmdReadBK4819Reg command.
func ReadBK4819RegRequest(reg uint8) []byte {
	return BuildCommand(CmdReadBK4819Reg, []byte{reg})
}

// ParseReadBK4819RegResponse extracts the register number and value from a
// CmdReadBK4819Reg reply payload.
func ParseReadBK4819RegResponse(payload []byte) (reg uint8, value uint16, err error) {
	body, err := ParseBody(payload)
	if err != nil {
		return 0, 0, err
	}
	if len(body) < 3 {
		return 0, 0, fmt.Errorf("protocol: BK4819 reg reply body too short (%d bytes)", len(body))
	}
	return body[0], binary.LittleEndian.Uint16(body[1:3]), nil
}

// WriteBK4819RegRequest returns the payload for a CmdWriteBK4819Reg command.
// The firmware sends NO reply. The caller must not call ReadFrame after writing.
func WriteBK4819RegRequest(reg uint8, value uint16) []byte {
	body := []byte{reg, uint8(value), uint8(value >> 8)}
	return BuildCommand(CmdWriteBK4819Reg, body)
}

// CmdSetVFO sets frequency, modulation, bandwidth, and squelch atomically
// through the firmware state machine. Available when ENABLE_UART_RW_BK_REGS
// is compiled in (same guard as CMD_0601/CMD_0602). No reply.
//
// This is the preferred tuning path: the firmware updates its internal VFO
// state, applies per-band squelch calibration, configures the BK4819, and
// refreshes the display, all in one command.
const CmdSetVFO uint16 = 0x0603

// Modulation constants for SetVFORequest, matching the firmware's ModulationMode_t.
const (
	ModulationFM  uint8 = 0 // FM (use with BandwidthWide or BandwidthNarrow)
	ModulationAM  uint8 = 1
	ModulationUSB uint8 = 2
)

// BK4819 bandwidth constants for SetVFORequest, matching BK4819_FilterBandwidth_t.
const (
	FWBandwidthWide   uint8 = 0 // 25 kHz
	FWBandwidthNarrow uint8 = 1 // 12.5 kHz
)

// BK4819 AF output type overrides for SetVFORequest.
// 0 = default for the selected modulation (FM uses AF_FM=1 with de-emphasis).
const (
	// AFDefault leaves the BK4819 AF type at the firmware's default for the
	// selected modulation. FM -> AF_FM (de-emphasis on). AM -> AF_AM.
	AFDefault uint8 = 0

	// AFBYP selects BYP mode (BK4819_AF_UNKNOWN3=9): FM without de-emphasis.
	// Use for packet/digital modes (AX.25, APRS, etc.) where flat audio
	// response is required. De-emphasis attenuates the 2200 Hz AFSK space
	// tone by ~3 dB, reducing decode reliability.
	AFBYP uint8 = 9

	// AFRaw selects raw baseband output (BK4819_AF_BASEBAND1=4).
	// Experimental; bypasses all audio post-processing.
	AFRaw uint8 = 4
)

// TX power level constants for SetVFORequest.
// Wire values 1-7 map directly to F4HWN OUTPUT_POWER_LOW1 through OUTPUT_POWER_HIGH.
// 0 = TXPowerKeep: leave the radio's current power level unchanged.
const (
	TXPowerKeep uint8 = 0 // do not change current power
	TXPowerLow1 uint8 = 1 // OUTPUT_POWER_LOW1
	TXPowerLow2 uint8 = 2 // OUTPUT_POWER_LOW2
	TXPowerLow3 uint8 = 3 // OUTPUT_POWER_LOW3
	TXPowerLow4 uint8 = 4 // OUTPUT_POWER_LOW4
	TXPowerLow5 uint8 = 5 // OUTPUT_POWER_LOW5
	TXPowerMid  uint8 = 6 // OUTPUT_POWER_MID
	TXPowerHigh uint8 = 7 // OUTPUT_POWER_HIGH
)

// SetVFORequest returns the payload for a CmdSetVFO command.
// modulation: ModulationFM, ModulationAM, ModulationUSB.
// bandwidth: FWBandwidthWide, FWBandwidthNarrow.
// squelch: 0 = fully open, 1-9 = calibrated threshold level.
// afOverride: AFDefault (0) or a specific BK4819 AF type (e.g. AFBYP for packet).
// powerLevel: TXPowerKeep (0) to leave unchanged, or TXPowerLow1 through TXPowerHigh (1-7).
// The firmware sends no reply.
func SetVFORequest(freqHz uint64, modulation, bandwidth, squelch, afOverride, powerLevel uint8) []byte {
	freq10 := uint32(freqHz / 10)
	body := make([]byte, 9)
	binary.LittleEndian.PutUint32(body[0:4], freq10)
	body[4] = modulation
	body[5] = bandwidth
	body[6] = squelch
	body[7] = afOverride
	body[8] = powerLevel
	return BuildCommand(CmdSetVFO, body)
}

// WriteConfigMemRequest returns the payload for a CmdWriteConfigMem command.
// data must be a non-zero multiple of 8 bytes (hardware constraint).
func WriteConfigMemRequest(address uint16, data []byte) ([]byte, error) {
	if len(data) == 0 || len(data)%8 != 0 {
		return nil, fmt.Errorf("protocol: WriteConfigMem data must be a non-zero multiple of 8 bytes, got %d", len(data))
	}
	body := make([]byte, 8+len(data))
	binary.LittleEndian.PutUint16(body[0:2], address)
	binary.LittleEndian.PutUint16(body[2:4], uint16(len(data)))
	copy(body[4:8], SessionTimestamp[:])
	copy(body[8:], data)
	return BuildCommand(CmdWriteConfigMem, body), nil
}
