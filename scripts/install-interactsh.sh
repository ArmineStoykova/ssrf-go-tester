#!/usr/bin/env bash
set -euo pipefail
if ! command -v go >/dev/null 2>&1; then
  echo "Go is required. Install Go 1.22+ first." >&2
  exit 1
fi
echo "Installing Interactsh tools to Go bin directory..."
go install github.com/projectdiscovery/interactsh/cmd/interactsh-server@latest
go install github.com/projectdiscovery/interactsh/cmd/interactsh-client@latest
echo "Done. Executables are normally in $(go env GOPATH)/bin"
echo "Use INTERACTSH_BIN_DIR to override their location."
