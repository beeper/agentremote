#!/usr/bin/env python3
"""Download models.dev provider SVGs and render sharp 512px PNGs."""

from __future__ import annotations

import base64
import json
import pathlib
import re
import shutil
import subprocess
import sys
import tempfile
import threading
import urllib.request
from http import HTTPStatus
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from urllib.parse import unquote


API_URL = "https://models.dev/api.json"
LOGO_BASE_URL = "https://models.dev/logos"
CANVAS_SIZE = 512
LOGO_SIZE = 410
SUPERSAMPLE = 4
SVG_ATTR_RE = re.compile(r"([:\w.-]+)\s*=\s*(['\"])(.*?)\2", re.DOTALL)


def fetch_bytes(url: str) -> bytes:
    request = urllib.request.Request(
        url,
        headers={
            "Accept": "*/*",
            "User-Agent": "ai-bridge-models-dev-logo-generator/1.0",
        },
    )
    with urllib.request.urlopen(request, timeout=30) as response:
        return response.read()


def parse_number(value: str | None) -> float | None:
    if not value:
        return None
    match = re.search(r"[-+]?(?:\d*\.\d+|\d+)", value)
    return float(match.group(0)) if match else None


def split_svg_document(svg: str) -> tuple[dict[str, str], str]:
    cleaned = re.sub(r"<\?xml[^>]*\?>", "", svg, flags=re.IGNORECASE)
    cleaned = re.sub(r"<!doctype[^>]*>", "", cleaned, flags=re.IGNORECASE)
    match = re.search(r"<svg\b([^>]*)>(.*)</svg>\s*$", cleaned, re.IGNORECASE | re.DOTALL)
    if not match:
        raise ValueError("could not parse SVG root")
    attrs = {name: value for name, _quote, value in SVG_ATTR_RE.findall(match.group(1))}
    return attrs, match.group(2)


def get_view_box(attrs: dict[str, str]) -> tuple[float, float, float, float]:
    view_box = attrs.get("viewBox") or attrs.get("viewbox")
    if view_box:
        parts = [float(part) for part in re.split(r"[\s,]+", view_box.strip()) if part]
        if len(parts) == 4 and parts[2] > 0 and parts[3] > 0:
            return parts[0], parts[1], parts[2], parts[3]

    width = parse_number(attrs.get("width"))
    height = parse_number(attrs.get("height"))
    if width and height and width > 0 and height > 0:
        return 0.0, 0.0, width, height

    return 0.0, 0.0, float(CANVAS_SIZE), float(CANVAS_SIZE)


def escape_attr(value: str) -> str:
    return (
        value.replace("&", "&amp;")
        .replace('"', "&quot;")
        .replace("<", "&lt;")
        .replace(">", "&gt;")
    )


def group_attrs(attrs: dict[str, str]) -> str:
    skip = {
        "height",
        "preserveAspectRatio",
        "version",
        "viewBox",
        "viewbox",
        "width",
        "x",
        "xmlns",
        "xmlns:xlink",
        "y",
    }
    rendered = []
    for name, value in attrs.items():
        if name not in skip:
            rendered.append(f'{name}="{escape_attr(value)}"')
    return (" " + " ".join(rendered)) if rendered else ""


def normalize_svg(svg: str) -> str:
    processed = re.sub(r"\bcurrentColor\b", "black", svg, flags=re.IGNORECASE)
    processed = re.sub(
        r"\b(fill|stroke)=(['\"])inherit\2",
        lambda match: f"{match.group(1)}={match.group(2)}black{match.group(2)}",
        processed,
        flags=re.IGNORECASE,
    )
    processed = re.sub(
        r"\b(fill|stroke)\s*:\s*inherit\b",
        lambda match: f"{match.group(1)}: black",
        processed,
        flags=re.IGNORECASE,
    )

    attrs, inner_svg = split_svg_document(processed)
    min_x, min_y, view_width, view_height = get_view_box(attrs)
    scale = min(LOGO_SIZE / view_width, LOGO_SIZE / view_height)
    translate_x = (CANVAS_SIZE - view_width * scale) / 2 - min_x * scale
    translate_y = (CANVAS_SIZE - view_height * scale) / 2 - min_y * scale
    render_size = CANVAS_SIZE * SUPERSAMPLE

    return (
        '<svg xmlns="http://www.w3.org/2000/svg" '
        'xmlns:xlink="http://www.w3.org/1999/xlink" '
        f'width="{render_size}" height="{render_size}" '
        f'viewBox="0 0 {CANVAS_SIZE} {CANVAS_SIZE}" color="black">'
        f'<g transform="translate({translate_x:g} {translate_y:g}) scale({scale:g})"'
        f"{group_attrs(attrs)}>"
        f"{inner_svg}"
        "</g></svg>"
    )


def chrome_binary() -> str | None:
    candidates = [
        "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
        "/Applications/Chromium.app/Contents/MacOS/Chromium",
        shutil.which("google-chrome"),
        shutil.which("chromium"),
        shutil.which("chromium-browser"),
    ]
    return next((candidate for candidate in candidates if candidate and pathlib.Path(candidate).exists()), None)


def renderer_html(provider_ids: list[str]) -> str:
    ids_json = json.dumps(provider_ids)
    return f"""<!doctype html>
<meta charset="utf-8">
<title>models.dev provider logo renderer</title>
<body>starting</body>
<script>
const ids = {ids_json};
const canvasSize = {CANVAS_SIZE};
const renderSize = {CANVAS_SIZE * SUPERSAMPLE};

function loadImage(url) {{
  return new Promise((resolve, reject) => {{
    const img = new Image();
    img.onload = () => resolve(img);
    img.onerror = () => reject(new Error("failed to load " + url));
    img.src = url;
  }});
}}

async function renderOne(id) {{
  const img = await loadImage("/normalized/" + encodeURIComponent(id) + ".svg");
  const high = document.createElement("canvas");
  high.width = renderSize;
  high.height = renderSize;
  const highCtx = high.getContext("2d", {{ alpha: true }});
  highCtx.clearRect(0, 0, renderSize, renderSize);
  highCtx.imageSmoothingEnabled = true;
  highCtx.imageSmoothingQuality = "high";
  highCtx.drawImage(img, 0, 0, renderSize, renderSize);

  const canvas = document.createElement("canvas");
  canvas.width = canvasSize;
  canvas.height = canvasSize;
  const ctx = canvas.getContext("2d", {{ alpha: true }});
  ctx.clearRect(0, 0, canvasSize, canvasSize);
  ctx.imageSmoothingEnabled = true;
  ctx.imageSmoothingQuality = "high";
  ctx.drawImage(high, 0, 0, canvasSize, canvasSize);

  function alphaBBox(sourceCtx) {{
    const image = sourceCtx.getImageData(0, 0, canvasSize, canvasSize);
    let minX = canvasSize, minY = canvasSize, maxX = -1, maxY = -1;
    for (let y = 0; y < canvasSize; y++) {{
      for (let x = 0; x < canvasSize; x++) {{
        const alpha = image.data[(y * canvasSize + x) * 4 + 3];
        if (alpha > 4) {{
          minX = Math.min(minX, x);
          minY = Math.min(minY, y);
          maxX = Math.max(maxX, x);
          maxY = Math.max(maxY, y);
        }}
      }}
    }}
    return maxX >= 0 ? [minX, minY, maxX + 1, maxY + 1] : null;
  }}

  let bbox = alphaBBox(ctx);
  if (bbox) {{
    const width = bbox[2] - bbox[0];
    const height = bbox[3] - bbox[1];
    const scale = Math.min({LOGO_SIZE} / width, {LOGO_SIZE} / height);
    const output = document.createElement("canvas");
    output.width = canvasSize;
    output.height = canvasSize;
    const outCtx = output.getContext("2d", {{ alpha: true }});
    outCtx.clearRect(0, 0, canvasSize, canvasSize);
    outCtx.imageSmoothingEnabled = true;
    outCtx.imageSmoothingQuality = "high";
    const destWidth = Math.round(width * scale);
    const destHeight = Math.round(height * scale);
    const destX = Math.round((canvasSize - destWidth) / 2);
    const destY = Math.round((canvasSize - destHeight) / 2);
    outCtx.drawImage(
      high,
      bbox[0] * {SUPERSAMPLE},
      bbox[1] * {SUPERSAMPLE},
      width * {SUPERSAMPLE},
      height * {SUPERSAMPLE},
      destX,
      destY,
      destWidth,
      destHeight,
    );
    canvas.width = canvasSize;
    canvas.height = canvasSize;
    ctx.clearRect(0, 0, canvasSize, canvasSize);
    ctx.drawImage(output, 0, 0);
    bbox = alphaBBox(ctx);
  }}
  const dataUrl = canvas.toDataURL("image/png");
  const saved = await fetch("/save/" + encodeURIComponent(id) + ".png", {{
    method: "POST",
    body: dataUrl,
  }});
  if (!saved.ok) throw new Error("save failed for " + id + ": " + await saved.text());
  return {{ id, bbox }};
}}

(async () => {{
  const results = [];
  const failures = [];
  for (const id of ids) {{
    document.body.textContent = JSON.stringify({{
      done: false,
      current: id,
      count: results.length,
      failures,
    }});
    try {{
      results.push(await renderOne(id));
    }} catch (error) {{
      failures.push({{ id, error: String(error && error.message || error) }});
    }}
  }}
  const done = {{ done: true, count: results.length, failures, results }};
  document.body.textContent = JSON.stringify(done);
  await fetch("/done", {{ method: "POST", body: JSON.stringify(done) }});
}})();
</script>
"""


class LogoRenderHandler(BaseHTTPRequestHandler):
    provider_ids: set[str]
    svg_dir: pathlib.Path
    png_dir: pathlib.Path
    html: str
    done_event: threading.Event
    result: dict[str, object] | None

    def log_message(self, _format: str, *_args: object) -> None:
        return

    def send_content(self, status: HTTPStatus, content_type: str, body: str | bytes) -> None:
        payload = body.encode("utf-8") if isinstance(body, str) else body
        self.send_response(status)
        self.send_header("Content-Type", content_type)
        self.send_header("Content-Length", str(len(payload)))
        self.send_header("Cache-Control", "no-store")
        self.end_headers()
        self.wfile.write(payload)

    def do_GET(self) -> None:
        if self.path == "/":
            self.send_content(HTTPStatus.OK, "text/html; charset=utf-8", self.html)
            return

        if self.path.startswith("/normalized/") and self.path.endswith(".svg"):
            provider_id = unquote(self.path[len("/normalized/") : -len(".svg")])
            if provider_id not in self.provider_ids:
                self.send_content(HTTPStatus.NOT_FOUND, "text/plain", "unknown provider")
                return
            svg = (self.svg_dir / f"{provider_id}.svg").read_text(encoding="utf-8")
            self.send_content(HTTPStatus.OK, "image/svg+xml; charset=utf-8", normalize_svg(svg))
            return

        self.send_content(HTTPStatus.NOT_FOUND, "text/plain", "not found")

    def do_POST(self) -> None:
        if self.path == "/done":
            length = int(self.headers.get("Content-Length", "0"))
            body = self.rfile.read(length).decode("utf-8")
            type(self).result = json.loads(body)
            type(self).done_event.set()
            self.send_content(HTTPStatus.OK, "text/plain", "ok")
            return

        if not self.path.startswith("/save/") or not self.path.endswith(".png"):
            self.send_content(HTTPStatus.NOT_FOUND, "text/plain", "not found")
            return

        provider_id = unquote(self.path[len("/save/") : -len(".png")])
        if provider_id not in self.provider_ids:
            self.send_content(HTTPStatus.NOT_FOUND, "text/plain", "unknown provider")
            return

        length = int(self.headers.get("Content-Length", "0"))
        body = self.rfile.read(length).decode("utf-8")
        png_data = re.sub(r"^data:image/png;base64,", "", body)
        (self.png_dir / f"{provider_id}.png").write_bytes(base64.b64decode(png_data))
        self.send_content(HTTPStatus.OK, "text/plain", "ok")


def render_pngs_with_chrome(
    provider_ids: list[str],
    svg_dir: pathlib.Path,
    png_dir: pathlib.Path,
    chrome: str,
    temp_root: pathlib.Path,
) -> dict[str, object]:
    LogoRenderHandler.provider_ids = set(provider_ids)
    LogoRenderHandler.svg_dir = svg_dir
    LogoRenderHandler.png_dir = png_dir
    LogoRenderHandler.html = renderer_html(provider_ids)
    LogoRenderHandler.done_event = threading.Event()
    LogoRenderHandler.result = None

    server = ThreadingHTTPServer(("127.0.0.1", 0), LogoRenderHandler)
    thread = threading.Thread(target=server.serve_forever, daemon=True)
    thread.start()
    url = f"http://127.0.0.1:{server.server_port}/"
    profile_dir = temp_root / "chrome-profile"

    process = subprocess.Popen(
        [
            chrome,
            "--headless=new",
            "--disable-background-networking",
            "--disable-gpu",
            "--hide-scrollbars",
            "--no-first-run",
            f"--user-data-dir={profile_dir}",
            url,
        ],
        stderr=subprocess.PIPE,
        stdout=subprocess.DEVNULL,
        text=True,
    )
    try:
        if not LogoRenderHandler.done_event.wait(timeout=180):
            stderr = ""
            if process.poll() is not None and process.stderr:
                stderr = process.stderr.read()
            raise RuntimeError(f"Chrome renderer timed out. {stderr}".strip())
        parsed = LogoRenderHandler.result or {}
    finally:
        if process.poll() is None:
            process.terminate()
            try:
                process.wait(timeout=10)
            except subprocess.TimeoutExpired:
                process.kill()
                process.wait(timeout=10)
        server.shutdown()
        server.server_close()

    if parsed.get("failures"):
        raise RuntimeError(json.dumps(parsed["failures"], indent=2))
    return parsed


def main() -> int:
    repo_root = pathlib.Path(__file__).resolve().parents[1]
    output_root = repo_root / "assets" / "models-dev-providers"
    svg_dir = output_root / "svg"
    png_dir = output_root / "png"
    svg_dir.mkdir(parents=True, exist_ok=True)
    png_dir.mkdir(parents=True, exist_ok=True)

    chrome = chrome_binary()
    if not chrome:
        print("Headless Chrome or Chromium is required for sharp SVG rendering.", file=sys.stderr)
        return 1

    providers = json.loads(fetch_bytes(API_URL).decode("utf-8"))
    manifest = []
    failures = []

    for provider_id, provider in sorted(providers.items()):
        name = provider.get("name", provider_id)
        logo_url = f"{LOGO_BASE_URL}/{provider_id}.svg"
        svg_path = svg_dir / f"{provider_id}.svg"
        try:
            svg_bytes = fetch_bytes(logo_url)
            if b"<svg" not in svg_bytes[:512].lower():
                raise ValueError("response did not look like SVG")
            svg_path.write_bytes(svg_bytes)
        except (OSError, ValueError) as exc:
            failures.append({"id": provider_id, "name": name, "error": str(exc)})
            continue

        manifest.append(
            {
                "id": provider_id,
                "name": name,
                "source_svg_url": logo_url,
                "svg": str(svg_path.relative_to(repo_root)),
                "png": str((png_dir / f"{provider_id}.png").relative_to(repo_root)),
                "canvas_size": CANVAS_SIZE,
                "max_logo_size": LOGO_SIZE,
                "padding_percent": 10,
                "supersample": SUPERSAMPLE,
            }
        )

    if not failures:
        with tempfile.TemporaryDirectory(prefix="models-dev-logos-") as temp_dir:
            try:
                render_pngs_with_chrome(
                    [provider["id"] for provider in manifest],
                    svg_dir,
                    png_dir,
                    chrome,
                    pathlib.Path(temp_dir),
                )
            except RuntimeError as exc:
                failures.append({"id": "chrome-render", "name": "Chrome render", "error": str(exc)})

    (output_root / "manifest.json").write_text(
        json.dumps(
            {
                "source": API_URL,
                "provider_count": len(providers),
                "generated_count": len(manifest) if not failures else 0,
                "failed_count": len(failures),
                "canvas_size": CANVAS_SIZE,
                "max_logo_size": LOGO_SIZE,
                "padding_percent": 10,
                "supersample": SUPERSAMPLE,
                "renderer": "headless-chrome",
                "providers": manifest if not failures else [],
                "failures": failures,
            },
            indent=2,
            sort_keys=True,
        )
        + "\n",
        encoding="utf-8",
    )

    print(f"providers: {len(providers)}")
    print(f"generated: {len(manifest) if not failures else 0}")
    print(f"failed: {len(failures)}")
    if failures:
        for failure in failures:
            print(f"failed {failure['id']}: {failure['error']}", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
