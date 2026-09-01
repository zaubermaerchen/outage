# outage

## Usage

```text
outage --event signal:USR1
outage --event signal:SIGUSR1
```

The `signal:SIGUSR1` event name is an alias for `signal:USR1`. When the event is
received, outage exits; it does not send a signal directly to the producer. Signal
events are unsupported on Windows.

Use standalone `-h` or `--help` for identical help output. Help tokens take priority
wherever they appear in the argument list. Use standalone `--version` to print the version.

## Release builds

Inject the release version at build time with Go's linker flags:

```sh
go build -ldflags "-X main.version=v0.1.0" ./cmd/outage
```
