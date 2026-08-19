#!/usr/bin/env python3
"""Render the annotated screenshots used by README.md and the website.

What it does, in order:
  1. Loads each raw screenshot named in SHOTS.
  2. Crops it to the region worth showing, then scales to a fixed output width.
  3. Draws callout cards joined by leader lines to the pixel they describe.
  4. Writes the result into docs/screenshots/.

WHY THIS EXISTS: the previous annotated screenshots were made by hand in an
image editor. They were accurate for v0.1.0 and then froze there — by v1.1.0
the app had grown Explorer, Containers and an in-app manual that no picture
in the README showed, and the ones it did show had the wrong version in the
title bar. A hand-made asset cannot be re-rendered when the UI moves, so it
rots silently while looking authoritative.

This makes the annotations a build artifact: re-shoot the raw captures, run
this, and every callout lands in the same place at the same size.

Inputs:  raw captures, paths in SHOTS. A missing capture is fatal — silently
         skipping one would publish a README with a stale image still in it.
Outputs: one PNG per entry in docs/screenshots/, plus a summary on stdout.

Notes:
  - A shot may carry a `redact` list. These are real captures of a working
    machine, so a home directory in frame publishes whatever happens to be in
    it. Redaction is a visible bar, never a substituted name: a screenshot that
    quietly shows invented content is worse than one that shows a black box.
  - Coordinates are FRACTIONS of the cropped image, not pixels, so a re-shoot
    at a different resolution keeps every callout attached to its target.
  - Colours come from the app's own palette (see PALETTE); #49c7c0 is the
    accent zxplore already uses for focus and selection.
  - Output is deterministic: fixed order, fixed geometry, no randomness.
"""

from __future__ import annotations

import sys
from pathlib import Path

from PIL import Image, ImageDraw, ImageFont

# ── Palette ───────────────────────────────────────────────────────────────
# Taken from the app's own theme rather than picked fresh, so a screenshot
# annotation never introduces a colour the product does not use.
PALETTE = {
    "accent": (73, 199, 192),      # #49c7c0 — zxplore focus/selection teal
    "card": (11, 18, 32),          # #0b1220 — the app's darkest ground
    "title": (238, 245, 248),
    "sub": (150, 176, 190),
    "shadow": (0, 0, 0),
}

FONT_BOLD = "/usr/share/fonts/abattis-cantarell-fonts/Cantarell-Bold.otf"
FONT_REG = "/usr/share/fonts/abattis-cantarell-fonts/Cantarell-Regular.otf"

SRC = Path.home() / "Pictures" / "Screenshots"
OUT = Path(__file__).resolve().parent / "screenshots"

# ── The shot list ─────────────────────────────────────────────────────────
# Each entry: the raw capture, the crop worth showing (fractions of the raw
# image as left/top/right/bottom), the output width, and the callouts.
#
# `at` is the point being described; `box` is where the card sits. Both are
# fractions of the CROPPED image. Keep cards clear of the text they point at —
# the leader line does the work, not proximity.
SHOTS: list[dict] = [
    {
        "out": "browser-annotated.png",
        "src": "Screenshot From 2026-08-18 21-06-12.png",
        "crop": (0.0, 0.02, 0.86, 1.0),
        "width": 1900,
        "callouts": [
            {
                "t": "It detects the stack",
                "s": "OpenZFS 2.4.3 on kldload 44 — extra tools light up when present",
                "at": (0.115, 0.038),
                "box": (0.36, 0.042),
            },
            {
                "t": "Every pool, one glance",
                "s": "health · capacity · fragmentation · last scrub · errors",
                "at": (0.13, 0.137),
                "box": (0.75, 0.042),
            },
            {
                "t": "Every property, with its source",
                "s": "[local] means someone set it · [default] and [inh:] never guess",
                "at": (0.735, 0.345),
                "box": (0.70, 0.245),
            },
            {
                "t": "Every dataset, sized",
                "s": "used / referenced / snapshot count",
                "at": (0.105, 0.62),
                "box": (0.68, 0.615),
            },
            {
                "t": "Both permission layers",
                "s": "POSIX mode and ACL, plus zfs allow delegations",
                "at": (0.40, 0.885),
                "box": (0.68, 0.745),
            },
            {
                "t": "Snapshots are first-class",
                "s": "roll back · clone · hold, from the same view",
                "at": (0.36, 0.962),
                "box": (0.68, 0.875),
            },
        ],
    },
    {
        "out": "editmode-annotated.png",
        "src": "Screenshot From 2026-08-18 21-06-27.png",
        "crop": (0.13, 0.20, 1.0, 0.78),
        "width": 1900,
        "callouts": [
            {
                "t": "Edit in place",
                "s": "changes apply immediately, as plain zfs set",
                "at": (0.11, 0.045),
                "box": (0.30, 0.135),
            },
            {
                "t": "The right control per property",
                "s": "dropdown or checkbox where the values are fixed, not free text",
                "at": (0.735, 0.245),
                "box": (0.60, 0.115),
            },
            {
                "t": "Grouped by what it affects",
                "s": "capacity · layout & tuning · encryption · mount & sharing",
                "at": (0.30, 0.60),
                "box": (0.27, 0.735),
            },
        ],
    },
    {
        "out": "transfer-annotated.png",
        "src": "Screenshot From 2026-08-18 21-06-49.png",
        "crop": (0.0, 0.10, 1.0, 1.0),
        "width": 1900,
        "callouts": [
            {
                "t": "Two panes, any direction",
                "s": "local ↔ remote, or remote ↔ remote — neither side is special",
                "at": (0.53, 0.135),
                "box": (0.30, 0.055),
            },
            {
                "t": "Stock ssh, nothing installed",
                "s": "the far end needs only OpenZFS and an sshd",
                "at": (0.39, 0.435),
                "box": (0.17, 0.60),
            },
            {
                "t": "Test before you trust it",
                "s": "checks reachability and the key before any send runs",
                "at": (0.515, 0.685),
                "box": (0.755, 0.60),
            },
        ],
    },
    {
        "out": "explorer-annotated.png",
        "src": "Screenshot From 2026-08-18 21-07-18.png",
        "crop": (0.0, 0.185, 1.0, 1.0),
        "width": 1900,
        # A real home directory is in frame. These bars cover working
        # directory names that are nobody's business in a public README.
        "redact": [
            (0.010, 0.185, 0.170, 0.209),
            (0.010, 0.641, 0.170, 0.735),
        ],
        "callouts": [
            {
                "t": "One path, every snapshot",
                "s": "each copy with its size and mtime, flagged when it differs",
                "at": (0.72, 0.28),
                "box": (0.70, 0.115),
            },
            {
                "t": "Browse the live tree",
                "s": "or step inside any snapshot — the .zfs directory, without the path",
                "at": (0.12, 0.42),
                "box": (0.235, 0.575),
            },
            {
                "t": "In 55 of 60 snapshots",
                "s": "the answer to “when did this file change?”, without a single zfs command",
                "at": (0.10, 0.925),
                "box": (0.33, 0.845),
            },
            {
                "t": "Restore in place, or alongside",
                "s": "overwrite live, or land it as name.from-SNAPSHOT and compare",
                "at": (0.60, 0.905),
                "box": (0.755, 0.80),
            },
        ],
    },
    {
        "out": "containers-annotated.png",
        "src": "Screenshot From 2026-08-18 21-07-30.png",
        "crop": (0.0, 0.163, 1.0, 1.0),
        "width": 1900,
        "callouts": [
            {
                "t": "It tells you the truth about the driver",
                "s": "overlay here — so layers are ordinary files, not datasets, and it says so",
                "at": (0.17, 0.058),
                "box": (0.26, 0.235),
            },
            {
                "t": "Images with real sizes",
                "s": "what is actually on disk, per tag",
                "at": (0.56, 0.155),
                "box": (0.26, 0.415),
            },
            {
                "t": "Snapshot the whole estate",
                "s": "one ZFS snapshot across the container root — roll back or replicate it",
                "at": (0.09, 0.963),
                "box": (0.30, 0.805),
            },
        ],
    },
    {
        "out": "manual-annotated.png",
        "src": "Screenshot From 2026-08-18 21-07-46.png",
        "crop": (0.30, 0.03, 0.72, 0.82),
        "width": 1500,
        "callouts": [
            {
                "t": "The man page ships inside the app",
                "s": "zxplore(1) — the same text as man zxplore, one key away",
                "at": (0.34, 0.055),
                "box": (0.60, 0.145),
            },
            {
                "t": "Nothing is hidden, nothing invented",
                "s": "every action maps to a plain zfs, zpool or ssh command",
                "at": (0.30, 0.325),
                "box": (0.44, 0.44),
            },
        ],
    },
]


def _font(path: str, size: int) -> ImageFont.FreeTypeFont:
    """Load a font, failing loudly — a silent bitmap fallback looks broken."""
    try:
        return ImageFont.truetype(path, size)
    except OSError as exc:  # pragma: no cover - environment problem
        raise SystemExit(f"annotate: cannot load font {path}: {exc}") from exc


def _card_box(
    draw: ImageDraw.ImageDraw,
    call: dict,
    size: tuple[int, int],
    fonts: tuple[ImageFont.FreeTypeFont, ImageFont.FreeTypeFont],
) -> tuple[int, int, int, int]:
    """Return the pixel rect of one callout card, centred on its `box` point."""
    bold, reg = fonts
    pad_x, pad_y, gap = 22, 16, 6

    tw = draw.textlength(call["t"], font=bold)
    sw = draw.textlength(call["s"], font=reg) if call["s"] else 0
    th = bold.size + gap + (reg.size if call["s"] else -gap)

    w = int(max(tw, sw)) + pad_x * 2
    h = int(th) + pad_y * 2
    cx, cy = call["box"][0] * size[0], call["box"][1] * size[1]

    # Keep the card fully on canvas; a clipped card reads as a rendering bug.
    x0 = min(max(int(cx - w / 2), 8), size[0] - w - 8)
    y0 = min(max(int(cy - h / 2), 8), size[1] - h - 8)
    return x0, y0, x0 + w, y0 + h


def _edge_point(rect: tuple[int, int, int, int], target: tuple[float, float]) -> tuple[int, int]:
    """Point on the card's border nearest the thing it points at.

    Anchoring the leader to the border rather than the centre keeps the line
    from crossing the card's own text.
    """
    x0, y0, x1, y1 = rect
    cx, cy = (x0 + x1) / 2, (y0 + y1) / 2
    tx, ty = target
    dx, dy = tx - cx, ty - cy
    if dx == 0 and dy == 0:
        return int(cx), int(cy)
    # Scale the direction vector until it hits the rectangle's edge.
    sx = (x1 - cx) / dx if dx else float("inf")
    sy = (y1 - cy) / dy if dy else float("inf")
    s = min(abs(sx), abs(sy))
    return int(cx + dx * s), int(cy + dy * s)


def render(shot: dict) -> Path:
    """Render one annotated screenshot and return where it was written."""
    src = SRC / shot["src"]
    if not src.exists():
        raise SystemExit(f"annotate: missing capture {src}")

    img = Image.open(src).convert("RGB")
    lf, tf, rf, bf = shot["crop"]
    img = img.crop(
        (int(lf * img.width), int(tf * img.height), int(rf * img.width), int(bf * img.height))
    )
    w = shot["width"]
    img = img.resize((w, int(img.height * w / img.width)), Image.LANCZOS)

    size = img.size
    scale = w / 1900
    bold = _font(FONT_BOLD, int(25 * scale))
    reg = _font(FONT_REG, int(19 * scale))

    # Cards and leaders go on an RGBA overlay so the card can sit at 95%
    # opacity — enough to read over dense terminal text without hiding it.
    layer = Image.new("RGBA", size, (0, 0, 0, 0))
    draw = ImageDraw.Draw(layer)

    # Redactions go down BEFORE the callouts, so a leader line may cross one
    # and stay visible.
    for x0f, y0f, x1f, y1f in shot.get("redact", []):
        box = [x0f * size[0], y0f * size[1], x1f * size[0], y1f * size[1]]
        draw.rectangle(box, fill=(24, 28, 38, 255))
        draw.rectangle(box, outline=PALETTE["accent"] + (90,), width=1)
        label = "redacted"
        lw = draw.textlength(label, font=reg)
        if lw < (box[2] - box[0]) - 8:
            draw.text((box[0] + 8, box[1] + (box[3] - box[1] - reg.size) / 2),
                      label, font=reg, fill=(120, 140, 155, 255))

    for call in shot["callouts"]:
        rect = _card_box(draw, call, size, (bold, reg))
        tgt = (call["at"][0] * size[0], call["at"][1] * size[1])
        edge = _edge_point(rect, tgt)

        draw.line([edge, (int(tgt[0]), int(tgt[1]))], fill=PALETTE["accent"] + (235,), width=2)
        # A filled dot inside a ring: reads at a glance against both the dark
        # ground and the light selection bars.
        for r, fill, outline in (
            (10, None, PALETTE["accent"] + (235,)),
            (4, PALETTE["accent"] + (255,), None),
        ):
            draw.ellipse(
                [tgt[0] - r, tgt[1] - r, tgt[0] + r, tgt[1] + r],
                fill=fill, outline=outline, width=2,
            )

        x0, y0, x1, y1 = rect
        draw.rounded_rectangle([x0 + 3, y0 + 4, x1 + 3, y1 + 4], 12,
                               fill=PALETTE["shadow"] + (90,))
        draw.rounded_rectangle(rect, 12, fill=PALETTE["card"] + (242,),
                               outline=PALETTE["accent"] + (255,), width=2)
        draw.text((x0 + 22, y0 + 16), call["t"], font=bold, fill=PALETTE["title"])
        if call["s"]:
            draw.text((x0 + 22, y0 + 16 + bold.size + 6), call["s"], font=reg,
                      fill=PALETTE["sub"])

    out = OUT / shot["out"]
    out.parent.mkdir(parents=True, exist_ok=True)
    Image.alpha_composite(img.convert("RGBA"), layer).convert("RGB").save(out)
    return out


def main() -> int:
    wanted = set(sys.argv[1:])
    written = 0
    for shot in SHOTS:
        if wanted and shot["out"] not in wanted:
            continue
        path = render(shot)
        img = Image.open(path)
        print(f"annotate: {path.name}  {img.width}x{img.height}  "
              f"{len(shot['callouts'])} callouts")
        written += 1
    if not written:
        raise SystemExit("annotate: nothing matched")
    return 0


if __name__ == "__main__":
    sys.exit(main())
