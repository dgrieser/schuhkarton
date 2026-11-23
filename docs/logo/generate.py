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
import json
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
    <clipPath id="sk-inbox"><rect x="260" y="155" width="720" height="640"/></clipPath>
  </defs>
  <!-- lid -->
  <!-- box interior -->
  <path fill="url(#sk-rim)" d="M341 567C341 540 374 526 415 526H831C872 526 905 542 905 571C905 598 870 626 823 626H423C376 626 341 599 341 567Z"/>
  <path fill="{interior}" d="M368 566C372 548 399 544 432 544H833C858 544 880 555 880 569C880 583 862 592 835 593H406C381 592 368 581 368 566Z"/>
  <g clip-path="url(#sk-inbox)">
  <!-- heart photo -->
  <g class="p2"><g transform="rotate(12 771 515)">
    <rect x="682" y="421" width="180" height="255" rx="29" fill="{paper0}"/>
    <rect x="707" y="448" width="133" height="208" rx="17" fill="url(#sk-heartBg)"/>
    <path fill="{heart}" d="M774 510C760 492 735 498 735 522C735 544 755 556 774 570C793 556 815 541 815 520C815 497 790 493 774 510Z"/>
  </g></g>
  <!-- landscape photo -->
  <g class="p1"><g transform="rotate(-12 551 507)">
    <rect x="412" y="393" width="281" height="307" rx="30" fill="{paper1}"/>
    <g clip-path="url(#sk-scene)">
      <rect x="429" y="405" width="238" height="260" rx="19" fill="url(#sk-sky)"/>
      <circle cx="611" cy="463" r="30" fill="{sun}"/>
      <path fill="url(#sk-mountains)" d="M429 560C460 548 476 510 509 505C540 503 570 546 598 559C626 540 638 527 654 530C681 537 696 564 712 572L712 678H429Z"/>
      <path fill="{shade}" opacity="0.5" d="M570 561C604 568 619 532 651 533C678 535 699 564 712 576V678H570Z"/>
    </g>
  </g></g>
  </g>
  <!-- box front -->
  <path fill="url(#sk-box)" d="M341 572C356 592 388 603 419 603H827C862 603 891 589 905 572V718C905 777 865 819 807 819H440C382 819 341 777 341 719Z"/>
  <!-- top rim -->
  <path fill="url(#sk-rim)" d="M341 568C341 583 357 592 383 592H855C881 591 896 582 905 568V581C905 607 865 626 827 626H420C382 626 341 605 341 581Z"/>
  <!-- front handle -->
  <rect x="530" y="667" width="185" height="55" rx="28" fill="url(#sk-slot)"/>
  <!-- sparkle -->
  <path class="star" fill="{sparkle}" d="M854 273C858 273 859 292 867 303C876 315 900 317 900 322C900 328 878 330 868 344C860 356 858 371 854 371C850 371 848 354 839 343C829 330 809 326 809 322C809 318 830 316 840 303C849 291 849 273 854 273Z"/>
  <path class="rays" d="M896 371L927 355M896 412L926 424" fill="none" stroke="{rays}" stroke-width="19" stroke-linecap="round"/>
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
    "blue":     ("Blau", 60, 1.5, -0.13, 25),
    "red":      ("Rot", -180, 2.2, -0.24, 105),
    "green":    ("Grün", -50, 1.5, -0.14, -110),
    "sunset":   ("Sonnenuntergang", 0, 1.0, 0.0),   # own colours: SCENES
    "nightsky": ("Nachthimmel", 0, 1.0, 0.0),      # own colours: SCENES
    "aurora":   ("Polarlicht", 0, 1.0, 0.0),        # SCENES
    "ocean":    ("Ozean", 0, 1.0, 0.0),             # SCENES
    "forest":   ("Wald", 0, 1.0, 0.0),              # SCENES
    "fire":     ("Feuer", 0, 1.0, 0.0),             # SCENES
    "neon":     ("Neon", 0, 1.0, 0.0),              # SCENES
    "candy":    ("Bonbon", 0, 1.0, 0.0),            # SCENES
    "retro":    ("Retro", 0, 1.0, 0.0),             # SCENES
    "kraft":    ("Karton", 0, 1.0, 0.0),            # SCENES
    "gold":     ("Gold", 0, 1.0, 0.0),              # SCENES
    "sepia":    ("Sepia", 0, 1.0, 0.0),             # TINTS
    "sunrise":    ("Sonnenaufgang", 0, 1.0, 0.0),  # SCENES
    "copper":     ("Kupfer", 0, 1.0, 0.0),  # SCENES
    "bronze":     ("Bronze", 0, 1.0, 0.0),  # SCENES
    "silver":     ("Silber", 0, 1.0, 0.0),  # SCENES
    "metallic":   ("Metallic", 0, 1.0, 0.0),  # SCENES
    "glass":      ("Glas", 0, 1.0, 0.0),  # SCENES
    "neonblue":   ("Neon Blau", 0, 1.0, 0.0),  # SCENES
    "neongreen":  ("Neon Grün", 0, 1.0, 0.0),  # SCENES
    "neonorange": ("Neon Orange", 0, 1.0, 0.0),  # SCENES
    "neonpink":   ("Neon Pink", 0, 1.0, 0.0),  # SCENES
    "rainbow":    ("Regenbogen", 0, 1.0, 0.0),  # SCENES
    "toxic":      ("Giftig", 0, 1.0, 0.0),  # SCENES
    "vaporwave":  ("Vaporwave", 0, 1.0, 0.0),  # SCENES
    "thermal":    ("Wärmebild", 0, 1.0, 0.0),  # SCENES
    "negative": ("Negativ", 0, 1.0, 0.0),           # the original inverted
    "sage":     ("Salbei (gedeckt)", -50, 0.35, -0.04),
    "slate":    ("Schiefer (gedeckt)", 50, 0.3, -0.06),
    "clay":     ("Ton (gedeckt)", -150, 0.4, -0.05, 100),
    "mauve":    ("Malve (gedeckt)", 140, 0.35, -0.05, 55),
    "mono":     ("Schwarzweiß", 0, 0.0, -0.06),
}


# the variants in the strip at the top of the README (strip.png)
STRIP = ["aqua", "sunset", "forest", "nightsky", "kraft", "neonpink"]

# variants with colours of their own instead of a turn of the original:
# the photos change with them (an evening sky, a moon instead of the sun)
SCENES = {
    "sunset": {
        "lidTop0": "#ffb38a", "lidTop1": "#ff9fa8",
        "lidInside0": "#b0569e", "lidInside1": "#d2689a",
        "rim0": "#ff9b8a", "rim1": "#f0809a",
        "box0": "#ffd2a8", "box1": "#ffc0b4", "box2": "#f3c2dc",
        "slot0": "#7a3a8c", "slot1": "#8e3f8f",
        "sky0": "#ff8a5c", "sky1": "#ffc070",
        "mountains0": "#7a4fb0", "mountains1": "#a0559f",
        "heartBg0": "#ff9fb4", "heartBg1": "#f27a9c",
        "interior": "#a24f93", "shade": "#5e3a8f",
        "paper0": "#fff8f2", "paper1": "#fffaf4", "heart": "#fff6ee",
        "sun": "#ffe9a0", "sparkle": "#ffe08a", "rays": "#ffa27a",
    },
    "nightsky": {
        "lidTop0": "#3a4c9e", "lidTop1": "#4a5cb4",
        "lidInside0": "#18205a", "lidInside1": "#232c70",
        "rim0": "#33438e", "rim1": "#2a387c",
        "box0": "#2c3a82", "box1": "#24316f", "box2": "#33428c",
        "slot0": "#ffd76a", "slot1": "#ffc94f",
        "sky0": "#1b2462", "sky1": "#2e3d8c",
        "mountains0": "#0e1540", "mountains1": "#1a2258",
        "heartBg0": "#4a3f9c", "heartBg1": "#5a3f9a",
        "interior": "#141a4a", "shade": "#080c2c",
        "paper0": "#eef0ff", "paper1": "#f2f3ff", "heart": "#ffe8a3",
        "sun": "#f6f1cf", "sparkle": "#ffe48a", "rays": "#a9b8ff",
    },
    # dark box, lid in green and violet like the northern lights; the photo
    # is a night with a green sky
    "aurora": {
        "lidTop0": "#5ef2b0", "lidTop1": "#8a7cf4",
        "lidInside0": "#1c2a4a", "lidInside1": "#2a2f66",
        "rim0": "#2f4f7a", "rim1": "#3b3f86",
        "box0": "#1e3550", "box1": "#1b2a48", "box2": "#2b2a5e",
        "slot0": "#5ef2b0", "slot1": "#7fe3c4",
        "sky0": "#2bd99a", "sky1": "#6a5ce0",
        "mountains0": "#0f1a33", "mountains1": "#1a2140",
        "heartBg0": "#7a5ce8", "heartBg1": "#5b48c4",
        "interior": "#121c36", "shade": "#060b1c",
        "paper0": "#eefaf6", "paper1": "#f2f6ff", "heart": "#c8ffe8",
        "sun": "#f4f6e0", "sparkle": "#9bffd2", "rays": "#b39cff",
    },
    # deep sea blue and turquoise; the photo is a beach with waves
    "ocean": {
        "lidTop0": "#38c6d9", "lidTop1": "#4fb3e8",
        "lidInside0": "#0c4a7a", "lidInside1": "#145a90",
        "rim0": "#1f86b8", "rim1": "#1670a6",
        "box0": "#2a9fd0", "box1": "#1c86c0", "box2": "#2fa9c9",
        "slot0": "#073a5e", "slot1": "#0a466e",
        "sky0": "#9fe3ff", "sky1": "#c9f1ff",
        "mountains0": "#0e7fc0", "mountains1": "#16a0c8",
        "heartBg0": "#ff9e8a", "heartBg1": "#f28470",
        "interior": "#0a3d68", "shade": "#0a5e96",
        "sun": "#ffe27a", "sparkle": "#bff6ff", "rays": "#8fe0ff",
    },
    # moss and fir green with a wooden handle; the photo has hills of firs
    "forest": {
        "lidTop0": "#7fbf6a", "lidTop1": "#6aa85e",
        "lidInside0": "#2d5a3a", "lidInside1": "#356a44",
        "rim0": "#4f8a52", "rim1": "#437a48",
        "box0": "#5f9a5a", "box1": "#4e8a50", "box2": "#6aa368",
        "slot0": "#6b4a2b", "slot1": "#7a5432",
        "sky0": "#bfe6d0", "sky1": "#dff2d8",
        "mountains0": "#2f6b3e", "mountains1": "#3f7f46",
        "heartBg0": "#e8a07a", "heartBg1": "#d98a62",
        "interior": "#24482f", "shade": "#1d3f27",
        "paper0": "#fbf8ef", "paper1": "#fcfaf2", "heart": "#fff7e8",
        "sun": "#fff0a0", "sparkle": "#e6f59a", "rays": "#a8d98a",
    },
    # yellow to red like flames, dark red inside, embers in the photo
    "fire": {
        "lidTop0": "#ffd23f", "lidTop1": "#ff8a2a",
        "lidInside0": "#b3141e", "lidInside1": "#d8261c",
        "rim0": "#ff6a22", "rim1": "#f0451c",
        "box0": "#ff9a2e", "box1": "#ff6a26", "box2": "#f4402a",
        "slot0": "#5a0a0e", "slot1": "#6e0e12",
        "sky0": "#ff5a1f", "sky1": "#ffb02e",
        "mountains0": "#4a0d14", "mountains1": "#7a1418",
        "heartBg0": "#ffb43a", "heartBg1": "#ff8a1f",
        "interior": "#8e0f18", "shade": "#2a0508",
        "paper0": "#fff6e6", "paper1": "#fff8ea", "heart": "#fff2d0",
        "sun": "#fff3a8", "sparkle": "#ffe14a", "rays": "#ff7a2a",
    },
    # glowing pink and cyan on a box that is almost black
    "neon": {
        "lidTop0": "#ff2bd6", "lidTop1": "#b33cff",
        "lidInside0": "#1a0b33", "lidInside1": "#260f4a",
        "rim0": "#00e5ff", "rim1": "#2bb8ff",
        "box0": "#1d1238", "box1": "#140c2a", "box2": "#24123f",
        "slot0": "#39ff88", "slot1": "#2bf0a0",
        "sky0": "#2a0f5c", "sky1": "#5a1690",
        "mountains0": "#ff2bd6", "mountains1": "#00e5ff",
        "heartBg0": "#00e5ff", "heartBg1": "#2b8cff",
        "interior": "#0c0620", "shade": "#7a00ff",
        "paper0": "#f4f0ff", "paper1": "#f6f2ff", "heart": "#ff2bd6",
        "sun": "#fff23a", "sparkle": "#39ff88", "rays": "#ff2bd6",
    },
    # sweets: pink box, mint lid, lemon handle
    "candy": {
        "lidTop0": "#9df5d8", "lidTop1": "#b8f7e0",
        "lidInside0": "#ff8fc8", "lidInside1": "#ffa6d4",
        "rim0": "#ffb3d9", "rim1": "#ff9fcf",
        "box0": "#ffc7e3", "box1": "#ffb8dc", "box2": "#ffd3ea",
        "slot0": "#ffd84a", "slot1": "#ffcc33",
        "sky0": "#a8e8ff", "sky1": "#c8f0ff",
        "mountains0": "#c9a2ff", "mountains1": "#ff9fd6",
        "heartBg0": "#ff6fae", "heartBg1": "#ff5a9e",
        "interior": "#ff7fbf", "shade": "#a98aff",
        "sun": "#fff27a", "sparkle": "#ffe066", "rays": "#7fe8d0",
    },
    # the seventies: mustard, burnt orange, brown and teal
    "retro": {
        "lidTop0": "#e8892f", "lidTop1": "#d9772a",
        "lidInside0": "#7a4a22", "lidInside1": "#8a5428",
        "rim0": "#2f8f8a", "rim1": "#287e7a",
        "box0": "#e9b949", "box1": "#e0a93a", "box2": "#efc45a",
        "slot0": "#6b3a1c", "slot1": "#7a4220",
        "sky0": "#7fc4b4", "sky1": "#9fd2c0",
        "mountains0": "#c4562a", "mountains1": "#a9462a",
        "heartBg0": "#e8892f", "heartBg1": "#d46a2a",
        "interior": "#5e3a1c", "shade": "#7a3a1e",
        "paper0": "#fbf1dc", "paper1": "#fcf3e0", "heart": "#fbf1dc",
        "sun": "#ffe08a", "sparkle": "#f2c94c", "rays": "#2f8f8a",
    },
    # a real shoebox: brown cardboard, darker inside, a cut-out handle
    "kraft": {
        "lidTop0": "#d9b08a", "lidTop1": "#cfa47c",
        "lidInside0": "#9a6c45", "lidInside1": "#a9784e",
        "rim0": "#b98a5e", "rim1": "#a97b52",
        "box0": "#d6ab7e", "box1": "#c99c6f", "box2": "#dcb48a",
        "slot0": "#4a2e1a", "slot1": "#56361e",
        "interior": "#7e5535",
        "paper0": "#fffaf2", "paper1": "#fffbf4", "heart": "#fff8ee",
        "sparkle": "#ffe0a8", "rays": "#c99c6f",
    },
    # black box with a gold lid and handle
    "gold": {
        "lidTop0": "#f7d77a", "lidTop1": "#d9a93a",
        "lidInside0": "#8a6420", "lidInside1": "#a77a2a",
        "rim0": "#e6c060", "rim1": "#c99a36",
        "box0": "#2a2a2e", "box1": "#1c1c20", "box2": "#34343a",
        "slot0": "#f2cf6a", "slot1": "#d9a93a",
        "sky0": "#3a3a42", "sky1": "#4a4a52",
        "mountains0": "#c99a36", "mountains1": "#e6c060",
        "heartBg0": "#2e2e34", "heartBg1": "#222228",
        "interior": "#121214", "shade": "#8a6420",
        "paper0": "#fbf6e8", "paper1": "#fcf8ec", "heart": "#f2cf6a",
        "sun": "#fff0b0", "sparkle": "#ffe08a", "rays": "#e6c060",
    },
    # morning: pale yellow, apricot and a light blue sky
    "sunrise": {
        "lidTop0": "#ffe6a0", "lidTop1": "#ffc9b0",
        "lidInside0": "#f29a8a", "lidInside1": "#f7b38f",
        "rim0": "#ffcf9a", "rim1": "#f7b88a",
        "box0": "#fff0c8", "box1": "#ffe0c0", "box2": "#ffd6dc",
        "slot0": "#d0704a", "slot1": "#de7f52",
        "sky0": "#ffd0a8", "sky1": "#bfe0ff",
        "mountains0": "#8fb0e0", "mountains1": "#a8a0d8",
        "heartBg0": "#ffb8a8", "heartBg1": "#ff9f9a",
        "interior": "#e88a7e", "shade": "#7f98d0",
        "sun": "#fff0b0", "sparkle": "#ffd75a", "rays": "#ffb08a",
    },
    # metals: strong light and dark stops in the gradients for the shine
    "copper": {
        "lidTop0": "#f6b48a", "lidTop1": "#b05e34",
        "lidInside0": "#6e3219", "lidInside1": "#8f4626",
        "rim0": "#e09464", "rim1": "#a95a32",
        "box0": "#f0a878", "box1": "#b8642f", "box2": "#f2b892",
        "slot0": "#4a200e", "slot1": "#5e2a14",
        "interior": "#5a2812", "sparkle": "#ffb27a", "rays": "#c97a4a",
    },
    "bronze": {
        "lidTop0": "#e6bd78", "lidTop1": "#8a5e24",
        "lidInside0": "#55391a", "lidInside1": "#6e4b1e",
        "rim0": "#c89a4c", "rim1": "#8f6828",
        "box0": "#d6aa60", "box1": "#9a6e2e", "box2": "#e2be80",
        "slot0": "#36240e", "slot1": "#463012",
        "interior": "#43300f", "sparkle": "#f0c870", "rays": "#b8893f",
    },
    "silver": {
        "lidTop0": "#f6f8fa", "lidTop1": "#aab1ba",
        "lidInside0": "#6e7680", "lidInside1": "#8a929c",
        "rim0": "#d6dbe1", "rim1": "#a0a8b2",
        "box0": "#eef1f4", "box1": "#b9c0c8", "box2": "#f4f6f8",
        "slot0": "#454b53", "slot1": "#5a616a",
        "interior": "#5e656e", "sparkle": "#b8c8e0", "rays": "#9aa3ad",
    },
    # chrome: hard reflections, almost black and almost white
    "metallic": {
        "lidTop0": "#ffffff", "lidTop1": "#4e5864",
        "lidInside0": "#1e2329", "lidInside1": "#434b55",
        "rim0": "#e6ebf1", "rim1": "#5e6874",
        "box0": "#f6f8fb", "box1": "#7c8896", "box2": "#e2e8ef",
        "slot0": "#14171c", "slot1": "#343b44",
        "sky0": "#9aa6b4", "sky1": "#dfe6ee",
        "mountains0": "#3a424c", "mountains1": "#6a7480",
        "heartBg0": "#c4ccd6", "heartBg1": "#8a96a4",
        "interior": "#1a1e24", "shade": "#20262e",
        "paper0": "#f4f6f8", "paper1": "#f6f8fa", "heart": "#ffffff",
        "sun": "#ffffff", "sparkle": "#8fd0ff", "rays": "#7c8896",
    },
    # see-through: the box front lets the photos show
    "glass": {
        "lidTop0": "#c8f4ffb0", "lidTop1": "#a8e0ffa0",
        "lidInside0": "#5aa8d890", "lidInside1": "#7ab8e890",
        "rim0": "#9fd8f0d0", "rim1": "#80c4ecc8",
        "box0": "#d8f6ff80", "box1": "#a8e4fa70", "box2": "#e8fbff90",
        "slot0": "#2a8fb0d0", "slot1": "#3aa0c0d0",
        "interior": "#4a98c8a0",
        "sparkle": "#8fe8ff", "rays": "#80c4ec",
    },
    "neonblue": {"lidTop0": "#00b3ff", "lidTop1": "#3a5cff", "lidInside0": "#05081c", "lidInside1": "#0a1030", "rim0": "#00f0ff", "rim1": "#00b3ff", "box0": "#0e1640", "box1": "#0a0f2e", "box2": "#141c4a", "slot0": "#00f0ff", "slot1": "#00b3ff", "sky0": "#061038", "sky1": "#0c2a6a", "mountains0": "#00b3ff", "mountains1": "#3a5cff", "heartBg0": "#00f0ff", "heartBg1": "#3a5cff", "interior": "#05081c", "shade": "#0a1030", "paper0": "#f4f4fa", "paper1": "#f6f6fb", "heart": "#ffffff", "sun": "#7af9ff", "sparkle": "#7af9ff", "rays": "#3a5cff"},
    "neongreen": {"lidTop0": "#39ff14", "lidTop1": "#00e88a", "lidInside0": "#030c06", "lidInside1": "#082010", "rim0": "#b6ff00", "rim1": "#39ff14", "box0": "#0c2414", "box1": "#08180d", "box2": "#10301a", "slot0": "#39ff14", "slot1": "#39ff14", "sky0": "#062a14", "sky1": "#0c3a1e", "mountains0": "#39ff14", "mountains1": "#00e88a", "heartBg0": "#b6ff00", "heartBg1": "#00e88a", "interior": "#030c06", "shade": "#082010", "paper0": "#f4f4fa", "paper1": "#f6f6fb", "heart": "#ffffff", "sun": "#e5ff3a", "sparkle": "#e5ff3a", "rays": "#00e88a"},
    "neonorange": {"lidTop0": "#ff8a00", "lidTop1": "#ff3d00", "lidInside0": "#100602", "lidInside1": "#2a0f04", "rim0": "#ffd000", "rim1": "#ff8a00", "box0": "#2a1206", "box1": "#1f0d04", "box2": "#341808", "slot0": "#ffb000", "slot1": "#ff8a00", "sky0": "#2a1206", "sky1": "#5a1e08", "mountains0": "#ff8a00", "mountains1": "#ff3d00", "heartBg0": "#ffd000", "heartBg1": "#ff3d00", "interior": "#100602", "shade": "#2a0f04", "paper0": "#f4f4fa", "paper1": "#f6f6fb", "heart": "#ffffff", "sun": "#ffe600", "sparkle": "#ffe600", "rays": "#ff3d00"},
    "neonpink": {"lidTop0": "#ff2bd6", "lidTop1": "#ff4f9a", "lidInside0": "#0e030d", "lidInside1": "#2a0628", "rim0": "#ff8af2", "rim1": "#ff2bd6", "box0": "#2a0a28", "box1": "#1e061c", "box2": "#360e34", "slot0": "#ff8af2", "slot1": "#ff2bd6", "sky0": "#2a0a2a", "sky1": "#5a0f58", "mountains0": "#ff2bd6", "mountains1": "#ff4f9a", "heartBg0": "#ff8af2", "heartBg1": "#ff4f9a", "interior": "#0e030d", "shade": "#2a0628", "paper0": "#f4f4fa", "paper1": "#f6f6fb", "heart": "#ffffff", "sun": "#ff9af5", "sparkle": "#ff9af5", "rays": "#ff2bd6"},
    # every part another colour
    "rainbow": {
        "lidTop0": "#ff4d4d", "lidTop1": "#ff9f1c",
        "lidInside0": "#ffd60a", "lidInside1": "#ffe14d",
        "rim0": "#3ddc84", "rim1": "#2ec4b6",
        "box0": "#4d9fff", "box1": "#7a5cff", "box2": "#c04dff",
        "slot0": "#ff4d9f", "slot1": "#ff2b7a",
        "interior": "#2e8a6a",
        "sparkle": "#ff4d9f", "rays": "#3ddc84",
    },
    # slime green and poison purple
    "toxic": {
        "lidTop0": "#a6ff00", "lidTop1": "#5af03a",
        "lidInside0": "#3a0a5a", "lidInside1": "#4a106e",
        "rim0": "#8a2be2", "rim1": "#6a1ab8",
        "box0": "#8aff3a", "box1": "#4fd61c", "box2": "#b8ff4a",
        "slot0": "#2a0040", "slot1": "#3a0058",
        "sky0": "#3aff7a", "sky1": "#c6ff00",
        "mountains0": "#6a1ab8", "mountains1": "#8a2be2",
        "heartBg0": "#ff2bd6", "heartBg1": "#c41ab8",
        "interior": "#2a063e", "shade": "#2a063e",
        "paper0": "#f6ffe8", "paper1": "#f8ffec", "heart": "#c6ff00",
        "sun": "#fffb3a", "sparkle": "#a6ff00", "rays": "#8a2be2",
    },
    # pink, violet and turquoise of the nineties internet
    "vaporwave": {
        "lidTop0": "#ff71ce", "lidTop1": "#b967ff",
        "lidInside0": "#01cdfe", "lidInside1": "#05ffa1",
        "rim0": "#fffb96", "rim1": "#ffd896",
        "box0": "#b967ff", "box1": "#ff71ce", "box2": "#01cdfe",
        "slot0": "#2a1a5e", "slot1": "#3a2470",
        "sky0": "#ff71ce", "sky1": "#fffb96",
        "mountains0": "#01cdfe", "mountains1": "#05ffa1",
        "heartBg0": "#b967ff", "heartBg1": "#8a4fff",
        "interior": "#4a2aa0", "shade": "#2a1a5e",
        "sun": "#fffb96", "sparkle": "#05ffa1", "rays": "#01cdfe",
    },
    # a heat camera: warm parts glow yellow, cold ones are deep blue
    "thermal": {
        "lidTop0": "#ffef3a", "lidTop1": "#ff6a00",
        "lidInside0": "#c0004a", "lidInside1": "#ff2a2a",
        "rim0": "#ff3a00", "rim1": "#e0002a",
        "box0": "#ffd400", "box1": "#ff7a00", "box2": "#ff2a6a",
        "slot0": "#1a0a6a", "slot1": "#2a0a8a",
        "sky0": "#2a0a8a", "sky1": "#5a00a0",
        "mountains0": "#ff2a6a", "mountains1": "#ff7a00",
        "heartBg0": "#ffef3a", "heartBg1": "#ffd400",
        "interior": "#8a0050", "shade": "#ffef3a",
        "paper0": "#ffb000", "paper1": "#ffc000", "heart": "#ffffff",
        "sun": "#ffffff", "sparkle": "#ffe600", "rays": "#ff2a6a",
    },
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
        if k in FIXED_HUE and chroma > 1:
            out[k] = h  # strong colours: the photos stay as they are
            continue
        L, C, H = hex_to_oklch(h)
        if k in INNER and inner is not None:
            H += inner
        elif k not in FIXED_HUE:
            H += turn
        out[k] = oklch_to_hex(min(0.99, max(0.0, L + light)), C * chroma, H)
    return out


# variants in one colour: every colour of the original keeps its lightness
# (shifted) and gets this hue and chroma, like an old print
TINTS = {
    "sepia": (65, 0.06, -0.09),
}


def tint(hue, chroma, light):
    out = {}
    for k, h in ORIGINAL.items():
        L, _, _ = hex_to_oklch(h)
        L = min(0.97, max(0.0, L + light))
        # the light parts (paper) a little paler, the dark ones a little richer
        out[k] = oklch_to_hex(L, chroma * (0.6 if L > 0.93 else 1.0 + (0.8 - L)), hue)
    return out


# --- the lid ------------------------------------------------------------------

# The lid comes from the 3D scene (scene.py, run in Blender): lid.json has it
# frame by frame as outlines on the page, its silhouette, its top face while
# that is seen and its inside while that is seen, and whether it is behind
# the photos. Here they become paths: in the logo the open lid, in the
# loading animation every frame, morphing from one to the next.
LID = json.loads((HERE / "lid.json").read_text())
LID_K = 26  # points of an outline in the animation (all frames the same, so they morph)


def _resample(poly, anchor, k):
    """A closed outline as k points evenly along it, starting next to anchor (the same
    point of the lid in every frame), always running the same way round."""
    pts = [tuple(p) for p in poly]
    area = sum(a[0] * b[1] - b[0] * a[1] for a, b in zip(pts, pts[1:] + pts[:1]))
    if area < 0:
        pts.reverse()
    seg = [math.dist(a, b) for a, b in zip(pts, pts[1:] + pts[:1])]
    total = sum(seg) or 1
    # where along the outline the anchor is nearest
    best, at, run = None, 0.0, 0.0
    for (a, b), s in zip(zip(pts, pts[1:] + pts[:1]), seg):
        u = 0.0
        if s:
            u = max(0.0, min(1.0, ((anchor[0] - a[0]) * (b[0] - a[0]) + (anchor[1] - a[1]) * (b[1] - a[1])) / (s * s)))
        q = (a[0] + (b[0] - a[0]) * u, a[1] + (b[1] - a[1]) * u)
        dd = math.dist(q, anchor)
        if best is None or dd < best:
            best, at = dd, run + u * s
        run += s
    edges = list(zip(pts, pts[1:] + pts[:1]))
    out = []
    for i in range(k):
        target = (at + total * i / k) % total
        run = 0.0
        for e, ((a, b), s) in enumerate(zip(edges, seg)):
            if run + s >= target or e == len(edges) - 1:
                u = min(1.0, (target - run) / s) if s else 0.0
                out.append((a[0] + (b[0] - a[0]) * u, a[1] + (b[1] - a[1]) * u))
                break
            run += s
    return out


def _outline(pts):
    """A smooth closed outline: through the middles of the edges, the points as controls."""
    f = lambda v: f"{v:.0f}"
    mid = [((p[0] + q[0]) / 2, (p[1] + q[1]) / 2) for p, q in zip(pts, pts[1:] + pts[:1])]
    return f"M{f(mid[-1][0])} {f(mid[-1][1])}" + "".join(f"Q{f(p[0])} {f(p[1])} {f(m[0])} {f(m[1])}" for p, m in zip(pts, mid)) + "Z"


def lid_static(frame=None):
    """The lid of the logo: open (a frame of the scene where it stands open)."""
    f = LID["lid"][LID["open"] if frame is None else frame]
    sil = _outline(_resample(f["sil"], f["silAnchor"], 96))
    if f["top"]:
        face = f'<path fill="url(#sk-lidTop)" d="{_outline(_resample(f["top"], f["topAnchor"], 96))}"/>'
        shade = f'<path fill="#000" opacity=".14" d="{sil}"/>'
    else:
        face = f'<path fill="url(#sk-lidInside)" d="{_outline(_resample(f["inside"], f["insideAnchor"], 96))}"/>'
        shade = ""
    return f'  <g class="lid">\n  <path fill="url(#sk-lidTop)" d="{sil}"/>{shade}\n  {face}\n  </g>\n'


def svg(name):
    return _colours(name).replace("  <!-- lid -->\n", lid_static())


def _colours(name):
    if name == "negative":  # like a film negative, the photos too
        return TEMPLATE.format(**{k: "#%06x" % (0xffffff - int(h[1:], 16)) for k, h in ORIGINAL.items()})
    if name in TINTS:
        return TEMPLATE.format(**tint(*TINTS[name]))
    if name in SCENES:
        # colours a scene leaves out are the original's (usually the photos)
        return TEMPLATE.format(**{**ORIGINAL, **SCENES[name]})
    return TEMPLATE.format(**palette(*VARIANTS[name][1:]))


# --- loading animation -----------------------------------------------------------

# The logo as a loading animation (web/logo/NAME-loading.svg), the loop of the
# 3D scene (3.6 s): the box is closed; the lid swings up and over to the side
# (a little past and back) and the photos rise at once, standing straighter;
# while they stay out they slowly lean out to their angle in the drawing and
# the star and the rays drift outwards; then the photos sink back into the
# box and only then the lid swings shut, hopping a
# little as it lands. The lid's outlines morph from frame to frame (SMIL
# <animate> of d, runs in an <img> too; frames a straight line between their
# neighbours would draw are left out). It is drawn twice: behind the photos
# (where the drawing has it) once it is lifted above the box, in front of the
# box while closed or nearly; the two look the same where they swap. Without
# motion (reduced motion) the plain logo stands still.
LID_TOLERANCE = 2.0  # px: how far a left-out frame may be from where the morph draws it


# Where the photos, the star and the rays turn and grow from: the middle of each one's box
# as Chrome measures it (getBBox of the drawing's elements)
CENTRE = {"p1": (569.97, 543.35), "p2": (765.01, 547.98), "star": (854.5, 322.0), "rays": (911.5, 389.5)}
EASE = {"ease": (.25, .1, .25, 1), "ease-in": (.42, 0, 1, 1), "ease-out": (0, 0, .58, 1),
        "ease-in-out": (.42, 0, .58, 1), "linear": (0, 0, 1, 1)}


def _bezier(x1, y1, x2, y2):
    """A CSS cubic-bezier timing function (y may overshoot)."""
    def at(a, b, t):
        return 3 * a * t * (1 - t) ** 2 + 3 * b * t * t * (1 - t) + t ** 3

    def f(x):
        lo, hi = 0.0, 1.0
        for _ in range(60):
            mid = (lo + hi) / 2
            if at(x1, x2, mid) < x:
                lo = mid
            else:
                hi = mid
        return at(y1, y2, (lo + hi) / 2)
    return f


def _track(keys, n):
    """The values of keyframes [(frame, values, timing of the stretch to the next)] at
    every frame 0 … n, as CSS would draw them."""
    keys = [(min(max(f, 0), n), v, e) for f, v, e in keys]
    out = []
    for fr in range(n + 1):
        for (f0, v0, e), (f1, v1, _) in zip(keys, keys[1:]):
            if f0 <= fr <= f1:
                k = 0.0 if f1 == f0 else (fr - f0) / (f1 - f0)
                k = _bezier(*(EASE.get(e, e) if e else EASE["ease"]))(k)
                out.append(tuple(a + (b - a) * k for a, b in zip(v0, v1)))
                break
    return out


def _smil(tag, attr, vals, n, fmt, tol, extra=""):
    """One animation of the loop from values at every frame: linear between the frames kept,
    a frame left out when a straight line between its neighbours draws it within tol."""
    def fits(a, c):  # a straight line from frame a to c draws every frame between
        return all(abs(x + (z - x) * (i - a) / (c - a) - y) <= tol
                   for i in range(a + 1, c) for x, y, z in zip(vals[a], vals[i], vals[c]))
    keep = [0]
    for i in range(1, n):
        if not fits(keep[-1], i + 1):
            keep.append(i)
    keep.append(n)
    times = ";".join(_num(i / n) for i in keep)
    values = ";".join(fmt(vals[i]) for i in keep)
    return (f'<{tag} attributeName="{attr}"{extra} dur="{LOOP}s" repeatCount="indefinite" '
            f'keyTimes="{times}" values="{values}"/>')


def loading_moves():
    """The photos, the star and the rays in the scene's time, on the same clock as the lid
    (SMIL: CSS animations in an image run on a clock of their own and drift away from it).
    The photos rise standing straighter and slowly lean out to their angle in the drawing
    while they are out, the star and the rays come and slowly drift outwards to where the
    drawing has them; then all go before the lid shuts. Where they end up while out is the
    drawing. Returns, per element, the animations (photos) or the wrapper (star, rays)."""
    ph = LID["photos"]
    up, out, down, lean = ph["up"], ph["out"], ph["down"], ph["lean"]
    n = LID["frames"]
    rise = (.2, .8, .3, 1.15)
    slow = (.35, .45, .4, 1)
    tr = lambda v: f"{_num(v[0])} {_num(v[1])}"
    move = lambda keys: _track(keys, n)

    def photo(cls, dy, d0, d1, sign, overshoot):
        y = move([(0, (0, dy), None), (up[0] + d0, (0, dy), (.2, .8, .3, overshoot)), (up[1] + d0, (0, 0), None),
                  (down[0] - d1, (0, 0), "ease-in"), (down[1] - d1, (0, dy), None), (n, (0, dy), None)])
        r = move([(0, (sign * lean,), None), (up[0] + d0, (sign * lean,), slow), (out, (0,), None), (n, (0,), None)])
        cx, cy = CENTRE[cls]
        return (_smil("animateTransform", "transform", y, n, tr, .1, ' type="translate" additive="sum"')
                + _smil("animateTransform", "transform", r, n, lambda v: f"{_num(v[0])} {cx} {cy}", .05,
                        ' type="rotate" additive="sum"'))

    def parts(keys, comps):
        """translate, scale, rotate and opacity of the star or the rays (CSS transform lists
        interpolate function by function)."""
        v = move(keys)
        tx = [(a[0], a[1]) for a in v]
        sc = [(a[2],) for a in v]
        ro = [(a[3],) for a in v]
        op = [(a[4],) for a in v]
        anims = [_smil("animateTransform", "transform", tx, n, tr, .1, ' type="translate" additive="sum"'),
                 _smil("animateTransform", "transform", sc, n, lambda x: _num(x[0]), .002, ' type="scale" additive="sum"')]
        if "rotate" in comps:
            anims.append(_smil("animateTransform", "transform", ro, n, lambda x: _num(x[0]), .1, ' type="rotate" additive="sum"'))
        anims.append(_smil("animate", "opacity", op, n, lambda x: _num(x[0]), .005))
        return "".join(anims)

    star = parts([(0, (-34, 34, 0, -90, 0), None), (up[1] - 4, (-34, 34, 0, -90, 0), (.2, .8, .3, 1.4)),
                  (up[1] + 3, (-30, 30, 1, 0, 1), "ease-in-out"), (out, (0, 0, 1, 0, 1), None),
                  (down[0], (0, 0, 1, 0, 1), "ease-in"), (down[0] + 6, (0, 0, 0, 45, 0), None), (n, (0, 0, 0, 45, 0), None)],
                 ("rotate",))
    rays = parts([(0, (-34, 0, .2, 0, 0), None), (up[1] - 1, (-34, 0, .2, 0, 0), "ease-out"),
                  (up[1] + 5, (-30, 0, 1, 0, 1), "ease-in-out"), (out, (0, 0, 1, 0, 1), None),
                  (down[0], (0, 0, 1, 0, 1), "ease-in"), (down[0] + 5, (12, 0, .2, 0, 0), None), (n, (12, 0, .2, 0, 0), None)], ())
    return {"p1": photo("p1", 260, 0, 0, 1, 1.15), "p2": photo("p2", 240, 2, 1, -1, 1.2), "star": star, "rays": rays}


LOADING_CSS = """
  <style>
    .lid-still { display: none; }
    /* without motion: the plain logo */
    @media (prefers-reduced-motion: reduce) {
      .lid-move { display: none; }
      .lid-still { display: inline; }
    }
  </style>
"""


LOOP = LID["frames"] / LID["fps"]  # seconds


def _num(v):
    return f"{v:.4f}".rstrip("0").rstrip(".") or "0"


def _show(when, attr="visibility"):
    """Shown in the stretches of the loop in when (pairs of fractions). display hides a
    whole group; visibility would give way to a child shown by its own."""
    on, off = ("inline", "none") if attr == "display" else ("visible", "hidden")
    times, vals = [0.0], [off]
    for a, b in when:
        times += [a, b]
        vals += [on, off]
    if len(times) > 1 and times[1] == 0:
        times, vals = times[1:], vals[1:]
    if times[-1] >= 1:
        times, vals = times[:-1], vals[:-1]
    return (f'<animate attributeName="{attr}" dur="{LOOP}s" repeatCount="indefinite" calcMode="discrete" '
            f'keyTimes="{";".join(map(_num, times))}" values="{";".join(vals)}"/>')


def _spans(flags):
    """The stretches of the loop where a frame's flag is set (a frame lasts until the next)."""
    n, spans, start = LID["frames"], [], None
    for i, on in enumerate(flags):
        if on and start is None:
            start = i / n
        elif not on and start is not None:
            spans.append((start, i / n))
            start = None
    if start is not None:
        spans.append((start, 1.0))
    return spans


def _lid_frames():
    """Every frame's three outlines as LID_K points each; an outline not seen in a frame
    takes the shape of the nearest frame where it is (hidden there, it only must morph)."""
    frames = LID["lid"]
    out = {"sil": [_resample(f["sil"], f["silAnchor"], LID_K) for f in frames]}
    for part in ("top", "inside"):
        seen = [i for i, f in enumerate(frames) if f[part]]
        shapes = []
        for i, f in enumerate(frames):
            j = min(seen, key=lambda s: abs(s - i))
            shapes.append(_resample(frames[j][part], frames[j][part + "Anchor"], LID_K))
        out[part] = shapes
    return out


def _keep(shapes):
    """The frames to keep: the morph between two kept ones stays within LID_TOLERANCE of
    every frame left out between them."""
    n = len(shapes["sil"])
    pts = [sum((shapes[k][i] for k in ("sil", "top", "inside")), []) for i in range(n)]

    def err(a, b):
        worst = 0.0
        for i in range(a + 1, b):
            u = (i - a) / (b - a)
            for p, q, r in zip(pts[a], pts[b], pts[i]):
                worst = max(worst, math.dist((p[0] + (q[0] - p[0]) * u, p[1] + (q[1] - p[1]) * u), r))
        return worst

    keep, i = [0], 0
    while i < n - 1:
        j = i + 1
        while j + 1 < n and err(i, j + 1) <= LID_TOLERANCE:
            j += 1
        keep.append(j)
        i = j
    return keep


def _lid_moving():
    shapes = _lid_frames()
    keep = _keep(shapes)
    times = ";".join(_num(i / LID["frames"]) for i in keep)

    def morph(part):
        vals = ";".join(_outline(shapes[part][i]) for i in keep)
        return f'<animate attributeName="d" dur="{LOOP}s" repeatCount="indefinite" keyTimes="{times}" values="{vals}"/>'

    frames = LID["lid"]
    top = _spans([bool(f["top"]) for f in frames])
    inside = _spans([bool(f["inside"]) for f in frames])
    first = lambda part: _outline(shapes[part][0])
    return (f'  <g id="sk-lidMove">\n'
            f'  <g fill="url(#sk-lidTop)"><path id="sk-lidS" d="{first("sil")}">{morph("sil")}</path></g>\n'
            # the sides a shade darker while the top face is seen (the open lid as drawn: not)
            f'  <use href="#sk-lidS" fill="#000" opacity=".14">{_show(top)}</use>\n'
            f'  <path fill="url(#sk-lidTop)" d="{first("top")}">{morph("top")}{_show(top)}</path>\n'
            f'  <path fill="url(#sk-lidInside)" d="{first("inside")}" visibility="hidden">{morph("inside")}{_show(inside)}</path>\n'
            f'  </g>\n')


def loading_svg(name):
    s = svg(name)
    s = s.replace("  </defs>\n", "  </defs>" + LOADING_CSS, 1)
    mv = loading_moves()
    # each moving part twice: still (shown without motion) and moving; the class on
    # wrappers without animations of their own, as for the lid
    for cls in ("p1", "p2"):
        i = s.index(f'  <g class="{cls}">')
        j = s.index("</g></g>\n", i) + len("</g></g>\n")
        el = s[i:j]
        moving = el.replace(f'<g class="{cls}">', f'<g class="{cls}">{mv[cls]}', 1)
        s = s[:i] + f'  <g class="lid-still">\n{el}  </g>\n  <g class="lid-move">\n{moving}  </g>\n' + s[j:]
    for cls in ("star", "rays"):
        # turned and grown about their middle: moved there, animated, moved back
        i = s.index(f'  <path class="{cls}"')
        j = s.index("/>", i) + 3
        cx, cy = CENTRE[cls]
        el = s[i:j]
        moved = el.strip().replace(f'class="{cls}"', f'class="{cls}" transform="translate({-cx} {-cy})"', 1)
        s = (s[:i] + f'  <g class="lid-still">\n{el}  </g>\n'
             f'  <g class="lid-move"><g transform="translate({cx} {cy})"><g>{mv[cls]}{moved}</g></g></g>\n' + s[j:])
    start = s.index('  <g class="lid">')
    end = s.index("  </g>\n", start) + len("  </g>\n")
    still = s[start:end].replace('class="lid"', 'class="lid lid-still"', 1)
    behind = [f["behind"] for f in LID["lid"]]
    # the class on a wrapper without animations: reduced motion hides it, a SMIL display
    # animation would win over that on the group itself
    back = f'  <g class="lid-move"><g>{_show(_spans(behind), "display")}\n{_lid_moving()}  </g></g>\n'
    front = (f'  <g class="lid-move"><g>{_show(_spans([not b for b in behind]), "display")}\n'
             f'  <use href="#sk-lidMove"/>\n  </g></g>\n')
    s = s[:start] + still + back + s[end:]
    s = s.replace("</svg>", front + "</svg>")
    return s.replace('aria-label="Schuhkarton logo"', 'aria-label="Schuhkarton lädt"')


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
        (ui / f"{name}-loading.svg").write_text(loading_svg(name))
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
    # the table of the variants in docs/logo/README.md
    cells = [f'<td align="center"><img src="svg/schuhkarton-{n}.svg" width="120" alt="{t}"><br><b>{n}</b><br><sub>{t}</sub><br>'
             f'<sub><a href="svg/schuhkarton-{n}.svg">SVG</a> · <a href="png/schuhkarton-{n}-512.png">PNG 512</a> · '
             f'<a href="png/schuhkarton-{n}-1024.png">PNG 1024</a> · <a href="jpg/schuhkarton-{n}.jpg">JPG</a></sub></td>'
             for n, (t, *_) in VARIANTS.items()]
    table = "<table>\n" + "\n".join("<tr>\n" + "\n".join(cells[i:i + 4]) + "\n</tr>" for i in range(0, len(cells), 4)) + "\n</table>"
    readme = HERE / "README.md"
    text = readme.read_text()
    readme.write_text(text[:text.index("<table>")] + table + text[text.index("</table>") + len("</table>"):])
    # the strip in the README header: a few variants side by side, transparent
    cell, gap = 320, 24
    strip = Image.new("RGBA", (len(STRIP) * cell + (len(STRIP) - 1) * gap, cell), (0, 0, 0, 0))
    for i, name in enumerate(STRIP):
        im = Image.open(HERE / "png" / f"schuhkarton-{name}-1024.png").convert("RGBA").resize((cell, cell), Image.LANCZOS)
        strip.alpha_composite(im, (i * (cell + gap), 0))
    strip.save(HERE / "strip.png", optimize=True)
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
