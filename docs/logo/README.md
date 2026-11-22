# Logo

`logo.svg` is the logo (aqua); `strip.png` (the README header) shows six of
the variants side by side on a transparent background (`STRIP` in
`generate.py`). The UI has every variant in `web/logo/`:
`NAME.svg` (logo and favicon), `NAME.ico` (16–64 px, also served as
`/favicon.ico`) and `NAME-180.png` (apple-touch-icon, on white). Which one it
shows is set under *Einstellungen → Aussehen* or with `ui.logo` (default
`aqua`). With several they take turns (`ui.logo_change`: every visit,
hourly, daily or weekly) in a random order that never shows one twice in a
row.

The lid comes from a 3D scene: `scene.py` builds it in Blender (the box as
drawn, a camera looking at it the way the drawing does, and a rounded lid
with a lip and a recess, its open pose fitted to the drawing's lid),
animates it and writes every frame of the lid as outlines on the page to
`lid.json`; `schuhkarton.blend` is the scene, to look at or change:

```sh
blender -b --python docs/logo/scene.py
```

The box, the photos, the star and the rays stay the drawing; the logo shows
the lid of an open frame.

`NAME-loading.svg` in `web/logo/` is the loading animation the UI shows
instead of a spinner, the loop of the scene (3.6 s): the box is closed, the
lid swings up and over to the side (a little past and back) and the photos
rise at once, standing straighter; while they stay out they slowly lean out
to their angle in the drawing and the star and the rays drift outwards; then
the photos sink back and only then the lid
swings shut, hopping a little as it lands. The lid's outlines morph from
frame to frame (SMIL inside the SVG, so it also runs in an `<img>`; with
reduced motion it shows the logo standing still). Every variant gets its
colours on the same frames.

## Variants

Pastel, strong (blue, red, green), scenes with colours of their own (sunset,
night sky, aurora, ocean, forest, fire, five neons, candy, retro, kraft
cardboard, metals, glass, rainbow, toxic, vaporwave, thermal camera), sepia,
negative, muted (gedeckt) and black and white. In the pastel, strong and
muted ones only the box changes its colour; the scenes change the photos too. PNGs are transparent, JPGs are on white.

<table>
<tr>
<td align="center"><img src="svg/schuhkarton-aqua.svg" width="120" alt="Aqua (original)"><br><b>aqua</b><br><sub>Aqua (original)</sub><br><sub><a href="svg/schuhkarton-aqua.svg">SVG</a> · <a href="png/schuhkarton-aqua-512.png">PNG 512</a> · <a href="png/schuhkarton-aqua-1024.png">PNG 1024</a> · <a href="jpg/schuhkarton-aqua.jpg">JPG</a></sub></td>
<td align="center"><img src="svg/schuhkarton-peach.svg" width="120" alt="Pfirsich"><br><b>peach</b><br><sub>Pfirsich</sub><br><sub><a href="svg/schuhkarton-peach.svg">SVG</a> · <a href="png/schuhkarton-peach-512.png">PNG 512</a> · <a href="png/schuhkarton-peach-1024.png">PNG 1024</a> · <a href="jpg/schuhkarton-peach.jpg">JPG</a></sub></td>
<td align="center"><img src="svg/schuhkarton-rose.svg" width="120" alt="Rosé"><br><b>rose</b><br><sub>Rosé</sub><br><sub><a href="svg/schuhkarton-rose.svg">SVG</a> · <a href="png/schuhkarton-rose-512.png">PNG 512</a> · <a href="png/schuhkarton-rose-1024.png">PNG 1024</a> · <a href="jpg/schuhkarton-rose.jpg">JPG</a></sub></td>
<td align="center"><img src="svg/schuhkarton-lavender.svg" width="120" alt="Lavendel"><br><b>lavender</b><br><sub>Lavendel</sub><br><sub><a href="svg/schuhkarton-lavender.svg">SVG</a> · <a href="png/schuhkarton-lavender-512.png">PNG 512</a> · <a href="png/schuhkarton-lavender-1024.png">PNG 1024</a> · <a href="jpg/schuhkarton-lavender.jpg">JPG</a></sub></td>
</tr>
<tr>
<td align="center"><img src="svg/schuhkarton-mint.svg" width="120" alt="Minze"><br><b>mint</b><br><sub>Minze</sub><br><sub><a href="svg/schuhkarton-mint.svg">SVG</a> · <a href="png/schuhkarton-mint-512.png">PNG 512</a> · <a href="png/schuhkarton-mint-1024.png">PNG 1024</a> · <a href="jpg/schuhkarton-mint.jpg">JPG</a></sub></td>
<td align="center"><img src="svg/schuhkarton-lemon.svg" width="120" alt="Zitrone"><br><b>lemon</b><br><sub>Zitrone</sub><br><sub><a href="svg/schuhkarton-lemon.svg">SVG</a> · <a href="png/schuhkarton-lemon-512.png">PNG 512</a> · <a href="png/schuhkarton-lemon-1024.png">PNG 1024</a> · <a href="jpg/schuhkarton-lemon.jpg">JPG</a></sub></td>
<td align="center"><img src="svg/schuhkarton-sky.svg" width="120" alt="Himmel"><br><b>sky</b><br><sub>Himmel</sub><br><sub><a href="svg/schuhkarton-sky.svg">SVG</a> · <a href="png/schuhkarton-sky-512.png">PNG 512</a> · <a href="png/schuhkarton-sky-1024.png">PNG 1024</a> · <a href="jpg/schuhkarton-sky.jpg">JPG</a></sub></td>
<td align="center"><img src="svg/schuhkarton-blue.svg" width="120" alt="Blau"><br><b>blue</b><br><sub>Blau</sub><br><sub><a href="svg/schuhkarton-blue.svg">SVG</a> · <a href="png/schuhkarton-blue-512.png">PNG 512</a> · <a href="png/schuhkarton-blue-1024.png">PNG 1024</a> · <a href="jpg/schuhkarton-blue.jpg">JPG</a></sub></td>
</tr>
<tr>
<td align="center"><img src="svg/schuhkarton-red.svg" width="120" alt="Rot"><br><b>red</b><br><sub>Rot</sub><br><sub><a href="svg/schuhkarton-red.svg">SVG</a> · <a href="png/schuhkarton-red-512.png">PNG 512</a> · <a href="png/schuhkarton-red-1024.png">PNG 1024</a> · <a href="jpg/schuhkarton-red.jpg">JPG</a></sub></td>
<td align="center"><img src="svg/schuhkarton-green.svg" width="120" alt="Grün"><br><b>green</b><br><sub>Grün</sub><br><sub><a href="svg/schuhkarton-green.svg">SVG</a> · <a href="png/schuhkarton-green-512.png">PNG 512</a> · <a href="png/schuhkarton-green-1024.png">PNG 1024</a> · <a href="jpg/schuhkarton-green.jpg">JPG</a></sub></td>
<td align="center"><img src="svg/schuhkarton-sunset.svg" width="120" alt="Sonnenuntergang"><br><b>sunset</b><br><sub>Sonnenuntergang</sub><br><sub><a href="svg/schuhkarton-sunset.svg">SVG</a> · <a href="png/schuhkarton-sunset-512.png">PNG 512</a> · <a href="png/schuhkarton-sunset-1024.png">PNG 1024</a> · <a href="jpg/schuhkarton-sunset.jpg">JPG</a></sub></td>
<td align="center"><img src="svg/schuhkarton-nightsky.svg" width="120" alt="Nachthimmel"><br><b>nightsky</b><br><sub>Nachthimmel</sub><br><sub><a href="svg/schuhkarton-nightsky.svg">SVG</a> · <a href="png/schuhkarton-nightsky-512.png">PNG 512</a> · <a href="png/schuhkarton-nightsky-1024.png">PNG 1024</a> · <a href="jpg/schuhkarton-nightsky.jpg">JPG</a></sub></td>
</tr>
<tr>
<td align="center"><img src="svg/schuhkarton-aurora.svg" width="120" alt="Polarlicht"><br><b>aurora</b><br><sub>Polarlicht</sub><br><sub><a href="svg/schuhkarton-aurora.svg">SVG</a> · <a href="png/schuhkarton-aurora-512.png">PNG 512</a> · <a href="png/schuhkarton-aurora-1024.png">PNG 1024</a> · <a href="jpg/schuhkarton-aurora.jpg">JPG</a></sub></td>
<td align="center"><img src="svg/schuhkarton-ocean.svg" width="120" alt="Ozean"><br><b>ocean</b><br><sub>Ozean</sub><br><sub><a href="svg/schuhkarton-ocean.svg">SVG</a> · <a href="png/schuhkarton-ocean-512.png">PNG 512</a> · <a href="png/schuhkarton-ocean-1024.png">PNG 1024</a> · <a href="jpg/schuhkarton-ocean.jpg">JPG</a></sub></td>
<td align="center"><img src="svg/schuhkarton-forest.svg" width="120" alt="Wald"><br><b>forest</b><br><sub>Wald</sub><br><sub><a href="svg/schuhkarton-forest.svg">SVG</a> · <a href="png/schuhkarton-forest-512.png">PNG 512</a> · <a href="png/schuhkarton-forest-1024.png">PNG 1024</a> · <a href="jpg/schuhkarton-forest.jpg">JPG</a></sub></td>
<td align="center"><img src="svg/schuhkarton-fire.svg" width="120" alt="Feuer"><br><b>fire</b><br><sub>Feuer</sub><br><sub><a href="svg/schuhkarton-fire.svg">SVG</a> · <a href="png/schuhkarton-fire-512.png">PNG 512</a> · <a href="png/schuhkarton-fire-1024.png">PNG 1024</a> · <a href="jpg/schuhkarton-fire.jpg">JPG</a></sub></td>
</tr>
<tr>
<td align="center"><img src="svg/schuhkarton-neon.svg" width="120" alt="Neon"><br><b>neon</b><br><sub>Neon</sub><br><sub><a href="svg/schuhkarton-neon.svg">SVG</a> · <a href="png/schuhkarton-neon-512.png">PNG 512</a> · <a href="png/schuhkarton-neon-1024.png">PNG 1024</a> · <a href="jpg/schuhkarton-neon.jpg">JPG</a></sub></td>
<td align="center"><img src="svg/schuhkarton-candy.svg" width="120" alt="Bonbon"><br><b>candy</b><br><sub>Bonbon</sub><br><sub><a href="svg/schuhkarton-candy.svg">SVG</a> · <a href="png/schuhkarton-candy-512.png">PNG 512</a> · <a href="png/schuhkarton-candy-1024.png">PNG 1024</a> · <a href="jpg/schuhkarton-candy.jpg">JPG</a></sub></td>
<td align="center"><img src="svg/schuhkarton-retro.svg" width="120" alt="Retro"><br><b>retro</b><br><sub>Retro</sub><br><sub><a href="svg/schuhkarton-retro.svg">SVG</a> · <a href="png/schuhkarton-retro-512.png">PNG 512</a> · <a href="png/schuhkarton-retro-1024.png">PNG 1024</a> · <a href="jpg/schuhkarton-retro.jpg">JPG</a></sub></td>
<td align="center"><img src="svg/schuhkarton-kraft.svg" width="120" alt="Karton"><br><b>kraft</b><br><sub>Karton</sub><br><sub><a href="svg/schuhkarton-kraft.svg">SVG</a> · <a href="png/schuhkarton-kraft-512.png">PNG 512</a> · <a href="png/schuhkarton-kraft-1024.png">PNG 1024</a> · <a href="jpg/schuhkarton-kraft.jpg">JPG</a></sub></td>
</tr>
<tr>
<td align="center"><img src="svg/schuhkarton-gold.svg" width="120" alt="Gold"><br><b>gold</b><br><sub>Gold</sub><br><sub><a href="svg/schuhkarton-gold.svg">SVG</a> · <a href="png/schuhkarton-gold-512.png">PNG 512</a> · <a href="png/schuhkarton-gold-1024.png">PNG 1024</a> · <a href="jpg/schuhkarton-gold.jpg">JPG</a></sub></td>
<td align="center"><img src="svg/schuhkarton-sepia.svg" width="120" alt="Sepia"><br><b>sepia</b><br><sub>Sepia</sub><br><sub><a href="svg/schuhkarton-sepia.svg">SVG</a> · <a href="png/schuhkarton-sepia-512.png">PNG 512</a> · <a href="png/schuhkarton-sepia-1024.png">PNG 1024</a> · <a href="jpg/schuhkarton-sepia.jpg">JPG</a></sub></td>
<td align="center"><img src="svg/schuhkarton-sunrise.svg" width="120" alt="Sonnenaufgang"><br><b>sunrise</b><br><sub>Sonnenaufgang</sub><br><sub><a href="svg/schuhkarton-sunrise.svg">SVG</a> · <a href="png/schuhkarton-sunrise-512.png">PNG 512</a> · <a href="png/schuhkarton-sunrise-1024.png">PNG 1024</a> · <a href="jpg/schuhkarton-sunrise.jpg">JPG</a></sub></td>
<td align="center"><img src="svg/schuhkarton-copper.svg" width="120" alt="Kupfer"><br><b>copper</b><br><sub>Kupfer</sub><br><sub><a href="svg/schuhkarton-copper.svg">SVG</a> · <a href="png/schuhkarton-copper-512.png">PNG 512</a> · <a href="png/schuhkarton-copper-1024.png">PNG 1024</a> · <a href="jpg/schuhkarton-copper.jpg">JPG</a></sub></td>
</tr>
<tr>
<td align="center"><img src="svg/schuhkarton-bronze.svg" width="120" alt="Bronze"><br><b>bronze</b><br><sub>Bronze</sub><br><sub><a href="svg/schuhkarton-bronze.svg">SVG</a> · <a href="png/schuhkarton-bronze-512.png">PNG 512</a> · <a href="png/schuhkarton-bronze-1024.png">PNG 1024</a> · <a href="jpg/schuhkarton-bronze.jpg">JPG</a></sub></td>
<td align="center"><img src="svg/schuhkarton-silver.svg" width="120" alt="Silber"><br><b>silver</b><br><sub>Silber</sub><br><sub><a href="svg/schuhkarton-silver.svg">SVG</a> · <a href="png/schuhkarton-silver-512.png">PNG 512</a> · <a href="png/schuhkarton-silver-1024.png">PNG 1024</a> · <a href="jpg/schuhkarton-silver.jpg">JPG</a></sub></td>
<td align="center"><img src="svg/schuhkarton-metallic.svg" width="120" alt="Metallic"><br><b>metallic</b><br><sub>Metallic</sub><br><sub><a href="svg/schuhkarton-metallic.svg">SVG</a> · <a href="png/schuhkarton-metallic-512.png">PNG 512</a> · <a href="png/schuhkarton-metallic-1024.png">PNG 1024</a> · <a href="jpg/schuhkarton-metallic.jpg">JPG</a></sub></td>
<td align="center"><img src="svg/schuhkarton-glass.svg" width="120" alt="Glas"><br><b>glass</b><br><sub>Glas</sub><br><sub><a href="svg/schuhkarton-glass.svg">SVG</a> · <a href="png/schuhkarton-glass-512.png">PNG 512</a> · <a href="png/schuhkarton-glass-1024.png">PNG 1024</a> · <a href="jpg/schuhkarton-glass.jpg">JPG</a></sub></td>
</tr>
<tr>
<td align="center"><img src="svg/schuhkarton-neonblue.svg" width="120" alt="Neon Blau"><br><b>neonblue</b><br><sub>Neon Blau</sub><br><sub><a href="svg/schuhkarton-neonblue.svg">SVG</a> · <a href="png/schuhkarton-neonblue-512.png">PNG 512</a> · <a href="png/schuhkarton-neonblue-1024.png">PNG 1024</a> · <a href="jpg/schuhkarton-neonblue.jpg">JPG</a></sub></td>
<td align="center"><img src="svg/schuhkarton-neongreen.svg" width="120" alt="Neon Grün"><br><b>neongreen</b><br><sub>Neon Grün</sub><br><sub><a href="svg/schuhkarton-neongreen.svg">SVG</a> · <a href="png/schuhkarton-neongreen-512.png">PNG 512</a> · <a href="png/schuhkarton-neongreen-1024.png">PNG 1024</a> · <a href="jpg/schuhkarton-neongreen.jpg">JPG</a></sub></td>
<td align="center"><img src="svg/schuhkarton-neonorange.svg" width="120" alt="Neon Orange"><br><b>neonorange</b><br><sub>Neon Orange</sub><br><sub><a href="svg/schuhkarton-neonorange.svg">SVG</a> · <a href="png/schuhkarton-neonorange-512.png">PNG 512</a> · <a href="png/schuhkarton-neonorange-1024.png">PNG 1024</a> · <a href="jpg/schuhkarton-neonorange.jpg">JPG</a></sub></td>
<td align="center"><img src="svg/schuhkarton-neonpink.svg" width="120" alt="Neon Pink"><br><b>neonpink</b><br><sub>Neon Pink</sub><br><sub><a href="svg/schuhkarton-neonpink.svg">SVG</a> · <a href="png/schuhkarton-neonpink-512.png">PNG 512</a> · <a href="png/schuhkarton-neonpink-1024.png">PNG 1024</a> · <a href="jpg/schuhkarton-neonpink.jpg">JPG</a></sub></td>
</tr>
<tr>
<td align="center"><img src="svg/schuhkarton-rainbow.svg" width="120" alt="Regenbogen"><br><b>rainbow</b><br><sub>Regenbogen</sub><br><sub><a href="svg/schuhkarton-rainbow.svg">SVG</a> · <a href="png/schuhkarton-rainbow-512.png">PNG 512</a> · <a href="png/schuhkarton-rainbow-1024.png">PNG 1024</a> · <a href="jpg/schuhkarton-rainbow.jpg">JPG</a></sub></td>
<td align="center"><img src="svg/schuhkarton-toxic.svg" width="120" alt="Giftig"><br><b>toxic</b><br><sub>Giftig</sub><br><sub><a href="svg/schuhkarton-toxic.svg">SVG</a> · <a href="png/schuhkarton-toxic-512.png">PNG 512</a> · <a href="png/schuhkarton-toxic-1024.png">PNG 1024</a> · <a href="jpg/schuhkarton-toxic.jpg">JPG</a></sub></td>
<td align="center"><img src="svg/schuhkarton-vaporwave.svg" width="120" alt="Vaporwave"><br><b>vaporwave</b><br><sub>Vaporwave</sub><br><sub><a href="svg/schuhkarton-vaporwave.svg">SVG</a> · <a href="png/schuhkarton-vaporwave-512.png">PNG 512</a> · <a href="png/schuhkarton-vaporwave-1024.png">PNG 1024</a> · <a href="jpg/schuhkarton-vaporwave.jpg">JPG</a></sub></td>
<td align="center"><img src="svg/schuhkarton-thermal.svg" width="120" alt="Wärmebild"><br><b>thermal</b><br><sub>Wärmebild</sub><br><sub><a href="svg/schuhkarton-thermal.svg">SVG</a> · <a href="png/schuhkarton-thermal-512.png">PNG 512</a> · <a href="png/schuhkarton-thermal-1024.png">PNG 1024</a> · <a href="jpg/schuhkarton-thermal.jpg">JPG</a></sub></td>
</tr>
<tr>
<td align="center"><img src="svg/schuhkarton-negative.svg" width="120" alt="Negativ"><br><b>negative</b><br><sub>Negativ</sub><br><sub><a href="svg/schuhkarton-negative.svg">SVG</a> · <a href="png/schuhkarton-negative-512.png">PNG 512</a> · <a href="png/schuhkarton-negative-1024.png">PNG 1024</a> · <a href="jpg/schuhkarton-negative.jpg">JPG</a></sub></td>
<td align="center"><img src="svg/schuhkarton-sage.svg" width="120" alt="Salbei (gedeckt)"><br><b>sage</b><br><sub>Salbei (gedeckt)</sub><br><sub><a href="svg/schuhkarton-sage.svg">SVG</a> · <a href="png/schuhkarton-sage-512.png">PNG 512</a> · <a href="png/schuhkarton-sage-1024.png">PNG 1024</a> · <a href="jpg/schuhkarton-sage.jpg">JPG</a></sub></td>
<td align="center"><img src="svg/schuhkarton-slate.svg" width="120" alt="Schiefer (gedeckt)"><br><b>slate</b><br><sub>Schiefer (gedeckt)</sub><br><sub><a href="svg/schuhkarton-slate.svg">SVG</a> · <a href="png/schuhkarton-slate-512.png">PNG 512</a> · <a href="png/schuhkarton-slate-1024.png">PNG 1024</a> · <a href="jpg/schuhkarton-slate.jpg">JPG</a></sub></td>
<td align="center"><img src="svg/schuhkarton-clay.svg" width="120" alt="Ton (gedeckt)"><br><b>clay</b><br><sub>Ton (gedeckt)</sub><br><sub><a href="svg/schuhkarton-clay.svg">SVG</a> · <a href="png/schuhkarton-clay-512.png">PNG 512</a> · <a href="png/schuhkarton-clay-1024.png">PNG 1024</a> · <a href="jpg/schuhkarton-clay.jpg">JPG</a></sub></td>
</tr>
<tr>
<td align="center"><img src="svg/schuhkarton-mauve.svg" width="120" alt="Malve (gedeckt)"><br><b>mauve</b><br><sub>Malve (gedeckt)</sub><br><sub><a href="svg/schuhkarton-mauve.svg">SVG</a> · <a href="png/schuhkarton-mauve-512.png">PNG 512</a> · <a href="png/schuhkarton-mauve-1024.png">PNG 1024</a> · <a href="jpg/schuhkarton-mauve.jpg">JPG</a></sub></td>
<td align="center"><img src="svg/schuhkarton-mono.svg" width="120" alt="Schwarzweiß"><br><b>mono</b><br><sub>Schwarzweiß</sub><br><sub><a href="svg/schuhkarton-mono.svg">SVG</a> · <a href="png/schuhkarton-mono-512.png">PNG 512</a> · <a href="png/schuhkarton-mono-1024.png">PNG 1024</a> · <a href="jpg/schuhkarton-mono.jpg">JPG</a></sub></td>
</tr>
</table>

## Building

All files here and the icons in `web/` come from `generate.py` and
`lid.json` (needs Python with Pillow, and Node with Playwright and
Chromium):

```sh
python3 docs/logo/generate.py
```

A variant is a turn of the hue, a factor for the chroma and a shift of the
lightness in OKLCH, applied to the colours of the original; new variants go
into `VARIANTS`.
