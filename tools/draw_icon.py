"""Draws the Vector icon and logo: the robot from the front, its screen showing green eyes.

Usage: python draw_icon.py <out.png> icon <px> | logo <height>
"""
import sys
from PIL import Image, ImageDraw, ImageFont

OUT = sys.argv[1]
N = 1024  # drawn large, then scaled down (antialiasing)

def robot(size=N):
    im = Image.new("RGBA", (size, size), (0, 0, 0, 0))
    d = ImageDraw.Draw(im)
    s = size / 1024
    R = lambda *v: [int(x * s) for x in v]
    # tracks
    d.rounded_rectangle(R(110, 640, 330, 900), radius=int(60 * s), fill=(38, 40, 44))
    d.rounded_rectangle(R(694, 640, 914, 900), radius=int(60 * s), fill=(38, 40, 44))
    for x0 in (110, 694):
        for i in range(5):
            y = 670 + i * 46
            d.rounded_rectangle(R(x0 + 20, y, x0 + 200, y + 24), radius=int(10 * s), fill=(62, 65, 70))
    # body
    d.rounded_rectangle(R(200, 560, 824, 870), radius=int(70 * s), fill=(28, 29, 32))
    d.rounded_rectangle(R(200, 560, 824, 600), radius=int(20 * s), fill=(55, 57, 62))
    # lift arms on both sides of the head
    d.rounded_rectangle(R(130, 250, 210, 640), radius=int(36 * s), fill=(70, 73, 79))
    d.rounded_rectangle(R(814, 250, 894, 640), radius=int(36 * s), fill=(70, 73, 79))
    # head
    d.rounded_rectangle(R(190, 150, 834, 600), radius=int(110 * s), fill=(22, 23, 26))
    d.rounded_rectangle(R(190, 150, 834, 600), radius=int(110 * s), outline=(80, 83, 90), width=int(14 * s))
    # screen
    d.rounded_rectangle(R(270, 230, 754, 520), radius=int(60 * s), fill=(0, 0, 0))
    # eyes (green rounded rectangles with a slight glow)
    glow = (0, 255, 170, 70)
    for x0 in (330, 574):
        d.rounded_rectangle(R(x0 - 14, 286, x0 + 134, 474), radius=int(56 * s), fill=glow)
        d.rounded_rectangle(R(x0, 300, x0 + 120, 460), radius=int(48 * s), fill=(28, 230, 160))
    return im

def icon(px):
    return robot().resize((px, px), Image.LANCZOS)

def logo(height, text="Vector"):
    font = None
    for path in ("/System/Library/Fonts/SFNSRounded.ttf", "/System/Library/Fonts/Supplemental/Arial Rounded Bold.ttf", "/System/Library/Fonts/Helvetica.ttc"):
        try:
            font = ImageFont.truetype(path, int(height * 0.62)); break
        except OSError:
            pass
    ic = icon(height)
    tmp = ImageDraw.Draw(Image.new("RGBA", (1, 1)))
    w = tmp.textbbox((0, 0), text, font=font)[2]
    im = Image.new("RGBA", (height + int(height * 0.15) + w + 4, height), (0, 0, 0, 0))
    im.paste(ic, (0, 0), ic)
    d = ImageDraw.Draw(im)
    bbox = d.textbbox((0, 0), text, font=font)
    y = (height - (bbox[3] - bbox[1])) // 2 - bbox[1]
    d.text((height + int(height * 0.15), y), text, font=font, fill=(28, 200, 145))
    return im

kind = sys.argv[2]
if kind == "icon":
    icon(int(sys.argv[3])).save(OUT)
else:
    logo(int(sys.argv[3])).save(OUT)
