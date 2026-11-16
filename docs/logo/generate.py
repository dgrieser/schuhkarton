#!/usr/bin/env python3
"""Builds the Schuhkarton logo and its colour variants.

    python3 docs/logo/generate.py

writes docs/logo/svg/*.svg, docs/logo/png/*.png (512 and 1024 px, transparent),
docs/logo/jpg/*.jpg (1024 px on white) and the icons the UI uses in web/
(web/logo/NAME.svg, NAME.ico, NAME-180.png for each variant; ui.logo in the
settings picks one) and internal/config/logos.go, the list of the names. Rendering needs Playwright with Chromium and Pillow.

Every variant starts from the original colours: each colour is moved in
OKLCH (hue turned, chroma scaled, lightness shifted), so the shading of the
original stays and only the mood changes.
"""
import io
import math
import os
import pathlib
import subprocess
import sys
import tempfile

HERE = pathlib.Path(__file__).resolve().parent
WEB = HERE.parent.parent / "web"

# the original drawing; {name} are the colours below
TEMPLATE = """<svg xmlns="http://www.w3.org/2000/svg" viewBox="260 155 720 720" role="img" aria-label="Schuhkarton logo">
  <defs>
    <linearGradient id="sk-lidTop" x1="0" y1="0" x2="0.9" y2="1"><stop stop-color="{lidTop0}"/><stop offset="1" stop-color="{lidTop1}"/></linearGradient>
    <linearGradient id="sk-lidInside" x1="0" y1="1" x2="1" y2="0"><stop stop-color="{lidInside0}"/><stop offset="1" stop-color="{lidInside1}"/></linearGradient>
    <linearGradient id="sk-rim" x1="0" y1="0" x2="0.7" y2="1"><stop stop-color="{rim0}"/><stop offset="1" stop-color="{rim1}"/></linearGradient>
    <linearGradient id="sk-box" x1="0" y1="0" x2="1" y2="1"><stop stop-color="{box0}"/><stop offset="0.5" stop-color="{box1}"/><stop offset="1" stop-color="{box2}"/></linearGradient>
    <linearGradient id="sk-slot" x1="0" y1="0" x2="0.8" y2="1"><stop stop-color="{slot0}"/><stop offset="1" stop-color="{slot1}"/></linearGradient>
    <linearGradient id="sk-sky" x1="0" y1="1" x2="1" y2="0"><stop stop-color="{sky0}"/><stop offset="1" stop-color="{sky1}"/></linearGradient>
    <linearGradient id="sk-mountains" x1="0" y1="0" x2="1" y2="1"><stop stop-color="{mountains0}"/><stop offset="1" stop-color="{mountains1}"/></linearGradient>
    <linearGradient id="sk-heartBg" x1="0" y1="0" x2="1" y2="1"><stop stop-color="{heartBg0}"/><stop offset="1" stop-color="{heartBg1}"/></linearGradient>
    <clipPath id="sk-scene"><rect x="429" y="405" width="238" height="260" rx="19"/></clipPath>
  </defs>
  <!-- open lid -->
  <path fill="url(#sk-lidTop)" d="M306 479C286 451 298 401 322 367C339 341 363 321 401 307L672 222C719 207 740 207 760 220C781 236 795 265 784 294C773 326 745 359 694 392L408 493C357 510 326 515 306 479Z"/>
  <path fill="url(#sk-lidInside)" d="M324 459C339 420 393 400 463 381L722 307C755 297 773 289 783 299C776 331 740 361 694 392L406 493C361 511 338 507 324 484C320 476 320 468 324 459Z"/>
  <!-- box interior -->
  <path fill="url(#sk-rim)" d="M341 567C341 540 374 526 415 526H831C872 526 905 542 905 571C905 598 870 626 823 626H423C376 626 341 599 341 567Z"/>
  <path fill="{interior}" d="M368 566C372 548 399 544 432 544H833C858 544 880 555 880 569C880 583 862 592 835 593H406C381 592 368 581 368 566Z"/>
  <!-- heart photo -->
  <g transform="rotate(12 771 515)">
    <rect x="682" y="421" width="180" height="255" rx="29" fill="{paper0}"/>
    <rect x="707" y="448" width="133" height="208" rx="17" fill="url(#sk-heartBg)"/>
    <path fill="{heart}" d="M774 510C760 492 735 498 735 522C735 544 755 556 774 570C793 556 815 541 815 520C815 497 790 493 774 510Z"/>
  </g>
  <!-- landscape photo -->
  <g transform="rotate(-12 551 507)">
    <rect x="412" y="393" width="281" height="307" rx="30" fill="{paper1}"/>
    <g clip-path="url(#sk-scene)">
      <rect x="429" y="405" width="238" height="260" rx="19" fill="url(#sk-sky)"/>
      <circle cx="611" cy="463" r="30" fill="{sun}"/>
      <path fill="url(#sk-mountains)" d="M429 560C460 548 476 510 509 505C540 503 570 546 598 559C626 540 638 527 654 530C681 537 696 564 712 572L712 678H429Z"/>
      <path fill="{shade}" opacity="0.5" d="M570 561C604 568 619 532 651 533C678 535 699 564 712 576V678H570Z"/>
    </g>
  </g>
  <!-- box front -->
  <path fill="url(#sk-box)" d="M341 572C356 592 388 603 419 603H827C862 603 891 589 905 572V718C905 777 865 819 807 819H440C382 819 341 777 341 719Z"/>
  <!-- top rim -->
  <path fill="url(#sk-rim)" d="M341 568C341 583 357 592 383 592H855C881 591 896 582 905 568V581C905 607 865 626 827 626H420C382 626 341 605 341 581Z"/>
  <!-- front handle -->
  <rect x="530" y="667" width="185" height="55" rx="28" fill="url(#sk-slot)"/>
  <!-- sparkle -->
  <path fill="{sparkle}" d="M854 273C858 273 859 292 867 303C876 315 900 317 900 322C900 328 878 330 868 344C860 356 858 371 854 371C850 371 848 354 839 343C829 330 809 326 809 322C809 318 830 316 840 303C849 291 849 273 854 273Z"/>
  <path d="M896 371L927 355M896 412L926 424" fill="none" stroke="{rays}" stroke-width="19" stroke-linecap="round"/>
</svg>
"""

ORIGINAL = {
    "lidTop0": "#8df6eb", "lidTop1": "#9bf9ee",
    "lidInside0": "#7591ed", "lidInside1": "#8b9bf8",
    "rim0": "#8dd1f0", "rim1": "#7abaf0",
    "box0": "#b3f1fa", "box1": "#a5edf7", "box2": "#c1f3f7",
    "slot0": "#078e9e", "slot1": "#0798a9",
    "sky0": "#48dcca", "sky1": "#5bdccc",
    "mountains0": "#329be8", "mountains1": "#568eef",
    "heartBg0": "#c4a6f2", "heartBg1": "#b78ae9",
    "interior": "#708eec", "shade": "#558cea",
    "paper0": "#fffafa", "paper1": "#fffdfb", "heart": "#fffcf8",
    "sun": "#ffef96", "sparkle": "#a4ffce", "rays": "#9fbfff",
}

# colours that keep their hue: the photos in the box (paper, sky, sun,
# mountains, heart) stay recognisable, only the box changes
FIXED_HUE = {"paper0", "paper1", "heart", "sun", "sky0", "sky1", "mountains0", "mountains1", "shade", "heartBg0", "heartBg1"}

# the inside of the lid and of the box and its rim: their own turn, so a warm box does
# not get a green inside
INNER = {"lidInside0", "lidInside1", "interior", "rim0", "rim1"}

# name: (title, hue turn in degrees, chroma factor, lightness shift[, turn of the inside])
VARIANTS = {
    "aqua":     ("Aqua (original)", 0, 1.0, 0.0),
    "peach":    ("Pfirsich", -130, 0.8, 0.01, 110),
    "rose":     ("Rosé", 175, 0.75, 0.01, 55),
    "lavender": ("Lavendel", 110, 0.8, 0.0, 30),
    "mint":     ("Minze", -30, 0.8, 0.01),
    "lemon":    ("Zitrone", -85, 0.8, 0.02),
    "sky":      ("Himmel", 45, 0.85, 0.0),
    "sage":     ("Salbei (gedeckt)", -50, 0.35, -0.04),
    "slate":    ("Schiefer (gedeckt)", 50, 0.3, -0.06),
    "clay":     ("Ton (gedeckt)", -150, 0.4, -0.05, 100),
    "mauve":    ("Malve (gedeckt)", 140, 0.35, -0.05, 55),
    "mono":     ("Schwarzweiß", 0, 0.0, -0.06),
}


# --- OKLCH -------------------------------------------------------------------

def _lin(c):
    return c / 12.92 if c <= 0.04045 else ((c + 0.055) / 1.055) ** 2.4


def _gam(c):
    c = max(0.0, min(1.0, c))
    return 12.92 * c if c <= 0.0031308 else 1.055 * c ** (1 / 2.4) - 0.055


def hex_to_oklch(h):
    r, g, b = (_lin(int(h[i:i + 2], 16) / 255) for i in (1, 3, 5))
    l = (0.4122214708 * r + 0.5363325363 * g + 0.0514459929 * b) ** (1 / 3)
    m = (0.2119034982 * r + 0.6806995451 * g + 0.1073969566 * b) ** (1 / 3)
    s = (0.0883024619 * r + 0.2817188376 * g + 0.6299787005 * b) ** (1 / 3)
    L = 0.2104542553 * l + 0.7936177850 * m - 0.0040720468 * s
    A = 1.9779984951 * l - 2.4285922050 * m + 0.4505937099 * s
    B = 0.0259040371 * l + 0.7827717662 * m - 0.8086757660 * s
    return L, math.hypot(A, B), math.degrees(math.atan2(B, A))


def oklch_to_rgb(L, C, H):
    A, B = C * math.cos(math.radians(H)), C * math.sin(math.radians(H))
    l = (L + 0.3963377774 * A + 0.2158037573 * B) ** 3
    m = (L - 0.1055613458 * A - 0.0638541728 * B) ** 3
    s = (L - 0.0894841775 * A - 1.2914855480 * B) ** 3
    return (4.0767416621 * l - 3.3077115913 * m + 0.2309699292 * s,
            -1.2684380046 * l + 2.6097574011 * m - 0.3413193965 * s,
            -0.0041960863 * l - 0.7034186147 * m + 1.7076147010 * s)


def oklch_to_hex(L, C, H):
    # lower the chroma until the colour fits into sRGB
    while C > 0 and any(v < -1e-4 or v > 1 + 1e-4 for v in oklch_to_rgb(L, C, H)):
        C -= 0.002
    return "#" + "".join(f"{round(_gam(v) * 255):02x}" for v in oklch_to_rgb(L, max(C, 0), H))


def palette(turn, chroma, light, inner=None):
    out = {}
    for k, h in ORIGINAL.items():
        if (turn, chroma, light, inner) == (0, 1.0, 0.0, None):
            out[k] = h
            continue
        L, C, H = hex_to_oklch(h)
        if k in INNER and inner is not None:
            H += inner
        elif k not in FIXED_HUE:
            H += turn
        out[k] = oklch_to_hex(min(0.99, max(0.0, L + light)), C * chroma, H)
    return out


def svg(name):
    return TEMPLATE.format(**palette(*VARIANTS[name][1:]))


# --- rendering ----------------------------------------------------------------

RENDER_JS = r"""
const { chromium } = require('playwright');
const fs = require('fs');
(async () => {
  const jobs = JSON.parse(fs.readFileSync(process.argv[2], 'utf8'));
  const browser = await chromium.launch();
  const page = await browser.newPage({ deviceScaleFactor: 1 });
  for (const j of jobs) {
    await page.setViewportSize({ width: j.size, height: j.size });
    const src = 'data:image/svg+xml;base64,' + Buffer.from(fs.readFileSync(j.svg)).toString('base64');
    await page.setContent(`<html><body style="margin:0;background:${j.bg || 'transparent'}">` +
      `<img src="${src}" style="display:block;width:${j.size}px;height:${j.size}px;padding:${j.pad || 0}px;box-sizing:border-box"></body></html>`);
    await page.waitForFunction(() => document.images[0].complete);
    await page.screenshot({ path: j.out, omitBackground: !j.bg });
  }
  await browser.close();
})();
"""


def render(jobs):
    import json
    with tempfile.TemporaryDirectory() as tmp:
        js, spec = os.path.join(tmp, "render.js"), os.path.join(tmp, "jobs.json")
        pathlib.Path(js).write_text(RENDER_JS)
        pathlib.Path(spec).write_text(json.dumps([{k: str(v) if isinstance(v, pathlib.Path) else v for k, v in j.items()} for j in jobs]))
        env = dict(os.environ, NODE_PATH=subprocess.run(["npm", "root", "-g"], capture_output=True, text=True).stdout.strip())
        subprocess.run(["node", js, spec], check=True, env=env)


def main():
    from PIL import Image

    for d in ("svg", "png", "jpg"):
        (HERE / d).mkdir(exist_ok=True)
    jobs = []
    for name in VARIANTS:
        p = HERE / "svg" / f"schuhkarton-{name}.svg"
        p.write_text(svg(name))
        for size in (512, 1024):
            jobs.append({"svg": p, "size": size, "out": HERE / "png" / f"schuhkarton-{name}-{size}.png"})
        jobs.append({"svg": p, "size": 1024, "bg": "#ffffff", "pad": 64, "out": HERE / "jpg" / f"schuhkarton-{name}.png"})

    # the UI: web/logo/NAME.svg (logo and favicon), NAME.ico, NAME-180.png
    # (apple-touch-icon, on white); ui.logo in the settings picks one
    (HERE / "logo.svg").write_text(svg("aqua"))
    ui = WEB / "logo"
    ui.mkdir(exist_ok=True)
    for name in VARIANTS:
        p = ui / f"{name}.svg"
        p.write_text(svg(name))
        jobs += [
            {"svg": p, "size": 180, "bg": "#ffffff", "pad": 14, "out": ui / f"{name}-180.png"},
            {"svg": p, "size": 64, "out": ui / f"_{name}-64.png"},
        ]
    render(jobs)

    # JPEG: no transparency, so the PNG rendered on white becomes the JPEG
    for name in VARIANTS:
        src = HERE / "jpg" / f"schuhkarton-{name}.png"
        Image.open(src).convert("RGB").save(HERE / "jpg" / f"schuhkarton-{name}.jpg", quality=92, optimize=True)
        src.unlink()
        src = ui / f"_{name}-64.png"
        Image.open(src).convert("RGBA").save(ui / f"{name}.ico", sizes=[(16, 16), (32, 32), (48, 48), (64, 64)])
        src.unlink()
    # the names the server accepts for ui.logo
    names = ", ".join(f'"{n}"' for n in VARIANTS)
    go = WEB.parent / "internal" / "config" / "logos.go"
    go.write_text("// Code generated by docs/logo/generate.py. DO NOT EDIT.\n\npackage config\n\n"
                  "// Logos are the colour variants of the logo in web/logo (ui.logo); the first is the default.\n"
                  f"var Logos = []string{{{names}}}\n")
    for name, (title, *_) in VARIANTS.items():
        print(f"{name:9} {title}")


if __name__ == "__main__":
    sys.exit(main())
