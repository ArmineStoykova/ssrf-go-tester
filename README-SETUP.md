# Ubuntu launcher and Interactsh setup

This branch is for review. It does **not** change your existing Desktop icon or running process automatically.

## Requirements

- Ubuntu with bash, curl, xdg-open, python3 and Go 1.22+
- Interactsh Server and Client available on PATH, in Go's bin directory, or via `INTERACTSH_BIN_DIR`

To install Interactsh explicitly (downloads ProjectDiscovery dependencies), run `bash scripts/install-interactsh.sh`. Review this script before running it. This project never installs tools automatically when opening the desktop launcher.

## Build and install launcher

```bash
go build -o ssrf-go-tester .
bash scripts/install-desktop.sh
```

The installer generates a machine-specific `.desktop` file in `~/.local/share/applications/`. The committed `SSRF-Go-Tester.desktop` is a reviewable example; the installer replaces its example path with the actual checkout directory.

## Behavior

- First click starts the Go backend and opens the browser after a readiness check.
- Later clicks reopen the browser without starting a second backend.
- Closing the browser does **not** stop the backend; this is intentional.
- Closing the terminal running the backend may stop the application.
- If port 8766 belongs to another application, the launcher refuses to start.

Interactsh Server and Client are controlled by the buttons in the browser, not by the desktop launcher.

## Limitations

The Go backend resolves Interactsh from `INTERACTSH_BIN_DIR`, `PATH`, `GOBIN`, `GOPATH/bin` or `~/go/bin`. The local Interactsh `oob.test` service is not a public OOB endpoint.

## Interactsh session troubleshooting

Only one local Interactsh server can own the configured ports at a time. Before pressing **Start Interactsh**, stop any Interactsh server/client launched from an older checkout. The application checks local TCP listener ports and displays a conflict instead of silently attaching to another checkout.

Each new session clears the previous `interactsh-payload.txt` file. After the client starts, the backend waits briefly for registration and payload generation. If no payload appears, the status and Generate OOB URL controls show a diagnostic message. Do not copy a payload from another checkout or assume a running process has registered successfully.

After pulling a new version, rebuild with `go build -o ssrf-go-tester .` and restart the Go backend; reloading the browser alone does not load new Go code.
