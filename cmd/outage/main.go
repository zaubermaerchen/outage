package main

// This file validates command-line options and coordinates version output
// and stream copying.

import (
	"errors"
	"fmt"
	"io"
	"os"
)

const (
	exitOK        = 0
	exitCopyError = 1
	exitArgError  = 2
)

const helpText = `Usage: outage --event signal:USR1
       outage --event signal:SIGUSR1

Copy stdin to stdout until the event is received. Receiving the event exits outage;
it does not send a signal directly to the producer.
Signal events are unsupported on Windows.

Options:
  --event signal:USR1       Exit on USR1 (signal:SIGUSR1 is an alias).
  --version                 Print the version (standalone).
  -h, --help                Show this help.
Help options take priority over every other argument.
`

var version = "dev"

func main() {
	os.Exit(run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}

func run(args []string, in io.Reader, out io.Writer, errOut io.Writer) int {
	for _, arg := range args {
		if arg != "-h" && arg != "--help" {
			continue
		}
		ignoreSIGPIPE()
		if _, err := fmt.Fprint(out, helpText); err != nil {
			writeDiagnostic(errOut, err)
			return exitCopyError
		}
		return exitOK
	}

	if len(args) == 1 && args[0] == "--version" {
		ignoreSIGPIPE()
		if _, err := fmt.Fprintf(out, "outage %s\n", version); err != nil {
			writeDiagnostic(errOut, err)
			return exitCopyError
		}
		return exitOK
	}

	if err := validateArgs(args); err != nil {
		writeDiagnostic(errOut, err)
		return exitArgError
	}

	signalCh, stopSignalMonitor := installSignalMonitor()
	defer stopSignalMonitor()

	copyDone := make(chan error, 1)
	go func() {
		_, err := io.Copy(out, in)
		copyDone <- err
	}()

	select {
	case <-signalCh:
		return exitOK
	case err := <-copyDone:
		if err != nil {
			writeDiagnostic(errOut, err)
			return exitCopyError
		}
		return exitOK
	}
}

func validateArgs(args []string) error {
	if len(args) == 0 {
		return errors.New("missing --event argument")
	}

	seenEvent := false
	for i := 0; i < len(args); i++ {
		if args[i] != "--event" {
			return fmt.Errorf("unexpected argument %q", args[i])
		}
		if seenEvent {
			return errors.New("duplicate --event argument")
		}
		seenEvent = true

		if i+1 >= len(args) {
			return errors.New("missing value for --event")
		}
		i++
		if args[i] != "signal:USR1" && args[i] != "signal:SIGUSR1" {
			return fmt.Errorf("unsupported event %q", args[i])
		}
		if !signalEventSupported() {
			return fmt.Errorf("unsupported event %q on this platform", args[i])
		}
	}

	return nil
}

func writeDiagnostic(errOut io.Writer, err error) {
	_, _ = fmt.Fprintf(errOut, "outage: %v\n", err)
}
