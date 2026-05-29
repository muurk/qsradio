// SPDX-License-Identifier: Apache-2.0

// Package rigctl implements a subset of the hamlib net rigctld protocol over TCP.
// It translates incoming rigctld commands into radio.Radio interface calls.
//
// Implemented commands (the digital-modes subset):
//
//	dump_state       report radio capabilities to the client
//	F / f            set / get VFO frequency
//	T / t            set / get PTT state
//	M / m            set / get mode and passband width
//	V / v            set / get VFO (single-VFO rig: always returns VFOA)
//	chk_vfo          report whether per-VFO commands are used (always no)
//	l RAWSTR         get raw signal strength (S-meter)
//	q / quit         close the connection
//
// All other commands return RPRT -11 (RIG_ENAVAIL).
package rigctl

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"log"
	"net"
	"strings"

	"github.com/muurk/qsradio/pkg/qsradio/radio"
)

// Mode bitmask constants (hamlib rig.h).
const (
	rigModeAM    = 0x000001
	rigModeCW    = 0x000002
	rigModeUSB   = 0x000004
	rigModeLSB   = 0x000008
	rigModeFM    = 0x000020
	rigModeFMN   = 0x200000
	rigModePKTFM = 0x001000 // RIG_MODE_PKTFM: packet/digital on FM
)

// Level bitmask constants from hamlib 4.x rig.h CONSTANT_64BIT_FLAG(n).
const (
	rigLevelAF      = 0x000008  // RIG_LEVEL_AF      (1<<3)
	rigLevelRF      = 0x000010  // RIG_LEVEL_RF      (1<<4)
	rigLevelSQL     = 0x000020  // RIG_LEVEL_SQL     (1<<5)
	rigLevelRFPower = 0x001000  // RIG_LEVEL_RFPOWER (1<<12)
	rigLevelMicGain = 0x002000  // RIG_LEVEL_MICGAIN (1<<13)
	rigLevelRawStr  = 0x4000000 // RIG_LEVEL_RAWSTR  (1<<26)
)

const (
	rprtOK = "RPRT 0\n"
	rprtNA = "RPRT -11\n" // RIG_ENAVAIL: function not available
	rprtER = "RPRT -1\n"  // generic error
)

// dumpStateBody is the fixed portion of the dump_state response.
// Format follows rigctld protocol version 1 (RIGCTLD_PROT_VER = 1).
//
// Line 1:  protocol version
// Line 2:  rig model (2 = generic dummy, avoids hamlib model lookup)
// Line 3:  ITU region (deprecated, always 0)
// RX range: 18 MHz - 1300 MHz, FM+FMN+AM, no power limit, VFO A, antenna 1
// TX range: 136-175 MHz and 400-520 MHz, FM+FMN only, 500mW-5W
// Tuning step, filter, and capability fields follow.
const dumpStateBody = "" +
	"1\n" + // protocol version
	"2\n" + // rig model
	"0\n" + // ITU region (deprecated)
	// RX frequency ranges: FM|FMN|AM|PKTFM|USB|LSB across full BK4819 range
	"18000000 1300000000 0x20102d -1 -1 0x1 0x0\n" +
	"0 0 0 0 0 0 0\n" + // end RX
	// TX frequency ranges: FM|FMN|PKTFM on VHF/UHF only (no HF TX)
	"136000000 175000000 0x201020 500 5000 0x1 0x0\n" +
	"400000000 520000000 0x201020 500 5000 0x1 0x0\n" +
	"0 0 0 0 0 0 0\n" + // end TX
	// Tuning steps
	"0x20102d 1000\n" + // all modes: 1 kHz step
	"0 0\n" + // end tuning steps
	// Filters
	"0x200020 15000\n" + // FM+FMN: 15 kHz wide
	"0x200020 8000\n" + // FM+FMN: 8 kHz narrow
	"0x001000 12500\n" + // PKTFM: 12.5 kHz
	"0x000001 6000\n" + // AM: 6 kHz
	"0x00000c 2700\n" + // USB+LSB: 2.7 kHz (SSB standard)
	"0 0\n" + // end filters
	// Scalar capabilities
	"0\n" + // max_rit
	"0\n" + // max_xit
	"0\n" + // max_ifshift
	"0\n" + // announces
	"\n" + // preamp list (empty)
	"\n" + // attenuator list (empty)
	"0x0\n" + // has_get_func
	"0x0\n" + // has_set_func
	"0x4003038\n" + // has_get_level: AF|RF|SQL|RFPOWER|MICGAIN|RAWSTR
	"0x0003038\n" + // has_set_level: AF|RF|SQL|RFPOWER|MICGAIN
	"0x0\n" + // has_get_parm
	"0x0\n" + // has_set_parm
	// Protocol-1 key=value extension (included unconditionally for compatibility).
	"vfo_ops=0x0\n" +
	"ptt_type=0x1\n" + // RIG_PTT_RIG: PTT controlled via rigctld T command
	"targetable_vfo=0x0\n" +
	"has_set_vfo=1\n" +
	"has_get_vfo=1\n" +
	"has_set_freq=1\n" +
	"has_get_freq=1\n" +
	"has_set_conf=0\n" +
	"has_get_conf=0\n" +
	"has_power2mW=0\n" +
	"has_mW2power=0\n" +
	"has_get_ant=0\n" +
	"has_set_ant=0\n" +
	"timeout=0\n" +
	"rig_model=2\n" +
	"rigctld_version=qsradio-0.1\n" +
	"done\n"

// Server is a hamlib rigctld-compatible TCP server.
type Server struct {
	Radio radio.Radio
}

// Serve listens on addr and handles incoming connections until ctx is cancelled.
func (s *Server) Serve(ctx context.Context, addr string) error {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("rigctl: listen %s: %w", addr, err)
	}
	go func() {
		<-ctx.Done()
		ln.Close()
	}()
	defer ln.Close()

	for {
		conn, err := ln.Accept()
		if err != nil {
			select {
			case <-ctx.Done():
				return nil
			default:
				return fmt.Errorf("rigctl: accept: %w", err)
			}
		}
		go s.handleConn(conn)
	}
}

func (s *Server) handleConn(conn net.Conn) {
	remote := conn.RemoteAddr().String()
	log.Printf("rigctl: client connected: %s", remote)
	defer func() {
		log.Printf("rigctl: client disconnected: %s", remote)
		conn.Close()
	}()
	sc := bufio.NewScanner(conn)
	for sc.Scan() {
		line := strings.TrimRight(sc.Text(), "\r")
		log.Printf("rigctl: [%s] rx: %q", remote, line)
		if !s.dispatch(conn, line) {
			return
		}
	}
}

// dispatch handles one command line. Returns false if the connection should close.
func (s *Server) dispatch(w io.Writer, line string) bool {
	// Strip extended-mode prefix '+' (echo mode); we do not echo.
	if strings.HasPrefix(line, "+") {
		line = line[1:]
	}

	parts := strings.Fields(line)
	if len(parts) == 0 {
		return true
	}

	cmd := parts[0]
	args := parts[1:]

	// Normalise long-form commands (backslash prefix).
	if strings.HasPrefix(cmd, `\`) {
		cmd = cmd[1:]
	}

	switch cmd {
	case "q", "quit":
		fmt.Fprint(w, rprtOK)
		return false

	case "f", "get_freq":
		freq, err := s.Radio.GetFrequency()
		if err != nil {
			fmt.Fprint(w, rprtER)
			return true
		}
		fmt.Fprintf(w, "%d\n%s", freq, rprtOK)

	case "F", "set_freq":
		if len(args) < 1 {
			fmt.Fprint(w, rprtER)
			return true
		}
		// Handle both "F 144937500" and "F VFOA 144937500" (VFO-extended from NET rigctl)
		freqArg := args[0]
		if (freqArg == "VFOA" || freqArg == "VFOB" || freqArg == "VFO") && len(args) >= 2 {
			freqArg = args[1]
		}
		var freq uint64
		if _, err := fmt.Sscan(freqArg, &freq); err != nil {
			fmt.Fprint(w, rprtER)
			return true
		}
		if err := s.Radio.SetFrequency(freq); err != nil {
			fmt.Fprint(w, rprtER)
		} else {
			fmt.Fprint(w, rprtOK)
		}

	case "t", "get_ptt":
		active, err := s.Radio.GetPTT()
		if err != nil {
			fmt.Fprint(w, rprtER)
			return true
		}
		v := 0
		if active {
			v = 1
		}
		fmt.Fprintf(w, "%d\n%s", v, rprtOK)

	case "T", "set_ptt":
		if len(args) < 1 {
			fmt.Fprint(w, rprtER)
			return true
		}
		// Handle both "T 1" and "T VFOA 1" (hamlib extended format)
		pttArg := args[0]
		if (pttArg == "VFOA" || pttArg == "VFOB" || pttArg == "VFO") && len(args) >= 2 {
			pttArg = args[1]
		}
		var v int
		if _, err := fmt.Sscan(pttArg, &v); err != nil {
			fmt.Fprint(w, rprtER)
			return true
		}
		if err := s.Radio.SetPTT(v != 0); err != nil {
			fmt.Fprint(w, rprtER)
		} else {
			fmt.Fprint(w, rprtOK)
		}

	case "m", "get_mode":
		mode, bw, err := s.Radio.GetMode()
		if err != nil {
			fmt.Fprint(w, rprtER)
			return true
		}
		fmt.Fprintf(w, "%s\n%d\n%s", modeString(mode), int(bw), rprtOK)

	case "M", "set_mode":
		if len(args) < 2 {
			fmt.Fprint(w, rprtER)
			return true
		}
		mode, bw := parseMode(args[0], args[1])
		if err := s.Radio.SetMode(mode, bw); err != nil {
			fmt.Fprint(w, rprtER)
		} else {
			fmt.Fprint(w, rprtOK)
		}

	case "v", "get_vfo":
		fmt.Fprintf(w, "VFOA\n%s", rprtOK)

	case "V", "set_vfo":
		// Single-VFO rig: accept any VFO name.
		fmt.Fprint(w, rprtOK)

	case "chk_vfo":
		// We do not use per-VFO command prefixes.
		fmt.Fprintf(w, "CHKVFO 0\n%s", rprtOK)

	case "get_powerstat":
		// Radio is on. 1 = on, 0 = off.
		fmt.Fprintf(w, "1\n%s", rprtOK)

	case "set_powerstat":
		fmt.Fprint(w, rprtOK)

	case "get_lock_mode":
		// Dial not locked.
		fmt.Fprintf(w, "0\n%s", rprtOK)

	case "set_lock_mode":
		fmt.Fprint(w, rprtOK)

	case "s", "get_split_vfo":
		// Single-VFO rig: no split. Return split=0, VFO=VFOA.
		fmt.Fprintf(w, "0\nVFOA\n%s", rprtOK)

	case "S", "set_split_vfo":
		// Single-VFO rig: ignore split requests.
		fmt.Fprint(w, rprtOK)

	case "l", "get_level":
		switch {
		case len(args) > 0 && strings.EqualFold(args[0], "RAWSTR"):
			strength, err := s.Radio.GetSMeter()
			if err != nil {
				fmt.Fprint(w, rprtNA)
				return true
			}
			fmt.Fprintf(w, "%d\n%s", strength, rprtOK)
		case len(args) > 0 && strings.EqualFold(args[0], "SQL"):
			level, err := s.Radio.GetSquelch()
			if err != nil {
				fmt.Fprint(w, rprtER)
				return true
			}
			fmt.Fprintf(w, "%.6f\n%s", float64(level)/9.0, rprtOK)
		case len(args) > 0 && strings.EqualFold(args[0], "AF"):
			gain, err := s.Radio.GetAudioGain()
			if err != nil {
				fmt.Fprint(w, rprtER)
				return true
			}
			fmt.Fprintf(w, "%.6f\n%s", float64(gain)/63.0, rprtOK)
		case len(args) > 0 && strings.EqualFold(args[0], "RF"):
			gain, err := s.Radio.GetRFGain()
			if err != nil {
				fmt.Fprint(w, rprtER)
				return true
			}
			// Normalise 0-7 to 0.0-1.0.
			fmt.Fprintf(w, "%.6f\n%s", float64(gain)/7.0, rprtOK)
		case len(args) > 0 && (strings.EqualFold(args[0], "MIC") || strings.EqualFold(args[0], "MICGAIN")):
			gain, err := s.Radio.GetMicGain()
			if err != nil {
				fmt.Fprint(w, rprtER)
				return true
			}
			// 16 levels (0-15) mapped to 0.0-1.0.
			fmt.Fprintf(w, "%.6f\n%s", float64(gain)/15.0, rprtOK)
		case len(args) > 0 && strings.EqualFold(args[0], "RFPOWER"):
			p, err := s.Radio.GetTXPower()
			if err != nil {
				fmt.Fprint(w, rprtER)
				return true
			}
			// 7 levels (Low1=0 through High=6) mapped linearly to 0.0-1.0.
			fmt.Fprintf(w, "%.6f\n%s", float64(p)/6.0, rprtOK)
		default:
			fmt.Fprint(w, rprtNA)
		}

	case "L", "set_level":
		if len(args) < 2 {
			fmt.Fprint(w, rprtNA)
			return true
		}
		switch strings.ToUpper(args[0]) {
		case "SQL":
			var v float64
			if _, err := fmt.Sscan(args[1], &v); err != nil {
				fmt.Fprint(w, rprtER)
				return true
			}
			var level int
			if v <= 1.0 {
				level = int(v * 9)
			} else {
				level = int(v)
			}
			if level < 0 {
				level = 0
			} else if level > 9 {
				level = 9
			}
			if err := s.Radio.SetSquelch(level); err != nil {
				fmt.Fprint(w, rprtER)
			} else {
				fmt.Fprint(w, rprtOK)
			}
		case "AF":
			var v float64
			if _, err := fmt.Sscan(args[1], &v); err != nil {
				fmt.Fprint(w, rprtER)
				return true
			}
			var gain uint8
			if v <= 1.0 {
				gain = uint8(v * 63)
			} else {
				gain = uint8(v)
			}
			if err := s.Radio.SetAudioGain(gain); err != nil {
				fmt.Fprint(w, rprtER)
			} else {
				fmt.Fprint(w, rprtOK)
			}
		case "RF":
			var v float64
			if _, err := fmt.Sscan(args[1], &v); err != nil {
				fmt.Fprint(w, rprtER)
				return true
			}
			var gain uint8
			if v <= 1.0 {
				gain = uint8(v * 7)
			} else {
				gain = uint8(v)
			}
			if err := s.Radio.SetRFGain(gain); err != nil {
				fmt.Fprint(w, rprtER)
			} else {
				fmt.Fprint(w, rprtOK)
			}
		case "MIC", "MICGAIN": // hamlib 3.x used "MIC", 4.x uses "MICGAIN"
			var v float64
			if _, err := fmt.Sscan(args[1], &v); err != nil {
				fmt.Fprint(w, rprtER)
				return true
			}
			if v < 0 {
				v = 0
			} else if v > 1 {
				v = 1
			}
			gain := uint8(v*15 + 0.5)
			if err := s.Radio.SetMicGain(gain); err != nil {
				fmt.Fprint(w, rprtER)
			} else {
				fmt.Fprint(w, rprtOK)
			}
		case "RFPOWER":
			var v float64
			if _, err := fmt.Sscan(args[1], &v); err != nil {
				fmt.Fprint(w, rprtER)
				return true
			}
			if v < 0 {
				v = 0
			} else if v > 1 {
				v = 1
			}
			// Quantise 0.0-1.0 to 7 levels (Low1=0 through High=6).
			p := radio.TXPower(int(v*6 + 0.5))
			if p > radio.TXPowerHigh {
				p = radio.TXPowerHigh
			}
			if err := s.Radio.SetTXPower(p); err != nil {
				fmt.Fprint(w, rprtER)
			} else {
				fmt.Fprint(w, rprtOK)
			}
		default:
			fmt.Fprint(w, rprtNA)
		}

	case "get_info":
		fmt.Fprintf(w, "%s\n%s", s.Radio.GetInfo(), rprtOK)

	case "dump_state":
		fmt.Fprint(w, dumpStateBody)
		fmt.Fprint(w, rprtOK)

	default:
		fmt.Fprint(w, rprtNA)
	}

	return true
}

// modeString converts a radio.Mode to the hamlib mode string.
func modeString(m radio.Mode) string {
	switch m {
	case radio.ModeFM:
		return "FM"
	case radio.ModeNFM:
		return "FMN"
	case radio.ModeAM:
		return "AM"
	case radio.ModePKT:
		return "PKTFM"
	case radio.ModeUSB:
		return "USB"
	case radio.ModeLSB:
		return "LSB"
	default:
		return "FM"
	}
}

// parseMode converts a hamlib mode string and bandwidth string into radio types.
func parseMode(modeStr, bwStr string) (radio.Mode, radio.Bandwidth) {
	var bw int
	fmt.Sscan(bwStr, &bw)

	var mode radio.Mode
	switch strings.ToUpper(modeStr) {
	case "FMN":
		mode = radio.ModeNFM
	case "AM":
		mode = radio.ModeAM
	case "PKTFM", "FM-D": // FM-D is the hamlib 4.x NET wire name for RIG_MODE_PKTFM
		mode = radio.ModePKT
	case "USB":
		mode = radio.ModeUSB
	case "LSB":
		mode = radio.ModeLSB
	default:
		mode = radio.ModeFM
	}

	if bw <= 0 {
		switch mode {
		case radio.ModeNFM, radio.ModePKT, radio.ModeUSB, radio.ModeLSB:
			bw = int(radio.BandwidthNarrow)
		default:
			bw = int(radio.BandwidthWide)
		}
	}

	return mode, radio.Bandwidth(bw)
}
