# Third-party files

Served from the binary instead of a CDN, so the gallery loads no code from
other sites and works without internet access. Unchanged copies of the npm
packages:

| Folder | Package | License |
|---|---|---|
| `leaflet/` | `leaflet@1.9.4` (`dist/leaflet.js`, `dist/leaflet.css`, `dist/images/`) | BSD-2-Clause, see `leaflet/LICENSE` |
| `markercluster/` | `leaflet.markercluster@1.5.3` (`dist/leaflet.markercluster.js`, `dist/MarkerCluster.css`) | MIT, see `markercluster/MIT-LICENCE.txt` |

To update: `npm pack leaflet@<version>` (and `leaflet.markercluster`), copy
the same files from the archive's `package/dist/`, and compare `leaflet.js`
with the hash on https://leafletjs.com/download.html.
