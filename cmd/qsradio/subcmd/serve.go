// SPDX-License-Identifier: Apache-2.0

package subcmd

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/muurk/qsradio/pkg/qsradio/radio"
	"github.com/muurk/qsradio/pkg/qsradio/rigctl"
)

// Serve implements "qsradio serve": opens the radio and starts a rigctld
// TCP server. PTT commands from connected clients will key the radio.
// Send SIGINT or SIGTERM to shut down cleanly.
func Serve(args []string) error {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	port := fs.String("port", "", "serial port (e.g. /dev/ttyACM0 on Linux, /dev/cu.usbmodem... on macOS, COM5 on Windows)")
	rigctldAddr := fs.String("rigctld", ":4532", "rigctld listen address")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *port == "" {
		return fmt.Errorf("--port is required")
	}

	r, err := radio.Open(*port)
	if err != nil {
		return err
	}
	defer r.Close()

	ver, err := r.GetFirmwareVersion()
	if err != nil {
		return fmt.Errorf("get firmware version: %w", err)
	}
	fmt.Fprintf(os.Stderr, "connected: %s\n", ver)
	fmt.Fprintf(os.Stderr, "WARNING: PTT commands from rigctld clients will transmit RF.\n")
	fmt.Fprintf(os.Stderr, "rigctld listening on %s\n", *rigctldAddr)

	srv := &rigctl.Server{Radio: r}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Cancel on SIGINT or SIGTERM.
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigs
		fmt.Fprintf(os.Stderr, "\nshutting down...\n")
		cancel()
	}()

	return srv.Serve(ctx, *rigctldAddr)
}
