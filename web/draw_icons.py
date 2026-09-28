"""Draws the RentMap app icons (the favicon's pin-and-house mark) as PNGs.

Run from the repo root after changing the mark: python web/draw_icons.py (needs Pillow).
"""
from PIL import Image, ImageDraw

MINT, GRAPHITE = (0xB8, 0xF7, 0xE4, 255), (0x25, 0x27, 0x2C, 255)
SS = 4  # supersampling

def mark(d, s, cx, cy):
    """Pin + house in the favicon's 40-unit geometry, scaled by s around (cx, cy)."""
    P = lambda x, y: (cx + (x - 20) * s, cy + (y - 20) * s)
    c, r = P(20, 17.7), 9.5 * s
    d.ellipse([c[0] - r, c[1] - r, c[0] + r, c[1] + r], fill=GRAPHITE)
    d.polygon([P(13.06, 24.19), P(26.94, 24.19), P(20, 31.6)], fill=GRAPHITE)
    d.polygon([P(15.8, 18.6), P(20, 15), P(24.2, 18.6), P(24.2, 23.8), P(15.8, 23.8)], fill=MINT)

def icon(size, maskable, out):
    n = size * SS
    im = Image.new("RGBA", (n, n), (0, 0, 0, 0))
    d = ImageDraw.Draw(im)
    if maskable:  # full bleed; the mark stays inside the 80 % safe zone
        d.rectangle([0, 0, n, n], fill=MINT)
        mark(d, n / 40 * 0.8, n / 2, n / 2)
    else:
        d.rounded_rectangle([0, 0, n - 1, n - 1], radius=n * 11 / 40, fill=MINT)
        mark(d, n / 40, n / 2, n / 2)
    im.resize((size, size), Image.LANCZOS).save(out, optimize=True)

d = "web/static/img/"
icon(192, False, d + "icon-192.png")
icon(512, False, d + "icon-512.png")
icon(512, True, d + "icon-maskable-512.png")
icon(180, True, d + "apple-touch-icon.png")  # iOS rounds the corners itself
