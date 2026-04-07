# GSD Cloud daemon

The Go binary that runs on user machines and connects to GSD Cloud. Open source so you can audit exactly what's running on your machine before installing it.

## Install

```bash
curl -fsSL https://install.gsd.build | sh
```

Then:

```bash
gsd-cloud login
gsd-cloud start
```

## What it does

- Maintains a persistent websocket connection to the GSD Cloud relay
- Manages local Claude Code sessions on your behalf
- Streams session output back to the cloud for cross-device access
- Stores session state in a local write-ahead log (`~/.gsd-cloud/`)

## Build from source

```bash
go build ./...
./gsd-cloud version
```

## License

MIT — see [LICENSE](./LICENSE).
