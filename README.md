# outage

> Convert external events into broken pipes.

`outage` forwards a byte stream until an external event tells it to exit. Put it
between a producer and consumer when the middle process should stop promptly on
USR1 or USR2 and let the resulting pipe closure propagate naturally upstream.

## Usage

```sh
producer | outage signal:USR1 | consumer
```

`signal:SIGUSR1` is an equivalent, case-sensitive alias:

```sh
producer | outage signal:SIGUSR1 | consumer
```

USR2 is also supported, with `signal:SIGUSR2` as its equivalent, case-sensitive
alias:

```sh
producer | outage signal:USR2 | consumer
producer | outage signal:SIGUSR2 | consumer
```

Multiple conditions can be combined in one positional event specification with
the exact separator ` && `:

```sh
producer | outage 'signal:USR1 && file:/tmp/foo' | consumer
```

Every condition must occur before `outage` exits. Once a condition occurs it is
latched for the lifetime of the process, so the conditions do not need to occur
simultaneously. The separator is the literal ASCII space-ampersand-ampersand-
space sequence; operands are not trimmed. Quote the complete expression so the
shell passes it as one positional argument. An expression containing a leading,
trailing, or consecutive separator is invalid. Paths containing the exact
separator cannot be represented, and only AND combinations are supported (not
OR, parentheses, negation, or other expression syntax).

To exit when a path exists, use a positional file event:

```sh
producer | outage file:/tmp/foo | consumer
```

The file event triggers immediately when `/tmp/foo` already exists. Otherwise,
`outage` polls for the path while continuing to forward stdin to stdout. Any
existing path, including a directory, triggers the event; the path after the
first `file:` prefix is passed through unchanged.

To exit after a duration, use a positional duration event:

```sh
producer | outage duration:30s | consumer
```

The value follows Go's duration syntax, such as `500ms` or `1m30s`, and is
measured from the start of the `outage` process. A zero duration exits
immediately without reading stdin. While a positive duration is pending,
`outage` forwards stdin to stdout; when it elapses, `outage` exits without
waiting for EOF or draining the remaining input. Empty, malformed, and
negative duration values are argument errors.

To exit at a local wall-clock time, use a positional datetime event:

```sh
producer | outage datetime:2026-09-03T18:00 | consumer
```

The accepted timezone-less forms are `YYYY-MM-DDTHH:MM` and
`YYYY-MM-DDTHH:MM:SS`; omitted seconds mean `00`. RFC3339 forms with an
explicit numeric offset or `Z` are also accepted, but seconds are required, for
example `YYYY-MM-DDTHH:MM:SS+09:00` or `YYYY-MM-DDTHH:MM:SSZ`. Timezone-less
values are interpreted in the local timezone captured when `outage` starts;
explicit timezone values identify their absolute instant directly. Datetimes
are monitored immediately, without waiting for stdin. A datetime that has
already been reached exits without reading stdin. Local times skipped by a
daylight-saving transition are invalid; when a local time occurs twice, the
earlier absolute occurrence is selected. IANA timezone names, fractional
seconds, and other formats are invalid. While a future datetime is pending,
stdin is forwarded to stdout; when it is reached, `outage` exits without waiting
for EOF or draining remaining input.

Normal forwarding:

```text
producer ──bytes──> outage ──same bytes──> consumer
```

After `outage` receives USR1:

```text
producer ──later write──> [outage stdin endpoint closed]
                         outage exits 0
consumer <──EOF───────── [outage stdout endpoint closed]
```

During normal operation, `outage` forwards stdin to stdout byte-for-byte. EOF,
including empty input, completes normally after all data already read has been
forwarded.

On USR1, `outage` itself exits with status 0 without waiting for EOF or draining
the remaining stdin. Delivery of data still in flight is not guaranteed.
USR2 has the same termination semantics.
Diagnostics are written to stderr. Stdin or stdout I/O errors exit with status
1; invalid normal-operation arguments exit with status 2.

## Guarantee boundary

`outage` does not discover the producer or send it SIGPIPE or any other signal.
Exiting closes the stdin pipe endpoint held by `outage`. A later upstream write
may result in SIGPIPE, EPIPE, or producer-specific behavior, depending on the OS
and the producer. Stopping the producer itself is not guaranteed.

## Command-line interface

- Normal operation requires exactly one positional event specification. The CLI
  supports `signal:USR1` and `signal:USR2`, plus their case-sensitive
  `signal:SIGUSR1` and `signal:SIGUSR2` aliases. `file:<path>` exits when the
  path exists, waiting and forwarding stdin to stdout if it is not present yet.
  The path may be relative or absolute and may contain colons. `duration:<value>`
  exits after the Go duration value elapses, measured from process start.
  `datetime:YYYY-MM-DDTHH:MM[:SS]` exits when the local wall clock reaches the
  specified time, using the local timezone captured at startup. RFC3339 values
  with seconds and an explicit numeric offset or `Z` are also supported. Any
  of these conditions may be combined with the exact ` && ` separator; all
  conditions are latched independently and must be satisfied before exit.
- `-h` and `--help` display help. A help token has highest priority wherever it
  appears in the argument list, even alongside invalid arguments or `--version`.
- Standalone `--version` prints the version.

## Platform support

USR1 and USR2 events are supported on the Unix systems covered by the
implementation, including Linux and macOS. Windows builds support help and
version output, portable polling for `file:<path>` events, and portable
`duration:<value>` and datetime events on all target platforms. Signal events
remain unsupported on Windows.

## Release builds

Inject the release version at build time with Go's linker flags:

```sh
go build -ldflags "-X main.version=v0.2.1" ./cmd/outage
```
