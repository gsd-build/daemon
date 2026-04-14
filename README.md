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

## Operator Diagnostics Boundary

The daemon exposes a small remote surface on purpose:

- relay heartbeats and online/offline state
- local socket status and active-session metadata
- task output that the user already asked the daemon to produce

What it does **not** do today:

- no remote raw daemon-log streaming for operators
- no background support bundle upload
- no automatic upload of recent local logs during failures

If an incident requires daemon log inspection, the supported path is still local access on the user's machine via `gsd-cloud logs` or the log file under `~/.gsd-cloud/`.

The only bounded upload path in the daemon today is explicit task-related image upload to the relay. That path is not a general-purpose diagnostics channel and should not be treated as one during incident response.

## Build from source

```bash
go build -o gsd-cloud .
./gsd-cloud version
```

## License

MIT — see [LICENSE](./LICENSE).
