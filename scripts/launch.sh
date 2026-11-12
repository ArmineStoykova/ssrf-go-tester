#!/usr/bin/env bash
set -euo pipefail

APP_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
URL="http://127.0.0.1:8766"
cd "$APP_DIR"

# Serialize rapid launcher clicks to avoid starting two backends.
exec 9>"${XDG_RUNTIME_DIR:-/tmp}/ssrf-go-tester-launch-${UID}.lock"
flock -w 15 9 || { echo "Could not acquire launcher lock." >&2; exit 1; }

is_ready() {
  curl --noproxy '*' --silent --fail --max-time 2 "$URL/api/interactsh/control" |
    grep -q '"server"'
}

if is_ready; then
  xdg-open "$URL" >/dev/null 2>&1
  exit 0
fi

# Never launch over an occupied port belonging to another service.
if (echo > /dev/tcp/127.0.0.1/8766) >/dev/null 2>&1; then
  echo "Port 8766 is occupied by another service." >&2
  exit 1
fi

if [[ ! -x ./ssrf-go-tester ]]; then
  echo "Build first: go build -o ssrf-go-tester ." >&2
  exit 1
fi

mkdir -p "${XDG_STATE_HOME:-$HOME/.local/state}"
nohup ./ssrf-go-tester </dev/null >>"${XDG_STATE_HOME:-$HOME/.local/state}/ssrf-go-tester.log" 2>&1 &
for i in {1..40}; do
  if is_ready; then
    xdg-open "$URL" >/dev/null 2>&1
    exit 0
  fi
  sleep 0.25
done

echo "SSRF Go Tester did not become ready; check the backend log." >&2
exit 1
