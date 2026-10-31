# Schuhkarton

The digital shoebox: a fast photo and video gallery for your own folders,
served by one small Go binary and used in the browser on desktop, tablet and
phone.

- **Views:** timeline (justified rows, square grid or mosaic, grouped by year,
  month or day, with a year scrubber), memories ("on this day"), map, places,
  people, albums (folder tree), videos, favorites.
- **Search:** places, people, dates (German month and weekday names, seasons,
  "weihnachten"), folders, camera, keywords and the **text in the pictures**
  (`XMP-dc:Description`, e.g. OCR text). Filters: `ort:`, `person:`,
  `jahr:2019`, `monat:juni`, `typ:video|live|selfie|screenshot|panorama`,
  `kamera:`, `album:`, `text:`, `datum:2018-06`, and `-wort` to exclude.
  Suggestions while typing; text matches are highlighted on the tiles.
- **Viewer:** zoom (wheel, pinch, double tap), swipe, slideshow, filmstrip,
  details with mini map, camera data, people and the full picture text, face
  boxes with naming, Live Photos (hover or long press), favorites, download.
- **Videos:** animated previews made from a few short snippets of each video
  (play on hover, or always with the play toggle), H.264 transcoding on the fly
  for browsers that cannot play HEVC.
- **Faces:** YuNet detection and SFace recognition (OpenCV), clustered into
  people; name a group and equal names merge.
- **Touch:** pinch the grid to change the tile size, long press to select,
  bottom sheet details on phones and tablets.
- Light, dark or system theme. Selection with ZIP download.

## Requirements

- Go 1.25+ to build.
- `ffmpeg` (video posters, previews, transcoding).
- `exiftool` for metadata (dates, GPS, camera, descriptions). Without it,
  dates come from file and folder names.
- Optional: Python 3 with Pillow, `pillow_heif` and OpenCV for HEIC/AVIF and
  faces. Face models: `face_detection_yunet_2023mar.onnx` and
  `face_recognition_sface_2021dec.onnx` from
  [opencv_zoo](https://github.com/opencv/opencv_zoo), placed in
  `~/.cache/schuhkarton/models` (or `-models DIR`).

## Use

```sh
make install                                   # into ~/bin
schuhkarton ~/Downloads/Bilder                 # http://127.0.0.1:8088
schuhkarton -listen 0.0.0.0:8088 ~/Bilder      # whole home network, prints a link with ?t=<token>
schuhkarton Familie=/mnt/nas/Bilder Urlaub=~/Urlaub
schuhkarton index -previews /mnt/nas/Bilder    # build the whole cache once, then exit
```

Everything derived lives in the cache (`~/.cache/schuhkarton`): index, thumbnails,
video previews, transcodes, face crops, favorites and people names. Originals
are only read, never changed. On a slow network share, run `schuhkarton index
-previews` once so browsing only touches the local cache.

`schuhkarton -h` lists all options (`-exclude`, `-workers`, `-rescan 30m`,
`-no-faces`, `-token`, `-exiftool`, `-python`, `-models`, `-web`).

## Layout

| Path | Content |
|---|---|
| `cmd/schuhkarton` | command line, HTTP server start |
| `internal/gallery` | scanning, metadata (exiftool), dates and places, thumbnails, video previews, Python helper (HEIC, faces), face clustering, search |
| `internal/server` | JSON API, media endpoints, token auth, ZIP download |
| `web` | the single-page UI (vanilla JS, Leaflet from unpkg), embedded into the binary |

Folder conventions understood: `<Jahr>/<MM Monat Ort>/` gives the place,
`<Sammlung>/<Jahr>/<MM Monat Kategorie>/` a category. When the camera date
disagrees with such a folder, the folder wins (reset camera clocks).

## Development

```sh
make dev     # UI from ./web, reload the browser after edits
make check   # gofmt, vet, JS syntax, tests
```
