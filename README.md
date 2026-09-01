# outage

## Release builds

Inject the release version at build time with Go's linker flags:

```sh
go build -ldflags "-X main.version=v0.1.0" ./cmd/outage
```
