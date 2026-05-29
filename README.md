# qsradio

Turns a Quansheng UV-K5 into a hamlib-compatible rig. Works with Direwolf,
WSJT-X, fldigi, and any rigctld client.

qsradio implements the UV-K5 serial protocol and exposes a rigctld-compatible
TCP interface on port 4532. Any software that supports hamlib can then drive
the radio: tuning frequencies, switching modes, controlling PTT and squelch.

## Why this exists

The Quansheng UV-K5 is an inexpensive VHF/UHF transceiver that the amateur
radio community has thoroughly reverse-engineered. With an
[AIOC cable](https://github.com/skuep/AIOC) it is already a capable digital
modes radio: the AIOC provides USB serial, USB audio, and PTT control over
a single cable, so Direwolf can transmit and receive without any additional
software.

The AIOC alone does not provide software control of the radio's parameters, though.
Frequency, mode, bandwidth, squelch, and transmit power all require manual
operation at the keypad. For many setups that is perfectly workable. For
others (running the radio headless on a Raspberry Pi, automatic frequency
following in WSJT-X, real-time Doppler correction with gpredict, or
switching between repeaters without physical access to the radio) it becomes
the limiting factor.

qsradio fills that gap by implementing the UV-K5 serial protocol and
exposing it as a standard rigctld interface.

It also adds a `PKTFM` mode that configures the BK4819 audio chain
specifically for AFSK and FSK signals. De-emphasis is bypassed,
voice-oriented filters are disabled, and the RX compander is switched off.
If you are running digital modes on a UV-K5 in standard FM, the radio is
de-emphasising your audio and you probably do not know it.

## Hardware

| Component | Purpose | Approx. cost |
|-----------|---------|--------------|
| [Quansheng UV-K5](https://www.quansheng.com) | VHF/UHF transceiver | ~£25 |
| [AIOC cable](https://github.com/skuep/AIOC) | USB serial + audio + PTT via K1 connector | ~£30 |
| Raspberry Pi 4 or any Linux host | Runs qsradio | varies |

The AIOC (All-In-One Cable) is what makes this practical. It presents the
UV-K5's K1 port as a USB serial device, a USB audio device, and a PTT source
via modem control lines, all over a single USB connection.

qsradio also runs on macOS and Windows. The primary deployment target is
Linux on a Raspberry Pi.

## Firmware

**Full capability requires the headless-cat firmware.** Flash it once; it
persists across power cycles. The firmware is maintained separately at
[github.com/muurk/uv-k5-firmware-headless-cat](https://github.com/muurk/uv-k5-firmware-headless-cat).

**Flash in browser (Chrome or Edge):**
[Flash headless-cat](https://egzumer.github.io/uvtools/?firmwareURL=https://raw.githubusercontent.com/muurk/uv-k5-firmware-headless-cat/headless-cat/f4hwn.headless-cat.packed.bin)

**Flash with k5prog:** download
[f4hwn.headless-cat.packed.bin](https://raw.githubusercontent.com/muurk/uv-k5-firmware-headless-cat/headless-cat/f4hwn.headless-cat.packed.bin)
and run:

```bash
k5prog -F f4hwn.headless-cat.packed.bin -p /dev/ttyACM0 -YYY
```

The headless-cat firmware is a fork of
[F4HWN v4.3](https://github.com/armel/uv-k5-firmware-custom), which in turn
builds on the work of [egzumer](https://github.com/egzumer/uv-k5-firmware-custom)
and [DualTachyon](https://github.com/DualTachyon/uv-k5-firmware). The
protocol documentation by
[amnemonic](https://github.com/amnemonic/Quansheng_UV-K5_Firmware) was
instrumental in understanding the serial framing.

qsradio does work with unmodified F4HWN firmware, but frequency changes
require a reboot to apply and PKTFM optimisation is not available. For
anything beyond basic use, the headless-cat binary is the right starting
point.

## Quick start

With the headless-cat firmware flashed (see the Firmware section above)
and the AIOC connected:

```bash
# Verify the radio responds.
# Linux: /dev/ttyACM0  macOS: /dev/cu.usbmodem...  Windows: COM5
# On a Pi, run: dmesg | grep tty  after plugging in to find the device.
qsradio info --port /dev/ttyACM0

# Start the rigctld bridge.
qsradio serve --port /dev/ttyACM0 --rigctld :4532

# Point any hamlib client at localhost:4532.
```

## Usage

### Checking the connection

```
$ qsradio info --port /dev/ttyACM0
firmware: headless-cat v4.3
CMD_0601/0602 (BK4819 reg access): YES  live freq=144.937500 MHz
RSSI:                              raw=142  (-89 dBm)
CMD_0527      (RSSI):              YES
```

### Starting the rigctld bridge

Each received command is logged verbatim, so it is easy to see exactly what
each client is sending.

```
$ qsradio serve --port /dev/ttyACM0 --rigctld :4532
connected: headless-cat v4.3
WARNING: PTT commands from rigctld clients will transmit RF.
rigctld listening on :4532
2026/05/28 22:15:43 rigctl: client connected: 127.0.0.1:52341
2026/05/28 22:15:43 rigctl: [127.0.0.1:52341] rx: "dump_state"
2026/05/28 22:15:43 rigctl: [127.0.0.1:52341] rx: "V VFOA"
2026/05/28 22:15:43 rigctl: [127.0.0.1:52341] rx: "F 144937500"
2026/05/28 22:15:43 rigctl: [127.0.0.1:52341] rx: "M PKTFM 25000"
2026/05/28 22:15:51 rigctl: [127.0.0.1:52341] rx: "T VFOA 1"
2026/05/28 22:15:54 rigctl: [127.0.0.1:52341] rx: "T VFOA 0"
2026/05/28 22:15:54 rigctl: [127.0.0.1:52341] rx: "l RAWSTR"
```

### Tuning directly

```
$ qsradio set-freq --port /dev/ttyACM0 --freq 144937500
open+handshake:    143ms
get freq+mode:     51ms
  before: 144.812500 MHz  mode=FM  bw=25000 Hz
live BK4819 write: 8ms  (two CMD_0602 register writes, no reboot needed)
live BK4819 read:  6ms  (two CMD_0601 register reads)
  after:  144.937500 MHz  (target 144.937500 MHz)
  radio tuned OK (no reboot needed)
```

### Testing with rigctl

```bash
# Set PKTFM mode (flat audio, optimised for digital modes).
rigctl -m 2 -r localhost:4532 M PKTFM 25000

# Tune to a frequency.
rigctl -m 2 -r localhost:4532 F 144937500

# Read S-meter (raw 0-511 BK4819 value; dBm = raw/2 - 160).
rigctl -m 2 -r localhost:4532 l RAWSTR
142
```

### Direwolf

```
ADEVICE plughw:1,0
MODEM 1200
PTT RIG 2 localhost:4532
```

## Commands

| Command | Description |
|---------|-------------|
| `qsradio serve` | Start the rigctld bridge |
| `qsradio info` | Read firmware version and capabilities |
| `qsradio set-freq` | Tune VFO A to a frequency |
| `qsradio dump-eeprom` | Dump the full EEPROM to stdout |
| `qsradio reg-read` | Read a BK4819 register directly (diagnostic) |
| `qsradio rawdump` | Dump raw serial frames (diagnostic) |

## Building from source

Requires Go 1.25 or newer. No C toolchain needed (CGO is disabled).

```bash
make build   # current platform
make dist    # all release targets: Linux amd64/arm64/armv7, macOS, Windows
make check   # vet + fmt + tests
```

## Architecture

Four layers, cleanly separated:

- **transport**: wire framing, XOR obfuscation, CRC16-CCITT
- **protocol**: opcode definitions and payload codecs (pure encode/decode, no I/O)
- **radio**: serial port ownership, capability probing, shadow state, EEPROM calibration
- **rigctl**: hamlib TCP server, depends only on the `Radio` interface

The `pkg/qsradio` packages can be imported directly to drive the radio from
your own Go code. The `Radio` interface is the stable contract; the UV-K5
implementation, a test fake, and any future hardware variants all satisfy it.

## Related projects

**[QuanshengDock](https://github.com/nicsure/QuanshengDock)** by nicsure is a
Windows desktop application (C#, WPF) that provides a rich graphical interface
for the UV-K5: spectrum display, VFO control, and audio passthrough. It
requires a separate custom firmware maintained alongside the application.
A community fork,
[QuanshengDock-mod](https://github.com/BranoSundancer/QuanshengDock-mod-om1atb)
by OM1ATB, adds a hamlib rigctld interface via a virtual COM port.

QuanshengDock is a mature project and the natural choice if you want a
Windows GUI. qsradio takes a different approach: it is a headless command-line
tool with no graphical interface, runs on Linux and Raspberry Pi as well as
macOS and Windows, and uses the widely-deployed F4HWN firmware lineage rather
than a separate firmware. It is intended for integration into existing software
toolchains rather than as a standalone application.

## Acknowledgements

The UV-K5 open-source firmware community has been at this for years.
[DualTachyon](https://github.com/DualTachyon/uv-k5-firmware) produced the
original reverse-engineered firmware.
[amnemonic](https://github.com/amnemonic/Quansheng_UV-K5_Firmware) documented
the serial protocol in detail; the `libuvk5.py` reference implementation is
what this project's transport layer is built on.
[egzumer](https://github.com/egzumer/uv-k5-firmware-custom) extended that
work, and [F4HWN (armel)](https://github.com/armel/uv-k5-firmware-custom)
produced the v4.3 branch that headless-cat forks from.

[skuep's AIOC](https://github.com/skuep/AIOC) is the hardware piece that
makes the whole thing practical; three separate cables become one.

[nicsure's QuanshengDock](https://github.com/nicsure/QuanshengDock) is a
well-established Windows application for UV-K5 host control with a GUI,
spectrum display, and VFO interface. If you use Windows and want a graphical
interface, it is worth knowing about.

## License

Apache 2.0. See [LICENSE](LICENSE).

The headless-cat firmware is also Apache 2.0:
[github.com/muurk/uv-k5-firmware-headless-cat](https://github.com/muurk/uv-k5-firmware-headless-cat)
