package main

// This file validates command-line arguments and coordinates event monitoring,
// version output, and stream copying.

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
)

const (
	exitOK        = 0
	exitCopyError = 1
	exitArgError  = 2
)

const helpText = `Usage: outage signal:USR1
       outage signal:SIGUSR1
       outage signal:USR2
       outage signal:SIGUSR2
       outage file:<path>

Copy stdin to stdout until the event is received. Receiving the event exits outage;
it does not send a signal directly to the producer.
Signal events are unsupported on Windows.
File events exit when the specified path exists.

Arguments:
  signal:USR1                Exit on USR1 (signal:SIGUSR1 is an alias).
  signal:USR2                Exit on USR2 (signal:SIGUSR2 is an alias).
  file:<path>                Exit when the specified path exists.
Options:
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

	event := args[0]
	var eventCh <-chan os.Signal
	var stopEventMonitor func()
	if strings.HasPrefix(event, "file:") {
		path := strings.TrimPrefix(event, "file:")
		if _, err := os.Stat(path); err == nil {
			return exitOK
		} else if !os.IsNotExist(err) {
			writeDiagnostic(errOut, err)
			return exitArgError
		}
		ignoreSIGPIPE()
		eventCh, stopEventMonitor = installFileMonitor(path)
	} else {
		eventCh, stopEventMonitor = installSignalMonitor(event)
	}
	defer stopEventMonitor()

	copyDone := make(chan error, 1)
	go func() {
		_, err := io.Copy(out, in)
		copyDone <- err
	}()

	select {
	case <-eventCh:
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
		return errors.New("missing event argument")
	}
	if strings.HasPrefix(args[0], "-") {
		return fmt.Errorf("unexpected argument %q", args[0])
	}
	if len(args) > 1 {
		return fmt.Errorf("unexpected argument %q", args[1])
	}

	event := args[0]
	if strings.HasPrefix(event, "file:") {
		if strings.TrimPrefix(event, "file:") == "" {
			return fmt.Errorf("invalid file event %q", event)
		}
		return nil
	}

	if event != "signal:USR1" && event != "signal:SIGUSR1" &&
		event != "signal:USR2" && event != "signal:SIGUSR2" {
		return fmt.Errorf("unsupported event %q", event)
	}
	if !signalEventSupported() {
		return fmt.Errorf("unsupported event %q on this platform", event)
	}

	return nil
}

func writeDiagnostic(errOut io.Writer, err error) {
	_, _ = fmt.Fprintf(errOut, "outage: %v\n", err)
}
