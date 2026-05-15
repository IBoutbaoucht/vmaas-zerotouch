#!/usr/bin/env bash
#
# record-demo.sh -- record an *automated* end-to-end VMaaS demo.
#
# The script:
#   1) spins up an ffmpeg screen recording (x11grab)
#   2) runs scripts/_demo_drive.py which uses Playwright to drive Chromium:
#        - opens http://localhost:8081/
#        - injects the API token into localStorage
#        - clicks Provision, waits for status=ready, captures the IP
#        - SSHes into the new VM in an xterm window
#        - clicks Delete, accepts the confirm() dialog
#   3) encodes both an MP4 (lossless-ish, faststart) and an optimized GIF
#      (two-pass palette) ready to embed in the README.
#
# Usage:
#   docker compose up -d                  # backend + frontend must be running
#   ./scripts/record-demo.sh              # one-time first run creates a venv
#
# Tunables (env):
#   NAME=demo            basename for the output files
#   OUTDIR=docs          where to write demo.mp4 / demo.gif
#   GIF_WIDTH=1280       max width of the GIF (height auto)
#   FPS=15               GIF framerate
#   DURATION_MAX=180     hard cap on recording length, in seconds
#   SSH_KEY=~/.ssh/vmaas-lab   private key for the SSH step
#
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

NAME="${NAME:-demo}"
OUTDIR="${OUTDIR:-docs}"
FPS="${FPS:-15}"
GIF_WIDTH="${GIF_WIDTH:-1280}"
DURATION_MAX="${DURATION_MAX:-180}"
VENV="$ROOT/scripts/.demo-venv"

mkdir -p "$OUTDIR"
MP4="$OUTDIR/$NAME.mp4"
GIF="$OUTDIR/$NAME.gif"
PALETTE="$(mktemp --tmpdir vmaas-palette.XXXXXX.png)"
trap 'rm -f "$PALETTE"' EXIT

for tool in ffmpeg xterm xdotool xrandr python3; do
    command -v "$tool" >/dev/null || {
        echo "missing: $tool -- sudo apt-get install -y $tool" >&2
        exit 1
    }
done

if [[ -z "${VMAAS_TOKEN:-}" && -f .env ]]; then
    VMAAS_TOKEN="$(awk -F= '/^VMAAS_TOKEN=/{print $2}' .env)"
fi
if [[ -z "${VMAAS_TOKEN:-}" ]]; then
    echo "VMAAS_TOKEN not set -- either export it or put it in .env" >&2
    exit 1
fi
export VMAAS_TOKEN

if ! curl -fsS http://localhost:8081/healthz >/dev/null; then
    echo "VMaaS UI not reachable at http://localhost:8081/healthz" >&2
    echo "Start the stack first: docker compose up -d" >&2
    exit 1
fi

if [[ ! -d "$VENV" ]]; then
    echo "first-run: creating venv at $VENV (one-time, ~200 MB)..."
    python3 -m venv "$VENV"
    "$VENV/bin/pip" install --quiet --upgrade pip
    "$VENV/bin/pip" install --quiet playwright
    "$VENV/bin/python" -m playwright install chromium
fi

# Crop the recording to the Chromium window that _demo_drive.py launches at
# --window-size=1700,950 --window-position=100,40. This means only the browser
# is captured -- no taskbar, no other windows on the workspace.
# H.264 requires even dimensions; 1700x950 is fine.
CROP_W="${CROP_W:-1700}"
CROP_H="${CROP_H:-950}"
CROP_X="${CROP_X:-100}"
CROP_Y="${CROP_Y:-40}"

cat <<EOF

VMaaS automated demo recorder
  capture area  : ${CROP_W}x${CROP_H} at +${CROP_X},+${CROP_Y} (browser window only)
  output        : $MP4 / $GIF
  max duration  : ${DURATION_MAX}s (stops as soon as the driver finishes)
  driver        : scripts/_demo_drive.py (Playwright)

EOF
PREROLL="${PREROLL:-3}"
if [[ -t 0 ]]; then
    read -r -p "Press Enter to start (${PREROLL}s countdown)... " _
else
    echo "  non-interactive run; starting in ${PREROLL}s..."
fi
for ((i=PREROLL; i>0; i--)); do echo "  $i..."; sleep 1; done
echo "  starting driver (ffmpeg starts after the page is loaded)"

# The driver owns ffmpeg so that recording starts only *after* the UI is
# painted and stops *before* the browser window closes -- otherwise the
# fixed crop rectangle would capture whatever sits behind Chromium at the
# start and end of the run.
set +e
DEMO_MP4="$MP4" \
DEMO_DISPLAY="${DISPLAY:-:0.0}" \
DEMO_CROP_W="$CROP_W" DEMO_CROP_H="$CROP_H" \
DEMO_CROP_X="$CROP_X" DEMO_CROP_Y="$CROP_Y" \
timeout --foreground "$DURATION_MAX" "$VENV/bin/python" scripts/_demo_drive.py
DRIVER_RC=$?
set -e

if [[ ! -s "$MP4" ]]; then
    echo "Recording produced no output." >&2
    exit 1
fi

echo "  encoding GIF (two-pass palette)..."
ffmpeg -y -hide_banner -loglevel error -i "$MP4" \
    -vf "fps=${FPS},scale=${GIF_WIDTH}:-1:flags=lanczos,palettegen=max_colors=192" \
    "$PALETTE"
ffmpeg -y -hide_banner -loglevel error -i "$MP4" -i "$PALETTE" \
    -filter_complex "fps=${FPS},scale=${GIF_WIDTH}:-1:flags=lanczos[v];[v][1:v]paletteuse=dither=sierra2_4a" \
    "$GIF"

MP4_SIZE="$(du -h "$MP4" | cut -f1)"
GIF_SIZE="$(du -h "$GIF" | cut -f1)"

cat <<EOF

  done.
    $MP4  ($MP4_SIZE)
    $GIF  ($GIF_SIZE)

  Embed in README.md:
    ![VMaaS demo]($OUTDIR/$NAME.gif)

EOF

if (( DRIVER_RC != 0 )); then
    echo "  WARN: driver exited rc=$DRIVER_RC -- the recording was still saved."
    exit "$DRIVER_RC"
fi
