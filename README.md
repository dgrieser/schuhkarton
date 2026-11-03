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
  `tageszeit:abends`, `typ:video|live|selfie|screenshot|panorama|text|gesprochen|gps|favorit`,
  `kamera:`, `kategorie:`, `album:`, `text:`, `datum:2018-06`; `-` excludes
  (`-typ:screenshot`), quotes group words (`ort:"Porta Westfalica"`).
- **Viewer:** zoom (wheel, pinch, double tap), swipe, slideshow, filmstrip, and
  details with a mini map, camera data, people and the full picture text. Also
  face boxes with naming, Live Photos, favorites, download and printing
  (full resolution, turned like on screen, HEIC and RAW too; `P` or Ctrl+P
  in the viewer).
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

On Linux there are also packages with a system service (see
[Running as a service](#running-as-a-service)):

| Format | Systems | Install |
|---|---|---|
| `.deb` | Debian, Ubuntu, Raspberry Pi OS | `sudo apt install ./schuhkarton_<version>_arm64.deb` |
| `.rpm` | Fedora, RHEL, openSUSE | `sudo dnf install ./schuhkarton-<version>-1.x86_64.rpm` |
| `.pkg.tar.zst` | Arch Linux | `sudo pacman -U schuhkarton-<version>-1-x86_64.pkg.tar.zst` |
| `.apk` | Alpine (OpenRC) | `apk add --allow-untrusted schuhkarton_<version>_x86_64.apk` |

The Linux binary is fully static: it runs on any glibc or musl system, older
ones like Raspberry Pi OS 11 (bullseye) included.

Or build it with Go 1.26 or newer: `make install` installs it into `~/bin`.

Optional tools, found on the `PATH`:

| Tool | For | Install |
|---|---|---|
| `exiftool` | capture date, GPS, camera, picture text | `apt install libimage-exiftool-perl`, `brew install exiftool` |
| `ffmpeg` | video thumbnails, previews, HEVC playback | `apt install ffmpeg`, `brew install ffmpeg` |

Without exiftool, dates come from file and folder names. HEIC and AVIF photos
are decoded by the binary itself; no extra tool is needed. On 32-bit ARM (a
Raspberry Pi with the 32-bit OS) that decoder is very slow (minutes per
photo); install `libheif-examples` there and `heif-dec`/`heif-convert` is used
instead.

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
   once. Also choose preview options, the rescan interval and the time
   window for background work; this step shows whether exiftool and ffmpeg
   were found.
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

## Running as a service

The Linux packages install a systemd system service, a systemd user service
and the user `schuhkarton`. Nothing starts on its own.

**System service** (a Raspberry Pi, a home server): runs as the user
`schuhkarton` with its [data folder](#one-data-folder) in
`/var/lib/schuhkarton` (`config.yaml`, `users.json`, `cache/`).

```sh
sudo systemctl enable --now schuhkarton
journalctl -u schuhkarton                  # the setup link
```

- `/etc/default/schuhkarton` holds environment variables for the service. As
  shipped it sets `SCHUHKARTON_SERVER_LISTEN=0.0.0.0:8088`, so the setup link
  works from another computer in the home network. Settings given there are
  shown as fixed on the settings page. Change them in the file and run
  `sudo systemctl restart schuhkarton`.
- The photo folders must be readable by `schuhkarton`, and writable for admin
  edits, moves and the import folder (group membership or ACLs, e.g.
  `setfacl -R -m u:schuhkarton:rwX /srv/photos`, or `uid=`/`gid=` options for
  an SMB mount). Or run the service as your own user:

  ```sh
  sudo systemctl edit schuhkarton
  # [Service]
  # User=pi
  # Group=pi
  ```

  systemd then gives `/var/lib/schuhkarton` to that user.
- Command line tools with the same data:
  `sudo -u schuhkarton schuhkarton -home /var/lib/schuhkarton user list`.
- Photos on a network share: add the mount to `/etc/fstab` (with
  `_netdev`); the service starts after remote file systems. If a share is
  offline, the gallery keeps its photos (see [Data](#data)).
- Updates through the package restart a running service. Removing the
  package stops it; `/var/lib/schuhkarton` and the user stay.

**User service** (a desktop): runs with your own account and the usual
folders (`~/.config/schuhkarton`, `~/.cache/schuhkarton`).

```sh
systemctl --user enable --now schuhkarton
journalctl --user -u schuhkarton           # the setup link
loginctl enable-linger                     # optional: keep running while logged out
```

Variables for it, such as an API key, go into
`~/.config/schuhkarton/environment` (`KEY=value` lines).

**Alpine** uses an OpenRC service with the same files:
`rc-update add schuhkarton && rc-service schuhkarton start`; the log is
`/var/log/schuhkarton.log`; another user goes into `/etc/conf.d/schuhkarton`
as `command_user="pi:pi"`.

Without a package, the unit files are in the archives under
`deploy/systemd/` (`schuhkarton-user.service` is the user service).

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
first, a few at a time (`ai.workers`). The sidebar shows the progress with
the speed over the last hour and the time left (in the narrow sidebar a ring,
details on hover); *Einstellungen → KI* shows the same with failures and
tokens used. They live in `ai.json` in the cache and follow moved and edited
photos. A wrong key or model stops the job with a message in the sidebar. A
busy service (rate limit, HTTP 429 or 5xx) pauses it for a minute, then up
to 30 minutes, and the sidebar says until when. Admins can have single photos
or a selection described again. Names of people are never asked for or
stored.

**Videos** are described from what happens in them, not from one frame.
ffmpeg makes a small silent clip for the model: H.264, at most 480 px on the
long edge and 32 frames, two per second for short videos and spread over the
whole length of longer ones (key frames only, which is fast). It goes to the
service as a `video_url` together with the words spoken in the video, so a
video waits for speech recognition when that is on. With
`Qwen3.8-27B-NVFP4` at mittwald a video takes about 2,500–3,000 tokens and
2–3 seconds (a photo about 1,100). Services that take no videos (the answer
names "video") get 8 frames of the clip as pictures instead, which is
remembered until the settings change. Without ffmpeg, or when the clip
cannot be made, the preview frame is described (the clip is tried again
after a week). Video descriptions made
from one frame by older versions are made again from the clip.

### Text recognition (OCR)

The same service can read the text in pictures: letters, recipes, notes,
receipts, signs, handwriting too. *Einstellungen → KI → Text erkennen*
switches it on for **documents only** (pictures in documents folders) or
**all photos**; it also works with descriptions off. The default model is
`GLM-OCR` at mittwald AI hosting, which reads handwriting far better than
classic OCR (Tesseract, PaddleOCR). Any vision model of an own service works
too; models other than GLM-OCR get a plain "write down the text" prompt.

Photos are sent from the original file, at most 2048 px on the long edge.
The text lives in `ocr.json` in the cache, follows moved files, is found by
the search (`text:` searches only text in pictures) and shows under *Text
im Bild* in the details. A description in the file (`XMP-dc:Description`)
shows below it as *Text aus der Datei*. Admins can have a picture read again
(*Neu erkennen*). Progress, pauses and failures show like those of the
descriptions.

### Speech recognition

*Einstellungen → KI → Sprache erkennen* writes down what is said in videos
and Live Photos with a transcription model behind `/audio/transcriptions`;
the default is `whisper-large-v3-turbo` at mittwald AI hosting. When it
hears nothing although the sound is loud enough for speech, a second model
listens (`ai.speech_fallback`, default `cohere-transcribe-03-2026`): it catches
speech in noisy clips that whisper drops, but gives no confidence values, so
it only gets loud sound. ffmpeg takes the sound as
small mono MP3 (at most 30 minutes per video). Videos without sound or with
very quiet sound are skipped, and what the models tend to invent for silence
and noise ("Vielen Dank.", "*Musik*", "Untertitel im Auftrag des ZDF",
endless repetitions, segments the model was unsure of) is dropped. The text
lives in `speech.json`, is found by the search (`typ:gesprochen` finds videos
with speech) and shows under *Gesprochen* in the details; admins can have it
recognized again.

```yaml
ai:
  url: https://llm.aihosting.mittwald.de/v1
  api_key_env: MITTWALD_LLM_API_KEY
  mode: "off"        # descriptions
  ocr: documents     # off | documents | all
  ocr_model: GLM-OCR
  ocr_workers: 2
  speech: "on"       # off | on
  speech_model: whisper-large-v3-turbo
  speech_fallback: cohere-transcribe-03-2026  # second try; empty: none
  speech_workers: 2
```

### Metadata in the files

With `library.write_metadata` on (the default; *Einstellungen → Bibliothek*),
Schuhkarton writes what it found out into the files' XMP metadata with
exiftool, so other programs see it and a lost cache does not lose it:

| What | Where |
|---|---|
| AI description | `XMP-iptcCore:AltTextAccessibility` (x-default in the first AI language, `de`, `en`) |
| AI keywords | `XMP-dc:Subject`, next to the keywords already there |
| People | `XMP-iptcExt:PersonInImage`, and named faces as MWG regions (`XMP-mwg-rs:RegionInfo`) |
| Recognized and spoken text, models, dates, what the model saw of a video | the `XMP-schuhkarton` namespace (`https://github.com/dgrieser/schuhkarton/ns/1.0/`) |

Files whose name does not fit their content, like a HEIC photo saved as
`IMG_1.jpeg`, keep their name: Schuhkarton recognizes the format by the
first bytes of the file (once for a library indexed by an older version)
and decodes, turns, prints and serves the file by what it is. exiftool
refuses to write such a file ("Not a valid JPEG (looks more like a MOV)"),
so it writes a copy with the right extension in the cache, whose content
then goes back into the file itself (name, links, owner and permissions
stay; a failed write puts the old content back).

Keywords, names and face regions from other programs stay; Schuhkarton
only replaces what it wrote itself (remembered in `XMP-schuhkarton:Tags`,
`People` and `Regions`). `XMP-dc:Description` is left to people. RAW files,
formats exiftool cannot write (BMP, …) and all videos get a sidecar beside
them (`IMG_1.ARW.xmp`, `IMG_2.MOV.xmp`), which moves with the file: writing
into a video would rewrite all of it, gigabytes over a network share. The file times stay (photos
without a date in their metadata are dated by them); the backup still sees
the change by the inode change time and copies the file again.

`XMP-schuhkarton:Sig` marks what was written; files whose mark matches are not
touched again. When the cache is new, a scan reads descriptions, texts and
the mark back from the files, so nothing is sent to the AI service twice and
nothing is written again. The exiftool definition of the namespace is
`exiftool.config` in the cache:

```sh
exiftool -config ~/.cache/schuhkarton/exiftool.config -XMP-schuhkarton:all IMG_1.jpg
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
`-config FILE` or `SCHUHKARTON_CONFIG` names another file.

**Separate process for the heavy work:** decoding a photo (HEIC and RAW
previews included), scaling it and writing the JPEGs runs in a short-lived
child process (`schuhkarton convert`), one per photo, as many at once as
there are CPUs. Its memory goes back to the system when it ends, a photo that
needs too much memory only takes the child down, and background work runs
with a lower CPU priority (nice 10) than what somebody looks at.

**Cache on a small disk:** `cache.max_size` (e.g. `4GB`) with
`cache.overflow_dir` (e.g. a folder on the NAS) keeps the cache folder below
the cap: above it the large files (lightbox previews, video previews,
converted videos) move to the overflow folder, oldest first, and are read
from there; they are not moved back. Thumbnails, face crops and the index
always stay in the cache folder. The overflow folder must exist already, on another
disk: while it is missing or the share is not mounted, nothing moves. The moving is
background work and keeps to the time window. The overflow folder can be rebuilt any time, so it is not
part of the backup.

**One data folder:** `-home DIR` (or `SCHUHKARTON_HOME=DIR`) keeps everything
Schuhkarton writes in one place: `DIR/config.yaml`, `DIR/users.json` and the
cache in `DIR/cache/` (index, thumbnails, faces, people, favorites, edits,
descriptions …). Paths inside the folder are written relative to it, so moving
to another machine (say a Raspberry Pi) is: stop Schuhkarton, copy the folder,
start it there with `-home`, and adjust the photo folders if their paths
differ. To gather an existing installation, copy `config.yaml` and
`users.json` into the folder, the cache into `cache/`, and remove `cache.dir`
from `config.yaml` (or set it to `cache`).

Every key can also
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
  write_metadata: true        # write descriptions, keywords, texts and names into the files' XMP
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
  max_size: ""                # e.g. 4GB: above it large previews and videos move to overflow_dir
  overflow_dir: ""            # e.g. a folder on the NAS; read from there, not moved back
media:
  workers: 12
  thumb_size: 360             # short edge of grid thumbnails
  preview_size: 2048          # long edge of lightbox previews
  jpeg_quality: 82
  slow_decodes: 1             # 32-bit ARM: HEIC/AVIF decoded at once by the slow built-in decoder
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
background:                   # time windows for background work; none: any time
  windows:                    # weekly; past midnight a window belongs to its start day
    - {days: mon-fri, from: "23:00", to: "08:00"}
    - {days: "sat,sun", from: "00:00", to: "07:00"}   # days empty: every day
  once:                       # one-time, dropped when over
    - {from: "2026-11-03 09:00", to: "13:00"}
  from: ""                    # older form: one window for every day
  to: ""
worker:                       # for "schuhkarton worker"
  listen: 0.0.0.0:8089
  token: ""
  workers: 12
```

Thumbnail sizes apply to new thumbnails; to rebuild all of them, delete
`thumbs/` in the cache folder.

**Background work at night:** `background.windows` (on the settings page
under *Storage*) keep the work Schuhkarton does on its own inside weekly time
windows, e.g. Monday to Friday 23:00 to 08:00 and at the weekend 00:00 to
07:00; `background.once` adds one-time windows (today 09:00 to 13:00) that
drop out of the settings when they are over. Days are written like
`mon-fri`, `sat,sun` or `mo,mi,fr`. The windows cover thumbnails and previews,
faces, descriptions, text and speech recognition, writing metadata into
files, duplicates, the import folder, regular rescans and backups. Work that
is still running when the window closes stops and goes on in the next one;
a scan or an import that has begun finishes. The status shows when the work
starts again. The scan at start, what somebody opens (a thumbnail, a video)
and what an admin starts by hand (*Rescan*, *Back up now*) run at once.
No windows (the default) means any time.

## Backup and restore

The backup folder holds `originals/<folder name>/…`, `cache/…` (index,
thumbnails, video previews, faces, people, favorites), `config/config.yaml`
and `config/users.json` (they contain tokens and password hashes: keep the
backup private) and
`schuhkarton-backup.json` (the last run). Files that are new or differ in size
or time are copied (`schuhkarton-copied.json` keeps the exact times, so a
backup on a share that keeps whole seconds only still notices every change); browser transcodes are left out because they can be made
again. A run that was cancelled (Schuhkarton stopped) or failed starts again
two minutes after the next start, a failed one also after an hour at the
latest; only a complete run waits for the full interval.

To restore, copy the photos back, start Schuhkarton and choose *Anderen Cache
übernehmen* in the setup with the backup's `cache/` folder. Cached files match
when the folder names are the same.

## Data

Originals change only when an admin edits, rotates, moves or deletes them
(see [Editing](#editing-admins)), and when descriptions, texts and names are
written into their metadata (see [Metadata in the files](#metadata-in-the-files)). Everything derived lives in the cache:
`index.gob.gz`, `thumbs/` (thumbnails, previews, video previews, transcodes),
`faces/` (crops), `people.json` (names), `favorites.json` (per account),
`edits.json` (admin edits), `trash.json`, `ai.json` (descriptions), `ocr.json` (text in pictures), `speech.json` (spoken text), `exiftool.config` (the XMP-schuhkarton tags), `notes.json`, `marks.json` (per account), `dupes.json` (hashes), `token`. One process per cache; a second one refuses to start. Accounts are
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
