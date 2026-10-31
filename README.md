# Schuhkarton

The digital shoebox: a fast photo and video gallery for your own folders. One
small binary serves it to the browser on desktop, tablet and phone. Face
recognition is optional and runs in a separate container, on the same machine
or another one.

- **Views:** a timeline (justified rows, square grid or mosaic, grouped by year,
  month or day, with a year scrubber), memories ("on this day"), a map, places,
  people, albums (the folder tree), videos and favorites.
- **Search without syntax:** a filter panel with people, places, year, month,
  season, weekday, weekend or workday, time of day, type (photos, videos, Live
  Photos, selfies, screenshots, panoramas, with text, with location,
  favorites), camera and category. Several values of one kind mean "or";
  different kinds mean "and". Plain words work too ("sommer 2019 lübbecke"),
  and they search the **text in the pictures** (`XMP-dc:Description`, e.g.
  OCR text). Text matches are highlighted on the tiles.
- **Syntax for power users:** `ort:`, `person:`, `jahr:2019`, `monat:juni`,
  `jahreszeit:sommer`, `wochentag:samstag`, `tag:wochenende|werktag`,
  `tageszeit:abends`, `typ:video|live|selfie|screenshot|panorama|text|gps|favorit`,
  `kamera:`, `kategorie:`, `album:`, `text:`, `datum:2018-06`; `-` excludes
  (`-typ:screenshot`), quotes group words (`ort:"Porta Westfalica"`).
- **Viewer:** zoom (wheel, pinch, double tap), swipe, slideshow, filmstrip, and
  details with a mini map, camera data, people and the full picture text. Also
  face boxes with naming, Live Photos, favorites and download.
- **Videos:** animated previews made from short snippets of each video, and
  on-the-fly H.264 for browsers that cannot play HEVC.
- **People:** YuNet detection and SFace recognition, clustered into groups. Give
  groups names; equal names merge them. "Nicht diese Person" removes a single
  face from a group.
- **Backup:** copies photos, cache and settings to a folder (a USB disk or a
  share) in the background. It only adds and updates; nothing in the backup is
  ever deleted.
- Light, dark or system theme. Pinch the grid to resize it, long press to
  select, download a selection as a ZIP.

## Install

Download the archive for your system from the
[releases](https://github.com/dgrieser/schuhkarton/releases): Linux (amd64,
arm64, armv7, e.g. a Raspberry Pi), macOS (Intel, Apple Silicon) or Windows
(amd64, arm64). It contains one binary.

Or build it with Go 1.26 or newer: `make install` installs it into `~/bin`.

Optional tools, found on the `PATH`:

| Tool | For | Install |
|---|---|---|
| `exiftool` | capture date, GPS, camera, picture text | `apt install libimage-exiftool-perl`, `brew install exiftool` |
| `ffmpeg` | video thumbnails, previews, HEVC playback | `apt install ffmpeg`, `brew install ffmpeg` |

Without exiftool, dates come from file and folder names. HEIC and AVIF photos
are decoded by the binary itself; no extra tool is needed.

## First start

```sh
schuhkarton                              # http://127.0.0.1:8088
schuhkarton -listen 0.0.0.0:8088         # reachable from the home network (e.g. on a NAS or a Pi)
```

On the first start Schuhkarton prints a one-time setup link (`/setup?token=…`).
The setup page walks through:

1. **Photos:** pick one or more folders. There is a folder browser and quick
   picks like *Pictures*, and every folder is checked and counted.
2. **Storage:** where the cache lives. If an **existing cache** turns up (for
   example from an older version or a backup), you can use it as it is or
   copy it in. Its thumbnails, faces, names and favorites are then there at
   once. Also choose preview options and the rescan interval; this step shows
   whether exiftool and ffmpeg were found.
3. **Faces:** off (the default), or the face recognition container, with the
   `docker run` command to copy, a token and a connection test.
4. **Backup:** the folder, how often, and what to back up.
5. **Access:** this computer only or the whole home network, with an access
   key or without, plus the title and the default theme.
6. **Summary:** saves the settings and opens the gallery.

Later, the same pages are available under **Einstellungen** in the sidebar.
Saving there restarts the gallery in place.

Folders given on the command line skip the setup:

```sh
schuhkarton ~/Bilder Urlaub=/mnt/nas/Urlaub
schuhkarton index -previews /mnt/nas/Bilder   # build the whole cache once, then exit
```

## Face recognition container

The gallery stays one binary; face recognition (Python, OpenCV and the
YuNet/SFace models) comes as the `schuhkarton-faces` image for amd64 and arm64:

```sh
docker run -d --name schuhkarton-faces --restart unless-stopped -p 8089:8089 \
  -e SCHUHKARTON_WORKER_TOKEN=<token> ghcr.io/dgrieser/schuhkarton-faces:latest
```

or with [deploy/compose.yaml](deploy/compose.yaml). Then choose
*Einstellungen → Gesichter → Container* and enter `http://<host>:8089` and the
token. The gallery sends one 1600 px JPEG per photo (HEIC as the original) and
stores the faces, embeddings and crops in its cache. Clustering into people
happens in the gallery. If the container is unreachable, the scan resumes when
it is back.

Without Docker, `schuhkarton worker` runs the same service wherever Python 3
with `opencv-python-headless`, `pillow` and `pillow-heif` is installed (the
models come from `schuhkarton models download`). In the gallery,
`faces.mode: local` uses such a Python on the same machine.

## Configuration

The setup page writes `config.yaml` to `$XDG_CONFIG_HOME/schuhkarton/` (macOS:
`~/Library/Application Support/schuhkarton/`, Windows: `%AppData%\schuhkarton\`).
`-config FILE` or `SCHUHKARTON_CONFIG` names another file. Every key can also
be set as an environment variable (`faces.url` → `SCHUHKARTON_FACES_URL`;
folders as `Name=/path,/other/path`) or with a flag (`schuhkarton -h`).
Settings fixed by the environment or a flag show as locked on the settings
page. `schuhkarton config` prints the effective configuration, and
`schuhkarton doctor` checks folders, cache, tools and the face service.

```yaml
server:
  listen: 127.0.0.1:8088      # 0.0.0.0:8088 for the home network
  token: auto                 # auto: required only on a network address; off; or the key itself
  public_url: ""              # e.g. https://fotos.example.org behind a reverse proxy
  tls_cert: ""                # with tls_key: serve HTTPS directly
  tls_key: ""
library:
  folders:
    - {name: Bilder, path: /home/me/Bilder}
  exclude: [_Duplikate, .*, "@eaDir", "#recycle", "$RECYCLE.BIN", System Volume Information, "*.tmp"]
  rescan: ""                  # e.g. 1h; empty: only at start
cache:
  dir: ~/.cache/schuhkarton
  previews: false             # true: large previews in advance (slow network shares)
media:
  workers: 12
  thumb_size: 360             # short edge of grid thumbnails
  preview_size: 2048          # long edge of lightbox previews
  jpeg_quality: 82
  video_previews: true
  video_preview_size: 240
  transcode: true             # H.264 for browsers without HEVC
  ffmpeg: ""                  # paths, if not on the PATH
  exiftool: ""
faces:
  mode: off                   # off, remote (container) or local (Python here)
  url: ""                     # remote: http://host:8089
  token: ""                   # remote: the container's SCHUHKARTON_WORKER_TOKEN
  workers: 6
  min_score: 0.86             # detection threshold
  cluster_threshold: 0.48     # similarity to join an unnamed group (higher: more, purer groups)
  named_threshold: 0.42       # similarity to join a named person
  python: ""                  # local
  models_dir: ""              # local
ui:
  title: Schuhkarton
  theme: system               # default for new devices: system, light, dark
  map_tiles: https://tile.openstreetmap.org/{z}/{x}/{y}.png
  map_attribution: © OpenStreetMap
backup:
  dir: ""                     # e.g. /media/usb/schuhkarton; empty: no backup
  interval: 24h               # 0: only with "Jetzt sichern"
  originals: true
  cache: true
  config: true
worker:                       # for "schuhkarton worker"
  listen: 0.0.0.0:8089
  token: ""
  workers: 12
```

Thumbnail sizes apply to new thumbnails; to rebuild all of them, delete
`thumbs/` in the cache folder.

## Backup and restore

The backup folder holds `originals/<folder name>/…`, `cache/…` (index,
thumbnails, video previews, faces, people, favorites), `config/config.yaml`
(it contains tokens: keep the backup private) and
`schuhkarton-backup.json` (the last run). Files that are new or differ in size
or time are copied; browser transcodes are left out because they can be made
again.

To restore, copy the photos back, start Schuhkarton and choose *Anderen Cache
übernehmen* in the setup with the backup's `cache/` folder. Cached files match
when the folder names are the same.

## Data

Originals are only read, never changed. Everything derived lives in the cache:
`index.gob.gz`, `thumbs/` (thumbnails, previews, video previews, transcodes),
`faces/` (crops), `people.json` (names), `favorites.json`, `token`. One process
per cache; a second one refuses to start.

Folder conventions understood: `<Jahr>/<MM Monat Ort>/` gives the place, and
`<Sammlung>/<Jahr>/<MM Monat Kategorie>/` a category. When a camera date
disagrees with such a folder, the folder wins (cameras with reset clocks).

## Release

The workflows run on a self-hosted runner (labels `self-hosted, Linux, X64,
ubuntu`), like rootle's. Without one, `GITHUB_TOKEN=$(gh auth token) make
release` publishes a release from a clean checkout of the tag.

Pushing a tag `v*` runs GoReleaser (`.goreleaser.yaml`). It builds the
binaries for Linux (amd64, arm64, armv7), macOS (amd64, arm64) and Windows
(amd64, arm64) with `CGO_ENABLED=0` and publishes them with checksums as a
GitHub release. The face container is built for linux/amd64 and linux/arm64
and pushed to `ghcr.io/dgrieser/schuhkarton-faces`: `latest` and the version
on tags, `edge` from main.

```sh
git tag v0.1.0 && git push origin v0.1.0
```

## Development

```sh
make dev          # UI from ./web: reload the browser after edits
make check        # gofmt, vet, JS syntax, tests
make build-all    # cross-compile every release target into dist/
make snapshot     # GoReleaser dry run
make faces-image  # build the face container locally
```

| Path | Content |
|---|---|
| `cmd/schuhkarton` | commands (serve, index, worker, doctor, config, models), the runner that swaps setup and gallery |
| `internal/config` | YAML, `SCHUHKARTON_*` variables, flags, locked keys |
| `internal/gallery` | scanning, metadata (exiftool), dates and places, thumbnails (pure Go incl. HEIC/AVIF), video previews, face backends (container, local), clustering, search and filters, cache import |
| `internal/server` | JSON API, media, token auth, ZIP, setup and settings API |
| `internal/setup` | folder and tool checks, folder picker, model download |
| `internal/backup` | background backup |
| `internal/worker`, `internal/helper` | face recognition service and its Python helper |
| `web` | the UI (vanilla JS, Leaflet from unpkg), embedded into the binary |
| `Dockerfile.faces`, `deploy/` | face container, compose example |
