# outage

> Convert external events into broken pipes.

`outage` forwards a byte stream until an external event tells it to exit. Put it
between a producer and consumer when the middle process should stop promptly on
USR1 and let the resulting pipe closure propagate naturally upstream.

## Usage

```sh
producer | outage --event signal:USR1 | consumer
```

`signal:SIGUSR1` is an equivalent, case-sensitive alias:

```sh
producer | outage --event signal:SIGUSR1 | consumer
```

Normal forwarding:

```text
producer ──bytes──> outage ──same bytes──> consumer
```

After `outage` receives USR1:

```text
producer ──bytes──> [closed pipe]   outage exits 0   consumer observes EOF
```

During normal operation, `outage` forwards stdin to stdout byte-for-byte. EOF,
including empty input, completes normally after all data already read has been
forwarded.

On USR1, `outage` itself exits with status 0 without waiting for EOF or draining
the remaining stdin. Delivery of data still in flight is not guaranteed.
Diagnostics are written to stderr. Stdin or stdout I/O errors exit with status
1; invalid normal-operation arguments exit with status 2.

## Guarantee boundary

`outage` does not discover the producer or send it SIGPIPE or any other signal.
Exiting closes the stdin pipe endpoint held by `outage`. A later upstream write
may result in SIGPIPE, EPIPE, or producer-specific behavior, depending on the OS
and the producer. Stopping the producer itself is not guaranteed.

## Command-line interface

- Normal operation requires exactly one `--event` option. v0.1.0 supports
  `signal:USR1` and its case-sensitive `signal:SIGUSR1` alias.
- `-h` and `--help` display help. A help token has highest priority wherever it
  appears in the argument list, even alongside invalid arguments or `--version`.
- Standalone `--version` prints the version.

## Platform support

USR1 events are supported on the Unix systems covered by the implementation,
including Linux and macOS. Windows builds support help and version output, but
reject signal events as unsupported and provide no alternative event.

## Release builds

Inject the release version at build time with Go's linker flags:

```sh
go build -ldflags "-X main.version=v0.1.0" ./cmd/outage
```
