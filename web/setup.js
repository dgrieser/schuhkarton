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
  const LANG = window.skLang || 'de';
  const t = window.skT || ((s) => s);

  async function api(path, body, method) {
    const r = await fetch(path, body === undefined && !method ? {} : { method: method || 'POST', headers: { 'Content-Type': 'application/json' }, body: body === undefined ? undefined : JSON.stringify(body) });
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
  const admin = { name: '', pw: '', pw2: '' }; // first admin (install, or no admin yet)

  const locked = (key) => S.locked?.[key];
  const lockNote = (key) => (locked(key) ? `<span class="lock">${t('festgelegt durch {x}', { x: esc(locked(key)) })}</span>` : '');
  const dis = (key) => (locked(key) ? 'disabled' : '');

  const adminOK = () => admin.name.trim() && admin.pw.length >= 8 && admin.pw === admin.pw2;
  let STEPS = [];
  function buildSteps() {
    STEPS = [
      { id: 'fotos', title: t('Fotos'), render: renderFolders, ok: () => C.library.folders.length > 0 && C.library.folders.every((f) => { const c = checks.get(f.path); return !c || c.exists; }) },
      { id: 'speicher', title: t('Speicher'), render: renderStorage, ok: () => !!C.cache.dir },
      { id: 'gesichter', title: t('Gesichter'), render: renderFaces, ok: () => C.faces.mode !== 'remote' || !!C.faces.url },
      { id: 'sicherung', title: t('Sicherung'), render: renderBackup, ok: () => true },
      { id: 'zugriff', title: t('Zugriff'), render: renderAccess, ok: () => /:\d+$/.test(C.server.listen) },
      S.needAdmin ? { id: 'konto', title: t('Admin'), render: renderAdmin, ok: adminOK }
        : { id: 'benutzer', title: t('Benutzer'), render: renderUsers, ok: () => true },
      { id: 'fertig', title: t('Fertig'), render: renderSummary, ok: () => !S.needAdmin || adminOK() },
    ];
  }

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
    $('#next').textContent = last ? (S.mode === 'install' ? t('Einrichten und starten') : t('Speichern')) : t('Weiter');
    validate();
    scrollTo({ top: 0, behavior: 'smooth' });
  }

  function validate() {
    const ok = STEPS[step].ok();
    $('#next').disabled = !ok;
    $('#hint').textContent = ok ? '' : ({ fotos: t('Mindestens einen Fotoordner hinzufügen.'), gesichter: t('Adresse des Containers angeben oder Gesichter ausschalten.'),
      zugriff: t('Adresse mit Port angeben, z.B. 0.0.0.0:8088.'), konto: t('Name und Passwort (mindestens 8 Zeichen, zweimal gleich) für das Admin-Konto angeben.'),
      fertig: t('Erst ein Admin-Konto anlegen.') }[STEPS[step].id] || '');
  }

  // --- step 1: folders ----------------------------------------------------------
  async function checkFolder(path) {
    const r = await api('/api/setup/folder', { path });
    checks.set(path, r);
    return r;
  }

  function folderStats(c) {
    if (!c) return `<span class="muted">${t('wird geprüft …')}</span>`;
    if (c.problem && !(c.photos + c.videos)) return `<span class="bad">${icon('warn')}${esc(c.problem)}</span>`;
    return `<span class="good">${icon('check')}${num(c.photos)}${c.truncated ? '+' : ''} ${t('Fotos')} · ${num(c.videos)}${c.truncated ? '+' : ''} ${t('Videos')}</span>`;
  }

  function renderFolders(el) {
    const lockedF = locked('library.folders');
    el.innerHTML = `
      <section class="card">
        <h2>${t('Welche Fotos sollen in den Schuhkarton?')}</h2>
        <p class="muted">${t('Ordner mit Fotos und Videos. Unterordner werden mitgenommen. Schuhkarton zwingt keine Ordnerstruktur auf; Originale ändern sich nur, wenn ein Admin bearbeitet, dreht, verschiebt oder löscht.')}</p>
        ${lockedF ? `<p class="lock">${t('Die Ordner sind festgelegt durch {x}.', { x: esc(lockedF) })}</p>` : ''}
        <div class="folders" id="flist"></div>
        ${lockedF ? '' : `<div class="addrow">
          <input id="fpath" placeholder="${t('/pfad/zu/den/Fotos')}" spellcheck="false">
          <button class="btn" id="fbrowse">${icon('folder')}<span>${t('Durchsuchen')}</span></button>
          <button class="btn primary" id="fadd">${icon('plus')}<span>${t('Hinzufügen')}</span></button>
        </div>
        <div class="chips" id="fsugg"></div>`}
        <details class="more"><summary>${t('Ordnernamen, Ausnahmen, Bearbeiten')}</summary>
          <label class="check"><input type="checkbox" id="fmeta" ${C.library.folder_meta ? 'checked' : ''} ${dis('library.folder_meta')}>
            <span><b>${t('Ordnernamen auswerten')}</b><br><span class="muted">${t('Ordner wie {ex} liefern Datum, Ort und Kategorie, wenn die Fotos selbst nichts Passenderes wissen. Bearbeitete Angaben gehen immer vor.', { ex: '<code>2019/07 Juli Ort</code>' })}</span></span></label>
          ${lockNote('library.folder_meta')}
          <label class="check"><input type="checkbox" id="fwrite" ${C.library.write_files ? 'checked' : ''} ${dis('library.write_files')}>
            <span><b>${t('Änderungen in die Dateien schreiben')}</b><br><span class="muted">${t('Datum, Ort, GPS, Text und Drehung, die Admins ändern, landen mit exiftool in den Fotos selbst. Aus: nur im Schuhkarton gespeichert.')}</span></span></label>
          ${lockNote('library.write_files')}
          <label class="field"><span>${t('Papierkorb nach so vielen Tagen leeren (0 = nie)')}</span>
            <input type="number" id="ftrash" min="0" max="3650" value="${C.library.trash_days ?? 30}" ${dis('library.trash_days')}>${lockNote('library.trash_days')}</label>
          <label class="field"><span>${t('Diese Datei- und Ordnernamen auslassen (Muster, kommagetrennt)')}</span>
          <input id="fexcl" value="${esc((C.library.exclude || []).join(', '))}" ${dis('library.exclude')}>${lockNote('library.exclude')}</label>
        </details>
      </section>`;
    const list = $('#flist', el);
    const draw = () => {
      list.innerHTML = C.library.folders.length ? C.library.folders.map((f, i) => `
        <div class="folder">
          ${icon('folder')}
          <div class="grow"><input class="name" data-i="${i}" value="${esc(f.name || '')}" placeholder="${t('Name')}" ${lockedF ? 'disabled' : ''} title="${t('Name des Ordners in der Galerie')}">
          <div class="path">${esc(f.path)}</div><div class="stat">${folderStats(checks.get(f.path))}</div></div>
          ${lockedF ? '' : `<button class="btn ghost icon" data-rm="${i}" title="${t('Entfernen')}">${icon('x')}</button>`}
        </div>`).join('') : `<p class="empty-note">${t('Noch kein Ordner.')}</p>`;
      for (const b of $$('[data-rm]', list)) b.onclick = () => { C.library.folders.splice(+b.dataset.rm, 1); draw(); validate(); };
      for (const n of $$('input.name', list)) n.oninput = () => { C.library.folders[+n.dataset.i].name = n.value; };
    };
    draw();
    for (const f of C.library.folders) if (!checks.has(f.path)) checkFolder(f.path).then(() => { draw(); validate(); });
    const add = async (p) => {
      p = (p || '').trim();
      if (!p) return;
      if (C.library.folders.some((f) => f.path === p)) { toast(t('Ist schon dabei')); return; }
      const r = await checkFolder(p);
      if (!r.exists) { toast(r.problem ? t(r.problem) : t('Ordner nicht gefunden')); return; }
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
    $('#fmeta', el).onchange = (e) => { C.library.folder_meta = e.target.checked; };
    $('#fwrite', el).onchange = (e) => { C.library.write_files = e.target.checked; };
    $('#ftrash', el).oninput = (e) => { C.library.trash_days = Math.max(0, +e.target.value || 0); };
  }

  // --- step 2: storage ------------------------------------------------------------
  function renderStorage(el) {
    const caches = (S.caches || []).filter((c) => c.items > 0);
    const tr = t;
    const tools = (S.tools || []).map((t) => `<li class="${t.path ? 'good' : 'bad'}">${icon(t.path ? 'check' : 'warn')}<b>${t.name}</b>
      ${t.path ? `<span class="muted">${esc(t.version || '')} · ${esc(t.path)}</span>` : `<span>${tr('fehlt – nötig für {x}', { x: esc(tr(t.needed)) })}</span>`}</li>`).join('');
    el.innerHTML = `
      <section class="card">
        <h2>${t('Wo dürfen Vorschaubilder liegen?')}</h2>
        <p class="muted">${t('Im Cache liegen Index, Vorschaubilder, Video-Vorschauen, Gesichter, Favoriten und Namen. Etwa 5 % der Fotogröße. Liegen die Fotos auf einem Netzlaufwerk, gehört der Cache auf eine lokale Platte.')}</p>
        <label class="field"><span>${t('Cache-Ordner')}</span>
          <div class="addrow"><input id="cdir" value="${esc(C.cache.dir)}" spellcheck="false" ${dis('cache.dir')}>
          <button class="btn" id="cbrowse" ${dis('cache.dir')}>${icon('folder')}<span>${t('Durchsuchen')}</span></button></div>
          ${lockNote('cache.dir')}<div class="stat" id="cstat"></div></label>
        ${caches.length ? `<h3>${t('Vorhandene Caches')}</h3><p class="muted">${t('Ein Cache von früher spart Stunden: Vorschaubilder, Gesichter und Namen sind dann sofort da. Er passt, wenn die Ordnernamen gleich sind.')}</p>
          <div class="caches">${caches.map((c) => `
            <div class="cache ${importPath === c.path || C.cache.dir === c.path ? 'on' : ''}">
              <div class="grow"><b>${esc(c.path)}</b><div class="muted">${t('{n} Einträge · {f} Vorschaudateien ({b})', { n: num(c.items), f: num(c.files), b: bytes(c.bytes) })}
              ${c.people ? ` · ${t('Personen')}` : ''}${c.favorites ? ` · ${t('Favoriten')}` : ''}</div>
              <div class="muted">${t('Ordner')}: ${Object.entries(c.roots || {}).map(([n, k]) => `${esc(n)} (${num(k)})`).join(', ')}${c.inUse ? ` · <b>${t('wird gerade benutzt')}</b>` : ''}</div></div>
              <div class="btns"><button class="btn" data-use="${esc(c.path)}" ${dis('cache.dir')}>${t('Verwenden')}</button>
              <button class="btn" data-imp="${esc(c.path)}">${importPath === c.path ? t('Wird kopiert') : t('Kopieren')}</button></div>
            </div>`).join('')}</div>` : ''}
        <details class="more" ${importPath && !caches.some((c) => c.path === importPath) ? 'open' : ''}><summary>${t('Anderen Cache übernehmen (z.B. aus einer Sicherung)')}</summary>
          <div class="addrow"><input id="ipath" placeholder="${t('/sicherung/cache')}" value="${esc(caches.some((c) => c.path === importPath) ? '' : importPath)}" spellcheck="false">
          <button class="btn" id="ibrowse">${icon('folder')}<span>${t('Durchsuchen')}</span></button><button class="btn" id="icheck">${t('Prüfen')}</button></div>
          <div class="stat" id="istat"></div>
        </details>
      </section>
      <section class="card">
        <h2>${t('Vorschauen')}</h2>
        <label class="check"><input type="checkbox" id="prev" ${C.cache.previews ? 'checked' : ''} ${dis('cache.previews')}>
          <span><b>${t('Große Vorschauen vorab erzeugen')}</b><br><span class="muted">${t('Empfohlen, wenn die Fotos auf einem langsamen Netzlaufwerk liegen. Braucht mehr Platz.')}</span></span></label>
        <label class="check"><input type="checkbox" id="vprev" ${C.media.video_previews ? 'checked' : ''} ${dis('media.video_previews')}>
          <span><b>${t('Animierte Video-Vorschauen')}</b><br><span class="muted">${t('Ein paar kurze Ausschnitte je Video, abgespielt beim Darüberfahren.')}</span></span></label>
        <label class="check"><input type="checkbox" id="trans" ${C.media.transcode ? 'checked' : ''} ${dis('media.transcode')}>
          <span><b>${t('Videos für jeden Browser umwandeln')}</b><br><span class="muted">${t('iPhone-Videos (HEVC) laufen nicht überall; sie werden beim Abspielen umgewandelt.')}</span></span></label>
        <div class="row2">
          <label class="field"><span>${t('Ordner neu einlesen')}</span><select id="rescan" ${dis('library.rescan')}>
            ${[['', t('nur beim Start')], ['1h', t('stündlich')], ['6h', t('alle 6 Stunden')], ['24h', t('täglich')]].map(([v, l]) => `<option value="${v}" ${(C.library.rescan || '') === v ? 'selected' : ''}>${l}</option>`).join('')}
          </select></label>
          <label class="field"><span>${t('Gleichzeitige Jobs')}</span><input type="number" id="workers" min="1" max="64" value="${C.media.workers}" ${dis('media.workers')}></label>
        </div>
        <h3>${t('Werkzeuge')}</h3><ul class="tools">${tools}</ul>
        <p class="muted small">${t('Fehlt etwas: {a} und {b} über die Paketverwaltung installieren (z.B. {c}), dann neu starten.', { a: '<code>exiftool</code>', b: '<code>ffmpeg</code>', c: '<code>apt install libimage-exiftool-perl ffmpeg</code>' })}</p>
      </section>`;
    const cdir = $('#cdir', el);
    const checkC = async () => {
      const r = await api('/api/setup/cache', { path: cdir.value });
      const f = r.folder, c = r.cache;
      $('#cstat', el).innerHTML = f.problem ? `<span class="bad">${icon('warn')}${esc(t(f.problem))}</span>`
        : `<span class="good">${icon('check')}${t('{x} frei', { x: bytes(f.freeBytes) })}${c.valid ? ` · ${t('enthält schon {n} Einträge', { n: num(c.items) })}` : ''}</span>`;
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
      $('#istat', el).innerHTML = c.valid ? `<span class="good">${icon('check')}${t('{n} Einträge, {f} Vorschaudateien – wird beim Speichern kopiert', { n: num(c.items), f: num(c.files) })}</span>`
        : `<span class="bad">${icon('warn')}${t('Kein Schuhkarton-Cache gefunden.')}</span>`;
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
        <h2>${t('Personen erkennen?')}</h2>
        <p class="muted">${t('Schuhkarton kann Gesichter finden und gleiche Personen gruppieren, Namen vergeben Sie selbst. Das braucht Rechenleistung und läuft deshalb in einem eigenen Container, gern auf einem anderen Rechner. Ohne ihn funktioniert alles andere genauso.')}</p>
        <div class="choices">
          <label class="choice ${m === 'off' ? 'on' : ''}"><input type="radio" name="fm" value="off" ${m === 'off' ? 'checked' : ''} ${dis('faces.mode')}>
            <b>${t('Aus')}</b><span class="muted">${t('Keine Gesichtserkennung. Später jederzeit einschaltbar.')}</span></label>
          <label class="choice ${m === 'remote' ? 'on' : ''}"><input type="radio" name="fm" value="remote" ${m === 'remote' ? 'checked' : ''} ${dis('faces.mode')}>
            <b>${t('Gesichtserkennungs-Container')}</b><span class="muted">${t('Docker-Container auf diesem oder einem anderen Rechner.')}</span></label>
          ${m === 'local' ? `<label class="choice on"><input type="radio" name="fm" value="local" checked ${dis('faces.mode')}>
            <b>${t('Python auf diesem Rechner')}</b><span class="muted">${t('Fortgeschritten: Python mit OpenCV und den Modellen.')}</span></label>` : ''}
        </div>${lockNote('faces.mode')}
      </section>
      ${m === 'remote' ? `<section class="card">
        <h2>${t('Container')}</h2>
        <p class="muted">${t('1. Auf dem Rechner, der die Erkennung übernehmen soll, starten:')}</p>
        <pre class="code" id="dockercmd">docker run -d --name schuhkarton-faces --restart unless-stopped -p 8089:8089 \\
  -e SCHUHKARTON_WORKER_TOKEN=${esc(tok)} \\
  ghcr.io/dgrieser/schuhkarton-faces:latest</pre>
        <button class="btn" id="copycmd">${icon('copy')}<span>${t('Befehl kopieren')}</span></button>
        <p class="muted">${t('2. Adresse und Token hier eintragen und testen:')}</p>
        <div class="row2">
          <label class="field"><span>${t('Adresse')}</span><input id="furl" value="${esc(C.faces.url || `http://${host}:8089`)}" spellcheck="false" ${dis('faces.url')}>${lockNote('faces.url')}</label>
          <label class="field"><span>Token</span><div class="addrow"><input id="ftok" value="${esc(C.faces.token || '')}" spellcheck="false" ${dis('faces.token')}>
            <button class="btn" id="fgen" ${dis('faces.token')} title="${t('Neuen Token erzeugen')}">${t('Neu')}</button></div>${lockNote('faces.token')}</label>
        </div>
        <button class="btn primary" id="ftest">${t('Verbindung testen')}</button> <span id="fres"></span>
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
        ? `<span class="good">${icon('check')}${t('verbunden')} · ${f.info.recognition ? t('Erkennung und Zuordnung') : t('nur Erkennung (Modell SFace fehlt)')} · ${esc(f.info.version || '')}</span>`
        : `<span class="bad">${icon('warn')}${esc(f.error ? t(f.error) : (f.info?.missing || []).join(', ') || t('keine Erkennung möglich'))}</span>`;
    };
    showRes();
    const url = $('#furl', el), tk = $('#ftok', el);
    url.oninput = () => { C.faces.url = url.value.trim(); validate(); };
    tk.oninput = () => { C.faces.token = tk.value.trim(); };
    C.faces.url = url.value.trim();
    $('#fgen', el).onclick = () => { C.faces.token = randomToken(); renderFaces(el); };
    $('#copycmd', el).onclick = () => navigator.clipboard?.writeText($('#dockercmd', el).textContent).then(() => toast(t('Kopiert')));
    $('#ftest', el).onclick = async () => {
      $('#fres', el).textContent = t('teste …');
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
        <h2>${t('Automatisch sichern?')}</h2>
        <p class="muted">${t('Schuhkarton kopiert im Hintergrund Fotos, Vorschaubilder, Gesichter, Favoriten, Einstellungen und Konten in einen Sicherungsordner, z.B. auf eine USB-Platte oder ein Netzlaufwerk. Nur Neues und Geändertes wird kopiert, in der Sicherung wird nie etwas gelöscht.')}</p>
        <label class="field"><span>${t('Sicherungsordner (leer = keine Sicherung)')}</span>
          <div class="addrow"><input id="bdir" value="${esc(b.dir || '')}" placeholder="${t('/media/usb/schuhkarton-sicherung')}" spellcheck="false" ${dis('backup.dir')}>
          <button class="btn" id="bbrowse" ${dis('backup.dir')}>${icon('folder')}<span>${t('Durchsuchen')}</span></button></div>
          ${lockNote('backup.dir')}<div class="stat" id="bstat"></div></label>
        <div class="${on ? '' : 'dim'}">
          <label class="field"><span>${t('Wie oft')}</span><select id="bint" ${dis('backup.interval')}>
            ${[['1h', t('stündlich')], ['24h', t('täglich')], ['168h', t('wöchentlich')], ['0', t('nur auf Knopfdruck')]].map(([v, l]) => `<option value="${v}" ${(b.interval || '24h') === v ? 'selected' : ''}>${l}</option>`).join('')}
          </select></label>
          <label class="check"><input type="checkbox" id="borig" ${b.originals ? 'checked' : ''} ${dis('backup.originals')}><span><b>${t('Fotos und Videos')}</b> ${t('(die Originale)')}</span></label>
          <label class="check"><input type="checkbox" id="bcache" ${b.cache ? 'checked' : ''} ${dis('backup.cache')}><span><b>${t('Cache')}</b>: ${t('Vorschaubilder, Gesichter, Favoriten, Namen')}</span></label>
          <label class="check"><input type="checkbox" id="bconf" ${b.config ? 'checked' : ''} ${dis('backup.config')}><span><b>${t('Einstellungen und Konten')}</b></span></label>
          <p class="muted small">${t('Wiederherstellen: Fotos aus {o} zurückkopieren; den Cache bei der Einrichtung unter „Anderen Cache übernehmen“ aus {c} holen.', { o: '<code>originals/</code>', c: '<code>cache/</code>' })}</p>
        </div>
        ${S.mode === 'settings' && backupStatus?.enabled ? `<div class="bstatus" id="bnow"></div>` : ''}
      </section>`;
    const dir = $('#bdir', el);
    const check = async () => {
      if (!dir.value.trim()) { $('#bstat', el).innerHTML = ''; backupCheck = null; return; }
      const r = await api('/api/setup/cache', { path: dir.value });
      backupCheck = r.folder;
      const f = r.folder;
      $('#bstat', el).innerHTML = f.problem && !f.writable ? `<span class="bad">${icon('warn')}${esc(t(f.problem))}</span>`
        : `<span class="good">${icon('check')}${t('{x} frei', { x: bytes(f.freeBytes) })}${f.exists ? '' : ` · ${t('wird angelegt')}`}</span>`;
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
        const when = (d) => (d && !d.startsWith('0001') ? new Date(d).toLocaleString(LANG === 'de' ? 'de-DE' : 'en-GB') : '–');
        bnow.innerHTML = `<div class="grow">${s.running ? `<b>${t('Sicherung läuft')}</b> · ${esc(t(s.phase || ''))} · ${t('{n} kopiert', { n: num(s.copied) })}` :
          `<b>${t('Letzte Sicherung')}</b>: ${when(s.finished)} · ${t('{n} Dateien kopiert', { n: num(s.copied) })}${s.failed ? ` · ${t('{n} Fehler', { n: num(s.failed) })}` : ''}${s.error ? ` · ${esc(s.error)}` : ''}<br><span class="muted">${t('Nächste')}: ${when(s.next)}</span>`}</div>
          <button class="btn" id="brun" ${s.running ? 'disabled' : ''}>${t('Jetzt sichern')}</button>`;
        const br = $('#brun', bnow);
        if (br) br.onclick = async () => { await api('/api/backup/run', {}); toast(t('Sicherung startet')); setTimeout(poll, 1500); };
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
        <h2>${t('Wer darf zuschauen?')}</h2>
        <div class="choices">
          <label class="choice ${kind === 'local' ? 'on' : ''}"><input type="radio" name="acc" value="local" ${kind === 'local' ? 'checked' : ''} ${dis('server.listen')}>
            <b>${t('Nur dieser Computer')}</b><span class="muted">http://127.0.0.1:${port}</span></label>
          <label class="choice ${kind === 'lan' ? 'on' : ''}"><input type="radio" name="acc" value="lan" ${kind === 'lan' ? 'checked' : ''} ${dis('server.listen')}>
            <b>${t('Alle Geräte im Heimnetz')}</b><span class="muted">${t('Handy, Tablet, Fernseher')}${lan ? ` – ${t('z.B.')} http://${esc(lan)}:${port}` : ''}</span></label>
          ${kind === 'custom' ? `<label class="choice on"><input type="radio" name="acc" value="custom" checked><b>${t('Eigene Adresse')}</b><span class="muted">${esc(C.server.listen)}</span></label>` : ''}
        </div>${lockNote('server.listen')}
        <div class="row2">
          <label class="field"><span>${t('Port')}</span><input id="port" type="number" min="1" max="65535" value="${esc(port)}" ${dis('server.listen')}></label>
          <label class="field"><span>${t('Öffentliche Adresse (optional, z.B. hinter einem Reverse Proxy)')}</span><input id="purl" value="${esc(C.server.public_url || '')}" placeholder="https://fotos.example.org" ${dis('server.public_url')}></label>
        </div>
        <label class="check"><input type="checkbox" id="prot" ${protect ? 'checked' : ''} ${dis('server.token')}>
          <span><b>${t('Mit Zugangsschlüssel schützen')}</b><br><span class="muted">${t('Gilt für Zuschauer ohne Konto: Geräte brauchen einmal einen Link mit Schlüssel, danach merkt sich der Browser ihn. Im Heimnetz empfohlen.')}</span></span></label>
        ${lockNote('server.token')}
        ${S.mode === 'settings' && S.accessLink ? `<div class="field"><span>${t('Link für andere Geräte')}</span><div class="addrow"><input readonly value="${esc(S.accessLink)}" id="alink"><button class="btn" id="acopy">${icon('copy')}</button></div></div>` : ''}
        ${S.mode === 'settings' && S.listen && C.server.listen !== S.listen ? `<p class="lock">${t('Neue Adresse gilt sofort nach dem Speichern; danach ist Schuhkarton unter der neuen Adresse erreichbar.')}</p>` : ''}
      </section>
      <section class="card">
        <h2>${t('Konten')}</h2>
        <p class="muted">${t('Admins verwalten Einstellungen, Konten und Fotos. Mit Konto lassen sich Favoriten setzen und Personen benennen.')}</p>
        <label class="check"><input type="checkbox" id="anon" ${C.access.anonymous ? 'checked' : ''} ${dis('access.anonymous')}>
          <span><b>${t('Ohne Konto ansehen')}</b><br><span class="muted">${t('Fotos ansehen und herunterladen ohne Anmeldung (im Netz mit Zugangsschlüssel).')}</span></span></label>
        ${lockNote('access.anonymous')}
        <label class="check"><input type="checkbox" id="reg" ${C.access.register ? 'checked' : ''} ${dis('access.register')}>
          <span><b>${t('Selbst registrieren')}</b><br><span class="muted">${t('Jeder kann ein Konto anlegen; ein Admin muss es freigeben.')}</span></span></label>
        ${lockNote('access.register')}
      </section>
      <section class="card">
        <h2>${t('Aussehen')}</h2>
        <div class="row2">
          <label class="field"><span>${t('Name der Galerie')}</span><input id="title" value="${esc(C.ui.title)}" ${dis('ui.title')}></label>
          <label class="field"><span>${t('Design für neue Geräte')}</span><select id="theme" ${dis('ui.theme')}>
            ${[['system', t('wie das Gerät')], ['dark', t('dunkel')], ['light', t('hell')]].map(([v, l]) => `<option value="${v}" ${C.ui.theme === v ? 'selected' : ''}>${l}</option>`).join('')}</select></label>
          <label class="field"><span>${t('Sprache für neue Geräte')}</span><select id="ulang" ${dis('ui.language')}>
            ${[['auto', t('wie der Browser')], ['de', 'Deutsch'], ['en', 'English']].map(([v, l]) => `<option value="${v}" ${(C.ui.language || 'auto') === v ? 'selected' : ''}>${l}</option>`).join('')}</select></label>
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
    $('#anon', el).onchange = (e) => { C.access.anonymous = e.target.checked; };
    $('#reg', el).onchange = (e) => { C.access.register = e.target.checked; };
    $('#theme', el).onchange = (e) => { C.ui.theme = e.target.value; };
    $('#ulang', el).onchange = (e) => { C.ui.language = e.target.value; };
    const ac = $('#acopy', el);
    if (ac) ac.onclick = () => navigator.clipboard?.writeText($('#alink', el).value).then(() => toast(t('Link kopiert')));
  }

  // --- step 6a: first admin --------------------------------------------------------------
  function renderAdmin(el) {
    el.innerHTML = `
      <section class="card">
        <h2>${t('Admin-Konto anlegen')}</h2>
        <p class="muted">${t('Der Admin verwaltet Einstellungen, Konten und Fotos (bearbeiten, drehen, verschieben, löschen). Weitere Konten können später angelegt oder freigegeben werden.')}</p>
        <form id="aform" autocomplete="on" onsubmit="return false">
          <label class="field"><span>${t('Name')}</span><input id="aname" autocomplete="username" autocapitalize="none" spellcheck="false" maxlength="64" value="${esc(admin.name)}"></label>
          <div class="row2">
            <label class="field"><span>${t('Passwort')}</span><input id="apw" type="password" autocomplete="new-password" value="${esc(admin.pw)}"></label>
            <label class="field"><span>${t('Passwort wiederholen')}</span><input id="apw2" type="password" autocomplete="new-password" value="${esc(admin.pw2)}"></label>
          </div>
          <p class="muted small">${t('Mindestens 8 Zeichen.')} ${t('Schon auf der Anmeldeseite registriert? Denselben Namen und dasselbe Passwort eingeben, dann wird dieses Konto zum Admin.')} <span id="ahint"></span></p>
        </form>
      </section>`;
    const upd = () => {
      admin.name = $('#aname', el).value; admin.pw = $('#apw', el).value; admin.pw2 = $('#apw2', el).value;
      $('#ahint', el).innerHTML = admin.pw2 && admin.pw !== admin.pw2 ? `<span class="bad">${t('Die Passwörter sind verschieden')}</span>` : '';
      validate();
    };
    for (const i of $$('input', el)) i.oninput = upd;
    $('#aname', el).focus();
  }

  // --- step 6b: accounts -------------------------------------------------------------------
  async function renderUsers(el) {
    el.innerHTML = `<section class="card"><h2>${t('Benutzer')}</h2><p class="muted">${t('wird geladen …')}</p></section>`;
    let res;
    try { res = await api('/api/users'); } catch (e) { el.innerHTML = `<section class="card"><p class="bad">${esc(t(e.message))}</p></section>`; return; }
    const when = (d) => (d && !d.startsWith('0001') ? new Date(d).toLocaleDateString(LANG === 'de' ? 'de-DE' : 'en-GB') : '–');
    const users = res.users || [];
    el.innerHTML = `
      <section class="card">
        <h2>${t('Benutzer')}</h2>
        <p class="muted">${t('Änderungen an Konten gelten sofort, ohne Speichern.')}</p>
        <div class="users">${users.map((u) => `
          <div class="user ${u.pending ? 'pending' : ''} ${u.disabled ? 'disabled' : ''}" data-u="${esc(u.name)}">
            <div class="grow"><b>${esc(u.name)}</b>${u.name === res.me ? ` <span class="muted">(${t('du')})</span>` : ''}
              <div class="muted small">${u.pending ? `<span class="tag warn">${t('wartet auf Freigabe')}</span> ` : ''}${u.disabled ? `<span class="tag">${t('gesperrt')}</span> ` : ''}
              ${t('angelegt')} ${when(u.created)} · ${t('zuletzt angemeldet')} ${when(u.lastLogin)}</div></div>
            <div class="btns">
              ${u.pending ? `<button class="btn primary" data-a="approve">${t('Freigeben')}</button>` : ''}
              <select data-a="role" title="${t('Rolle')}"><option value="user" ${u.role !== 'admin' ? 'selected' : ''}>${t('Benutzer||Rolle')}</option><option value="admin" ${u.role === 'admin' ? 'selected' : ''}>${t('Admin')}</option></select>
              ${u.pending ? '' : `<button class="btn" data-a="disable">${u.disabled ? t('Entsperren') : t('Sperren')}</button>`}
              <button class="btn" data-a="pw">${t('Passwort')}</button>
              <button class="btn ghost" data-a="del">${u.pending ? t('Ablehnen') : t('Löschen')}</button>
            </div>
          </div>`).join('') || `<p class="empty-note">${t('Noch keine Konten.')}</p>`}</div>
      </section>
      <section class="card">
        <h2>${t('Konto anlegen')}</h2>
        <form id="nform" autocomplete="off" onsubmit="return false">
          <div class="row2">
            <label class="field"><span>${t('Name')}</span><input id="nname" autocapitalize="none" spellcheck="false" maxlength="64"></label>
            <label class="field"><span>${t('Passwort')}</span><input id="npw" type="password" autocomplete="new-password"></label>
          </div>
          <div class="addrow"><select id="nrole"><option value="user">${t('Benutzer||Rolle')}</option><option value="admin">${t('Admin')}</option></select>
            <button class="btn primary" id="nadd">${t('Anlegen')}</button></div>
        </form>
      </section>
      <section class="card">
        <h2>${t('Mein Passwort')}</h2>
        <form id="mform" onsubmit="return false">
          <input type="text" autocomplete="username" value="${esc(res.me)}" hidden>
          <div class="row2">
            <label class="field"><span>${t('Bisheriges Passwort')}</span><input id="mold" type="password" autocomplete="current-password"></label>
            <label class="field"><span>${t('Neues Passwort')}</span><input id="mnew" type="password" autocomplete="new-password"></label>
          </div>
          <button class="btn" id="msave">${t('Passwort ändern')}</button>
        </form>
      </section>`;
    const act = async (name, body, method) => {
      const r = await api(`/api/users/${encodeURIComponent(name)}`, body, method).catch((e) => ({ ok: false, error: e.message }));
      if (!r.ok) { toast(t(r.error || '')); return false; }
      return true;
    };
    for (const row of $$('[data-u]', el)) {
      const name = row.dataset.u;
      const u = users.find((x) => x.name === name);
      row.onclick = async (e) => {
        const a = e.target.closest('button[data-a]')?.dataset.a;
        if (!a) return;
        if (a === 'approve' && await act(name, { approve: true })) { toast(t('{name} freigegeben', { name })); renderUsers(el); }
        if (a === 'disable' && await act(name, { disabled: !u.disabled })) renderUsers(el);
        if (a === 'pw') {
          const b = e.target.closest('button');
          if (b.dataset.armed) return;
          b.dataset.armed = '1';
          b.outerHTML = `<span class="addrow"><input type="password" class="pwin" autocomplete="new-password" placeholder="${t('Neues Passwort')}"><button class="btn primary" data-a="pwset">OK</button></span>`;
          $('.pwin', row).focus();
        }
        if (a === 'pwset') {
          const pw = $('.pwin', row).value;
          if (await act(name, { password: pw })) { toast(t('Passwort gesetzt')); renderUsers(el); }
        }
        if (a === 'del') {
          const b = e.target.closest('button');
          if (!b.dataset.armed) { b.dataset.armed = '1'; b.textContent = t('Wirklich?'); b.classList.add('danger'); setTimeout(() => { if (document.contains(b)) { delete b.dataset.armed; b.textContent = u.pending ? t('Ablehnen') : t('Löschen'); b.classList.remove('danger'); } }, 4000); return; }
          if (await act(name, undefined, 'DELETE')) { toast(t('{name} gelöscht', { name })); renderUsers(el); }
        }
      };
      $('select[data-a=role]', row).onchange = async (e) => { if (!(await act(name, { role: e.target.value }))) renderUsers(el); else toast(t('Rolle geändert')); };
    }
    $('#nadd', el).onclick = async () => {
      const r = await api('/api/users', { name: $('#nname', el).value.trim(), password: $('#npw', el).value, role: $('#nrole', el).value }).catch((e) => ({ ok: false, error: e.message }));
      if (!r.ok) { toast(t(r.error || '')); return; }
      toast(t('Konto angelegt'));
      renderUsers(el);
    };
    $('#msave', el).onclick = async () => {
      const r = await api('/api/me/password', { old: $('#mold', el).value, new: $('#mnew', el).value }).catch((e) => ({ ok: false, error: e.message }));
      if (!r.ok) { toast(t(r.error || '')); return; }
      $('#mold', el).value = ''; $('#mnew', el).value = '';
      toast(t('Passwort geändert'));
    };
  }

  // --- step 6: summary ------------------------------------------------------------------
  function renderSummary(el) {
    const f = C.library.folders;
    const row = (k, v) => `<div class="sumrow"><span>${k}</span><b>${v}</b></div>`;
    el.innerHTML = `
      <section class="card">
        <h2>${S.mode === 'install' ? t('Alles bereit') : t('Einstellungen speichern')}</h2>
        <div class="summary">
          ${row(t('Fotos'), f.map((x) => `${esc(x.name)} <span class="muted">${esc(x.path)}</span>`).join('<br>'))}
          ${row(t('Cache'), esc(C.cache.dir) + (importPath ? `<br><span class="muted">${t('übernimmt {x}', { x: esc(importPath) })}</span>` : ''))}
          ${row(t('Vorschauen'), [C.cache.previews ? t('große vorab') : t('große bei Bedarf'), C.media.video_previews ? t('animierte Videos') : t('Videos ohne Animation'), C.media.transcode ? t('HEVC umwandeln') : ''].filter(Boolean).join(' · '))}
          ${row(t('Gesichter'), C.faces.mode === 'off' ? t('aus') : C.faces.mode === 'remote' ? `${t('Container')} ${esc(C.faces.url)}` : t('Python auf diesem Rechner'))}
          ${row(t('Sicherung'), C.backup.dir ? `${esc(C.backup.dir)} <span class="muted">${{ '1h': t('stündlich'), '24h': t('täglich'), '168h': t('wöchentlich'), '0': t('auf Knopfdruck') }[C.backup.interval] || esc(C.backup.interval)}</span>` : t('aus'))}
          ${row(t('Adresse'), esc(C.server.listen) + (tokenMode === 'off' ? ` · ${t('ohne Schlüssel')}` : tokenMode === 'new' ? ` · ${t('neuer Schlüssel')}` : ''))}
          ${row(t('Konten'), [S.needAdmin ? `${t('Admin')}: ${esc(admin.name)}` : '', C.access.anonymous ? t('ansehen ohne Konto') : t('nur mit Konto'), C.access.register ? t('Registrieren mit Freigabe') : t('Konten nur durch Admins')].filter(Boolean).join(' · '))}
          ${row(t('Datei'), `<code>${esc(S.configPath)}</code>`)}
        </div>
        <div id="prog" hidden><div class="bar"><i></i></div><p class="muted" id="progText"></p></div>
      </section>`;
  }

  async function save() {
    const btn = $('#next');
    btn.disabled = true;
    const prog = $('#prog');
    prog.hidden = false;
    $('#progText').textContent = t('Speichern …');
    const plan = { config: C, tokenMode, import: importPath };
    if (S.needAdmin) plan.admin = { name: admin.name.trim(), password: admin.pw };
    const res = await api('/api/setup/save', plan).catch((e) => ({ ok: false, error: e.message }));
    if (!res.ok) { $('#progText').innerHTML = `<span class="bad">${esc(t(res.error || ''))}</span>`; btn.disabled = false; return; }
    const poll = async () => {
      let p;
      try { p = await api('/api/setup/progress'); } catch { setTimeout(poll, 1000); return; }
      $('#prog .bar i').style.width = p.total ? `${(p.done / p.total) * 100}%` : p.finished ? '100%' : '30%';
      $('#progText').textContent = `${t(p.phase || '')}${p.total ? ` ${num(p.done)} / ${num(p.total)}` : ''}`;
      if (p.finished) {
        if (p.error) { $('#progText').innerHTML = `<span class="bad">${esc(t(p.error))}</span>`; btn.disabled = false; return; }
        $('#progText').textContent = t('Fertig – die Galerie öffnet sich …');
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
    $('#pickList').innerHTML = l.error ? `<p class="bad">${esc(l.error)}</p>` : (l.dirs || []).map((d) => `<button data-p="${esc(d.path)}">${icon('folder')}<span>${esc(d.name)}</span>${icon('right')}</button>`).join('') || `<p class="muted">${t('Keine Unterordner.')}</p>`;
    $('#pickInfo').textContent = t('{n} Unterordner', { n: (l.dirs || []).length });
    for (const b of $$('#picker [data-p]')) b.onclick = () => browse(b.dataset.p);
  }
  $('#pickInput').onkeydown = (e) => { if (e.key === 'Enter') browse(e.target.value); };
  $('#pickOk').onclick = () => { $('#picker').hidden = true; pickCb?.($('#pickInput').value); };
  for (const b of $$('#picker [data-close]')) b.onclick = () => { $('#picker').hidden = true; };
  $('#picker').onclick = (e) => { if (e.target.id === 'picker') $('#picker').hidden = true; };

  // --- start ----------------------------------------------------------------------------
  window.skTranslate?.();
  $('#prev').onclick = () => go(step - 1);
  $('#next').onclick = () => (step === STEPS.length - 1 ? save() : go(step + 1));
  (async () => {
    try { S = await api('/api/setup/state'); } catch (e) {
      $('#step').innerHTML = `<section class="card"><h2>${t('Kein Zugriff')}</h2><p>${esc(t(e.message))}</p></section>`;
      $('.setup-foot').hidden = true;
      return;
    }
    C = clone(S.config);
    C.library.folders ||= [];
    C.library.exclude ||= [];
    C.access ||= { anonymous: true, register: true };
    buildSteps();
    if (S.mode === 'settings') {
      $('#title').textContent = t('Einstellungen');
      $('#subtitle').textContent = `Schuhkarton ${S.version} · ${S.os}`;
      $('#back').hidden = false;
      document.title = `Schuhkarton – ${t('Einstellungen')}`;
      backupStatus = await api('/api/backup').catch(() => null);
      if (S.needAdmin) $('#subtitle').textContent = t('Noch kein Admin-Konto: bitte jetzt anlegen.');
    }
    const want = location.hash.slice(1) || (S.mode === 'settings' && S.needAdmin ? 'konto' : '');
    const wi = STEPS.findIndex((x) => x.id === want);
    go(wi >= 0 ? wi : 0);
    addEventListener('hashchange', () => { const i = STEPS.findIndex((x) => x.id === location.hash.slice(1)); if (i >= 0 && i !== step) go(i); });
  })();
})();
