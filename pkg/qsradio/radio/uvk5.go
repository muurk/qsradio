// SPDX-License-Identifier: Apache-2.0

package radio

import (
	"fmt"
	"sync"
	"time"

	"go.bug.st/serial"

	"github.com/muurk/qsradio/pkg/qsradio/protocol"
	"github.com/muurk/qsradio/pkg/qsradio/transport"
)

// caps holds which extended serial commands are available on the connected firmware.
type caps struct {
	// bk4819RegAccess is true when the firmware was built with ENABLE_UART_RW_BK_REGS.
	// Enables CMD_0601 (read BK4819 register) and CMD_0602 (write BK4819 register),
	// which allow live frequency changes by writing REG_0x38/REG_0x39 directly.
	bk4819RegAccess bool

	// rssiCmd is true when the firmware was built with ENABLE_EXTRA_UART_CMD.
	// Enables CMD_0527 which returns RSSI + noise + glitch directly from BK4819.
	rssiCmd bool
}

// uvk5 implements Radio for a real UV-K5 connected via an AIOC cable.
// All methods are safe for concurrent use from multiple goroutines.
type uvk5 struct {
	mu       sync.Mutex   // serialises serial I/O and all state that depends on it
	shadowMu sync.RWMutex // protects shadow-state reads so probes never wait for serial I/O
	port     serial.Port
	framer   transport.Framer
	version  string
	caps     caps

	// portPath is stored so openLocked/SetPTT can open the serial connection.
	portPath string
	// pttPort holds the serial.Port opened during an active PTT. It is separate
	// from r.port so that UART commands cannot be sent while transmitting, and
	// PTT always opens a fresh CDC connection (guaranteed TE=0 on the AIOC).
	pttPort serial.Port

	// PTT state tracked locally because serial RTS is write-only.
	pttActive bool

	// Shadow state: last known or set values. Used as fallback when EEPROM
	// reads are not possible, and for immediate rigctld readback.
	shadowFreq    uint64
	shadowMode    Mode
	shadowBW      Bandwidth
	shadowSquelch int

	// Device-specific calibration read from EEPROM at connect time.
	cal calibration

	// shadowAFGain tracks the current BK4819 AF Rx Gain-2 value (0-63).
	shadowAFGain uint8
	// shadowRFGain tracks the current BK4819 LNA gain value (0-7).
	shadowRFGain uint8
	// shadowMicGain tracks the current BK4819 REG_7D MIC sensitivity bits[3:0] (0-15).
	// Default 8 (REG_7D=0xE958): produces adequate FM deviation for AFSK packet TX
	// via the AIOC. Firmware default is 0 (0xE940) which underdrives the modulator.
	shadowMicGain uint8
}

// openLocked opens the serial port for a UART command and returns a cleanup
// function to close it afterwards. If the port was already open (e.g. during
// initialisation in Open()), the cleanup is a no-op so Open() can safely call
// the same public methods without them closing the port prematurely.
// Returns an error if PTT is currently active.
// Caller must hold r.mu.
func (r *uvk5) openLocked() (func(), error) {
	if r.pttPort != nil {
		return nil, fmt.Errorf("radio: serial command unavailable while transmitting")
	}
	if r.port != nil {
		return func() {}, nil // already open; caller should not close it
	}
	sp, err := serial.Open(r.portPath, &serial.Mode{
		BaudRate: 38400,
		DataBits: 8,
		Parity:   serial.NoParity,
		StopBits: serial.OneStopBit,
	})
	if err != nil {
		return nil, fmt.Errorf("radio: open %s: %w", r.portPath, err)
	}
	sp.SetReadTimeout(2 * time.Second)
	r.port = sp
	r.framer = transport.New(sp)
	return func() {
		r.port.Close()
		r.port = nil
	}, nil
}

// Open opens the serial device at path, performs the firmware version
// handshake, probes capabilities, and loads calibration. After initialisation
// the port is closed; subsequent commands use ephemeral open/close so that
// PTT always opens a genuinely fresh CDC connection with TE=0 on the AIOC.
func Open(path string) (Radio, error) {
	sp, err := serial.Open(path, &serial.Mode{
		BaudRate: 38400,
		DataBits: 8,
		Parity:   serial.NoParity,
		StopBits: serial.OneStopBit,
	})
	if err != nil {
		return nil, fmt.Errorf("radio: open %s: %w", path, err)
	}

	// Drain stale bytes from previous sessions.
	sp.SetReadTimeout(100 * time.Millisecond)
	stale := make([]byte, 512)
	sp.Read(stale)

	if err := sp.SetReadTimeout(2 * time.Second); err != nil {
		sp.Close()
		return nil, fmt.Errorf("radio: set timeout: %w", err)
	}

	r := &uvk5{
		port:          sp,
		portPath:      path,
		framer:        transport.New(sp),
		shadowFreq:    145_500_000,
		shadowMode:    ModeFM,
		shadowBW:      BandwidthWide,
		shadowMicGain: 8, // produces adequate FM deviation for AFSK packet TX via AIOC
		// port will be closed after initialisation; openLocked() reopens per-command
	}

	ver, err := r.GetFirmwareVersion()
	if err != nil {
		sp.Close()
		return nil, fmt.Errorf("radio: handshake failed: %w", err)
	}
	r.version = ver

	// Probe for extended commands with a short timeout so we degrade
	// gracefully on standard F4HWN builds.
	if err := sp.SetReadTimeout(300 * time.Millisecond); err != nil {
		sp.Close()
		return nil, fmt.Errorf("radio: set probe timeout: %w", err)
	}
	r.caps = r.probeCaps()
	if err := sp.SetReadTimeout(2 * time.Second); err != nil {
		sp.Close()
		return nil, fmt.Errorf("radio: restore timeout: %w", err)
	}

	// Read device-specific calibration from EEPROM. Failures are non-fatal;
	// squelch and TX power fall back to safe defaults if calibration is absent.
	r.loadCalibration()

	// Default TX power to High. loadCalibration() zeroes the cal struct so this
	// must come after. High gives sufficient RF output for digipeater access;
	// the operator can reduce it via SetTXPower at any time.
	// txPowerExplicit is set true so applyVFOConfig always carries an explicit
	// power level in CMD_0603; frequency and power changes are then independent.
	r.cal.shadowTXPower = TXPowerHigh
	r.cal.txPowerExplicit = true

	// Auto-calibrate the audio gain based on current signal conditions.
	r.autoCalibrateGain()

	// Apply flat audio immediately so all modes benefit from the start.
	// This disables the voice-oriented BK4819 filter stages via REG_2B/REG_7E.
	r.mu.Lock()
	_ = r.applyFlatAudioLocked()
	r.mu.Unlock()

	// Close the port after initialisation. From this point every UART command
	// uses an ephemeral open/close via openLocked()/closeLocked(). PTT (SetPTT)
	// opens its own fresh connection, guaranteeing the AIOC's TE=0 and making
	// the first PTT attempt as reliable as every subsequent one.
	r.port.Close()
	r.port = nil
	r.framer = nil // framer references the now-closed port; openLocked() sets a fresh one

	return r, nil
}

// autoCalibrateGain reads the current RSSI and sets BK4819 REG_48 AF Rx
// Gain-2 to target a consistent audio output level at the AIOC for Direwolf
// (~35 on Direwolf's 0-100 scale), independent of the hardware volume pot.
//
// Gain mapping (AF Rx Gain-2 field, 0.5 dB/step, 58 = firmware default = max):
//
//	RSSI > 180  (> -70 dBm, strong)     →  8  (~-25 dB from max)
//	RSSI 140-180 (-90 to -70 dBm)       → 22  (~-18 dB from max)
//	RSSI 100-140 (-110 to -90 dBm)      → 38  (~-10 dB from max)
//	RSSI  60-100 (-130 to -110 dBm)     → 50  (~-4 dB from max)
//	RSSI  < 60   (< -130 dBm, weak)     → 58  (firmware max)
func (r *uvk5) autoCalibrateGain() {
	r.mu.Lock()
	defer r.mu.Unlock()

	gain := uint8(38) // safe default if RSSI unavailable
	if r.caps.bk4819RegAccess || r.caps.rssiCmd {
		if raw, err := r.getSMeterBK4819(); err == nil {
			switch {
			case raw > 180:
				gain = 8
			case raw > 140:
				gain = 22
			case raw > 100:
				gain = 38
			case raw > 60:
				gain = 50
			default:
				gain = 58
			}
		}
	}
	_ = r.setAudioGainLocked(gain)
}

// setAudioGainLocked writes the AF Rx Gain-2 value to BK4819 REG_48.
// Caller must hold r.mu.
func (r *uvk5) setAudioGainLocked(gain uint8) error {
	if !r.caps.bk4819RegAccess {
		r.shadowAFGain = gain
		return nil
	}
	reg := protocol.BuildAFGainReg(gain)
	if err := r.framer.WriteFrame(protocol.WriteBK4819RegRequest(protocol.BK4819RegAFGain, reg)); err != nil {
		return fmt.Errorf("radio: set audio gain: %w", err)
	}
	r.shadowAFGain = gain
	return nil
}

func (r *uvk5) SetAudioGain(gain uint8) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	closePort, err := r.openLocked()
	if err != nil {
		return err
	}
	defer closePort()
	return r.setAudioGainLocked(gain)
}

func (r *uvk5) GetAudioGain() (uint8, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.shadowAFGain, nil
}

// SetRFGain sets the BK4819 LNA gain via REG_13 bits [9:8] (LNA Short, 0-3)
// and bits [7:5] (LNA, 0-7). We map a single 0-7 value to the LNA field;
// LNA Short is held at 3 (maximum) to preserve sensitivity headroom.
//
// Firmware default: REG_13 = 0x03BE → LNA Short=3 (0dB), LNA=5 (~-4dB).
// gain=7 → maximum LNA (best for weak/distant signals)
// gain=0 → minimum LNA (best for strong nearby signals)
func (r *uvk5) SetRFGain(gain uint8) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if gain > 7 {
		gain = 7
	}
	if !r.caps.bk4819RegAccess {
		r.shadowRFGain = gain
		return nil
	}
	closePort, err := r.openLocked()
	if err != nil {
		return err
	}
	defer closePort()
	reg13 := uint16(3<<8) | uint16(gain<<5) | uint16(0x1E)
	if err := r.framer.WriteFrame(protocol.WriteBK4819RegRequest(protocol.BK4819RegAGC0, reg13)); err != nil {
		return fmt.Errorf("radio: set RF gain: %w", err)
	}
	r.shadowRFGain = gain
	return nil
}

func (r *uvk5) GetRFGain() (uint8, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.shadowRFGain, nil
}

// SetMicGain sets BK4819 REG_7D bits[3:0] (MIC sensitivity, 0-15).
// Uses read-modify-write to preserve the upper 12 bits of REG_7D.
// 0 = minimum sensitivity (firmware default), 8 = recommended for packet TX.
func (r *uvk5) SetMicGain(gain uint8) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if gain > 15 {
		gain = 15
	}
	if !r.caps.bk4819RegAccess {
		r.shadowMicGain = gain
		return nil
	}
	closePort, err := r.openLocked()
	if err != nil {
		return err
	}
	defer closePort()
	// Read current REG_7D to preserve upper 12 bits.
	if err := r.framer.WriteFrame(protocol.ReadBK4819RegRequest(0x7D)); err != nil {
		return fmt.Errorf("radio: SetMicGain read: %w", err)
	}
	resp, err := r.framer.ReadFrame()
	if err != nil {
		return fmt.Errorf("radio: SetMicGain read: %w", err)
	}
	_, current, err := protocol.ParseReadBK4819RegResponse(resp)
	if err != nil {
		return fmt.Errorf("radio: SetMicGain parse: %w", err)
	}
	newVal := (current & 0xFFF0) | uint16(gain&0x0F)
	if err := r.framer.WriteFrame(protocol.WriteBK4819RegRequest(0x7D, newVal)); err != nil {
		return fmt.Errorf("radio: SetMicGain write: %w", err)
	}
	r.shadowMicGain = gain
	return nil
}

func (r *uvk5) GetMicGain() (uint8, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.shadowMicGain, nil
}

func (r *uvk5) GetInfo() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return fmt.Sprintf("qsradio UV-K5 firmware=%s", r.version)
}

// loadCalibration reads TX power and squelch calibration from the radio's EEPROM
// and caches it. Called once at Open(), after the capability probe.
// Calibration values differ between individual radios and must not be hardcoded.
func (r *uvk5) loadCalibration() {
	var c calibration

	// Squelch UHF thresholds: 6 rows x 16 bytes at 0x1E00.
	if data, err := r.ReadEEPROM(calSQLUHFBase, calSQLRows*calSQLRowLen); err == nil {
		for row := 0; row < calSQLRows; row++ {
			copy(c.sqlUHF[row][:], data[row*calSQLRowLen:(row+1)*calSQLRowLen])
		}
	}

	// Squelch VHF thresholds: 6 rows x 16 bytes at 0x1E60.
	if data, err := r.ReadEEPROM(calSQLVHFBase, calSQLRows*calSQLRowLen); err == nil {
		for row := 0; row < calSQLRows; row++ {
			copy(c.sqlVHF[row][:], data[row*calSQLRowLen:(row+1)*calSQLRowLen])
		}
	}

	// TX power calibration: 7 bands x 16 bytes at 0x1ED0.
	if data, err := r.ReadEEPROM(calTXPBase, calTXPBands*calTXPBandLen); err == nil {
		for band := 0; band < calTXPBands; band++ {
			copy(c.txp[band][:], data[band*calTXPBandLen:(band+1)*calTXPBandLen])
		}
	}

	c.loaded = true

	r.mu.Lock()
	r.cal = c
	r.mu.Unlock()
}

// probeCaps sends probe commands to detect which optional firmware features
// are compiled in. Caller must hold no lock and have a short read timeout set.
func (r *uvk5) probeCaps() caps {
	var c caps

	// Probe CMD_0601: read BK4819 REG_0x38. If ENABLE_UART_RW_BK_REGS is
	// compiled in, we get a 0x0601 reply; otherwise the firmware is silent.
	if err := r.framer.WriteFrame(protocol.ReadBK4819RegRequest(protocol.BK4819RegFreqLow)); err == nil {
		if resp, err := r.framer.ReadFrame(); err == nil {
			if opcode, _ := protocol.ParseResponseOpcode(resp); opcode == protocol.CmdReadBK4819Reg {
				c.bk4819RegAccess = true
			}
		}
	}
	// Drain any stale bytes after the probe.
	r.drainLocked()

	// Probe CMD_0527: RSSI query. ENABLE_EXTRA_UART_CMD required.
	if err := r.framer.WriteFrame(protocol.RSSIRequest()); err == nil {
		if resp, err := r.framer.ReadFrame(); err == nil {
			opcode, _ := protocol.ParseResponseOpcode(resp)
			if opcode == 0x0528 {
				c.rssiCmd = true
			}
		}
	}
	r.drainLocked()

	return c
}

// drainLocked reads and discards bytes until a read timeout (no lock held).
func (r *uvk5) drainLocked() {
	buf := make([]byte, 256)
	r.port.Read(buf)
}

func (r *uvk5) GetFirmwareVersion() (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	// Return cached version from initial Open() if available.
	if r.version != "" {
		return r.version, nil
	}
	// Fresh fetch (only during initial Open() when port is still open).
	if err := r.framer.WriteFrame(protocol.FirmwareVersionRequest()); err != nil {
		return "", err
	}
	resp, err := r.framer.ReadFrame()
	if err != nil {
		return "", err
	}
	return protocol.ParseFirmwareVersion(resp)
}

// readEEPROMLocked performs an EEPROM read without acquiring the mutex.
// The caller must hold r.mu.
func (r *uvk5) readEEPROMLocked(offset, length uint16) ([]byte, error) {
	if err := r.framer.WriteFrame(protocol.ConfigMemRequest(offset, length)); err != nil {
		return nil, err
	}
	resp, err := r.framer.ReadFrame()
	if err != nil {
		return nil, err
	}
	return protocol.ParseConfigMem(resp)
}

// writeEEPROMLocked performs an EEPROM write without acquiring the mutex.
// The caller must hold r.mu.
func (r *uvk5) writeEEPROMLocked(offset uint16, data []byte) error {
	req, err := protocol.WriteConfigMemRequest(offset, data)
	if err != nil {
		return err
	}
	if err := r.framer.WriteFrame(req); err != nil {
		return err
	}
	_, err = r.framer.ReadFrame()
	return err
}

func (r *uvk5) ReadEEPROM(offset, length uint16) ([]byte, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	closePort, err := r.openLocked()
	if err != nil {
		return nil, err
	}
	defer closePort()
	return r.readEEPROMLocked(offset, length)
}

func (r *uvk5) WriteEEPROM(offset uint16, data []byte) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	closePort, err := r.openLocked()
	if err != nil {
		return err
	}
	defer closePort()
	return r.writeEEPROMLocked(offset, data)
}

// SetPTT activates or releases PTT via the AIOC serial port control lines.
//
// AIOC PTT polarity: active when DTR=1 AND RTS=0.
//
// The AIOC firmware blocks SET_CONTROL_LINE_STATE while USART_CR1_TE is set
// (UART transmitter active). After any UART communication through a CDC
// connection, TE may remain set on the AIOC side. The only reliable way to
// reset TE is to close the CDC connection entirely and reopen it fresh.
//
// SetPTT therefore:
//   - On activate: closes the main serial port, opens a fresh PTT-only fd
//     (TE=0 on fresh open), then drives DTR=1, RTS=0.
//   - On release:  drives DTR=0, RTS=1, closes the PTT fd, and reopens the
//     main serial port for UART communication.
//
// UART commands cannot be sent while PTT is active. This is acceptable because
// the radio is in TX mode and does not respond to CAT during transmission.
func (r *uvk5) SetPTT(active bool) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if active {
		if r.pttPort != nil {
			return nil // already keyed
		}
		// r.port is nil (closed after last command) so this is a fresh CDC open.
		// open_count was 0 → full AIOC init → TE=0 guaranteed.
		sp, err := serial.Open(r.portPath, &serial.Mode{
			BaudRate: 38400,
			DataBits: 8,
			Parity:   serial.NoParity,
			StopBits: serial.OneStopBit,
		})
		if err != nil {
			return fmt.Errorf("radio: SetPTT open: %w", err)
		}
		r.pttPort = sp
		// Brief pause after open: let AIOC fully initialise the fresh CDC connection.
		time.Sleep(100 * time.Millisecond)
		// DTR=0, DTR=1 (RTS still high), RTS=0, matching pyserial's confirmed sequence.
		if err := pttAssert(sp); err != nil {
			sp.Close()
			r.pttPort = nil
			return fmt.Errorf("radio: SetPTT RTS: %w", err)
		}
	} else {
		if r.pttPort == nil {
			return nil // already released
		}
		pttRelease(r.pttPort)
		r.pttPort.Close()
		r.pttPort = nil
	}

	r.pttActive = active
	return nil
}

func (r *uvk5) GetPTT() (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.pttActive, nil
}

// SetFrequency changes the VFO A frequency.
//
//   - CMD_0603 path (preferred): sends a single SetVFO command that goes
//     through the firmware state machine: updates BK4819, squelch calibration,
//     and display atomically. Available when ENABLE_UART_RW_BK_REGS is compiled in.
//
//   - EEPROM path (fallback): writes the VFO band slot in EEPROM.
//     Requires a reboot to apply.
func (r *uvk5) SetFrequency(hz uint64) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.shadowMu.Lock()
	r.shadowFreq = hz
	r.shadowMu.Unlock()
	closePort, err := r.openLocked()
	if err != nil {
		return err
	}
	defer closePort()
	if r.caps.bk4819RegAccess {
		return r.applyVFOConfig()
	}
	return r.setFrequencyEEPROM(hz)
}

// setFrequencyEEPROM writes the active VFO A slot in EEPROM. Requires reboot.
// Caller must hold r.mu.
func (r *uvk5) setFrequencyEEPROM(hz uint64) error {
	_, freqSlotA, _, _, err := r.readVFORegisters()
	if err != nil {
		return err
	}
	slot := int(freqSlotA)
	if slot < vfoSlotMin || slot > vfoSlotMax {
		return fmt.Errorf("radio: FreqChannel_A=%d out of VFO range %d-%d", slot, vfoSlotMin, vfoSlotMax)
	}
	rec, err := r.readChannelSlot(slot)
	if err != nil {
		return err
	}
	rec.Freq = uint32(hz / 10)
	if err := r.writeChannelSlot(slot, rec); err != nil {
		return err
	}
	return nil
}

// GetFrequency returns the current VFO A frequency from shadow state.
// Shadow is always up to date because every SetFrequency goes through
// applyVFOConfig which the firmware confirms by updating the display.
func (r *uvk5) GetFrequency() (uint64, error) {
	r.shadowMu.RLock()
	defer r.shadowMu.RUnlock()
	return r.shadowFreq, nil
}

// SetMode configures the demodulation mode and bandwidth.
// With CMD_0603 available, goes through the firmware state machine atomically.
func (r *uvk5) SetMode(mode Mode, bandwidth Bandwidth) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.shadowMu.Lock()
	r.shadowMode = mode
	r.shadowBW = bandwidth
	r.shadowMu.Unlock()
	closePort, err := r.openLocked()
	if err != nil {
		return err
	}
	defer closePort()
	if r.caps.bk4819RegAccess {
		return r.applyVFOConfig()
	}
	return r.setModeEEPROM(mode, bandwidth)
}

// GetMode returns the current mode and bandwidth from shadow state.
func (r *uvk5) GetMode() (Mode, Bandwidth, error) {
	r.shadowMu.RLock()
	defer r.shadowMu.RUnlock()
	return r.shadowMode, r.shadowBW, nil
}

// setModeEEPROM writes mode and bandwidth to the active VFO A slot in EEPROM.
// Fallback when ENABLE_UART_RW_BK_REGS is absent. Requires a reboot.
// Caller must hold r.mu.
func (r *uvk5) setModeEEPROM(mode Mode, bandwidth Bandwidth) error {
	_, freqSlotA, _, _, err := r.readVFORegisters()
	if err != nil {
		return err
	}
	slot := int(freqSlotA)
	if slot < vfoSlotMin || slot > vfoSlotMax {
		return fmt.Errorf("radio: FreqChannel_A=%d out of VFO range", slot)
	}
	rec, err := r.readChannelSlot(slot)
	if err != nil {
		return err
	}
	setModeInRecord(&rec, mode, bandwidth)
	if err := r.writeChannelSlot(slot, rec); err != nil {
		return err
	}
	return nil
}

// Reboot sends CMD_REBOOT (0x05DD) and closes the serial port.
// The radio restarts and re-reads all EEPROM settings, applying any frequency
// or mode changes written since the last reboot.
// The caller must wait for the radio to restart (typically 3-5 s) before
// calling Open again.
func (r *uvk5) Reboot() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	// CMD_REBOOT: 2-byte opcode + 2-byte body_len (0) = 4-byte payload.
	// No reply is sent by the radio.
	payload := []byte{0xDD, 0x05, 0x00, 0x00}
	r.framer.WriteFrame(payload)
	return r.port.Close()
}

// SetSquelch sets the squelch level.
// Level 0 = fully open (recommended for Direwolf, captures all signals).
// Level 1-9 = calibrated thresholds from this radio's EEPROM.
// With CMD_0603 available, passes the level to the firmware's own
// RADIO_ConfigureSquelchAndOutputPower, which reads the correct
// per-band calibration values.
func (r *uvk5) SetSquelch(level int) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.shadowSquelch = level
	if r.caps.bk4819RegAccess {
		closePort, err := r.openLocked()
		if err != nil {
			return err
		}
		defer closePort()
		return r.applyVFOConfig()
	}
	return nil
}

func (r *uvk5) GetSquelch() (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.shadowSquelch, nil
}

// applyVFOConfig sends CMD_0603 with the current shadow state (frequency, mode,
// bandwidth, squelch, AF override). The firmware updates its internal VFO,
// applies per-band squelch calibration, tunes the BK4819, optionally overrides
// the AF output type, and refreshes the display, all atomically.
// Caller must hold r.mu.
func (r *uvk5) applyVFOConfig() error {
	var modulation, bandwidth, afOverride uint8

	switch r.shadowMode {
	case ModeAM:
		modulation = protocol.ModulationAM
		bandwidth = protocol.FWBandwidthNarrow
		afOverride = protocol.AFDefault // AF_AM selected automatically by firmware

	case ModeNFM:
		modulation = protocol.ModulationFM
		bandwidth = protocol.FWBandwidthNarrow
		afOverride = protocol.AFDefault // AF_FM with de-emphasis (narrow voice)

	case ModePKT:
		// Packet/digital FM: AFBYP output mode to bypass BK4819 de-emphasis.
		// Without AFBYP the de-emphasis stage attenuates the 2200 Hz FSK space tone
		// relative to the 1200 Hz mark tone, destroying POCSAG and AX.25 bit-error rates.
		// REG_2B flat audio and compander disable are applied after CMD_0603 via
		// applyFlatAudioLocked.
		//
		// Bandwidth MUST be FWBandwidthWide (0 = 25 kHz). The F4HWN firmware
		// radio.c RADIO_SetupRegisters contains:
		//   if (Bandwidth == BK4819_FILTER_BW_NARROW && gSetting_set_nfm == 1)
		//       Bandwidth = BK4819_FILTER_BW_NARROWER;  // 6.25 kHz
		// When gSetting_set_nfm is enabled (common factory default), sending
		// bandwidth=1 (12.5 kHz narrow) silently becomes 6.25 kHz in the BK4819,
		// which only allows ±1.25 kHz deviation, far too narrow for Bell 202 AFSK
		// (requires ±3 kHz). Wide (bandwidth=0) bypasses this firmware narrowing.
		modulation = protocol.ModulationFM
		bandwidth = protocol.FWBandwidthWide
		afOverride = protocol.AFBYP

	case ModeUSB:
		// Upper sideband: BK4819 AF_BASEBAND2 (USB demodulator).
		// Experimental for HF reception; the BK4819 is not a dedicated SSB
		// chip but AF_BASEBAND2 provides the closest approximation.
		modulation = protocol.ModulationUSB
		bandwidth = protocol.FWBandwidthNarrow
		afOverride = 5 // BK4819_AF_BASEBAND2

	case ModeLSB:
		// Lower sideband: BK4819 AF_BASEBAND1 (raw baseband).
		// Experimental for HF reception.
		modulation = protocol.ModulationFM
		bandwidth = protocol.FWBandwidthNarrow
		afOverride = 4 // BK4819_AF_BASEBAND1

	default: // ModeFM: wide FM voice
		modulation = protocol.ModulationFM
		bandwidth = protocol.FWBandwidthWide
		afOverride = protocol.AFDefault // AF_FM with de-emphasis (voice standard)
	}

	squelch := uint8(r.shadowSquelch)
	if squelch > 9 {
		squelch = 9
	}

	// Always send an explicit power level so that frequency and power changes
	// are independent: whatever shadowTXPower holds is what the radio uses,
	// regardless of what triggered the CMD_0603.
	// radio.TXPower values are 0-6 (Low1-High). Wire values are 1-7.
	powerLevel := uint8(r.cal.shadowTXPower) + 1
	payload := protocol.SetVFORequest(r.shadowFreq, modulation, bandwidth, squelch, afOverride, powerLevel)
	if err := r.framer.WriteFrame(payload); err != nil {
		return fmt.Errorf("radio: CMD_SET_VFO: %w", err)
	}
	// CMD_0603 has no reply and triggers RADIO_SetupRegisters in the firmware,
	// which performs multiple BK4819 SPI writes. Reading REG_2B via CMD_0601
	// immediately races against those writes; the firmware may not yet be ready
	// to handle the next serial command. A short pause avoids the race.
	time.Sleep(80 * time.Millisecond)
	return r.applyFlatAudioLocked()
}

// applyFlatAudioLocked disables the BK4819 voice-oriented audio filter stages
// and dynamic processing that degrade FSK tone quality.
//
// REG_2B filter stages disabled (firmware never writes this register, so the
// BK4819 reset default 0x0000 = all filters enabled is what we override):
//   - RX de-emphasis     (bit 8): was attenuating 2200 Hz AFSK space tone
//   - AFRxLPF3K          (bit 9): 3 kHz RX LPF, additional high-freq loss
//   - AFRxHPF300         (bit 10): sub-audio HPF
//   - TX pre-emphasis    (bit 0): would distort AFSK tones on transmit
//   - AFTxLPF1           (bit 1): TX voice LPF (~3 kHz)
//   - AFTxHPF300         (bit 2): sub-audio HPF on TX
//
// REG_7E bits [5:0] are cleared to bypass DC filters on both TX and RX paths.
//
// REG_28 / REG_31: the firmware enables a 1:2 RX expander (compander) by default.
// This causes "pumping" distortion on FSK; the expander amplitude-modulates the
// audio as each bit transition arrives, which corrupts POCSAG and AFSK decoding.
// We disable the compander entirely for packet/digital modes.
//
// Caller must hold r.mu.
func (r *uvk5) applyFlatAudioLocked() error {
	if !r.caps.bk4819RegAccess {
		return nil
	}

	// Direct write REG_2B: the firmware never touches this register, so the value
	// is always either 0x0000 (reset default) or our previous write. No read needed.
	if err := r.framer.WriteFrame(protocol.WriteBK4819RegRequest(protocol.BK4819RegAudioFilter, protocol.FlatAudioAllBits)); err != nil {
		return err
	}

	// Read-modify-write REG_7E: clear bits [5:0] to bypass DC filters.
	if err := r.framer.WriteFrame(protocol.ReadBK4819RegRequest(protocol.BK4819RegDCFilter)); err != nil {
		return err
	}
	resp, err := r.framer.ReadFrame()
	if err != nil {
		return err
	}
	_, reg7e, err := protocol.ParseReadBK4819RegResponse(resp)
	if err != nil {
		return err
	}
	if err := r.framer.WriteFrame(protocol.WriteBK4819RegRequest(protocol.BK4819RegDCFilter, reg7e&0xFFC0)); err != nil {
		return err
	}

	// Disable RX compander. REG_28 bits [15:14] = 00 disables the expander ratio.
	if err := r.framer.WriteFrame(protocol.WriteBK4819RegRequest(protocol.BK4819RegCompander, 0x0000)); err != nil {
		return err
	}
	// Direct write REG_31 = 0: clear compander enable (bit 3) and all other voice
	// features. RADIO_SetupRegisters already called BK4819_DisableScramble() and
	// BK4819_DisableVox() before we run, so bits 1 and 4 are already 0. Writing 0
	// is safe and avoids a read round-trip that can time out.
	if err := r.framer.WriteFrame(protocol.WriteBK4819RegRequest(protocol.BK4819RegControl, 0x0000)); err != nil {
		return err
	}

	// Re-apply MIC sensitivity (REG_7D bits[3:0]) after CMD_0603.
	// The firmware's RADIO_SetupRegisters resets REG_7D to its default (bits[3:0]=0),
	// so any SetMicGain call made before CMD_0603 would be overwritten.  Applying it
	// here guarantees the shadow value is in effect whenever flat audio is active.
	if r.shadowMicGain > 0 {
		if err := r.framer.WriteFrame(protocol.ReadBK4819RegRequest(protocol.BK4819RegMic)); err != nil {
			return err
		}
		resp, err := r.framer.ReadFrame()
		if err != nil {
			return err
		}
		_, regMic, err := protocol.ParseReadBK4819RegResponse(resp)
		if err != nil {
			return err
		}
		// bits[3:0] = shadowMicGain, preserve bits[15:4]
		newMic := (regMic & 0xFFF0) | uint16(r.shadowMicGain&0x0F)
		if err := r.framer.WriteFrame(protocol.WriteBK4819RegRequest(protocol.BK4819RegMic, newMic)); err != nil {
			return err
		}
	}

	return nil
}

// SetTXPower sets the transmit power level. The level is applied live via
// CMD_0603 and also written to all EEPROM channel slots so that
// RADIO_ConfigureChannel never reloads a stale value from EEPROM regardless
// of whether the radio is in VFO or memory-channel mode.
func (r *uvk5) SetTXPower(p TXPower) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.cal.shadowTXPower = p
	r.cal.txPowerExplicit = true
	if r.caps.bk4819RegAccess {
		closePort, err := r.openLocked()
		if err != nil {
			return err
		}
		defer closePort()
		if err := r.applyVFOConfig(); err != nil {
			return err
		}
		return r.persistPowerLevelEEPROM(uint8(p) + 1)
	}
	return nil
}

// persistPowerLevelEEPROM writes the OUTPUT_POWER field (bits [4:2] of byte 4
// in the second 8-byte block of each channel record) to all 14 VFO freq-channel
// EEPROM slots and to the two currently active memory-channel slots.
// This prevents RADIO_ConfigureChannel from reloading a stale power level when
// the radio is in either VFO mode or memory-channel mode.
// Caller must hold r.mu and have an open serial port (openLocked).
func (r *uvk5) persistPowerLevelEEPROM(wireLevel uint8) error {
	setPower := func(addr uint16) error {
		data, err := r.readEEPROMLocked(addr, 8)
		if err != nil {
			return err
		}
		data[4] = (data[4] & 0xE3) | ((wireLevel & 0x7) << 2)
		return r.writeEEPROMLocked(addr, data)
	}

	// All 14 VFO freq-channel slots: slots 200-213 at 0x0C80, 16 bytes each.
	// The power byte is at offset 8+4=12 within each 16-byte record.
	for i := uint16(0); i < 14; i++ {
		if err := setPower(0x0C80 + i*16 + 8); err != nil {
			return fmt.Errorf("radio: persist power to VFO slot %d: %w", i, err)
		}
	}

	// Active memory channels for VFO A and VFO B (ScreenChannel registers at 0xE80).
	screens, err := r.readEEPROMLocked(0xE80, 4)
	if err != nil {
		return fmt.Errorf("radio: read ScreenChannel: %w", err)
	}
	for _, ch := range []uint8{screens[0], screens[3]} {
		if ch <= 199 { // IS_MR_CHANNEL
			if err := setPower(uint16(ch)*16 + 8); err != nil {
				return fmt.Errorf("radio: persist power to MR channel %d: %w", ch, err)
			}
		}
	}
	return nil
}

func (r *uvk5) GetTXPower() (TXPower, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.cal.shadowTXPower, nil
}

// GetSMeter returns the raw BK4819 RSSI value (0-511).
//
// Two paths:
//   - CMD_0527 (preferred): single command, returns RSSI+noise+glitch.
//     Requires firmware ENABLE_EXTRA_UART_CMD.
//   - CMD_0601 (fallback): read REG_0x67 directly.
//     Requires firmware ENABLE_UART_RW_BK_REGS (already confirmed available).
func (r *uvk5) GetSMeter() (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	closePort, err := r.openLocked()
	if err != nil {
		return 0, err
	}
	defer closePort()
	if r.caps.rssiCmd {
		return r.getSMeterCmd0527()
	}
	if r.caps.bk4819RegAccess {
		return r.getSMeterBK4819()
	}
	return 0, fmt.Errorf("radio: GetSMeter: no supported path (need ENABLE_EXTRA_UART_CMD or ENABLE_UART_RW_BK_REGS)")
}

func (r *uvk5) getSMeterCmd0527() (int, error) {
	if err := r.framer.WriteFrame(protocol.RSSIRequest()); err != nil {
		return 0, err
	}
	resp, err := r.framer.ReadFrame()
	if err != nil {
		return 0, err
	}
	rssi, err := protocol.ParseRSSI(resp)
	if err != nil {
		return 0, err
	}
	return int(rssi.Raw), nil
}

// getSMeterBK4819 reads BK4819_REG_67 directly via CMD_0601.
// The raw 9-bit RSSI value (0-511) is returned.
// Caller must hold r.mu.
func (r *uvk5) getSMeterBK4819() (int, error) {
	if err := r.framer.WriteFrame(protocol.ReadBK4819RegRequest(protocol.BK4819RegRSSI)); err != nil {
		return 0, err
	}
	resp, err := r.framer.ReadFrame()
	if err != nil {
		return 0, err
	}
	_, val, err := protocol.ParseReadBK4819RegResponse(resp)
	if err != nil {
		return 0, err
	}
	return int(val & 0x01FF), nil // bits 8:0
}

// Capabilities returns the results of the capability probe done at connect time.
func (r *uvk5) Capabilities() Caps {
	r.mu.Lock()
	defer r.mu.Unlock()
	return Caps{
		BK4819RegAccess: r.caps.bk4819RegAccess,
		RSSICommand:     r.caps.rssiCmd,
	}
}

func (r *uvk5) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	// Release PTT if active.
	if r.pttPort != nil {
		ensureRTSHigh(r.pttPort)
		r.pttPort.Close()
		r.pttPort = nil
	} else if r.port != nil {
		ensureRTSHigh(r.port)
	}
	if r.port != nil {
		return r.port.Close()
	}
	return nil
}
