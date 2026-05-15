"""
VMaaS demo driver: drives the UI through one full provision -> ssh -> delete
cycle, and owns the ffmpeg screen-recording so that the recording starts only
*after* the page is loaded and stops *before* the browser closes.

Reads:
    VMAAS_TOKEN     bearer token (required) -- injected into localStorage
    VMAAS_UI        UI URL (default http://localhost:8081/)
    SSH_KEY         private key path (default ~/.ssh/vmaas-lab)
    SSH_USER        SSH login user (default cuneyt)
    DEMO_VM_NAME    VM name to provision (default demo-<rand>)
    READY_TIMEOUT_S max wait for status=ready (default 150)

Recording (controlled by the shell wrapper):
    DEMO_MP4        path to write the .mp4 to (e.g. docs/demo.mp4)
    DEMO_DISPLAY    X display to grab (e.g. :0.0)
    DEMO_CROP_W     crop width  (default 1700)
    DEMO_CROP_H     crop height (default 950)
    DEMO_CROP_X     crop x offset (default 100)
    DEMO_CROP_Y     crop y offset (default 40)
    DEMO_FPS        capture framerate (default 30)
"""
import os
import signal
import subprocess
import sys
import time
from pathlib import Path

from playwright.sync_api import sync_playwright

UI_URL = os.environ.get("VMAAS_UI", "http://localhost:8081/")
TOKEN = os.environ.get("VMAAS_TOKEN")
if not TOKEN:
    sys.exit("VMAAS_TOKEN env var is required")
SSH_KEY = Path(os.environ.get("SSH_KEY", str(Path.home() / ".ssh/vmaas-lab")))
SSH_USER = os.environ.get("SSH_USER", "cuneyt")
VM_NAME = os.environ.get("DEMO_VM_NAME", f"demo-{int(time.time()) % 10000}")
READY_TIMEOUT_S = int(os.environ.get("READY_TIMEOUT_S", "150"))

MP4_PATH = os.environ.get("DEMO_MP4")  # if unset, no recording is done
REC_DISPLAY = os.environ.get("DEMO_DISPLAY", os.environ.get("DISPLAY", ":0.0"))
REC_W = int(os.environ.get("DEMO_CROP_W", "1700"))
REC_H = int(os.environ.get("DEMO_CROP_H", "950"))
REC_X = int(os.environ.get("DEMO_CROP_X", "100"))
REC_Y = int(os.environ.get("DEMO_CROP_Y", "40"))
REC_FPS = int(os.environ.get("DEMO_FPS", "30"))


def log(msg):
    print(f"  {msg}", flush=True)


def pause(s, msg=None):
    if msg:
        log(f"[{msg}]")
    time.sleep(s)


def wait_for_ready(page, name, timeout_s):
    """Poll the table for `name`; return its IP once status=ready."""
    end = time.time() + timeout_s
    last = None
    while time.time() < end:
        row = page.locator("#vm-tbody tr", has_text=name).first
        if row.count() == 0:
            time.sleep(1)
            continue
        try:
            status = row.locator("td").nth(1).inner_text().strip()
            ip = row.locator("td").nth(2).inner_text().strip()
        except Exception:
            time.sleep(1)
            continue
        if status != last:
            log(f"   status={status!r} ip={ip!r}")
            last = status
        if status == "ready" and ip and ip != "—":
            return ip
        if status == "failed":
            raise SystemExit("VM provisioning failed -- check the backend logs")
        time.sleep(2)
    raise SystemExit(f"timed out waiting for {name} to become ready")


def start_recording():
    """Launch ffmpeg as a subprocess; return its Popen handle (or None)."""
    if not MP4_PATH:
        log("recording disabled (DEMO_MP4 not set)")
        return None
    Path(MP4_PATH).parent.mkdir(parents=True, exist_ok=True)
    cmd = [
        "ffmpeg", "-y", "-hide_banner", "-loglevel", "error",
        "-video_size", f"{REC_W}x{REC_H}",
        "-framerate", str(REC_FPS),
        "-f", "x11grab",
        "-i", f"{REC_DISPLAY}+{REC_X},{REC_Y}",
        "-c:v", "libx264", "-preset", "veryfast", "-pix_fmt", "yuv420p",
        "-movflags", "+faststart",
        MP4_PATH,
    ]
    log(f"recording -> {MP4_PATH} ({REC_W}x{REC_H} @ +{REC_X},{REC_Y})")
    p = subprocess.Popen(cmd, stdin=subprocess.DEVNULL)
    time.sleep(1.2)
    if p.poll() is not None:
        sys.exit(f"ffmpeg exited rc={p.returncode} at startup")
    return p


def stop_recording(p):
    if p is None:
        return
    log("stopping recording...")
    try:
        p.send_signal(signal.SIGINT)
        p.wait(timeout=10)
    except subprocess.TimeoutExpired:
        log("ffmpeg did not exit on SIGINT; killing")
        p.kill()
        p.wait(timeout=5)


def ssh_in_xterm(ip):
    geom = os.environ.get("XTERM_GEOMETRY", "110x28+780+490")
    # Show enough that a viewer can see: identity, kernel, network, cloud-init
    # result, and the sentinel log written by our userdata. Use small `sleep`
    # pauses between sections so the viewer's eye can follow.
    remote = (
        'echo "=== identity ==="; whoami; hostname; sleep 1; '
        'echo; echo "=== kernel ==="; uname -srm; sleep 1; '
        'echo; echo "=== network ==="; ip -4 -br addr show; sleep 1; '
        'echo; echo "=== cloud-init result ==="; '
        'cat /run/cloud-init/result.json 2>/dev/null | head -c 300 || echo "(no result.json yet)"; '
        'echo; sleep 1; '
        'echo; echo "=== vmaas sentinel log ==="; '
        'cat /var/log/vmaas-sentinel.log 2>/dev/null || echo "(no sentinel yet)"'
    )
    inner = (
        f'echo "$ ssh {SSH_USER}@{ip}"; echo; '
        f"ssh -i {SSH_KEY} -o StrictHostKeyChecking=no "
        f"-o UserKnownHostsFile=/dev/null -o ConnectTimeout=10 "
        f"{SSH_USER}@{ip} {remote!r}; "
        'echo; echo "-- end of SSH session --"; sleep 8'
    )
    log(f"opening xterm at geometry {geom}")
    subprocess.run(
        [
            "xterm",
            "-fa", "Monospace", "-fs", "11",
            "-geometry", geom,
            "-bg", "#0f172a", "-fg", "#cbd5e1",
            "-T", f"ssh {SSH_USER}@{ip}",
            "-e", "bash", "-c", inner,
        ],
        check=False,
    )


def main():
    with sync_playwright() as p:
        browser = p.chromium.launch(
            headless=False,
            args=[
                "--window-size=1700,950",
                "--window-position=100,40",
                "--disable-blink-features=AutomationControlled",
            ],
        )
        ctx = browser.new_context(viewport={"width": 1700, "height": 950})
        ctx.add_init_script(
            f"window.localStorage.setItem('vmaas_token', {TOKEN!r});"
        )

        page = ctx.new_page()
        page.on("dialog", lambda d: d.accept())

        log(f"-> {UI_URL}")
        page.goto(UI_URL)
        page.wait_for_selector("#vm-tbody", timeout=10_000)
        # Give Chromium one extra tick so the window is fully painted before
        # ffmpeg begins capturing -- otherwise the first frames may catch the
        # blank window background.
        pause(1, "UI loaded; about to start recording")

        ffmpeg = start_recording()
        try:
            pause(2, "recording -- showing the empty pool")

            page.fill("#new-name", VM_NAME)
            pause(1, f"typed name '{VM_NAME}'")
            page.click("#new-form button[type=submit]")
            log("-> clicked Provision")

            ip = wait_for_ready(page, VM_NAME, READY_TIMEOUT_S)
            log(f"-> VM ready at {ip}")
            pause(2, "let viewer see the 'ready' pill")

            ssh_in_xterm(ip)
            pause(1, "back to browser")

            page.bring_to_front()
            row = page.locator("#vm-tbody tr", has_text=VM_NAME).first
            row.locator(".del-btn").click()
            log("-> clicked Delete (dialog auto-accepted)")

            end = time.time() + 30
            while time.time() < end:
                if page.locator("#vm-tbody tr", has_text=VM_NAME).count() == 0:
                    break
                time.sleep(1)
            pause(2, "row gone, pool slot freed")
        finally:
            stop_recording(ffmpeg)

        browser.close()
        log("done.")


if __name__ == "__main__":
    main()
