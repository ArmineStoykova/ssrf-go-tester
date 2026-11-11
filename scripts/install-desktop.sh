#!/usr/bin/env bash
set -euo pipefail
APP_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
LAUNCHER="$HOME/.local/share/applications/SSRF-Go-Tester.desktop"
mkdir -p "$(dirname "$LAUNCHER")"
# Generate an absolute launcher path for this machine; don't commit local paths.
python3 - "$APP_DIR" "$LAUNCHER" <<'PY'
import pathlib, shlex, sys
app_dir, destination = sys.argv[1:]
text = pathlib.Path(app_dir, "SSRF-Go-Tester.desktop").read_text()
command = shlex.quote(str(pathlib.Path(app_dir, "scripts/launch.sh")))
text = "\n".join("Exec=/usr/bin/env bash " + command if line.startswith("Exec=") else line for line in text.splitlines()) + "\n"
pathlib.Path(destination).write_text(text)
PY
chmod +x "$APP_DIR/scripts/launch.sh"
echo "Installed $LAUNCHER"
echo "Launch from your Ubuntu application menu: SSRF Go Tester"
