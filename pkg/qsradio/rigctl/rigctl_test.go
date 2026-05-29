// SPDX-License-Identifier: Apache-2.0

package rigctl

import (
	"strings"
	"testing"

	"github.com/muurk/qsradio/pkg/qsradio/radio"
)

// fakeRadio is a minimal test double for radio.Radio.
// Setters record the last value they were called with so tests can assert
// on what the dispatch layer passed down.
type fakeRadio struct {
	freq    uint64
	mode    radio.Mode
	bw      radio.Bandwidth
	ptt     bool
	squelch int
	smeter  int
	txpow   radio.TXPower
	afGain  uint8
	rfGain  uint8
	micGain uint8

	lastFreq    uint64
	lastMode    radio.Mode
	lastBW      radio.Bandwidth
	lastPTT     bool
	lastSquelch int
	lastTXPow   radio.TXPower
	lastAFGain  uint8
	lastRFGain  uint8
	lastMicGain uint8
}

func (f *fakeRadio) GetFrequency() (uint64, error) { return f.freq, nil }
func (f *fakeRadio) SetFrequency(hz uint64) error  { f.lastFreq = hz; f.freq = hz; return nil }
func (f *fakeRadio) GetPTT() (bool, error)         { return f.ptt, nil }
func (f *fakeRadio) SetPTT(v bool) error           { f.lastPTT = v; f.ptt = v; return nil }
func (f *fakeRadio) GetMode() (radio.Mode, radio.Bandwidth, error) {
	return f.mode, f.bw, nil
}
func (f *fakeRadio) SetMode(m radio.Mode, bw radio.Bandwidth) error {
	f.lastMode = m
	f.lastBW = bw
	f.mode = m
	f.bw = bw
	return nil
}
func (f *fakeRadio) GetSMeter() (int, error)                     { return f.smeter, nil }
func (f *fakeRadio) GetSquelch() (int, error)                    { return f.squelch, nil }
func (f *fakeRadio) SetSquelch(level int) error                  { f.lastSquelch = level; return nil }
func (f *fakeRadio) GetTXPower() (radio.TXPower, error)          { return f.txpow, nil }
func (f *fakeRadio) SetTXPower(p radio.TXPower) error            { f.lastTXPow = p; return nil }
func (f *fakeRadio) GetAudioGain() (uint8, error)                { return f.afGain, nil }
func (f *fakeRadio) SetAudioGain(g uint8) error                  { f.lastAFGain = g; return nil }
func (f *fakeRadio) GetRFGain() (uint8, error)                   { return f.rfGain, nil }
func (f *fakeRadio) SetRFGain(g uint8) error                     { f.lastRFGain = g; return nil }
func (f *fakeRadio) GetMicGain() (uint8, error)                  { return f.micGain, nil }
func (f *fakeRadio) SetMicGain(g uint8) error                    { f.lastMicGain = g; return nil }
func (f *fakeRadio) GetInfo() string                             { return "fake radio" }
func (f *fakeRadio) GetFirmwareVersion() (string, error)         { return "fake v1.0", nil }
func (f *fakeRadio) ReadEEPROM(_, length uint16) ([]byte, error) { return make([]byte, length), nil }
func (f *fakeRadio) WriteEEPROM(_ uint16, _ []byte) error        { return nil }
func (f *fakeRadio) Close() error                                { return nil }

// dispatch helper: runs a single command line against a fake radio and
// returns the response text and whether the connection should stay open.
func dispatchLine(t *testing.T, r *fakeRadio, line string) (out string, keepOpen bool) {
	t.Helper()
	srv := &Server{Radio: r}
	var buf strings.Builder
	keepOpen = srv.dispatch(&buf, line)
	return buf.String(), keepOpen
}

// ---------------------------------------------------------------------------
// modeString
// ---------------------------------------------------------------------------

func TestModeString(t *testing.T) {
	cases := []struct {
		mode radio.Mode
		want string
	}{
		{radio.ModeFM, "FM"},
		{radio.ModeNFM, "FMN"}, // hamlib uses FMN, not NFM
		{radio.ModeAM, "AM"},
		{radio.ModePKT, "PKTFM"},
		{radio.ModeUSB, "USB"},
		{radio.ModeLSB, "LSB"},
		{"UNKNOWN", "FM"}, // unknown modes fall back to FM
	}
	for _, c := range cases {
		if got := modeString(c.mode); got != c.want {
			t.Errorf("modeString(%q) = %q, want %q", c.mode, got, c.want)
		}
	}
}

// ---------------------------------------------------------------------------
// parseMode
// ---------------------------------------------------------------------------

func TestParseMode(t *testing.T) {
	cases := []struct {
		modeStr string
		bwStr   string
		wantM   radio.Mode
		wantBW  radio.Bandwidth
	}{
		{"FM", "0", radio.ModeFM, radio.BandwidthWide},
		{"FMN", "0", radio.ModeNFM, radio.BandwidthNarrow},
		{"AM", "0", radio.ModeAM, radio.BandwidthWide},
		{"PKTFM", "0", radio.ModePKT, radio.BandwidthNarrow},
		{"FM-D", "0", radio.ModePKT, radio.BandwidthNarrow}, // hamlib 4.x NET wire name
		{"USB", "0", radio.ModeUSB, radio.BandwidthNarrow},
		{"LSB", "0", radio.ModeLSB, radio.BandwidthNarrow},
		{"pktfm", "0", radio.ModePKT, radio.BandwidthNarrow}, // case insensitive
		{"UNKNOWN", "0", radio.ModeFM, radio.BandwidthWide},  // unknown falls back to FM
		// Explicit bandwidth overrides the mode default.
		{"FM", "25000", radio.ModeFM, 25000},
		{"FMN", "12500", radio.ModeNFM, 12500},
		{"PKTFM", "25000", radio.ModePKT, 25000},
	}
	for _, c := range cases {
		gotM, gotBW := parseMode(c.modeStr, c.bwStr)
		if gotM != c.wantM {
			t.Errorf("parseMode(%q, %q) mode = %q, want %q", c.modeStr, c.bwStr, gotM, c.wantM)
		}
		if gotBW != c.wantBW {
			t.Errorf("parseMode(%q, %q) bw = %d, want %d", c.modeStr, c.bwStr, gotBW, c.wantBW)
		}
	}
}

// ---------------------------------------------------------------------------
// dispatch: frequency
// ---------------------------------------------------------------------------

func TestDispatch_GetFreq(t *testing.T) {
	r := &fakeRadio{freq: 144_937_500}
	out, open := dispatchLine(t, r, "f")
	if !open {
		t.Error("connection should stay open")
	}
	if !strings.Contains(out, "144937500") {
		t.Errorf("expected frequency in response, got %q", out)
	}
	if !strings.Contains(out, "RPRT 0") {
		t.Errorf("expected RPRT 0 in response, got %q", out)
	}
}

func TestDispatch_SetFreq(t *testing.T) {
	r := &fakeRadio{}
	out, _ := dispatchLine(t, r, "F 144937500")
	if r.lastFreq != 144_937_500 {
		t.Errorf("SetFrequency called with %d, want 144937500", r.lastFreq)
	}
	if out != rprtOK {
		t.Errorf("expected RPRT 0, got %q", out)
	}
}

func TestDispatch_SetFreq_ExtendedVFO(t *testing.T) {
	// Direwolf sends "F VFOA 144937500" rather than "F 144937500".
	r := &fakeRadio{}
	dispatchLine(t, r, "F VFOA 144937500")
	if r.lastFreq != 144_937_500 {
		t.Errorf("VFO-extended SetFrequency called with %d, want 144937500", r.lastFreq)
	}
}

// ---------------------------------------------------------------------------
// dispatch: PTT
// ---------------------------------------------------------------------------

func TestDispatch_GetPTT(t *testing.T) {
	r := &fakeRadio{ptt: true}
	out, _ := dispatchLine(t, r, "t")
	if !strings.Contains(out, "1") {
		t.Errorf("PTT active: expected 1 in response, got %q", out)
	}
}

func TestDispatch_SetPTT_On(t *testing.T) {
	r := &fakeRadio{}
	out, _ := dispatchLine(t, r, "T 1")
	if !r.lastPTT {
		t.Error("SetPTT should have been called with true")
	}
	if out != rprtOK {
		t.Errorf("expected RPRT 0, got %q", out)
	}
}

func TestDispatch_SetPTT_Off(t *testing.T) {
	r := &fakeRadio{ptt: true}
	dispatchLine(t, r, "T 0")
	if r.lastPTT {
		t.Error("SetPTT should have been called with false")
	}
}

func TestDispatch_SetPTT_ExtendedVFO(t *testing.T) {
	// Direwolf uses "T VFOA 1" not "T 1".
	r := &fakeRadio{}
	dispatchLine(t, r, "T VFOA 1")
	if !r.lastPTT {
		t.Error("VFO-extended PTT: SetPTT should have been called with true")
	}
}

// ---------------------------------------------------------------------------
// dispatch: mode
// ---------------------------------------------------------------------------

func TestDispatch_GetMode(t *testing.T) {
	r := &fakeRadio{mode: radio.ModePKT, bw: radio.BandwidthWide}
	out, _ := dispatchLine(t, r, "m")
	if !strings.Contains(out, "PKTFM") {
		t.Errorf("expected PKTFM in response, got %q", out)
	}
}

func TestDispatch_SetMode(t *testing.T) {
	r := &fakeRadio{}
	dispatchLine(t, r, "M PKTFM 25000")
	if r.lastMode != radio.ModePKT {
		t.Errorf("SetMode called with mode %q, want PKT", r.lastMode)
	}
	if r.lastBW != 25000 {
		t.Errorf("SetMode called with bw %d, want 25000", r.lastBW)
	}
}

// ---------------------------------------------------------------------------
// dispatch: S-meter and levels
// ---------------------------------------------------------------------------

func TestDispatch_GetLevel_RAWSTR(t *testing.T) {
	r := &fakeRadio{smeter: 142}
	out, _ := dispatchLine(t, r, "l RAWSTR")
	if !strings.Contains(out, "142") {
		t.Errorf("expected 142 in response, got %q", out)
	}
}

func TestDispatch_SetLevel_SQL(t *testing.T) {
	cases := []struct {
		input string
		want  int
	}{
		{"0.0", 0},
		{"1.0", 9},
		{"0.5", 4}, // int(0.5 * 9) = 4
		{"5", 5},   // value > 1.0 treated as direct integer
		{"10", 9},  // clamped to max
		{"-1", 0},  // clamped to min
	}
	for _, c := range cases {
		r := &fakeRadio{}
		dispatchLine(t, r, "L SQL "+c.input)
		if r.lastSquelch != c.want {
			t.Errorf("SQL %s: SetSquelch(%d), want %d", c.input, r.lastSquelch, c.want)
		}
	}
}

func TestDispatch_SetLevel_RFPOWER(t *testing.T) {
	cases := []struct {
		input string
		want  radio.TXPower
	}{
		{"0.0", radio.TXPowerLow1},
		{"1.0", radio.TXPowerHigh},
		{"0.5", radio.TXPower(3)}, // int(0.5*6 + 0.5) = 3
	}
	for _, c := range cases {
		r := &fakeRadio{}
		dispatchLine(t, r, "L RFPOWER "+c.input)
		if r.lastTXPow != c.want {
			t.Errorf("RFPOWER %s: SetTXPower(%d), want %d", c.input, r.lastTXPow, c.want)
		}
	}
}

// ---------------------------------------------------------------------------
// dispatch: connection lifecycle and edge cases
// ---------------------------------------------------------------------------

func TestDispatch_Quit(t *testing.T) {
	r := &fakeRadio{}
	out, open := dispatchLine(t, r, "q")
	if open {
		t.Error("q should close the connection (return false)")
	}
	if out != rprtOK {
		t.Errorf("expected RPRT 0 on quit, got %q", out)
	}
}

func TestDispatch_QuitLongForm(t *testing.T) {
	r := &fakeRadio{}
	_, open := dispatchLine(t, r, "quit")
	if open {
		t.Error("quit should close the connection")
	}
}

func TestDispatch_EmptyLine(t *testing.T) {
	r := &fakeRadio{}
	_, open := dispatchLine(t, r, "")
	if !open {
		t.Error("empty line should keep connection open")
	}
}

func TestDispatch_UnknownCommand(t *testing.T) {
	r := &fakeRadio{}
	out, open := dispatchLine(t, r, "BOGUS")
	if !open {
		t.Error("unknown command should keep connection open")
	}
	if out != rprtNA {
		t.Errorf("unknown command: expected RPRT -11, got %q", out)
	}
}

func TestDispatch_ExtendedModePrefix(t *testing.T) {
	// '+' prefix signals echo mode; qsradio strips it but does not echo.
	r := &fakeRadio{}
	out, _ := dispatchLine(t, r, "+F 144937500")
	if r.lastFreq != 144_937_500 {
		t.Errorf("extended-mode prefix: SetFrequency not called correctly, got %d", r.lastFreq)
	}
	if out != rprtOK {
		t.Errorf("expected RPRT 0, got %q", out)
	}
}

func TestDispatch_DumpState(t *testing.T) {
	r := &fakeRadio{}
	out, open := dispatchLine(t, r, "dump_state")
	if !open {
		t.Error("dump_state should keep connection open")
	}
	// Verify a selection of fields that hamlib clients depend on.
	// Modes appear as bitmasks, not name strings: PKTFM = 0x001000.
	for _, want := range []string{
		"0x001000",     // PKTFM present in filter list
		"0x20102d",     // RX mode bitmask includes PKTFM (bit 12)
		"ptt_type=0x1", // PTT via rigctld T command
		"has_set_freq=1",
		"has_get_freq=1",
		"RPRT 0",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("dump_state response missing %q", want)
		}
	}
}
