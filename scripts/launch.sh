#!/usr/bin/env bash
set -euo pipefail
APP_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
URL="http://127.0.0.1:8766"
cd "$APP_DIR"
# The /api/interactsh/control endpoint belongs to this application.
if curl --noproxy '*' --silent --fail --max-time 2 "$URL/api/interactsh/control" | grep -q '"server"'; then
  xdg-open "$URL" >/dev/null 2>&1 || true
  exit 0
fi
# Don't start a second copy if another service is using the port.
if (echo > /dev/tcp/127.0.0.1/8766) >/dev/null 2>&1; then
  echo "Port 8766 is occupied by another process." >&2
  exit 1
fi
if [[ ! -x ./ssrf-go-tester ]]; then
  echo "Build first: go build -o ssrf-go-tester ." >&2
  exit 1
fi
./ssrf-go-tester &
pid=$!
for i in {1..40}; do
  if curl --noproxy '*' --silent --fail --max-time 1 "$URL/api/interactsh/control" | grep -q '"server"'; then
    xdg-open "$URL" >/dev/null 2>&1 || true
    wait "$pid"
    exit $?
  fi
  if ! kill -0 "$pid" 2>/dev/null; then
    wait "$pid"
    exit $?
  fi
  sleep 0.25
done
echo "SSRF Go Tester did not become ready." >&2
exit 1
