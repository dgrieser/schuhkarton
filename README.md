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
- **Descriptions (optional):** an AI model (mittwald AI hosting, vLLM,
  Ollama) describes every photo in German and English and adds tags, so the
  search finds "Hund im Schnee" or "birthday cake".
- **People:** YuNet detection and SFace recognition, clustered into groups. Give
  groups names; equal names merge them. "Nicht diese Person" removes a single
  face from a group.
- **Backup:** copies photos, cache and settings to a folder (a USB disk or a
  share) in the background. It only adds and updates; nothing in the backup is
  ever deleted.
- **Accounts:** viewing works without an account (optional). With an account,
  people keep their own favorites and name faces. Anybody can register; an
  admin approves new accounts or creates them. Admins also manage the
  settings and the photos.
- **Duplicates:** finds identical files, the same picture in other copies
  (smaller, recompressed, turned, another format, WhatsApp) and series of
  nearly the same shot, and suggests the best copy. Admins compare side by
  side and clean up with one tap; what the copies knew moves to the one that
  stays.
- **Notes and marks:** people with an account leave notes on photos (for
  everybody, searchable) and mark photos for deletion (hidden for them, an
  admin decides).
- German and English (the browser's language, switchable in the sidebar); the
  search syntax works in both (`ort:`/`place:`, `monat:juni`/`month:june`).
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
   key or without; whether people may look without an account and whether
   they may register; the title and the default theme.
6. **Admin:** the first admin account (name and password).
7. **Summary:** saves the settings, signs the admin in and opens the gallery.

Later, the same pages are available to admins under **Einstellungen** in the
sidebar, with a **Benutzer** page instead of the admin step. Saving there
restarts the gallery in place.

## Accounts

| Who | May |
|---|---|
| Without an account | look at and download photos, when *Ohne Konto ansehen* (`access.anonymous`) is on |
| Account (`user`) | also set their own favorites and name people |
| Admin | also change settings, approve and manage accounts, rescan, back up, and edit photos |

Anybody can create an account on the sign-in page (`/login`) when
`access.register` is on; it stays pending until an admin approves it
(*Einstellungen → Benutzer*, where admins also create accounts, change roles,
lock accounts and set passwords). Everybody with an account changes their own
password in the sidebar.

The access key (`server.token`) only applies to viewers without an account;
signed-in people do not need it. Accounts live in `users.json` next to
`config.yaml` (argon2id password hashes, mode 0600); sessions last 90 days
from the last visit. Repeated wrong passwords from one address are slowed
down.

Updating from a version without accounts: the gallery keeps running as
before and prints a one-time link to create the first admin; the favorites
everybody shared so far become that admin's favorites. On the command line:

```sh
schuhkarton user add Anna admin   # asks for the password
schuhkarton user list
schuhkarton user approve Ben
schuhkarton user passwd Anna      # the way back in after a lost password
schuhkarton user role Ben admin | disable Ben | enable Ben | del Ben
```

Folders given on the command line skip the setup:

```sh
schuhkarton ~/Bilder Urlaub=/mnt/nas/Urlaub
schuhkarton index -previews /mnt/nas/Bilder   # build the whole cache once, then exit
```

## Photos, videos and documents

Files below folders named in `library.documents` (Dokumente, Documents,
Scans, Belege) are documents. Galleries show photos and videos; three
switches above every gallery add documents or hide photos or videos, in any
mix, remembered per device. A search that hits hidden documents says so and
offers to show them; `typ:dokument` (`type:document`), `kategorie:` and
`album:Dokumente` always include them. *Alben* shows everything.

## Duplicates (admins)

Schuhkarton compares the thumbnails in the background (a few milliseconds
per photo, no container needed): several perceptual hashes for every turn
and mirror image, plus colours and aspect ratio. *Duplikate* in the sidebar
shows what it found:

- **Identisch:** the same file, byte for byte.
- **Gleich:** the same picture as another copy: smaller, saved again,
  turned or mirrored, in another format, without metadata (WhatsApp).
- **Ähnlich:** a series: nearly the same shot within a few minutes. Two
  photos with camera times seconds apart are never "gleich".

Each group suggests the best copy and says why (resolution, original
format, camera date, position, Live Photo, favorites, named people …).
Compare the copies side by side (differences highlighted; tap a picture to
zoom), pick what stays, apply. On a desktop: ←/→ groups, 1–9 pick, Enter
apply, I "Keine Duplikate". *Alle Vorschläge übernehmen* cleans up every
identical and same group at once. The copies that go land in the trash
(undo there); favorites of every account, names of faces, place, category,
text, position, a camera date, notes and the AI description move to the
copy that stays. "Keine Duplikate" remembers that photos only look alike.

`dupes.sensitivity` (strict, normal, loose) trades sure matches for more
matches; `dupes.mode: off` switches the search off. Photos of the same scene
with a different framing are beyond perceptual hashes.

## Notes and marks for deletion

Everybody with an account can write notes on a photo or video (in the
details: who, what, where). Everybody who can see the photo reads them; the
search finds them in plain words, with `notiz:` / `note:` and with
`typ:notiz` / `type:note`. Authors change and delete their own notes,
admins any.

The flag in the viewer (key **M**) or in the selection marks photos for
deletion: they disappear for the account that marked them and show up in
*Vorgemerkt*, where they can be brought back. Admins see every account's
marks there (*Alle Konten*, the most marked first) and move them to the
trash or remove the marks.

## Editing (admins)

Admins see extra buttons in the viewer (also keys **E** edit, **R** rotate,
**Del** delete) and in the selection bar:

- **Edit:** date and time, place, position on the map, category and the
  picture text. For a selection: set the same values, or shift the date (a
  camera with a wrong clock). Changed fields are marked.
- **Rotate** photos by 90°.
- **Move** to any folder of the library, or a new one. Live Photos move with
  their video; names that are taken get " (2)".
- **Delete** moves files to the trash (`<folder>/.schuhkarton-papierkorb/`),
  with undo. *Papierkorb* in the sidebar restores them or deletes them for
  good; the trash empties itself after `library.trash_days`.

With `library.write_files` on (the default), edits are also written into the
files with exiftool (dates, GPS, `XMP-dc:Description`, `XMP-photoshop:City`
and `Category`; rotation as EXIF orientation for JPEG and TIFF). Off, they
stay in the cache (`edits.json`) only. Formats exiftool cannot rotate (HEIC,
PNG, …) keep the rotation in the cache either way. Favorites, names of
people and cached previews follow moved and edited files.

A library folder that cannot be read when the folders are scanned (a
network share that is down, an empty mount point) keeps what it had, and so
do subfolders that cannot be read; the sidebar says which folder is not
reachable. An empty library folder counts as emptied on purpose only when it
is the same directory it was while it had files (device and inode, on Windows
volume and file index, noted in `roots.json` in the cache) or when most of its
files from before are there again (a remount); otherwise it is a mount point
with nothing mounted and keeps its files. The same holds for shares mounted
below a library folder, once Schuhkarton has seen them mounted.

## Filing rules (admins)

Schuhkarton does not impose a folder layout: without rules nothing moves.
*Ablage* in the sidebar sets rules for where files should live, from their
metadata. Each rule has a condition in the search syntax (`typ:dokument`,
`ort:Berlin`, empty for every file) and a folder template; every file follows
the first rule that matches, files no rule matches stay where they are.

```
Dokumente/{jahr}/{datum:MM MMMM} {kategorie}     → Dokumente/2023/03 März Rechnung
{jahr}/{datum:MM MMMM} {ort|Kamera}              → 2019/06 Juni Lübbecke
```

- Fields: `jahr`, `monat`, `datum`, `ort`, `kategorie`, `kamera`, `typ`,
  `schlagwort`, `person`; the English names `year`, `month`, `date`, `place`,
  `category`, `camera`, `kind`, `keyword` work as well.
- `{datum:…}` formats the capture date with Unicode (CLDR/ICU) date symbols:
  `yyyy` 2025, `yy` 25, `MM` 07, `M` 7, `MMMM` Juli, `MMM` Jul, `dd` 05,
  `EEEE` Samstag, `QQQ` Q3, `ww` ISO week; text in single quotes stays as it is.
  Month and weekday names come in the language set for the rules (de, en, fr,
  es, it, nl, da, sv, pl, pt).
- `{field|text}` uses the text when the value is missing. Values never create
  folders of their own (`/` becomes `-`), folder names lose leading and
  trailing dots and spaces.

The page shows live, for every rule, how many files it decides, how many
would move and a few resulting paths. *Wie bisher* fills in the two rules
above, which describe the layout the library was built with. *Jetzt
einsortieren* moves everything into place in the background (progress in the
sidebar); with `auto` on, a file moves as soon as an admin changes its date,
place or category, several files at once in the background. Live Photo videos
move with their photo, emptied folders are removed, and a place, date or
category that so far came only from a folder name is kept as an edit.
Documents are told by their folder, so a rule never moves a document out of
a documents folder or a photo into one, nor a file into a folder the scan
skips (`library.exclude`); such files stay and the preview says how many. Saving
other rules during a run stops it after the current batch; the new rules
apply with the next run.

### Import folder

`library.import.dir` (*Einstellungen → Ordner → Import-Ordner*) names a folder
that empties itself into the library: photos and videos put there, in
subfolders too, move in on their own once they are completely written (same
size twice, a few seconds old; `.part`, `.crdownload` and the like wait). They
land below the library folder chosen with `library.import.folder` (default:
the first) with their subfolders, and the filing rules then put them in place
(also with `auto` off). Without rules they stay there. Names that are taken
get " (2)", Live Photo pairs and `.xmp`/`.aae` sidecars stay together (also
when one half comes later, up to a year, as long as the first one is still in
the library), emptied
subfolders are removed, and files that are no photos or videos stay in the
import folder. The import folder must not lie inside a library folder.

## Image descriptions (optional)

An AI model can describe every photo: one or two sentences and 8–20 tags,
in German and English (for example "Ein Hund spielt im Schnee" with *hund,
schnee, winter* and *dog, snow, winter*). The search then also finds words
that appear in no folder name or metadata, in both languages; the tags show
under *Beschreibung* in the details and are suggested while typing. `ki:` or
`ai:` searches only there (`ki:geburtstag`).

It is off by default, because every photo is sent, downscaled to 1024 px,
to the chosen service. *Einstellungen → KI* sets it up:

- **mittwald AI hosting** (OpenAI-compatible, hosted in Germany) with an API
  key and a model that understands images, e.g. `Qwen3.8-27B-NVFP4`.
- **Own service**: any OpenAI-compatible API, e.g. vLLM
  (`http://host:8000/v1`) or Ollama (`http://host:11434/v1`), with a
  vision model; no key needed.

The key can be entered, or given as `$MITTWALD_LLM_API_KEY` (any variable
name): then only the name is stored and the key stays in the environment.
*Modelle laden* lists the models of the service, and *Verbindung testen* lets
the model describe a small drawn picture (no photo).

Descriptions are made in the background after the thumbnails, newest photos
first, a few at a time (`ai.workers`); the sidebar shows the progress. They
live in `ai.json` in the cache and follow moved and edited photos. A wrong
key or model stops the job with a message in the sidebar; a busy service
pauses it. Admins can have single photos or a selection described again.
Names of people are never asked for or stored.

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

Signed-in accounts can hide a group of faces on its person page (*Ausblenden*).
A toast offers to undo it right away, and *Personen → Ausgeblendet* lists every
hidden group with a few of its faces and a *Wieder zeigen* button. Groups hidden
with this version come back exactly as they were, including their names; faces
hidden by older versions are grouped by similarity.

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
  token: auto                 # viewers without an account: auto (key needed only on a network address), off, or the key itself
  public_url: ""              # e.g. https://fotos.example.org behind a reverse proxy
  tls_cert: ""                # with tls_key: serve HTTPS directly
  tls_key: ""
access:
  anonymous: true             # look and download without an account
  register: true              # anybody may create an account; an admin approves it
library:
  folders:
    - {name: Bilder, path: /home/me/Bilder}
  exclude: [_Duplikate, .*, "@eaDir", "#recycle", "$RECYCLE.BIN", System Volume Information, "*.tmp"]
  rescan: ""                  # e.g. 1h; empty: only at start
  folder_meta: true           # read date, place and category from folder names like 2019/06 Juni Ort
  write_files: true           # write admin edits into the files (exiftool); false: cache only
  trash_days: 30              # empty the trash after this many days; 0: never
  documents: [Dokumente, Documents, Scans, Belege]  # folders whose files are documents
  import:                     # import folder (see "Import folder")
    dir: ""                   # e.g. /home/me/Import; empty: none
    folder: ""                # library folder to import into; empty: the first
  rules:                      # filing rules (see "Filing rules"); no list: nothing moves
    locale: de                # language of month and weekday names
    auto: true                # move files right after an admin edits date, place or category
    list:
      - {name: Dokumente, when: "typ:dokument", path: "Dokumente/{jahr}/{datum:MM MMMM} {kategorie}"}
      - {name: Fotos, path: "{jahr}/{datum:MM MMMM} {ort|Kamera}"}
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
ai:                           # image descriptions by a vision model (optional)
  mode: off                   # off or on
  url: https://llm.aihosting.mittwald.de/v1   # any OpenAI-compatible API (vLLM, Ollama: http://host:11434/v1)
  api_key: ""                 # the key itself, or:
  api_key_env: ""             # the name of a variable holding it, e.g. MITTWALD_LLM_API_KEY
  model: Qwen3.8-27B-NVFP4    # a model that understands images
  workers: 2                  # requests at the same time
  languages: [de, en]
  image_size: 1024            # long edge of the image sent
  thinking: false             # let reasoning models think first (slower)
ui:
  title: Schuhkarton
  theme: system               # default for new devices: system, light, dark
  language: auto              # default for new devices: auto (the browser's), de, en
  map_tiles: https://tile.openstreetmap.org/{z}/{x}/{y}.png
  map_attribution: © OpenStreetMap
dupes:
  mode: "on"                  # search for duplicates; off
  sensitivity: normal         # strict, normal, loose
  similar_minutes: 10         # a series: photos at most this far apart
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
and `config/users.json` (they contain tokens and password hashes: keep the
backup private) and
`schuhkarton-backup.json` (the last run). Files that are new or differ in size
or time are copied; browser transcodes are left out because they can be made
again.

To restore, copy the photos back, start Schuhkarton and choose *Anderen Cache
übernehmen* in the setup with the backup's `cache/` folder. Cached files match
when the folder names are the same.

## Data

Originals change only when an admin edits, rotates, moves or deletes them
(see [Editing](#editing-admins)). Everything derived lives in the cache:
`index.gob.gz`, `thumbs/` (thumbnails, previews, video previews, transcodes),
`faces/` (crops), `people.json` (names), `favorites.json` (per account),
`edits.json` (admin edits), `trash.json`, `notes.json`, `marks.json` (per account), `dupes.json` (hashes), `token`. One process per cache; a second one refuses to start. Accounts are
not in the cache but in `users.json` next to the settings.

Any folder layout works; Schuhkarton never sorts or renames files on its own.
With `library.folder_meta` on (the default) it also reads folder names:
`<Jahr>/<MM Monat Ort>/` gives the place, and
`<Sammlung>/<Jahr>/<MM Monat Kategorie>/` a category. When a camera date
disagrees with such a folder, the folder wins (cameras with reset clocks).
Edits by an admin always win over folder names and metadata. Without a
folder value, `XMP-photoshop:City` and `XMP-photoshop:Category` in the file
give place and category.

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
| `internal/gallery` | AI descriptions (OpenAI-compatible client), scanning, metadata (exiftool), dates and places, thumbnails (pure Go incl. HEIC/AVIF), video previews, face backends (container, local), clustering, search and filters, cache import |
| `internal/auth` | accounts, sessions, password hashes, login rate limit |
| `internal/server` | JSON API, media, sign-in and permissions, ZIP, setup and settings API |
| `internal/setup` | folder and tool checks, folder picker, model download |
| `internal/backup` | background backup |
| `internal/worker`, `internal/helper` | face recognition service and its Python helper |
| `web` | the UI (vanilla JS, Leaflet from unpkg), embedded into the binary |
| `Dockerfile.faces`, `deploy/` | face container, compose example |
