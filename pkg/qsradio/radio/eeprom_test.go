// SPDX-License-Identifier: Apache-2.0

package radio

import "testing"

// TestFreqBand verifies the 7-band frequency mapping including each boundary.
func TestFreqBand(t *testing.T) {
	cases := []struct {
		hz   uint64
		want int
	}{
		{0, 0},
		{49_999_999, 0},
		{50_000_000, 1}, // exactly at boundary
		{107_999_999, 1},
		{108_000_000, 2},
		{136_999_999, 2},
		{137_000_000, 3},
		{145_500_000, 3}, // typical 2m packet frequency
		{173_999_999, 3},
		{174_000_000, 4},
		{349_999_999, 4},
		{350_000_000, 5},
		{399_999_999, 5},
		{400_000_000, 6},
		{435_000_000, 6}, // typical 70cm
		{1_300_000_000, 6},
	}
	for _, c := range cases {
		if got := freqBand(c.hz); got != c.want {
			t.Errorf("freqBand(%d) = %d, want %d", c.hz, got, c.want)
		}
	}
}

// TestModeRoundTrip verifies that setModeInRecord followed by modeFromRecord
// returns the same mode and bandwidth for FM, NFM, and AM.
func TestModeRoundTrip(t *testing.T) {
	cases := []struct {
		mode Mode
		bw   Bandwidth
	}{
		{ModeFM, BandwidthWide},
		{ModeNFM, BandwidthNarrow},
		{ModeAM, BandwidthNarrow}, // AM always decodes as BandwidthNarrow
	}
	for _, c := range cases {
		r := channelRecord{}
		setModeInRecord(&r, c.mode, c.bw)
		gotMode, gotBW := modeFromRecord(r)
		if gotMode != c.mode {
			t.Errorf("mode round-trip %s: got mode %s", c.mode, gotMode)
		}
		if gotBW != c.bw {
			t.Errorf("mode round-trip %s: got bw %d, want %d", c.mode, gotBW, c.bw)
		}
	}
}

// TestSetModePreservesOtherBits verifies that setModeInRecord does not disturb
// bits in Tail[3] or Tail[4] that belong to other fields (offset direction,
// tone codes, etc.).
func TestSetModePreservesOtherBits(t *testing.T) {
	cases := []struct{ mode Mode }{
		{ModeFM}, {ModeNFM}, {ModeAM},
	}
	for _, c := range cases {
		r := channelRecord{}
		// Fill tail with sentinel values so any corruption is visible.
		for i := range r.Tail {
			r.Tail[i] = 0xFF
		}
		setModeInRecord(&r, c.mode, BandwidthWide)

		// Tail[3] lower nibble (offset direction bits) must survive.
		if r.Tail[3]&0x0F != 0x0F {
			t.Errorf("mode %s: Tail[3] lower nibble corrupted: 0x%02x", c.mode, r.Tail[3])
		}
		// Tail[4] all bits except bit 1 (bwBit) must survive.
		if r.Tail[4]&0xFD != 0xFD {
			t.Errorf("mode %s: Tail[4] non-bandwidth bits corrupted: 0x%02x", c.mode, r.Tail[4])
		}
	}
}

// TestChannelRecordEncodeDecode verifies the frequency and offset round-trip
// and that Tail bytes are preserved verbatim.
func TestChannelRecordEncodeDecode(t *testing.T) {
	// 145.9375 MHz = 14,593,750 in 10 Hz units.
	want := channelRecord{
		Freq:   14_593_750,
		Offset: 600_000,
		Tail:   [8]byte{0x01, 0x23, 0x45, 0x67, 0x89, 0xAB, 0xCD, 0xEF},
	}
	got := decodeChannelRecord(encodeChannelRecord(want))
	if got.Freq != want.Freq {
		t.Errorf("Freq: got %d, want %d", got.Freq, want.Freq)
	}
	if got.Offset != want.Offset {
		t.Errorf("Offset: got %d, want %d", got.Offset, want.Offset)
	}
	if got.Tail != want.Tail {
		t.Errorf("Tail corrupted: got %x, want %x", got.Tail, want.Tail)
	}
}

// TestChannelRecordOffset verifies the slot-to-EEPROM-offset mapping.
func TestChannelRecordOffset(t *testing.T) {
	cases := []struct {
		slot int
		want uint16
	}{
		{0, 0x0000},
		{1, 0x0010},
		{199, 0x0C70}, // last memory channel
		{200, 0x0C80}, // first VFO slot
		{213, 0x0D50}, // last VFO slot (213 * 16 = 3408 = 0xD50)
	}
	for _, c := range cases {
		if got := channelRecordOffset(c.slot); got != c.want {
			t.Errorf("channelRecordOffset(%d) = 0x%04x, want 0x%04x", c.slot, got, c.want)
		}
	}
}

// TestSQLValues_Unloaded verifies maximally permissive squelch thresholds
// are returned when calibration has not been loaded.
func TestSQLValues_Unloaded(t *testing.T) {
	var c calibration // loaded = false
	open, close_, openNoise, closeNoise, closeGlitch, openGlitch :=
		c.sqlValues(145_000_000, 5)
	if open != 0 || close_ != 0 {
		t.Errorf("unloaded RSSI should be 0: open=%d close=%d", open, close_)
	}
	if openNoise != 127 || closeNoise != 127 {
		t.Errorf("unloaded noise should be 127: open=%d close=%d", openNoise, closeNoise)
	}
	if closeGlitch != 255 || openGlitch != 255 {
		t.Errorf("unloaded glitch should be 255: close=%d open=%d", closeGlitch, openGlitch)
	}
}

// TestSQLValues_LevelZero verifies level 0 always returns maximally permissive
// thresholds regardless of what calibration data is loaded.
func TestSQLValues_LevelZero(t *testing.T) {
	c := calibration{loaded: true}
	for i := range c.sqlVHF {
		for j := range c.sqlVHF[i] {
			c.sqlVHF[i][j] = 0xAA
			c.sqlUHF[i][j] = 0xAA
		}
	}
	open, close_, openNoise, closeNoise, closeGlitch, openGlitch :=
		c.sqlValues(145_000_000, 0)
	if open != 0 || close_ != 0 || openNoise != 127 || closeNoise != 127 ||
		closeGlitch != 255 || openGlitch != 255 {
		t.Error("level 0 should return permissive thresholds regardless of calibration data")
	}
}

// TestSQLValues_BandSelection verifies VHF frequencies use sqlVHF and
// UHF frequencies use sqlUHF.
func TestSQLValues_BandSelection(t *testing.T) {
	c := calibration{loaded: true}
	c.sqlVHF[0][5] = 42 // openRSSI at squelch level 5, VHF
	c.sqlUHF[0][5] = 99 // openRSSI at squelch level 5, UHF

	openRSSI, _, _, _, _, _ := c.sqlValues(145_000_000, 5) // VHF
	if openRSSI != 42 {
		t.Errorf("VHF: expected sqlVHF value 42, got %d", openRSSI)
	}

	openRSSI, _, _, _, _, _ = c.sqlValues(435_000_000, 5) // UHF
	if openRSSI != 99 {
		t.Errorf("UHF: expected sqlUHF value 99, got %d", openRSSI)
	}
}

// TestTXPBytes_Unloaded verifies the safe conservative default is returned
// when calibration has not been loaded from the radio.
func TestTXPBytes_Unloaded(t *testing.T) {
	var c calibration
	got := c.txpBytes(145_000_000, TXPowerHigh)
	want := [3]byte{0x32, 0x32, 0x32}
	if got != want {
		t.Errorf("unloaded txpBytes: got %v, want %v", got, want)
	}
}
