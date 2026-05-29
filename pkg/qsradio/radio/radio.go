// SPDX-License-Identifier: Apache-2.0

// Package radio defines the Radio interface and associated types.
// All components above the protocol layer interact with a Radio value; they
// never call transport or protocol packages directly. This allows the real
// implementation (UV-K5 over serial), a fake (for tests and offline work),
// and future variants to be swapped without touching consumers.
package radio

// TXPower represents a transmit power level.
// Values correspond directly to the F4HWN OUTPUT_POWER_* enum minus USER(0):
// Low1 is the weakest, High is maximum.
type TXPower uint8

const (
	TXPowerLow1 TXPower = 0 // OUTPUT_POWER_LOW1 (weakest)
	TXPowerLow2 TXPower = 1 // OUTPUT_POWER_LOW2
	TXPowerLow3 TXPower = 2 // OUTPUT_POWER_LOW3
	TXPowerLow4 TXPower = 3 // OUTPUT_POWER_LOW4
	TXPowerLow5 TXPower = 4 // OUTPUT_POWER_LOW5
	TXPowerMid  TXPower = 5 // OUTPUT_POWER_MID
	TXPowerHigh TXPower = 6 // OUTPUT_POWER_HIGH (maximum)
)

// Mode identifies a modulation scheme.
type Mode string

const (
	ModeFM  Mode = "FM"  // FM voice; de-emphasis applied (standard)
	ModeNFM Mode = "NFM" // Narrow FM voice; de-emphasis applied
	ModeAM  Mode = "AM"  // AM
	ModePKT Mode = "PKT" // Packet/digital FM; BYP mode, no de-emphasis, flat audio
	ModeUSB Mode = "USB" // Upper sideband; BK4819 AF_BASEBAND2 (experimental HF)
	ModeLSB Mode = "LSB" // Lower sideband; BK4819 AF_BASEBAND1 (experimental HF)
)

// Bandwidth is a channel bandwidth in Hz.
type Bandwidth int

const (
	BandwidthWide   Bandwidth = 25000 // 25 kHz, standard FM
	BandwidthNarrow Bandwidth = 12500 // 12.5 kHz, narrow FM
)

// Caps describes which optional firmware features were detected at connect time.
type Caps struct {
	// BK4819RegAccess: CMD_0601/CMD_0602 are available (ENABLE_UART_RW_BK_REGS).
	// When true, SetFrequency writes BK4819 registers directly for live tuning.
	BK4819RegAccess bool
	// RSSICommand: CMD_0527 is available (ENABLE_EXTRA_UART_CMD).
	// When false, GetSMeter falls back to CMD_0601 on REG_0x67 if available.
	RSSICommand bool
}

// Capable is implemented by Radio backends that expose probed capabilities.
type Capable interface {
	Capabilities() Caps
}

// Rebooter is implemented by Radio backends that support a firmware reboot.
// A reboot is required after EEPROM writes to make the radio apply new settings.
type Rebooter interface {
	// Reboot sends a reboot command. The radio will restart and re-read EEPROM.
	// The serial connection should be considered closed after this call; the caller
	// must wait for startup (typically 3-5 s) and re-open via Open.
	Reboot() error
}

// Radio is the contract between the control stack and everything above it.
// Implementations must be safe for concurrent use from multiple goroutines.
type Radio interface {
	// Frequency control.
	SetFrequency(hz uint64) error
	GetFrequency() (uint64, error)

	// PTT (push-to-talk) control.
	SetPTT(active bool) error
	GetPTT() (bool, error)

	// Mode and bandwidth.
	SetMode(mode Mode, bandwidth Bandwidth) error
	GetMode() (Mode, Bandwidth, error)

	// Signal level. Returns the raw 9-bit BK4819 RSSI value (0-511).
	// dBm = raw/2 - 160. Use this value for hamlib RAWSTR.
	GetSMeter() (int, error)

	// Squelch level. 0 = fully open (always passes audio, recommended for Direwolf).
	// 1-9 = calibrated thresholds read from the radio's own EEPROM.
	SetSquelch(level int) error
	GetSquelch() (int, error)

	// TX power level. Reads calibration from the radio's EEPROM; never hardcoded.
	SetTXPower(p TXPower) error
	GetTXPower() (TXPower, error)

	// SetAudioGain sets the BK4819 AF Rx Gain-2 field (0-63, 0.5 dB/step).
	// 58 is the firmware default (maximum). Reducing this value lowers the
	// audio output level at the K1 port independently of the hardware volume
	// potentiometer, allowing software control of the AIOC input level.
	// AutoCalibrateGain() sets this automatically at connect time.
	SetAudioGain(gain uint8) error
	GetAudioGain() (uint8, error)

	// SetRFGain sets the BK4819 LNA gain (0-7, ~6 dB/step).
	// 3 is the firmware default. Reducing attenuates strong nearby signals;
	// increasing brings up weak distant signals.
	SetRFGain(gain uint8) error
	GetRFGain() (uint8, error)

	// SetMicGain sets the BK4819 MIC sensitivity via REG_7D bits[3:0] (0-15).
	// 0 is minimum sensitivity (firmware default). 8 is a good starting point
	// for packet TX via the AIOC; it increases FM deviation without distortion.
	// Applies immediately via CMD_0602 (read-modify-write on REG_7D).
	SetMicGain(gain uint8) error
	GetMicGain() (uint8, error)

	// GetInfo returns a human-readable description of the connected radio.
	GetInfo() string

	// Device information. Called once at connect; callers may cache the result.
	GetFirmwareVersion() (string, error)

	// EEPROM access. Available on all firmware variants that speak the base
	// F4HWN protocol. offset and length are in bytes.
	ReadEEPROM(offset, length uint16) ([]byte, error)
	WriteEEPROM(offset uint16, data []byte) error

	// Close releases the underlying serial port and frees resources.
	Close() error
}
