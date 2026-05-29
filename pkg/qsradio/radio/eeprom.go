// SPDX-License-Identifier: Apache-2.0

package radio

import (
	"encoding/binary"
	"fmt"
)

// calibration holds device-specific values read from EEPROM once at connect time.
// Every UV-K5 is individually calibrated; these values vary between units and
// must never be hardcoded.
type calibration struct {
	loaded bool

	// sqlUHF[row][0:16]: squelch thresholds for UHF bands (>= 174 MHz).
	// EEPROM base 0x1E00, 6 rows x 16 bytes.
	// Row 0 = RSSI open, 1 = RSSI close, 2 = noise open,
	// 3 = noise close, 4 = glitch close, 5 = glitch open.
	// Index within each row is the squelch level (1-9); index 0 is unused.
	sqlUHF [6][16]byte

	// sqlVHF[row][0:16]: squelch thresholds for VHF bands (< 174 MHz).
	// EEPROM base 0x1E60, same layout as sqlUHF.
	sqlVHF [6][16]byte

	// txp[band][0:16]: TX power calibration per frequency band.
	// EEPROM base 0x1ED0, 7 bands x 16 bytes.
	// Within each band row: Low = bytes [0:3], Mid = [3:6], High = [6:9].
	// Band 0 = 50 MHz, 1 = 108, 2 = 137, 3 = 174, 4 = 350, 5 = 400, 6 = 470.
	txp [7][16]byte

	// shadowTXPower is the last power level set via SetTXPower.
	// txPowerExplicit is false until SetTXPower is called; while false
	// CMD_0603 sends TXPowerKeep so the radio's EEPROM setting is preserved.
	shadowTXPower   TXPower
	txPowerExplicit bool
}

// sqlValues returns the 6 BK4819 squelch threshold parameters for the given
// frequency and squelch level. Level 0 returns maximally permissive values
// (squelch fully open). If calibration was not loaded, falls back to open.
func (c *calibration) sqlValues(hz uint64, level int) (openRSSI, closeRSSI, openNoise, closeNoise, closeGlitch, openGlitch uint8) {
	if !c.loaded || level <= 0 {
		return 0, 0, 127, 127, 255, 255
	}
	if level > 9 {
		level = 9
	}
	tbl := &c.sqlUHF
	if hz < 174_000_000 {
		tbl = &c.sqlVHF
	}
	return tbl[0][level], tbl[1][level], tbl[2][level], tbl[3][level], tbl[4][level], tbl[5][level]
}

// txpBytes returns the 3 PA calibration bytes for the given frequency and power
// level (0=Low, 1=Mid, 2=High). Returns a safe low-power default if calibration
// was not loaded.
func (c *calibration) txpBytes(hz uint64, level TXPower) [3]byte {
	if !c.loaded {
		return [3]byte{0x32, 0x32, 0x32} // conservative low-power default
	}
	band := freqBand(hz)
	op := int(level)
	if op > 2 {
		op = 2
	}
	var b [3]byte
	copy(b[:], c.txp[band][op*3:(op+1)*3])
	return b
}

// freqBand maps a frequency in Hz to a UV-K5 band index (0-6).
// Corresponds to the TX power calibration rows in EEPROM at 0x1ED0.
func freqBand(hz uint64) int {
	switch {
	case hz < 50_000_000:
		return 0
	case hz < 108_000_000:
		return 1
	case hz < 137_000_000:
		return 2
	case hz < 174_000_000:
		return 3
	case hz < 350_000_000:
		return 4
	case hz < 400_000_000:
		return 5
	default:
		return 6
	}
}

// EEPROM address map (from F4HWN CHIRP driver uvk5_egzumer_f4hwn_ver_4_3_0.py).
const (
	// channel[214] starts at 0x0000. Slots 0-199 are regular memories;
	// slots 200-213 are the 14 VFO band-pair slots (7 bands x A+B).
	channelBase    = 0x0000
	channelRecSize = 16 // bytes per channel or VFO slot record
	memChannels    = 200
	vfoSlotCount   = 14

	// VFO screen channel registers (1 byte each at 0xE80).
	regScreenChannelA = 0xE80 // which slot is displayed on VFO A
	regMrChannelA     = 0xE81
	regFreqChannelA   = 0xE82 // last-used VFO band slot for A (200-206)
	regScreenChannelB = 0xE83
	regMrChannelB     = 0xE84
	regFreqChannelB   = 0xE85

	// VFO slot number range.
	vfoSlotMin = 200
	vfoSlotMax = 213

	// Calibration EEPROM regions (from radio.c RADIO_ConfigureSquelchAndOutputPower).
	calSQLUHFBase = 0x1E00 // squelch thresholds for UHF (>= 174 MHz), 6 rows x 16 bytes
	calSQLVHFBase = 0x1E60 // squelch thresholds for VHF (< 174 MHz), same layout
	calSQLRows    = 6
	calSQLRowLen  = 16
	calTXPBase    = 0x1ED0 // TX power calibration, 7 bands x 16 bytes
	calTXPBands   = 7
	calTXPBandLen = 16
)

// channelRecordOffset returns the EEPROM byte offset for slot n (0-indexed).
func channelRecordOffset(n int) uint16 {
	return uint16(channelBase + n*channelRecSize)
}

// channelRecord is the decoded form of a 16-byte channel or VFO slot record.
// Bit-field layout from the F4HWN CHIRP driver MEM_FORMAT struct.
type channelRecord struct {
	// Offset 0x00: frequency in 10 Hz units (uint32 LE).
	Freq uint32
	// Offset 0x04: TX offset in 10 Hz units (uint32 LE).
	Offset uint32
	// Offsets 0x08-0x0F: tone/code/mode/power/misc fields, preserved verbatim
	// during frequency-only changes to avoid disturbing other settings.
	Tail [8]byte
}

func decodeChannelRecord(b []byte) channelRecord {
	r := channelRecord{}
	r.Freq = binary.LittleEndian.Uint32(b[0:4])
	r.Offset = binary.LittleEndian.Uint32(b[4:8])
	copy(r.Tail[:], b[8:16])
	return r
}

func encodeChannelRecord(r channelRecord) []byte {
	b := make([]byte, 16)
	binary.LittleEndian.PutUint32(b[0:4], r.Freq)
	binary.LittleEndian.PutUint32(b[4:8], r.Offset)
	copy(b[8:16], r.Tail[:])
	return b
}

// modeFromRecord decodes the modulation and bandwidth bits from a channel record.
// Byte 0x0B (Tail[3]): bits 7-4 = modulation (0=FM, 1=AM), bits 3-0 = offsetDir.
// Byte 0x0C (Tail[4]): bit 1 = bandwidth (0=wide, 1=narrow).
// Encoding follows CHIRP: modulation = mode_index/2, bandwidth = mode_index%2,
// where mode_index is the index into ["FM","NFM","AM"].
func modeFromRecord(r channelRecord) (Mode, Bandwidth) {
	modulation := (r.Tail[3] >> 4) & 0xF
	bwBit := (r.Tail[4] >> 1) & 0x1
	switch {
	case modulation == 0 && bwBit == 0:
		return ModeFM, BandwidthWide
	case modulation == 0 && bwBit == 1:
		return ModeNFM, BandwidthNarrow
	default:
		return ModeAM, BandwidthNarrow
	}
}

// setModeInRecord updates the modulation and bandwidth bits in a channel record.
func setModeInRecord(r *channelRecord, mode Mode, bw Bandwidth) {
	var modulation, bwBit uint8
	switch mode {
	case ModeFM:
		modulation, bwBit = 0, 0
	case ModeNFM:
		modulation, bwBit = 0, 1
	default: // AM
		modulation, bwBit = 1, 0
	}
	r.Tail[3] = (r.Tail[3] & 0x0F) | (modulation << 4)
	r.Tail[4] = (r.Tail[4] & 0xFD) | (bwBit << 1)
}

// readVFORegisters reads the 8-byte VFO screen-channel register block at 0xE80.
// Caller must hold r.mu.
func (r *uvk5) readVFORegisters() (screenA, freqSlotA, screenB, freqSlotB uint8, err error) {
	data, err := r.readEEPROMLocked(regScreenChannelA, 8)
	if err != nil {
		return 0, 0, 0, 0, fmt.Errorf("read VFO registers: %w", err)
	}
	return data[0], data[2], data[3], data[5], nil
}

// readChannelSlot reads the 16-byte record for the given slot number (0-indexed).
// Caller must hold r.mu.
func (r *uvk5) readChannelSlot(slot int) (channelRecord, error) {
	data, err := r.readEEPROMLocked(channelRecordOffset(slot), channelRecSize)
	if err != nil {
		return channelRecord{}, fmt.Errorf("read channel slot %d: %w", slot, err)
	}
	return decodeChannelRecord(data), nil
}

// writeChannelSlot writes the 16-byte record for the given slot number.
// Caller must hold r.mu.
func (r *uvk5) writeChannelSlot(slot int, rec channelRecord) error {
	return r.writeEEPROMLocked(channelRecordOffset(slot), encodeChannelRecord(rec))
}
