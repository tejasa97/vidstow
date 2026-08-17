#!/usr/bin/env python3
"""Rasterize the VidStow mark for app, docs, and Windows assets.

The sidebar SVG is the geometry source. Legacy CFBundleIconFile .icns
assets are drawn as their alpha silhouette; macOS does not apply the
system squircle mask. The Dock asset is therefore a rounded square on
Apple's 824/1024 icon grid (100px inset on a 1024 canvas) with
transparent padding so it matches neighboring icons.
"""

from __future__ import annotations

from pathlib import Path

from PIL import Image, ImageDraw

REPO = Path(__file__).resolve().parents[1]
BLUE = (47, 111, 237, 255)
WHITE = (255, 255, 255, 255)
VIEWBOX = 32.0
SUPERSAMPLE = 4
# Apple's macOS production icon grid: 824pt shape on a 1024pt canvas.
MAC_ICON_INSET = 100 / 1024


def render_mark(size: int, *, rounded: bool, inset: float = 0.0) -> Image.Image:
    work = size * SUPERSAMPLE
    pad = work * inset
    inner = work - 2 * pad
    scale = inner / VIEWBOX
    image = Image.new("RGBA", (work, work), (0, 0, 0, 0))
    draw = ImageDraw.Draw(image)

    def xy(x: float, y: float) -> tuple[float, float]:
        return (pad + x * scale, pad + y * scale)

    def box(x: float, y: float, w: float, h: float) -> tuple[float, float, float, float]:
        return (
            pad + x * scale,
            pad + y * scale,
            pad + (x + w) * scale,
            pad + (y + h) * scale,
        )

    if rounded:
        draw.rounded_rectangle(box(0, 0, 32, 32), radius=8 * scale, fill=BLUE)
    else:
        draw.rectangle(box(0, 0, 32, 32), fill=BLUE)
    draw.rounded_rectangle(box(14.2, 6.2, 3.6, 9.2), radius=0.85 * scale, fill=WHITE)
    draw.polygon([xy(16, 20.2), xy(9, 13), xy(23, 13)], fill=WHITE)

    stroke = 2.3 * scale
    tray = [
        xy(8.5, 18.75),
        xy(8.5, 23.15),
        xy(10.6, 25.25),
        xy(21.4, 25.25),
        xy(23.5, 23.15),
        xy(23.5, 18.75),
    ]
    draw.line(tray, fill=WHITE, width=max(1, round(stroke)), joint="curve")
    # Round the open ends to match stroke-linecap="round".
    radius = stroke / 2
    for point in (tray[0], tray[-1]):
        x, y = point
        draw.ellipse((x - radius, y - radius, x + radius, y + radius), fill=WHITE)

    if work == size:
        return image
    return image.resize((size, size), Image.Resampling.LANCZOS)


def write_png(path: Path, size: int, *, rounded: bool, inset: float = 0.0) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    render_mark(size, rounded=rounded, inset=inset).save(path, format="PNG")
    print(f"wrote {path.relative_to(REPO)} ({size}x{size})")


def write_ico(path: Path, source: Image.Image) -> None:
    sizes = [(16, 16), (32, 32), (48, 48), (64, 64), (128, 128), (256, 256)]
    path.parent.mkdir(parents=True, exist_ok=True)
    source.save(path, format="ICO", sizes=sizes)
    print(f"wrote {path.relative_to(REPO)}")


def main() -> None:
    appicon = render_mark(1024, rounded=True, inset=MAC_ICON_INSET)
    write_png(REPO / "build" / "appicon.png", 1024, rounded=True, inset=MAC_ICON_INSET)
    write_png(REPO / "docs" / "assets" / "vidstow-app-icon.png", 1024, rounded=True)
    write_png(REPO / "docs" / "assets" / "vidstow-logo.png", 1024, rounded=True)
    write_png(REPO / "frontend" / "src" / "assets" / "images" / "brand-mark.png", 64, rounded=True)
    write_ico(REPO / "build" / "windows" / "icon.ico", appicon)


if __name__ == "__main__":
    main()
