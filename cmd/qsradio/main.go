// SPDX-License-Identifier: Apache-2.0

package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/muurk/qsradio/cmd/qsradio/subcmd"
)

// version is set at build time via -ldflags "-X main.version=<tag>".
var version = "dev"

func main() {
	flag.Usage = usage
	flag.Parse()

	if flag.NArg() < 1 {
		usage()
		os.Exit(1)
	}

	var err error
	switch flag.Arg(0) {
	case "version":
		fmt.Printf("qsradio %s\nhttps://github.com/muurk/qsradio\n", version)
	case "info":
		err = subcmd.Info(flag.Args()[1:])
	case "dump-eeprom":
		err = subcmd.DumpEEPROM(flag.Args()[1:])
	case "set-freq":
		err = subcmd.SetFreq(flag.Args()[1:])
	case "serve":
		err = subcmd.Serve(flag.Args()[1:])
	case "reg-read":
		err = subcmd.RegRead(flag.Args()[1:])
	case "rawdump":
		err = subcmd.RawDump(flag.Args()[1:])
	default:
		fmt.Fprintf(os.Stderr, "unknown subcommand: %q\n\n", flag.Arg(0))
		usage()
		os.Exit(1)
	}

	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintf(os.Stderr, "usage: qsradio <command> [flags]\n\n")
	fmt.Fprintln(os.Stderr, "commands:")
	fmt.Fprintln(os.Stderr, "  version      print build version and exit")
	fmt.Fprintln(os.Stderr, "  info         read firmware version and capabilities")
	fmt.Fprintln(os.Stderr, "  dump-eeprom  read full EEPROM to stdout")
	fmt.Fprintln(os.Stderr, "  set-freq     tune to a frequency in Hz")
	fmt.Fprintln(os.Stderr, "  serve        start the rigctld bridge and serial talker")
	fmt.Fprintln(os.Stderr, "  reg-read     read a BK4819 register directly (diagnostic)")
	fmt.Fprintln(os.Stderr, "  rawdump      diagnostic wire-level frame dump")
	fmt.Fprintln(os.Stderr, "\nhttps://github.com/muurk/qsradio")
}
