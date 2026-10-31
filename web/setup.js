/* Schuhkarton — setup wizard and settings page */
'use strict';
(() => {
  const $ = (s, el = document) => el.querySelector(s);
  const $$ = (s, el = document) => [...el.querySelectorAll(s)];
  const esc = (s) => String(s ?? '').replace(/[&<>"']/g, (c) => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c]));
  const icon = (n, cls = '') => `<svg class="i ${cls}"><use href="#i-${n}"/></svg>`;
  const num = (n) => new Intl.NumberFormat('de-DE').format(n || 0);
  const bytes = (b) => (!b ? '?' : b > 1e12 ? `${(b / 1e12).toFixed(1)} TB` : b > 1e9 ? `${(b / 1e9).toFixed(1)} GB` : `${Math.round(b / 1e6)} MB`);
  const clone = (o) => JSON.parse(JSON.stringify(o));

  async function api(path, body) {
    const r = await fetch(path, body === undefined ? {} : { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body) });
    if (!r.ok) throw new Error((await r.text()) || r.statusText);
    return r.json();
  }
  let toastT;
  function toast(msg) {
    const t = $('#toast');
    t.textContent = msg;
    t.classList.add('on');
    clearTimeout(toastT);
    toastT = setTimeout(() => t.classList.remove('on'), 2600);
  }
  function randomToken() {
    const b = new Uint8Array(12);
    crypto.getRandomValues(b);
    return [...b].map((x) => x.toString(16).padStart(2, '0')).join('');
  }

  // ---------------------------------------------------------------------------
  let S;          // server state
  let C;          // the configuration being edited
  let step = 0;
  let tokenMode = 'keep';
  let importPath = '';
  const checks = new Map(); // folder path -> check result
  let faceTest = null;
  let backupCheck = null;
  let backupStatus = null;

  const locked = (key) => S.locked?.[key];
  const lockNote = (key) => (locked(key) ? `<span class="lock">festgelegt durch ${esc(locked(key))}</span>` : '');
  const dis = (key) => (locked(key) ? 'disabled' : '');

  const STEPS = [
    { id: 'fotos', title: 'Fotos', render: renderFolders, ok: () => C.library.folders.length > 0 && C.library.folders.every((f) => { const c = checks.get(f.path); return !c || c.exists; }) },
    { id: 'speicher', title: 'Speicher', render: renderStorage, ok: () => !!C.cache.dir },
    { id: 'gesichter', title: 'Gesichter', render: renderFaces, ok: () => C.faces.mode !== 'remote' || !!C.faces.url },
    { id: 'sicherung', title: 'Sicherung', render: renderBackup, ok: () => true },
    { id: 'zugriff', title: 'Zugriff', render: renderAccess, ok: () => /:\d+$/.test(C.server.listen) },
    { id: 'fertig', title: 'Fertig', render: renderSummary, ok: () => true },
  ];

  function drawSteps() {
    $('#steps').innerHTML = STEPS.map((s, i) => `<button data-i="${i}" class="${i === step ? 'on' : ''} ${i < step ? 'done' : ''}"><span>${i + 1}</span>${s.title}</button>`).join('');
    for (const b of $$('#steps button')) b.onclick = () => go(+b.dataset.i);
  }

  function go(i) {
    step = Math.max(0, Math.min(STEPS.length - 1, i));
    drawSteps();
    STEPS[step].render($('#step'));
    $('#prev').hidden = step === 0;
    const last = step === STEPS.length - 1;
    $('#next').textContent = last ? (S.mode === 'install' ? 'Einrichten und starten' : 'Speichern') : 'Weiter';
    validate();
    scrollTo({ top: 0, behavior: 'smooth' });
  }

  function validate() {
    const ok = STEPS[step].ok();
    $('#next').disabled = !ok;
    $('#hint').textContent = ok ? '' : ({ fotos: 'Mindestens einen Fotoordner hinzufügen.', gesichter: 'Adresse des Containers angeben oder Gesichter ausschalten.', zugriff: 'Adresse mit Port angeben, z.B. 0.0.0.0:8088.' }[STEPS[step].id] || '');
  }

  // --- step 1: folders ----------------------------------------------------------
  async function checkFolder(path) {
    const r = await api('/api/setup/folder', { path });
    checks.set(path, r);
    return r;
  }

  function folderStats(c) {
    if (!c) return '<span class="muted">wird geprüft …</span>';
    if (c.problem && !(c.photos + c.videos)) return `<span class="bad">${icon('warn')}${esc(c.problem)}</span>`;
    return `<span class="good">${icon('check')}${num(c.photos)}${c.truncated ? '+' : ''} Fotos · ${num(c.videos)}${c.truncated ? '+' : ''} Videos</span>`;
  }

  function renderFolders(el) {
    const lockedF = locked('library.folders');
    el.innerHTML = `
      <section class="card">
        <h2>Welche Fotos sollen in den Schuhkarton?</h2>
        <p class="muted">Ordner mit Fotos und Videos. Unterordner werden mitgenommen. Die Originale werden nur gelesen, nie verändert.
        Ordner nach dem Muster <code>2019/07 Juli Ort</code> liefern Datum und Ort, sonst zählen die Aufnahmedaten.</p>
        ${lockedF ? `<p class="lock">Die Ordner sind festgelegt durch ${esc(lockedF)}.</p>` : ''}
        <div class="folders" id="flist"></div>
        ${lockedF ? '' : `<div class="addrow">
          <input id="fpath" placeholder="/pfad/zu/den/Fotos" spellcheck="false">
          <button class="btn" id="fbrowse">${icon('folder')}<span>Durchsuchen</span></button>
          <button class="btn primary" id="fadd">${icon('plus')}<span>Hinzufügen</span></button>
        </div>
        <div class="chips" id="fsugg"></div>`}
        <details class="more"><summary>Ausnahmen</summary>
          <label class="field"><span>Diese Datei- und Ordnernamen auslassen (Muster, kommagetrennt)</span>
          <input id="fexcl" value="${esc((C.library.exclude || []).join(', '))}" ${dis('library.exclude')}>${lockNote('library.exclude')}</label>
        </details>
      </section>`;
    const list = $('#flist', el);
    const draw = () => {
      list.innerHTML = C.library.folders.length ? C.library.folders.map((f, i) => `
        <div class="folder">
          ${icon('folder')}
          <div class="grow"><input class="name" data-i="${i}" value="${esc(f.name || '')}" placeholder="Name" ${lockedF ? 'disabled' : ''} title="Name des Ordners in der Galerie">
          <div class="path">${esc(f.path)}</div><div class="stat">${folderStats(checks.get(f.path))}</div></div>
          ${lockedF ? '' : `<button class="btn ghost icon" data-rm="${i}" title="Entfernen">${icon('x')}</button>`}
        </div>`).join('') : '<p class="empty-note">Noch kein Ordner.</p>';
      for (const b of $$('[data-rm]', list)) b.onclick = () => { C.library.folders.splice(+b.dataset.rm, 1); draw(); validate(); };
      for (const n of $$('input.name', list)) n.oninput = () => { C.library.folders[+n.dataset.i].name = n.value; };
    };
    draw();
    for (const f of C.library.folders) if (!checks.has(f.path)) checkFolder(f.path).then(() => { draw(); validate(); });
    const add = async (p) => {
      p = (p || '').trim();
      if (!p) return;
      if (C.library.folders.some((f) => f.path === p)) { toast('Ist schon dabei'); return; }
      const r = await checkFolder(p);
      if (!r.exists) { toast(r.problem || 'Ordner nicht gefunden'); return; }
      C.library.folders.push({ name: p.split(/[\\/]/).filter(Boolean).pop() || p, path: r.path });
      $('#fpath', el).value = '';
      draw();
      validate();
    };
    if (!lockedF) {
      $('#fadd', el).onclick = () => add($('#fpath', el).value);
      $('#fpath', el).onkeydown = (e) => { if (e.key === 'Enter') add(e.target.value); };
      $('#fbrowse', el).onclick = () => pick($('#fpath', el).value, (p) => add(p));
      api('/api/setup/browse').then((l) => {
        const sugg = (l.roots || []).filter((r) => /bild|pict|foto|photo/i.test(r.name) && !C.library.folders.some((f) => f.path === r.path));
        $('#fsugg', el).innerHTML = sugg.map((r) => `<button class="chip" data-p="${esc(r.path)}">${icon('plus')}${esc(r.name)}</button>`).join('');
        for (const b of $$('[data-p]', el)) b.onclick = () => add(b.dataset.p);
      });
    }
    const ex = $('#fexcl', el);
    ex.oninput = () => { C.library.exclude = ex.value.split(',').map((s) => s.trim()).filter(Boolean); };
  }

  // --- step 2: storage ------------------------------------------------------------
  function renderStorage(el) {
    const caches = (S.caches || []).filter((c) => c.items > 0);
    const tools = (S.tools || []).map((t) => `<li class="${t.path ? 'good' : 'bad'}">${icon(t.path ? 'check' : 'warn')}<b>${t.name}</b>
      ${t.path ? `<span class="muted">${esc(t.version || '')} · ${esc(t.path)}</span>` : `<span>fehlt – nötig für ${esc(t.needed)}</span>`}</li>`).join('');
    el.innerHTML = `
      <section class="card">
        <h2>Wo dürfen Vorschaubilder liegen?</h2>
        <p class="muted">Im Cache liegen Index, Vorschaubilder, Video-Vorschauen, Gesichter, Favoriten und Namen. Etwa 5 % der Fotogröße.
        Liegen die Fotos auf einem Netzlaufwerk, gehört der Cache auf eine lokale Platte.</p>
        <label class="field"><span>Cache-Ordner</span>
          <div class="addrow"><input id="cdir" value="${esc(C.cache.dir)}" spellcheck="false" ${dis('cache.dir')}>
          <button class="btn" id="cbrowse" ${dis('cache.dir')}>${icon('folder')}<span>Durchsuchen</span></button></div>
          ${lockNote('cache.dir')}<div class="stat" id="cstat"></div></label>
        ${caches.length ? `<h3>Vorhandene Caches</h3><p class="muted">Ein Cache von früher spart Stunden: Vorschaubilder, Gesichter und Namen sind dann sofort da.
          Er passt, wenn die Ordnernamen gleich sind.</p>
          <div class="caches">${caches.map((c) => `
            <div class="cache ${importPath === c.path || C.cache.dir === c.path ? 'on' : ''}">
              <div class="grow"><b>${esc(c.path)}</b><div class="muted">${num(c.items)} Einträge · ${num(c.files)} Vorschaudateien (${bytes(c.bytes)})
              ${c.people ? ' · Personen' : ''}${c.favorites ? ' · Favoriten' : ''}</div>
              <div class="muted">Ordner: ${Object.entries(c.roots || {}).map(([n, k]) => `${esc(n)} (${num(k)})`).join(', ')}${c.inUse ? ' · <b>wird gerade benutzt</b>' : ''}</div></div>
              <div class="btns"><button class="btn" data-use="${esc(c.path)}" ${dis('cache.dir')}>Verwenden</button>
              <button class="btn" data-imp="${esc(c.path)}">${importPath === c.path ? 'Wird kopiert' : 'Kopieren'}</button></div>
            </div>`).join('')}</div>` : ''}
        <details class="more" ${importPath && !caches.some((c) => c.path === importPath) ? 'open' : ''}><summary>Anderen Cache übernehmen (z.B. aus einer Sicherung)</summary>
          <div class="addrow"><input id="ipath" placeholder="/sicherung/cache" value="${esc(caches.some((c) => c.path === importPath) ? '' : importPath)}" spellcheck="false">
          <button class="btn" id="ibrowse">${icon('folder')}<span>Durchsuchen</span></button><button class="btn" id="icheck">Prüfen</button></div>
          <div class="stat" id="istat"></div>
        </details>
      </section>
      <section class="card">
        <h2>Vorschauen</h2>
        <label class="check"><input type="checkbox" id="prev" ${C.cache.previews ? 'checked' : ''} ${dis('cache.previews')}>
          <span><b>Große Vorschauen vorab erzeugen</b><br><span class="muted">Empfohlen, wenn die Fotos auf einem langsamen Netzlaufwerk liegen. Braucht mehr Platz.</span></span></label>
        <label class="check"><input type="checkbox" id="vprev" ${C.media.video_previews ? 'checked' : ''} ${dis('media.video_previews')}>
          <span><b>Animierte Video-Vorschauen</b><br><span class="muted">Ein paar kurze Ausschnitte je Video, abgespielt beim Darüberfahren.</span></span></label>
        <label class="check"><input type="checkbox" id="trans" ${C.media.transcode ? 'checked' : ''} ${dis('media.transcode')}>
          <span><b>Videos für jeden Browser umwandeln</b><br><span class="muted">iPhone-Videos (HEVC) laufen nicht überall; sie werden beim Abspielen umgewandelt.</span></span></label>
        <div class="row2">
          <label class="field"><span>Ordner neu einlesen</span><select id="rescan" ${dis('library.rescan')}>
            ${[['', 'nur beim Start'], ['1h', 'stündlich'], ['6h', 'alle 6 Stunden'], ['24h', 'täglich']].map(([v, l]) => `<option value="${v}" ${(C.library.rescan || '') === v ? 'selected' : ''}>${l}</option>`).join('')}
          </select></label>
          <label class="field"><span>Gleichzeitige Jobs</span><input type="number" id="workers" min="1" max="64" value="${C.media.workers}" ${dis('media.workers')}></label>
        </div>
        <h3>Werkzeuge</h3><ul class="tools">${tools}</ul>
        <p class="muted small">Fehlt etwas: <code>exiftool</code> und <code>ffmpeg</code> über die Paketverwaltung installieren (z.B. <code>apt install libimage-exiftool-perl ffmpeg</code>), dann neu starten.</p>
      </section>`;
    const cdir = $('#cdir', el);
    const checkC = async () => {
      const r = await api('/api/setup/cache', { path: cdir.value });
      const f = r.folder, c = r.cache;
      $('#cstat', el).innerHTML = f.problem ? `<span class="bad">${icon('warn')}${esc(f.problem)}</span>`
        : `<span class="good">${icon('check')}${bytes(f.freeBytes)} frei${c.valid ? ` · enthält schon ${num(c.items)} Einträge` : ''}</span>`;
    };
    cdir.oninput = () => { C.cache.dir = cdir.value.trim(); clearTimeout(cdir._t); cdir._t = setTimeout(checkC, 400); validate(); };
    checkC();
    $('#cbrowse', el).onclick = () => pick(cdir.value, (p) => { cdir.value = p; cdir.oninput(); });
    for (const b of $$('[data-use]', el)) b.onclick = () => { C.cache.dir = b.dataset.use; importPath = ''; renderStorage(el); };
    for (const b of $$('[data-imp]', el)) b.onclick = () => { importPath = importPath === b.dataset.imp ? '' : b.dataset.imp; renderStorage(el); };
    const ip = $('#ipath', el);
    const checkI = async () => {
      if (!ip.value.trim()) { $('#istat', el).innerHTML = ''; return; }
      const r = await api('/api/setup/cache', { path: ip.value });
      const c = r.cache;
      if (c.valid) { importPath = c.path; }
      $('#istat', el).innerHTML = c.valid ? `<span class="good">${icon('check')}${num(c.items)} Einträge, ${num(c.files)} Vorschaudateien – wird beim Speichern kopiert</span>`
        : `<span class="bad">${icon('warn')}Kein Schuhkarton-Cache gefunden.</span>`;
    };
    $('#icheck', el).onclick = checkI;
    $('#ibrowse', el).onclick = () => pick(ip.value, (p) => { ip.value = p; checkI(); });
    $('#prev', el).onchange = (e) => { C.cache.previews = e.target.checked; };
    $('#vprev', el).onchange = (e) => { C.media.video_previews = e.target.checked; };
    $('#trans', el).onchange = (e) => { C.media.transcode = e.target.checked; };
    $('#rescan', el).onchange = (e) => { C.library.rescan = e.target.value; };
    $('#workers', el).oninput = (e) => { C.media.workers = Math.max(1, +e.target.value || 1); };
  }

  // --- step 3: faces --------------------------------------------------------------
  function renderFaces(el) {
    const m = C.faces.mode;
    const tok = C.faces.token && C.faces.token !== '********' ? C.faces.token : '<TOKEN>';
    const host = location.hostname;
    el.innerHTML = `
      <section class="card">
        <h2>Personen erkennen?</h2>
        <p class="muted">Schuhkarton kann Gesichter finden und gleiche Personen gruppieren, Namen vergeben Sie selbst.
        Das braucht Rechenleistung und läuft deshalb in einem eigenen Container, gern auf einem anderen Rechner. Ohne ihn funktioniert alles andere genauso.</p>
        <div class="choices">
          <label class="choice ${m === 'off' ? 'on' : ''}"><input type="radio" name="fm" value="off" ${m === 'off' ? 'checked' : ''} ${dis('faces.mode')}>
            <b>Aus</b><span class="muted">Keine Gesichtserkennung. Später jederzeit einschaltbar.</span></label>
          <label class="choice ${m === 'remote' ? 'on' : ''}"><input type="radio" name="fm" value="remote" ${m === 'remote' ? 'checked' : ''} ${dis('faces.mode')}>
            <b>Gesichtserkennungs-Container</b><span class="muted">Docker-Container auf diesem oder einem anderen Rechner.</span></label>
          ${m === 'local' ? `<label class="choice on"><input type="radio" name="fm" value="local" checked ${dis('faces.mode')}>
            <b>Python auf diesem Rechner</b><span class="muted">Fortgeschritten: Python mit OpenCV und den Modellen.</span></label>` : ''}
        </div>${lockNote('faces.mode')}
      </section>
      ${m === 'remote' ? `<section class="card">
        <h2>Container</h2>
        <p class="muted">1. Auf dem Rechner, der die Erkennung übernehmen soll, starten:</p>
        <pre class="code" id="dockercmd">docker run -d --name schuhkarton-faces --restart unless-stopped -p 8089:8089 \\
  -e SCHUHKARTON_WORKER_TOKEN=${esc(tok)} \\
  ghcr.io/dgrieser/schuhkarton-faces:latest</pre>
        <button class="btn" id="copycmd">${icon('copy')}<span>Befehl kopieren</span></button>
        <p class="muted">2. Adresse und Token hier eintragen und testen:</p>
        <div class="row2">
          <label class="field"><span>Adresse</span><input id="furl" value="${esc(C.faces.url || `http://${host}:8089`)}" spellcheck="false" ${dis('faces.url')}>${lockNote('faces.url')}</label>
          <label class="field"><span>Token</span><div class="addrow"><input id="ftok" value="${esc(C.faces.token || '')}" spellcheck="false" ${dis('faces.token')}>
            <button class="btn" id="fgen" ${dis('faces.token')} title="Neuen Token erzeugen">Neu</button></div>${lockNote('faces.token')}</label>
        </div>
        <button class="btn primary" id="ftest">Verbindung testen</button> <span id="fres"></span>
      </section>` : ''}`;
    for (const r of $$('input[name=fm]', el)) r.onchange = () => {
      C.faces.mode = r.value;
      if (r.value === 'remote' && !C.faces.token) C.faces.token = randomToken();
      if (r.value === 'remote' && !C.faces.url) C.faces.url = `http://${host}:8089`;
      renderFaces(el);
      validate();
    };
    if (m !== 'remote') return;
    const showRes = () => {
      const f = faceTest;
      if (!f) return;
      $('#fres', el).innerHTML = f.ok
        ? `<span class="good">${icon('check')}verbunden · ${f.info.recognition ? 'Erkennung und Zuordnung' : 'nur Erkennung (Modell SFace fehlt)'} · ${esc(f.info.version || '')}</span>`
        : `<span class="bad">${icon('warn')}${esc(f.error || (f.info?.missing || []).join(', ') || 'keine Erkennung möglich')}</span>`;
    };
    showRes();
    const url = $('#furl', el), tk = $('#ftok', el);
    url.oninput = () => { C.faces.url = url.value.trim(); validate(); };
    tk.oninput = () => { C.faces.token = tk.value.trim(); };
    C.faces.url = url.value.trim();
    $('#fgen', el).onclick = () => { C.faces.token = randomToken(); renderFaces(el); };
    $('#copycmd', el).onclick = () => navigator.clipboard?.writeText($('#dockercmd', el).textContent).then(() => toast('Kopiert'));
    $('#ftest', el).onclick = async () => {
      $('#fres', el).textContent = 'teste …';
      faceTest = await api('/api/setup/faces', { url: C.faces.url, token: C.faces.token }).catch((e) => ({ ok: false, error: e.message }));
      showRes();
    };
  }

  // --- step 4: backup ---------------------------------------------------------------
  function renderBackup(el) {
    const b = C.backup;
    const on = !!b.dir;
    el.innerHTML = `
      <section class="card">
        <h2>Automatisch sichern?</h2>
        <p class="muted">Schuhkarton kopiert im Hintergrund Fotos, Vorschaubilder, Gesichter, Favoriten und Einstellungen in einen Sicherungsordner,
        z.B. auf eine USB-Platte oder ein Netzlaufwerk. Nur Neues und Geändertes wird kopiert, in der Sicherung wird nie etwas gelöscht.</p>
        <label class="field"><span>Sicherungsordner (leer = keine Sicherung)</span>
          <div class="addrow"><input id="bdir" value="${esc(b.dir || '')}" placeholder="/media/usb/schuhkarton-sicherung" spellcheck="false" ${dis('backup.dir')}>
          <button class="btn" id="bbrowse" ${dis('backup.dir')}>${icon('folder')}<span>Durchsuchen</span></button></div>
          ${lockNote('backup.dir')}<div class="stat" id="bstat"></div></label>
        <div class="${on ? '' : 'dim'}">
          <label class="field"><span>Wie oft</span><select id="bint" ${dis('backup.interval')}>
            ${[['1h', 'stündlich'], ['24h', 'täglich'], ['168h', 'wöchentlich'], ['0', 'nur auf Knopfdruck']].map(([v, l]) => `<option value="${v}" ${(b.interval || '24h') === v ? 'selected' : ''}>${l}</option>`).join('')}
          </select></label>
          <label class="check"><input type="checkbox" id="borig" ${b.originals ? 'checked' : ''} ${dis('backup.originals')}><span><b>Fotos und Videos</b> (die Originale)</span></label>
          <label class="check"><input type="checkbox" id="bcache" ${b.cache ? 'checked' : ''} ${dis('backup.cache')}><span><b>Cache</b>: Vorschaubilder, Gesichter, Favoriten, Namen</span></label>
          <label class="check"><input type="checkbox" id="bconf" ${b.config ? 'checked' : ''} ${dis('backup.config')}><span><b>Einstellungen</b></span></label>
          <p class="muted small">Wiederherstellen: Fotos aus <code>originals/</code> zurückkopieren; den Cache bei der Einrichtung unter „Anderen Cache übernehmen“ aus <code>cache/</code> holen.</p>
        </div>
        ${S.mode === 'settings' && backupStatus?.enabled ? `<div class="bstatus" id="bnow"></div>` : ''}
      </section>`;
    const dir = $('#bdir', el);
    const check = async () => {
      if (!dir.value.trim()) { $('#bstat', el).innerHTML = ''; backupCheck = null; return; }
      const r = await api('/api/setup/cache', { path: dir.value });
      backupCheck = r.folder;
      const f = r.folder;
      $('#bstat', el).innerHTML = f.problem && !f.writable ? `<span class="bad">${icon('warn')}${esc(f.problem)}</span>`
        : `<span class="good">${icon('check')}${bytes(f.freeBytes)} frei${f.exists ? '' : ' · wird angelegt'}</span>`;
    };
    dir.oninput = () => { C.backup.dir = dir.value.trim(); clearTimeout(dir._t); dir._t = setTimeout(() => { check(); }, 400); $('.dim', el)?.classList.toggle('dim', !C.backup.dir); };
    check();
    $('#bbrowse', el).onclick = () => pick(dir.value, (p) => { dir.value = p; dir.oninput(); });
    $('#bint', el).onchange = (e) => { C.backup.interval = e.target.value; };
    $('#borig', el).onchange = (e) => { C.backup.originals = e.target.checked; };
    $('#bcache', el).onchange = (e) => { C.backup.cache = e.target.checked; };
    $('#bconf', el).onchange = (e) => { C.backup.config = e.target.checked; };
    const bnow = $('#bnow', el);
    if (bnow) {
      const drawB = () => {
        const s = backupStatus;
        const when = (t) => (t && !t.startsWith('0001') ? new Date(t).toLocaleString('de-DE') : '–');
        bnow.innerHTML = `<div class="grow">${s.running ? `<b>Sicherung läuft</b> · ${esc(s.phase || '')} · ${num(s.copied)} kopiert` :
          `<b>Letzte Sicherung</b>: ${when(s.finished)} · ${num(s.copied)} Dateien kopiert${s.failed ? ` · ${num(s.failed)} Fehler` : ''}${s.error ? ` · ${esc(s.error)}` : ''}<br><span class="muted">Nächste: ${when(s.next)}</span>`}</div>
          <button class="btn" id="brun" ${s.running ? 'disabled' : ''}>Jetzt sichern</button>`;
        const br = $('#brun', bnow);
        if (br) br.onclick = async () => { await api('/api/backup/run', {}); toast('Sicherung startet'); setTimeout(poll, 1500); };
      };
      const poll = async () => { backupStatus = await api('/api/backup').catch(() => backupStatus); drawB(); if (backupStatus?.running && document.contains(bnow)) setTimeout(poll, 2000); };
      drawB();
    }
  }

  // --- step 5: access -----------------------------------------------------------------
  function renderAccess(el) {
    const port = (C.server.listen.match(/:(\d+)$/) || [, '8088'])[1];
    const host = C.server.listen.replace(/:\d+$/, '');
    const kind = host === '127.0.0.1' || host === 'localhost' ? 'local' : host === '0.0.0.0' || host === '' || host === '[::]' ? 'lan' : 'custom';
    const protect = tokenMode === 'new' || (tokenMode === 'keep' && (S.hasToken || (S.tokenAuto && kind !== 'local')));
    const lan = (S.lan || [])[0];
    el.innerHTML = `
      <section class="card">
        <h2>Wer darf zuschauen?</h2>
        <div class="choices">
          <label class="choice ${kind === 'local' ? 'on' : ''}"><input type="radio" name="acc" value="local" ${kind === 'local' ? 'checked' : ''} ${dis('server.listen')}>
            <b>Nur dieser Computer</b><span class="muted">http://127.0.0.1:${port}</span></label>
          <label class="choice ${kind === 'lan' ? 'on' : ''}"><input type="radio" name="acc" value="lan" ${kind === 'lan' ? 'checked' : ''} ${dis('server.listen')}>
            <b>Alle Geräte im Heimnetz</b><span class="muted">Handy, Tablet, Fernseher${lan ? ` – z.B. http://${esc(lan)}:${port}` : ''}</span></label>
          ${kind === 'custom' ? `<label class="choice on"><input type="radio" name="acc" value="custom" checked><b>Eigene Adresse</b><span class="muted">${esc(C.server.listen)}</span></label>` : ''}
        </div>${lockNote('server.listen')}
        <div class="row2">
          <label class="field"><span>Port</span><input id="port" type="number" min="1" max="65535" value="${esc(port)}" ${dis('server.listen')}></label>
          <label class="field"><span>Öffentliche Adresse (optional, z.B. hinter einem Reverse Proxy)</span><input id="purl" value="${esc(C.server.public_url || '')}" placeholder="https://fotos.example.org" ${dis('server.public_url')}></label>
        </div>
        <label class="check"><input type="checkbox" id="prot" ${protect ? 'checked' : ''} ${dis('server.token')}>
          <span><b>Mit Zugangsschlüssel schützen</b><br><span class="muted">Geräte brauchen einmal einen Link mit Schlüssel, danach merkt sich der Browser ihn. Im Heimnetz empfohlen.</span></span></label>
        ${lockNote('server.token')}
        ${S.mode === 'settings' && S.accessLink ? `<div class="field"><span>Link für andere Geräte</span><div class="addrow"><input readonly value="${esc(S.accessLink)}" id="alink"><button class="btn" id="acopy">${icon('copy')}</button></div></div>` : ''}
        ${S.mode === 'settings' && S.listen && C.server.listen !== S.listen ? `<p class="lock">Neue Adresse gilt sofort nach dem Speichern; danach ist Schuhkarton unter der neuen Adresse erreichbar.</p>` : ''}
      </section>
      <section class="card">
        <h2>Aussehen</h2>
        <div class="row2">
          <label class="field"><span>Name der Galerie</span><input id="title" value="${esc(C.ui.title)}" ${dis('ui.title')}></label>
          <label class="field"><span>Design für neue Geräte</span><select id="theme" ${dis('ui.theme')}>
            ${[['system', 'wie das Gerät'], ['dark', 'dunkel'], ['light', 'hell']].map(([v, l]) => `<option value="${v}" ${C.ui.theme === v ? 'selected' : ''}>${l}</option>`).join('')}</select></label>
        </div>
      </section>`;
    const setListen = () => {
      const k = $('input[name=acc]:checked', el)?.value || kind;
      const p = $('#port', el).value || '8088';
      const h = k === 'local' ? '127.0.0.1' : k === 'lan' ? '0.0.0.0' : host;
      C.server.listen = `${h}:${p}`;
      validate();
    };
    for (const r of $$('input[name=acc]', el)) r.onchange = () => {
      setListen();
      if (r.value === 'lan' && !S.hasToken && tokenMode === 'keep') tokenMode = 'new';
      renderAccess(el);
    };
    $('#port', el).oninput = setListen;
    $('#purl', el).oninput = (e) => { C.server.public_url = e.target.value.trim(); };
    $('#prot', el).onchange = (e) => { tokenMode = e.target.checked ? (S.hasToken ? 'keep' : 'new') : 'off'; };
    $('#title', el).oninput = (e) => { C.ui.title = e.target.value; };
    $('#theme', el).onchange = (e) => { C.ui.theme = e.target.value; };
    const ac = $('#acopy', el);
    if (ac) ac.onclick = () => navigator.clipboard?.writeText($('#alink', el).value).then(() => toast('Link kopiert'));
  }

  // --- step 6: summary ------------------------------------------------------------------
  function renderSummary(el) {
    const f = C.library.folders;
    const row = (k, v) => `<div class="sumrow"><span>${k}</span><b>${v}</b></div>`;
    el.innerHTML = `
      <section class="card">
        <h2>${S.mode === 'install' ? 'Alles bereit' : 'Einstellungen speichern'}</h2>
        <div class="summary">
          ${row('Fotos', f.map((x) => `${esc(x.name)} <span class="muted">${esc(x.path)}</span>`).join('<br>'))}
          ${row('Cache', esc(C.cache.dir) + (importPath ? `<br><span class="muted">übernimmt ${esc(importPath)}</span>` : ''))}
          ${row('Vorschauen', [C.cache.previews ? 'große vorab' : 'große bei Bedarf', C.media.video_previews ? 'animierte Videos' : 'Videos ohne Animation', C.media.transcode ? 'HEVC umwandeln' : ''].filter(Boolean).join(' · '))}
          ${row('Gesichter', C.faces.mode === 'off' ? 'aus' : C.faces.mode === 'remote' ? `Container ${esc(C.faces.url)}` : 'Python auf diesem Rechner')}
          ${row('Sicherung', C.backup.dir ? `${esc(C.backup.dir)} <span class="muted">${{ '1h': 'stündlich', '24h': 'täglich', '168h': 'wöchentlich', '0': 'auf Knopfdruck' }[C.backup.interval] || esc(C.backup.interval)}</span>` : 'aus')}
          ${row('Adresse', esc(C.server.listen) + (tokenMode === 'off' ? ' · ohne Schlüssel' : tokenMode === 'new' ? ' · neuer Schlüssel' : ''))}
          ${row('Datei', `<code>${esc(S.configPath)}</code>`)}
        </div>
        <div id="prog" hidden><div class="bar"><i></i></div><p class="muted" id="progText"></p></div>
      </section>`;
  }

  async function save() {
    const btn = $('#next');
    btn.disabled = true;
    const prog = $('#prog');
    prog.hidden = false;
    $('#progText').textContent = 'Speichern …';
    const res = await api('/api/setup/save', { config: C, tokenMode, import: importPath }).catch((e) => ({ ok: false, error: e.message }));
    if (!res.ok) { $('#progText').innerHTML = `<span class="bad">${esc(res.error)}</span>`; btn.disabled = false; return; }
    const poll = async () => {
      let p;
      try { p = await api('/api/setup/progress'); } catch { setTimeout(poll, 1000); return; }
      $('#prog .bar i').style.width = p.total ? `${(p.done / p.total) * 100}%` : p.finished ? '100%' : '30%';
      $('#progText').textContent = `${p.phase || ''}${p.total ? ` ${num(p.done)} / ${num(p.total)}` : ''}`;
      if (p.finished) {
        if (p.error) { $('#progText').innerHTML = `<span class="bad">${esc(p.error)}</span>`; btn.disabled = false; return; }
        $('#progText').textContent = 'Fertig – die Galerie öffnet sich …';
        setTimeout(() => { location.href = p.link || '/'; }, 900);
        return;
      }
      setTimeout(poll, 700);
    };
    poll();
  }

  // --- folder picker ------------------------------------------------------------------
  let pickCb = null;
  async function pick(start, cb) {
    pickCb = cb;
    $('#picker').hidden = false;
    await browse(start || '');
  }
  async function browse(path) {
    const l = await api(`/api/setup/browse?path=${encodeURIComponent(path)}`);
    $('#pickInput').value = l.path;
    $('#pickUp').disabled = !l.parent;
    $('#pickUp').onclick = () => browse(l.parent);
    $('#pickRoots').innerHTML = (l.roots || []).map((r) => `<button class="chip" data-p="${esc(r.path)}">${esc(r.name)}</button>`).join('');
    $('#pickList').innerHTML = l.error ? `<p class="bad">${esc(l.error)}</p>` : (l.dirs || []).map((d) => `<button data-p="${esc(d.path)}">${icon('folder')}<span>${esc(d.name)}</span>${icon('right')}</button>`).join('') || '<p class="muted">Keine Unterordner.</p>';
    $('#pickInfo').textContent = `${(l.dirs || []).length} Unterordner`;
    for (const b of $$('#picker [data-p]')) b.onclick = () => browse(b.dataset.p);
  }
  $('#pickInput').onkeydown = (e) => { if (e.key === 'Enter') browse(e.target.value); };
  $('#pickOk').onclick = () => { $('#picker').hidden = true; pickCb?.($('#pickInput').value); };
  for (const b of $$('#picker [data-close]')) b.onclick = () => { $('#picker').hidden = true; };
  $('#picker').onclick = (e) => { if (e.target.id === 'picker') $('#picker').hidden = true; };

  // --- start ----------------------------------------------------------------------------
  $('#prev').onclick = () => go(step - 1);
  $('#next').onclick = () => (step === STEPS.length - 1 ? save() : go(step + 1));
  (async () => {
    try { S = await api('/api/setup/state'); } catch (e) {
      $('#step').innerHTML = `<section class="card"><h2>Kein Zugriff</h2><p>${esc(e.message)}</p></section>`;
      $('.setup-foot').hidden = true;
      return;
    }
    C = clone(S.config);
    C.library.folders ||= [];
    C.library.exclude ||= [];
    if (S.mode === 'settings') {
      $('#title').textContent = 'Einstellungen';
      $('#subtitle').textContent = `Schuhkarton ${S.version} · ${S.os}`;
      $('#back').hidden = false;
      document.title = 'Schuhkarton – Einstellungen';
      backupStatus = await api('/api/backup').catch(() => null);
    }
    go(0);
  })();
})();
