/* Schuhkarton — setup wizard and settings page */
'use strict';
(() => {
  const $ = (s, el = document) => el.querySelector(s);
  const $$ = (s, el = document) => [...el.querySelectorAll(s)];
  const esc = (s) => String(s ?? '').replace(/[&<>"']/g, (c) => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c]));
  const icon = (n, cls = '') => `<svg class="i ${cls}"><use href="#i-${n}"/></svg>`;
  // forms that only group fields (password managers): Enter submits nothing
  document.addEventListener('submit', (e) => { if (e.target.matches?.('form[data-nosubmit]')) e.preventDefault(); });
  const num = (n) => new Intl.NumberFormat('de-DE').format(n || 0);
  const bytes = (b) => (!b ? '?' : b > 1e12 ? `${(b / 1e12).toFixed(1)} TB` : b > 1e9 ? `${(b / 1e9).toFixed(1)} GB` : `${Math.round(b / 1e6)} MB`);
  const clone = (o) => JSON.parse(JSON.stringify(o));
  const LANG = window.skLang || 'de';
  const t = window.skT || ((s) => s);

  async function api(path, body, method) {
    const r = await fetch(path, body === undefined && !method ? {} : { method: method || 'POST', headers: { 'Content-Type': 'application/json' }, body: body === undefined ? undefined : JSON.stringify(body) });
    if (!r.ok) throw Object.assign(new Error((await r.text()) || r.statusText), { status: r.status });
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
  let aiTest = null;      // last connection test
  let aiModels = null;    // models offered by the service
  let aiKeyField = null;  // what the key field shows: the key, ******** or $VARIABLE
  let backupCheck = null;
  let backupStatus = null;
  const admin = { name: '', pw: '', pw2: '' }; // first admin (install, or no admin yet)
  // Settings of a running instance are tabs with a save button on each;
  // the first installation stays a wizard (numbered steps, back, next).
  const tabs = () => S.mode === 'settings';
  let saved = '';         // the settings as last loaded or saved (see snapshot)
  let saving = false;
  let saveErr = '';       // why the last save failed; stays until the next edit

  const locked = (key) => S.locked?.[key];
  // background windows: days as in the settings file ("mon-fri", "sat,sun";
  // empty: every day), as a bit set Sunday = 1 … Saturday = 64
  // the same names as schedule.ParseDays
  const DAYS = {
    sun: 0, su: 0, so: 0, sonntag: 0, sunday: 0, mon: 1, mo: 1, montag: 1, monday: 1, tue: 2, tu: 2, di: 2, dienstag: 2, tuesday: 2,
    wed: 3, we: 3, mi: 3, mittwoch: 3, wednesday: 3, thu: 4, th: 4, do: 4, donnerstag: 4, thursday: 4,
    fri: 5, fr: 5, freitag: 5, friday: 5, sat: 6, sa: 6, samstag: 6, saturday: 6,
  };
  const parseDays = (s) => {
    s = (s || '').trim().toLowerCase();
    if (['', '*', 'daily', 'täglich', 'all', 'alle'].includes(s)) return 127;
    if (s === 'none') return 0; // all days unticked: not "every day"; the step asks for one
    let m = 0;
    for (const part of s.split(',')) {
      const [a, b] = part.trim().split('-').map((x) => DAYS[x.trim()]);
      if (a === undefined || (part.includes('-') && b === undefined)) return 0;
      for (let d = a; ; d = (d + 1) % 7) { m |= 1 << d; if (d === (b ?? a)) break; }
    }
    return m;
  };
  const daysText = (m) => {
    if (m === 127) return '';
    if (!m) return 'none';
    const names = ['sun', 'mon', 'tue', 'wed', 'thu', 'fri', 'sat'], order = [1, 2, 3, 4, 5, 6, 0], out = [];
    for (let i = 0; i < 7;) {
      if (!(m & (1 << order[i]))) { i++; continue; }
      let j = i;
      while (j + 1 < 7 && m & (1 << order[j + 1])) j++;
      out.push(j > i ? `${names[order[i]]}-${names[order[j]]}` : names[order[i]]);
      i = j + 1;
    }
    return out.join(',');
  };
  // the kinds of background work a window can allow (schedule.Jobs)
  const JOBS = [['scan', 'Einlesen'], ['media', 'Vorschaubilder und Gesichter'], ['ai', 'Beschreibungen'], ['ocr', 'Texterkennung'],
    ['speech', 'Spracherkennung'], ['metadata', 'Metadaten schreiben'], ['dupes', 'Duplikate'], ['backup', 'Sicherung'],
    ['cache', 'Cache auslagern'], ['import', 'Import']];
  // as the server reads them: any case, spaces around, each once
  const normJobs = (jobs) => [...new Set((jobs || []).map((j) => String(j).trim().toLowerCase()))].filter((k) => JOBS.some(([x]) => x === k));
  const jobsText = (jobs) => {
    const j = normJobs(jobs);
    return j.length && j.length < JOBS.length ? ` (${t('nur')} ${JOBS.filter(([k]) => j.includes(k)).map(([, l]) => t(l)).join(', ')})` : '';
  };
  // the background windows in a line, for the summary
  const bgSummary = () => {
    const B = C.background || {}, short = ['So', 'Mo', 'Di', 'Mi', 'Do', 'Fr', 'Sa'];
    const days = (m) => m === 127 ? t('täglich') : [1, 2, 3, 4, 5, 6, 0].filter((d) => m & (1 << d)).map((d) => t(short[d])).join(', ');
    const parts = (B.windows || []).map((w) => `${days(parseDays(w.days))} ${w.from}–${w.to}${jobsText(w.jobs)}`);
    if (B.from && B.to) parts.unshift(`${t('täglich')} ${B.from}–${B.to}`);
    for (const o of B.once || []) parts.push(`${t('einmalig')} ${(o.from || '').replace('T', ' ')}–${o.to}${jobsText(o.jobs)}`);
    return parts.length ? esc(parts.join(' · ')) : t('jederzeit');
  };
  const windowsOK = () => {
    const B = C.background || {};
    const pair = (a, b) => !!a && !!b && a !== b;
    if (!B.from !== !B.to || (B.from && !pair(B.from, B.to))) return false;
    return (B.windows || []).every((w) => parseDays(w.days) && pair(w.from, w.to)) &&
      (B.once || []).every((o) => /^\d{4}-\d{2}-\d{2}[ T]\d{2}:\d{2}/.test(o.from || '') && /\d{2}:\d{2}$/.test(o.to || ''));
  };
  // cache.max_size like "4GB" needs cache.overflow_dir (see config.ParseSize)
  const cacheCapOK = () => {
    const m = (C.cache.max_size || '').trim(), o = (C.cache.overflow_dir || '').trim();
    const abs = (p) => p.startsWith('/') || p.startsWith('\\\\') || /^[A-Za-z]:[\\/]/.test(p); // also \\server\share
    if (!m) return !o || abs(o);
    return /^\d+(\.\d+)?\s*([KMGT]i?B?|B)?$/i.test(m) && abs(o);
  };
  const lockNote = (key) => (locked(key) ? `<span class="lock">${t('festgelegt durch {x}', { x: esc(locked(key)) })}</span>` : '');
  // the colours of the logo (ui.logo), named for the settings page
  const LOGOS = { aqua: 'Aqua', peach: 'Pfirsich', rose: 'Rosé', lavender: 'Lavendel', mint: 'Minze', lemon: 'Zitrone', sky: 'Himmel',
    blue: 'Blau', red: 'Rot', green: 'Grün', sunset: 'Sonnenuntergang', nightsky: 'Nachthimmel',
    aurora: 'Polarlicht', ocean: 'Ozean', forest: 'Wald', fire: 'Feuer', neon: 'Neon', candy: 'Bonbon', retro: 'Retro',
    kraft: 'Karton', gold: 'Gold', sepia: 'Sepia',
    sunrise: 'Sonnenaufgang', copper: 'Kupfer', bronze: 'Bronze', silver: 'Silber', metallic: 'Metallic', glass: 'Glas', neonblue: 'Neon Blau', neongreen: 'Neon Grün', neonorange: 'Neon Orange', neonpink: 'Neon Pink', rainbow: 'Regenbogen', negative: 'Negativ', toxic: 'Giftig', vaporwave: 'Vaporwave', thermal: 'Wärmebild',
    sage: 'Salbei (gedeckt)', slate: 'Schiefer (gedeckt)', clay: 'Ton (gedeckt)', mauve: 'Malve (gedeckt)', mono: 'Schwarzweiß' };
  // the chosen colours of the logo; older settings have one name
  const logos = () => [].concat(C.ui.logo || S.logos?.[0] || 'aqua');
  const dis = (key) => (locked(key) ? 'disabled' : '');
  // settings by key ("media.thumb_size"): in the form (C), as saved (S.config), the default (S.defaults)
  const getKey = (key, o = C) => key.split('.').reduce((v, k) => v?.[k], o);
  const setKey = (key, v) => { const ks = key.split('.'), last = ks.pop(); ks.reduce((o, k) => (o[k] ??= {}), C)[last] = v; };
  const defaultNote = (key) => t('Standard: {v}', { v: getKey(key, S.defaults) ?? '' });
  const fieldHelp = (help) => (help ? `<span class="muted small">${help}</span>` : '');
  // a number field; bindFields keeps the value from before for anything outside min..max
  const numField = (key, label, min, max, step, help = '') => `<label class="field"><span>${label}</span>
    <input type="number" data-num="${key}" min="${min}" max="${max}" step="${step}" value="${esc(String(getKey(key) ?? ''))}" ${dis(key)}>${lockNote(key)}${fieldHelp(help)}</label>`;
  const textField = (key, label, placeholder = '', help = '') => `<label class="field"><span>${label}</span>
    <input data-text="${key}" value="${esc(getKey(key) || '')}" placeholder="${esc(placeholder)}" spellcheck="false" ${dis(key)}>${lockNote(key)}${fieldHelp(help)}</label>`;
  // wires numField and textField; changed(key) runs after each change
  const bindFields = (el, changed) => {
    for (const inp of $$('[data-num]', el)) {
      const key = inp.dataset.num;
      const ok = () => inp.value !== '' && +inp.value >= +inp.min && +inp.value <= +inp.max;
      let before = getKey(key);
      inp.onfocus = () => { before = getKey(key); };
      // change too: the browser may clamp a value above max when the field is left
      inp.oninput = inp.onchange = () => { setKey(key, ok() ? +inp.value : before); changed?.(key); };
      inp.onblur = () => { if (!ok()) inp.value = getKey(key); };
    }
    for (const inp of $$('[data-text]', el)) inp.oninput = () => { setKey(inp.dataset.text, inp.value.trim()); changed?.(inp.dataset.text); };
  };
  // the start page of the gallery as this browser sees it (behind a proxy under its path)
  const homeLink = (link) => (!link ? S.home || '/' : link.startsWith('/') ? (S.home || '/') + link.slice(1) : link);
  // HTTPS switched on or off: this page's address stops answering, so the
  // progress cannot be asked for there. followTLS goes to the same host name
  // with the other scheme (and the new port, if that changed too) once it
  // answers, or after a while anyway (e.g. a certificate the browser asks about).
  // through a reverse proxy the browser's address does not change with it
  const tlsFlip = () => !S.proxied && !!C.server.tls_cert !== !!S.config.server.tls_cert;
  function followTLS(path) {
    // the port stays, also when the address left it out (80 for http, 443 for https)
    const port = C.server.listen !== S.config.server.listen ? (C.server.listen.match(/:(\d+)$/) || [])[1]
      : location.port || (location.protocol === 'https:' ? '443' : '80');
    const scheme = C.server.tls_cert ? 'https:' : 'http:';
    const dflt = port === (scheme === 'https:' ? '443' : '80');
    const origin = `${scheme}//${location.hostname}${port && !dflt ? `:${port}` : ''}`;
    $('#hint').textContent = t('Wechsel zu {u} …', { u: origin });
    const go = () => { location.href = origin + path; };
    const probe = (n) => fetch(`${origin}/healthz`, { mode: 'no-cors', cache: 'no-store' })
      .then(() => setTimeout(go, 500), () => (n > 0 ? setTimeout(() => probe(n - 1), 1000) : go()));
    setTimeout(() => probe(30), 1500);
  }

  const adminOK = () => admin.name.trim() && admin.pw.length >= 8 && admin.pw === admin.pw2;
  let STEPS = [];
  function buildSteps() {
    STEPS = [
      { id: 'fotos', title: t('Fotos'), render: renderFolders, ok: () => C.library.folders.length > 0 && C.library.folders.every((f) => {
        const c = checks.get(f.path);
        // a folder that is configured already may be offline for a while
        return !c || c.exists || (tabs() && (S.config.library.folders || []).some((x) => x.path === f.path));
      }) },
      { id: 'speicher', title: t('Speicher'), render: renderStorage, ok: () => !!C.cache.dir && windowsOK() && cacheCapOK() },
      { id: 'gesichter', title: t('Gesichter'), render: renderFaces, ok: () => C.faces.mode !== 'remote' || !!C.faces.url },
      { id: 'ki', title: t('KI'), render: renderAI, ok: () => !aiUsed() || (/^https?:\/\//.test(C.ai.url || '') && (C.ai.mode !== 'on' || !!C.ai.model) && (!ocrOn() || !!C.ai.ocr_model) && (!speechOn() || !!C.ai.speech_model)) },
      { id: 'sicherung', title: t('Sicherung'), render: renderBackup, ok: () => true },
      { id: 'zugriff', title: t('Zugriff'), render: renderAccess, ok: () => /:\d+$/.test(C.server.listen) },
      S.needAdmin ? { id: 'konto', title: t('Admin'), render: renderAdmin, ok: adminOK }
        : { id: 'benutzer', title: t('Benutzer'), render: renderUsers, ok: () => true },
      { id: 'fertig', title: t('Fertig'), render: renderSummary, ok: () => !S.needAdmin || adminOK() },
    ];
    if (tabs()) { // the summary is the overview, first
      STEPS.pop();
      STEPS.unshift({ id: 'uebersicht', title: t('Übersicht'), render: renderSummary, ok: () => true });
    }
  }

  // snapshot is what saving would send, without empty values (pages fill
  // in empty defaults when they are opened), to tell unsaved changes
  function snapshot() {
    const clean = (v) => {
      if (Array.isArray(v)) return v.map(clean);
      if (v && typeof v === 'object') {
        const o = {};
        for (const k of Object.keys(v).sort()) {
          const c = clean(v[k]);
          if (c === '' || c == null || (Array.isArray(c) && !c.length) || (typeof c === 'object' && !Array.isArray(c) && !Object.keys(c).length)) continue;
          o[k] = c;
        }
        return o;
      }
      return v;
    };
    // the same settings written differently (pages rewrite them when opened):
    // the import target as a folder's name, its path, or empty for the first
    // folder; the old single background window as from/to
    const c = clone(C), imp = c.library?.import, fs = c.library?.folders || [];
    if (imp?.folder) {
      imp.folder = fs.find((f) => f.name === imp.folder || f.path === imp.folder)?.path || imp.folder;
      if (imp.folder === fs[0]?.path) imp.folder = '';
    }
    const B = c.background;
    if (B?.from && B?.to) { B.windows = [{ days: '', from: B.from, to: B.to }, ...(B.windows || [])]; B.from = B.to = ''; }
    return JSON.stringify(clean({ C: c, tokenMode, importPath, admin: S.needAdmin ? admin : null }));
  }
  const dirty = () => snapshot() !== saved;

  function drawSteps() {
    $('#steps').classList.toggle('tabs', tabs());
    $('#steps').innerHTML = tabs()
      ? STEPS.map((s, i) => `<button data-i="${i}" class="${i === step ? 'on' : ''}">${s.title}</button>`).join('')
      : STEPS.map((s, i) => `<button data-i="${i}" class="${i === step ? 'on' : ''} ${i < step ? 'done' : ''}"><span>${i + 1}</span>${s.title}</button>`).join('');
    for (const b of $$('#steps button')) b.onclick = () => go(+b.dataset.i);
  }

  function go(i) {
    step = Math.max(0, Math.min(STEPS.length - 1, i));
    drawSteps();
    STEPS[step].render($('#step'));
    $('#prev').hidden = step === 0 || tabs();
    const last = step === STEPS.length - 1;
    $('#next').textContent = tabs() ? t('Speichern') : last ? (S.mode === 'install' ? t('Einrichten und starten') : t('Speichern')) : t('Weiter');
    if (tabs()) history.replaceState(null, '', `#${STEPS[step].id}`);
    validate();
    scrollTo({ top: 0, behavior: 'smooth' });
  }

  function validate() {
    if (saving) return;
    const cur = STEPS[step].ok();
    // tabs: the checks of the pages only advise (the server checks the
    // settings when saving and says what is wrong); saving needs changes
    const bad = tabs() ? (cur ? STEPS.find((x) => !x.ok()) : STEPS[step]) : null;
    // except what only this page can check: the first admin's password, and
    // that a folder is left (the gallery needs one to start)
    const ok = tabs() ? dirty() && (!S.needAdmin || adminOK()) && C.library.folders.length > 0 : cur;
    $('#next').disabled = !ok;
    if (tabs() && saveErr) { $('#hint').innerHTML = `<span class="bad">${esc(t(saveErr))}</span>`; return; }
    if (tabs() && bad && bad !== STEPS[step]) { $('#hint').textContent = t('Bitte „{x}“ prüfen.', { x: bad.title }); return; }
    if (tabs() && cur && saveErr) { $('#hint').innerHTML = `<span class="bad">${esc(t(saveErr))}</span>`; return; }
    if (tabs() && cur) { $('#hint').textContent = dirty() ? t('Ungespeicherte Änderungen') : ''; return; }
    $('#hint').textContent = cur ? '' : ({ fotos: t('Mindestens einen Fotoordner hinzufügen.'), gesichter: t('Adresse des Containers angeben oder Gesichter ausschalten.'),
      zugriff: t('Adresse mit Port angeben, z.B. 0.0.0.0:8088.'), konto: t('Name und Passwort (mindestens 8 Zeichen, zweimal gleich) für das Admin-Konto angeben.'),
      fertig: t('Erst ein Admin-Konto anlegen.'), speicher: !C.cache.dir ? '' : !cacheCapOK() ? t('Obergrenze wie 4GB oder 500MB, dazu einen Auslagerungsordner (absoluter Pfad).') : t('Zeitfenster: mindestens ein Tag, Beginn und Ende (verschieden); einmalige mit Datum.') }[STEPS[step].id] || '');
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
        <p class="muted">${t('Ordner mit Fotos und Videos. Unterordner werden mitgenommen. Schuhkarton zwingt keine Ordnerstruktur auf; Originale ändern sich nur, wenn ein Admin bearbeitet, dreht, verschiebt oder löscht, und wenn Erkanntes in ihre Metadaten geschrieben wird (abschaltbar unten).')}</p>
        ${lockedF ? `<p class="lock">${t('Die Ordner sind festgelegt durch {x}.', { x: esc(lockedF) })}</p>` : ''}
        <div class="folders" id="flist"></div>
        ${lockedF ? '' : `<div class="addrow">
          <input id="fpath" placeholder="${t('/pfad/zu/den/Fotos')}" spellcheck="false">
          <button class="btn" id="fbrowse">${icon('folder')}<span>${t('Durchsuchen')}</span></button>
          <button class="btn primary" id="fadd">${icon('plus')}<span>${t('Hinzufügen')}</span></button>
        </div>
        <div class="chips" id="fsugg"></div>`}
        <div class="import">
          <h3>${t('Import-Ordner')}</h3>
          <p class="muted">${t('Fotos und Videos, die hier landen (auch in Unterordnern), wandern von selbst in die Bibliothek: nach den Ablage-Regeln, ohne Regeln mit ihren Unterordnern in den Fotoordner. Der Import-Ordner leert sich dabei; andere Dateien bleiben liegen.')}</p>
          <div class="addrow">
            <input id="fimp" value="${esc(C.library.import?.dir || '')}" placeholder="${t('leer: kein Import-Ordner')}" spellcheck="false" ${dis('library.import.dir')}>
            <button class="btn" id="fimpb" ${dis('library.import.dir')}>${icon('folder')}<span>${t('Durchsuchen')}</span></button>
          </div>${lockNote('library.import.dir')}
          <label class="field"><span>${t('In diesen Fotoordner')}</span><select id="fimpf" ${dis('library.import.folder')}></select>${lockNote('library.import.folder')}</label>
        </div>
        <details class="more"><summary>${t('Ordnernamen, Ausnahmen, Bearbeiten')}</summary>
          <label class="check"><input type="checkbox" id="fmeta" ${C.library.folder_meta ? 'checked' : ''} ${dis('library.folder_meta')}>
            <span><b>${t('Ordnernamen auswerten')}</b><br><span class="muted">${t('Ordner wie {ex} liefern Datum, Ort und Kategorie, wenn die Fotos selbst nichts Passenderes wissen. Bearbeitete Angaben gehen immer vor.', { ex: '<code>2019/07 Juli Ort</code>' })}</span></span></label>
          ${lockNote('library.folder_meta')}
          <label class="check"><input type="checkbox" id="fwrite" ${C.library.write_files ? 'checked' : ''} ${dis('library.write_files')}>
            <span><b>${t('Änderungen in die Dateien schreiben')}</b><br><span class="muted">${t('Datum, Ort, GPS, Text und Drehung, die Admins ändern, landen mit exiftool in den Fotos selbst. Aus: nur im Schuhkarton gespeichert.')}</span></span></label>
          ${lockNote('library.write_files')}
          <label class="check"><input type="checkbox" id="fxmp" ${C.library.write_metadata ? 'checked' : ''} ${dis('library.write_metadata')}>
            <span><b>${t('Erkanntes in die Dateien schreiben')}</b><br><span class="muted">${t('KI-Beschreibungen und Stichwörter, erkannter und gesprochener Text und die Namen der Personen landen in den XMP-Metadaten der Dateien (bei RAW und Formaten, die exiftool nicht schreibt, in einer .xmp-Datei daneben). Andere Programme sehen sie, und der Schuhkarton liest sie wieder ein, wenn der Cache fehlt.')}</span></span></label>
          ${lockNote('library.write_metadata')}
          <label class="field"><span>${t('Gelöschtes und Änderungen so viele Tage aufheben (0 = Papierkorb nie leeren)')}</span>
            <input type="number" id="ftrash" min="0" max="3650" value="${C.library.trash_days ?? 30}" ${dis('library.trash_days')}>${lockNote('library.trash_days')}</label>
          <label class="check"><input type="checkbox" id="fdupes" ${(C.dupes?.mode || 'on') === 'on' ? 'checked' : ''} ${dis('dupes.mode')}>
            <span><b>${t('Duplikate suchen')}</b><br><span class="muted">${t('Vergleicht die Vorschaubilder (auch gedreht, verkleinert, neu gespeichert) und zeigt Admins doppelte Fotos und Serien. Läuft im Hintergrund, braucht kaum Rechenzeit.')}</span></span></label>
          ${lockNote('dupes.mode')}
          <label class="field"><span>${t('Empfindlichkeit')}</span><select id="fdsens" ${dis('dupes.sensitivity')}>
            ${[['strict', t('streng – nur sehr sichere Treffer')], ['normal', t('normal')], ['loose', t('großzügig – mehr Treffer, mehr prüfen')]].map(([v, l]) => `<option value="${v}" ${(C.dupes?.sensitivity || 'normal') === v ? 'selected' : ''}>${l}</option>`).join('')}
          </select>${lockNote('dupes.sensitivity')}</label>
          ${numField('dupes.similar_minutes', t('Serie: höchstens so viele Minuten zwischen zwei Fotos'), 1, 1440, 1, defaultNote('dupes.similar_minutes'))}
          <label class="field"><span>${t('Ordner mit Dokumenten (Scans, Belege; standardmäßig ausgeblendet, kommagetrennt)')}</span>
          <input id="fdocs" value="${esc((C.library.documents || []).join(', '))}" ${dis('library.documents')}>${lockNote('library.documents')}</label>
          <label class="field"><span>${t('Diese Datei- und Ordnernamen auslassen (Muster, kommagetrennt)')}</span>
          <input id="fexcl" value="${esc((C.library.exclude || []).join(', '))}" ${dis('library.exclude')}>${lockNote('library.exclude')}</label>
        </details>
      </section>`;
    const list = $('#flist', el);
    // the library folder imports go to: one of the folders above (the first when none is chosen)
    C.library.import ||= { dir: '', folder: '' };
    // the target is held as the folder itself, so renaming it (even through an empty name) keeps it
    let impTarget = C.library.folders.find((f) => C.library.import.folder && (f.name === C.library.import.folder || f.path === C.library.import.folder)) || null;
    const at = C; // late callbacks after a save (new settings, new page) do nothing
    const drawImport = () => {
      if (C !== at) return;
      const sel = $('#fimpf', el);
      const fs = C.library.folders;
      if (impTarget && !fs.includes(impTarget)) impTarget = null; // removed: back to the first folder
      // by path: names may still change (or be left empty) until the settings are saved
      C.library.import.folder = impTarget && impTarget !== fs[0] ? impTarget.path : '';
      sel.innerHTML = fs.map((f, i) => `<option value="${i}" ${(impTarget || fs[0]) === f ? 'selected' : ''}>${esc(f.name || t('Ohne Namen'))}${i === 0 ? ` (${t('Standard')})` : ''}</option>`).join('');
    };
    const draw = () => {
      if (C !== at) return;
      list.innerHTML = C.library.folders.length ? C.library.folders.map((f, i) => `
        <div class="folder">
          ${icon('folder')}
          <div class="grow"><input class="name" data-i="${i}" value="${esc(f.name || '')}" placeholder="${t('Name')}" ${lockedF ? 'disabled' : ''} title="${t('Name des Ordners in der Galerie')}">
          <div class="path">${esc(f.path)}</div><div class="stat">${folderStats(checks.get(f.path))}</div></div>
          ${lockedF ? '' : `<button class="btn ghost icon" data-rm="${i}" title="${t('Entfernen')}">${icon('x')}</button>`}
        </div>`).join('') : `<p class="empty-note">${t('Noch kein Ordner.')}</p>`;
      for (const b of $$('[data-rm]', list)) b.onclick = () => { C.library.folders.splice(+b.dataset.rm, 1); draw(); validate(); };
      for (const n of $$('input.name', list)) {
        n.oninput = () => {
          C.library.folders[+n.dataset.i].name = n.value;
          drawImport(); // the import target follows the rename
        };
      }
      drawImport();
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
    $('#fimp', el).oninput = (e) => { C.library.import.dir = e.target.value.trim(); };
    $('#fimpb', el).onclick = () => pick($('#fimp', el).value, (p) => { C.library.import.dir = p; $('#fimp', el).value = p; });
    $('#fimpf', el).onchange = (e) => { impTarget = C.library.folders[+e.target.value] || null; drawImport(); };
    const ex = $('#fexcl', el);
    ex.oninput = () => { C.library.exclude = ex.value.split(',').map((s) => s.trim()).filter(Boolean); };
    $('#fdocs', el).oninput = (e) => { C.library.documents = e.target.value.split(',').map((x) => x.trim()).filter(Boolean); };
    $('#fmeta', el).onchange = (e) => { C.library.folder_meta = e.target.checked; };
    $('#fwrite', el).onchange = (e) => { C.library.write_files = e.target.checked; };
    $('#fxmp', el).onchange = (e) => { C.library.write_metadata = e.target.checked; };
    $('#ftrash', el).oninput = (e) => { C.library.trash_days = Math.max(0, +e.target.value || 0); };
    C.dupes ||= { mode: 'on', sensitivity: 'normal', similar_minutes: 10 };
    $('#fdupes', el).onchange = (e) => { C.dupes.mode = e.target.checked ? 'on' : 'off'; };
    $('#fdsens', el).onchange = (e) => { C.dupes.sensitivity = e.target.value; };
    bindFields(el);
  }

  // --- step 2: storage ------------------------------------------------------------
  // the previews' size and quality; they apply to previews made from then on
  const SIZES = ['media.thumb_size', 'media.preview_size', 'media.jpeg_quality', 'media.video_preview_size'];
  const sizesChanged = () => SIZES.some((k) => getKey(k) !== getKey(k, S.config));
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
        <h3>${t('Obergrenze und Auslagerung')}</h3>
        <p class="muted">${t('Wird der Cache-Ordner größer als die Obergrenze, wandern große Vorschauen, Video-Vorschauen und umgewandelte Videos in den Auslagerungsordner (die ältesten zuerst, z.B. auf das NAS) und werden von dort gelesen. Vorschaubilder, Gesichter und Index bleiben hier. Der Auslagerungsordner muss schon existieren und auf einer anderen Platte liegen; fehlt er (Netzlaufwerk nicht eingehängt), wird nichts ausgelagert. Obergrenze leer: keine.')}</p>
        <div class="row2">
          <label class="field"><span>${t('Obergrenze')}</span><input id="cmax" placeholder="4GB" value="${esc(C.cache.max_size || '')}" spellcheck="false" ${dis('cache.max_size')}>${lockNote('cache.max_size')}</label>
          <label class="field"><span>${t('Auslagerungsordner')}</span>
            <div class="addrow"><input id="cover" placeholder="/mnt/nas/schuhkarton-cache" value="${esc(C.cache.overflow_dir || '')}" spellcheck="false" ${dis('cache.overflow_dir')}>
            <button class="btn" id="cobrowse" ${dis('cache.overflow_dir')}>${icon('folder')}</button></div>${lockNote('cache.overflow_dir')}</label>
        </div>
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
        <label class="check"><input type="checkbox" id="cprev" ${C.cache.previews ? 'checked' : ''} ${dis('cache.previews')}>
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
        ${S.slowHEIF ? `<label class="field"><span>${t('HEIC/AVIF gleichzeitig auf dem langsamen Weg')}</span><input type="number" id="slowdec" min="1" max="8" value="${C.media.slow_decodes || 1}" ${dis('media.slow_decodes')}>${lockNote('media.slow_decodes')}
          <span class="muted small">${t('Auf diesem Prozessor braucht der eingebaute WebAssembly-Decoder für HEIC/AVIF Minuten und einige hundert MB pro Foto. Er springt nur ein, wo weder der Go-Decoder noch heif-dec ein Foto lesen können, bei AVIF ohne heif-dec also immer. Weitere Fotos warten, statt den Speicher zu füllen.')}</span></label>` : ''}
        <details class="more" ${sizesChanged() ? 'open' : ''}><summary>${t('Größe und Qualität der Vorschauen')}</summary>
          <div class="row2">
            ${numField('media.thumb_size', t('Vorschaubilder im Raster (kurze Seite, Pixel)'), 64, 2000, 10, defaultNote('media.thumb_size'))}
            ${numField('media.preview_size', t('Große Vorschau (lange Seite, Pixel)'), 256, 8192, 16, defaultNote('media.preview_size'))}
            ${numField('media.jpeg_quality', t('JPEG-Qualität (1–100)'), 1, 100, 1, defaultNote('media.jpeg_quality'))}
            ${numField('media.video_preview_size', t('Video-Vorschau (kurze Seite, Pixel)'), 64, 1080, 10, defaultNote('media.video_preview_size'))}
          </div>
          <p class="note ${sizesChanged() ? 'warn' : ''}" id="sizewarn">${icon('info')}<span>${t('Gilt nur für Vorschauen, die ab jetzt entstehen. Vorhandene bleiben, wie sie sind; im Raster stehen dann alte und neue nebeneinander. Größere Werte brauchen mehr Platz im Cache und mehr Rechenzeit.')}</span></p>
        </details>
        <h3>${t('Hintergrundarbeit')}</h3>
        <p class="muted">${t('Vorschaubilder, Gesichter, Beschreibungen, Text- und Spracherkennung, Schreiben in Dateien, Duplikate, Import, regelmäßiges Einlesen und Sicherung laufen nur in diesen Zeitfenstern, z.B. Mo–Fr 23:00–08:00 und Sa–So 00:00–07:00. Ein Fenster über Mitternacht gehört zum Tag, an dem es beginnt. Keine Fenster: jederzeit. Was jemand anschaut oder von Hand startet, läuft sofort.')}</p>
        <p class="muted">${t('Pro Fenster lässt sich wählen, welche Arbeit darin laufen darf, z.B. tagsüber nur Duplikate oder nur die Sicherung. Arbeit, die kein Fenster erlaubt, wartet.')}</p>
        <div id="bgwin"></div>
        <div class="btns"><button class="btn" id="bgadd" ${dis('background.windows')}>${icon('plus')}<span>${t('Zeitfenster')}</span></button></div>
        <h3>${t('Einmalig')}</h3>
        <p class="muted">${t('Zusätzliche Zeiten, z.B. heute 09:00–13:00. Sie fallen weg, wenn sie vorbei sind.')}</p>
        <div id="bgonce"></div>
        <div class="btns"><button class="btn" id="bgoadd" ${dis('background.once')}>${icon('plus')}<span>${t('Einmalig')}</span></button></div>
        ${lockNote('background.windows')}${lockNote('background.once')}
        <h3>${t('Werkzeuge')}</h3><ul class="tools">${tools}</ul>
        <div class="row2">
          ${textField('media.exiftool', t('Pfad zu exiftool'), t('leer: aus dem Suchpfad'))}
          ${textField('media.ffmpeg', t('Pfad zu ffmpeg'), t('leer: aus dem Suchpfad'))}
        </div>
        <p class="muted small">${t('Fehlt etwas: {a} und {b} über die Paketverwaltung installieren (z.B. {c}), dann neu starten.', { a: '<code>exiftool</code>', b: '<code>ffmpeg</code>', c: '<code>apt install libimage-exiftool-perl ffmpeg</code>' })}${S.slowHEIF ? ' ' + t('{a} kommt mit {b}.', { a: '<code>heif-dec</code>', b: '<code>apt install libheif-examples</code>' }) : ''}</p>
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
    const cmax = $('#cmax', el), cover = $('#cover', el);
    cmax.oninput = () => { C.cache.max_size = cmax.value.trim(); validate(); };
    cover.oninput = () => { C.cache.overflow_dir = cover.value.trim(); validate(); };
    $('#cobrowse', el).onclick = () => pick(cover.value || '/', (p) => { cover.value = p; cover.oninput(); });
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
    $('#cprev', el).onchange = (e) => { C.cache.previews = e.target.checked; };
    $('#vprev', el).onchange = (e) => { C.media.video_previews = e.target.checked; };
    $('#trans', el).onchange = (e) => { C.media.transcode = e.target.checked; };
    $('#rescan', el).onchange = (e) => { C.library.rescan = e.target.value; };
    $('#workers', el).oninput = (e) => { C.media.workers = Math.max(1, +e.target.value || 1); };
    const slowdec = $('#slowdec', el);
    if (slowdec) slowdec.oninput = (e) => { C.media.slow_decodes = Math.max(1, Math.min(8, +e.target.value || 1)); };
    bindFields(el, (key) => { if (SIZES.includes(key)) $('#sizewarn', el).classList.toggle('warn', sizesChanged()); });
    // the older single window becomes a rule for every day
    const B = (C.background ??= { from: '', to: '' });
    B.windows ||= [];
    B.once ||= [];
    if (B.from && B.to && !locked('background.from') && !locked('background.to')) {
      B.windows.unshift({ days: '', from: B.from, to: B.to });
      B.from = B.to = '';
    }
    const order = [1, 2, 3, 4, 5, 6, 0];
    const short = ['So', 'Mo', 'Di', 'Mi', 'Do', 'Fr', 'Sa'];
    const wdis = dis('background.windows'), odis = dis('background.once');
    // which kinds of work a window allows: none chosen means all
    const jobChips = (o, d) => {
      const j = normJobs(o.jobs);
      return `<div class="chips bgjobs">${JOBS.map(([k, label]) =>
        `<button type="button" class="chip ${!j.length || j.includes(k) ? 'on' : ''}" data-j="${k}" ${d}>${esc(t(label))}</button>`).join('')}</div>`;
    };
    const bindJobs = (row, o, redraw) => row.querySelectorAll('[data-j]').forEach((b) => b.onclick = () => {
      const all = JOBS.map(([k]) => k);
      const j = normJobs(o.jobs);
      const cur = new Set(j.length ? j : all);
      if (cur.has(b.dataset.j)) cur.delete(b.dataset.j); else cur.add(b.dataset.j);
      if (!cur.size) return; // at least one; remove the window to allow nothing
      o.jobs = cur.size === all.length ? [] : all.filter((k) => cur.has(k));
      redraw(); validate();
    });
    const drawWin = () => {
      $('#bgwin', el).innerHTML = B.windows.map((w, i) => {
        const m = parseDays(w.days);
        return `<div class="bgrow" data-i="${i}"><div class="chips">${order.map((d) => `<button type="button" class="chip ${m & (1 << d) ? 'on' : ''}" data-d="${d}" ${wdis}>${t(short[d])}</button>`).join('')}</div>
          <input type="time" class="bgf" value="${esc(w.from || '')}" ${wdis}><span>–</span><input type="time" class="bgt" value="${esc(w.to || '')}" ${wdis}>
          <button class="btn" data-del="${i}" title="${t('Entfernen')}" ${wdis}>${icon('x')}</button>${jobChips(w, wdis)}</div>`;
      }).join('') || `<p class="muted small">${t('Keine Zeitfenster: Hintergrundarbeit läuft jederzeit.')}</p>`;
      $('#bgwin', el).querySelectorAll('.bgrow').forEach((row) => {
        const w = B.windows[+row.dataset.i];
        row.querySelectorAll('[data-d]').forEach((b) => b.onclick = () => {
          w.days = daysText(parseDays(w.days) ^ (1 << +b.dataset.d)); drawWin(); validate();
        });
        $('.bgf', row).oninput = (e) => { w.from = e.target.value; validate(); };
        $('.bgt', row).oninput = (e) => { w.to = e.target.value; validate(); };
        $('[data-del]', row).onclick = () => { B.windows.splice(+row.dataset.i, 1); drawWin(); validate(); };
        bindJobs(row, w, drawWin);
      });
    };
    const drawOnce = () => {
      $('#bgonce', el).innerHTML = B.once.map((o, i) => {
        const [date, from] = (o.from || '').replace('T', ' ').split(' ');
        return `<div class="bgrow" data-i="${i}"><input type="date" class="bgd" value="${esc(date || '')}" ${odis}>
          <input type="time" class="bgf" value="${esc((from || '').slice(0, 5))}" ${odis}><span>–</span><input type="time" class="bgt" value="${esc((o.to || '').slice(-5))}" ${odis}>
          <button class="btn" data-del="${i}" title="${t('Entfernen')}" ${odis}>${icon('x')}</button>${jobChips(o, odis)}</div>`;
      }).join('');
      $('#bgonce', el).querySelectorAll('.bgrow').forEach((row) => {
        const o = B.once[+row.dataset.i];
        // an end with its own date (several days) keeps it; only the time is edited here
        const endDay = /^\d{4}-\d{2}-\d{2}/.exec(o.to || '')?.[0];
        const set = () => {
          o.from = `${$('.bgd', row).value} ${$('.bgf', row).value}`;
          o.to = endDay ? `${endDay} ${$('.bgt', row).value}` : $('.bgt', row).value;
          validate();
        };
        $('.bgd', row).oninput = set;
        $('.bgf', row).oninput = set;
        $('.bgt', row).oninput = set;
        $('[data-del]', row).onclick = () => { B.once.splice(+row.dataset.i, 1); drawOnce(); validate(); };
        bindJobs(row, o, drawOnce);
      });
    };
    $('#bgadd', el).onclick = () => { B.windows.push({ days: 'mon-fri', from: '23:00', to: '06:00' }); drawWin(); validate(); };
    $('#bgoadd', el).onclick = () => {
      const now = new Date(), p = (n) => String(n).padStart(2, '0');
      const day = `${now.getFullYear()}-${p(now.getMonth() + 1)}-${p(now.getDate())}`;
      B.once.push({ from: `${day} ${p(now.getHours())}:00`, to: `${p((now.getHours() + 4) % 24)}:00` });
      drawOnce(); validate();
    };
    drawWin();
    drawOnce();
  }

  // --- step 3: faces --------------------------------------------------------------
  // the thresholds of finding and grouping faces: key, label, help
  const FACE_TUNING = [
    ['faces.min_score', 'Gesicht erkennen ab', 'Wie sicher die Erkennung sein muss. Höher: weniger Fehltreffer, kleine oder unscharfe Gesichter fehlen eher. Gilt für Fotos, die danach nach Gesichtern durchsucht werden.'],
    ['faces.cluster_threshold', 'Unbenannte Gruppe ab', 'Ähnlichkeit, ab der Gesichter eine Gruppe bilden. Höher: mehr, dafür reinere Gruppen (Geschwister sehen sich ähnlich).'],
    ['faces.named_threshold', 'Benannte Person ab', 'Ähnlichkeit, ab der ein Gesicht zu einer Person mit Namen kommt. Niedriger: mehr Treffer, aber auch mehr falsche.'],
    ['faces.same_photo_threshold', 'Zweimal im selben Bild ab', 'Eine Person ist in einem Bild einmal. Ein zweites Gesicht, das ihr ähnelt, kommt nur ab dieser Ähnlichkeit auch zu ihr (eine Collage), sonst zur nächstbesten Person.'],
  ];
  function renderFaces(el) {
    const m = C.faces.mode;
    const tok = C.faces.token && C.faces.token !== '********' ? C.faces.token : '<TOKEN>';
    const host = location.hostname;
    // the detection threshold is used here only with local Python (the container keeps its own)
    const tuning = FACE_TUNING.filter(([k]) => k !== 'faces.min_score' || m === 'local');
    const tuned = [...tuning.map(([k]) => k), 'faces.workers'].some((k) => getKey(k) !== getKey(k, S.defaults));
    const minScore = (help) => numField('faces.min_score', t('Gesicht erkennen ab'), 0.05, 1, 0.01, `${help} ${defaultNote('faces.min_score')}`);
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
      </section>` : ''}
      ${m === 'local' ? `<section class="card">
        <h2>${t('Python auf diesem Rechner')}</h2>
        <div class="row2">
          ${textField('faces.python', t('Python mit OpenCV'), 'python3', t('leer: python3 aus dem Suchpfad'))}
          ${textField('faces.models_dir', t('Ordner mit den Modellen'), '', t('YuNet und SFace als ONNX-Dateien'))}
        </div>
      </section>` : ''}
      ${m !== 'off' ? `<section class="card">
        <details class="more" ${tuned ? 'open' : ''}><summary>${t('Feinabstimmung')}</summary>
          <p class="muted small">${t('Werte von 0 bis 1. Beim Speichern startet die Galerie neu und ordnet die Gesichter neu zu; was von Hand benannt ist, bleibt.')}</p>
          <div class="row2">${tuning.map(([k, label, help]) => numField(k, t(label), 0.05, 1, 0.01, `${t(help)} ${defaultNote(k)}`)).join('')}</div>
          <button class="btn" id="ftunedef" ${tuning.some(([k]) => locked(k)) ? 'disabled' : ''}>${t('Standardwerte')}</button>
          ${numField('faces.workers', t('Fotos gleichzeitig untersuchen'), 1, 16, 1, `${t('Mehr ist schneller, braucht aber mehr Speicher, hier und im Container.')} ${defaultNote('faces.workers')}`)}
        </details>
      </section>` : ''}
      <section class="card">
        <details class="more"><summary>${t('Dieser Rechner als Gesichtserkennung für andere (schuhkarton worker)')}</summary>
          <p class="muted small">${t('Statt des Containers kann {c} die Gesichter für eine andere Galerie erkennen. Diese Werte gelten beim nächsten Start des Workers.', { c: '<code>schuhkarton worker</code>' })}</p>
          <div class="row2">
            ${textField('worker.listen', t('Adresse'), '0.0.0.0:8089')}
            ${textField('worker.token', 'Token', '', t('Die Galerie trägt denselben Token ein.'))}
            ${numField('worker.workers', t('Fotos gleichzeitig'), 1, 64, 1, defaultNote('worker.workers'))}
            ${m === 'local' ? '' : minScore(t('Wie sicher die Erkennung dieses Workers sein muss. Höher: weniger Fehltreffer, kleine oder unscharfe Gesichter fehlen eher.'))}
          </div>
        </details>
      </section>`;
    bindFields(el);
    const tuneDef = $('#ftunedef', el);
    if (tuneDef) tuneDef.onclick = () => { for (const [k] of tuning) setKey(k, getKey(k, S.defaults)); renderFaces(el); };
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
        ? `<span class="good">${icon('check')}${t('verbunden')} · ${f.info.recognition ? (f.info.aura ? t('Erkennung und Zuordnung mit zwei Modellen (SFace, AuraFace)') : t('Erkennung und Zuordnung')) : t('nur Erkennung (Modell SFace fehlt)')} · ${esc(f.info.version || '')}</span>`
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

  // --- step 3b: AI: descriptions and text recognition ---------------------------------
  const MITTWALD = 'https://llm.aihosting.mittwald.de/v1';
  const ocrOn = () => !!C.ai.ocr && C.ai.ocr !== 'off';
  const speechOn = () => C.ai.speech === 'on';
  const aiUsed = () => C.ai.mode === 'on' || ocrOn() || speechOn();
  const aiPreset = () => (!aiUsed() ? 'off' : (C.ai.url || MITTWALD).replace(/\/$/, '') === MITTWALD ? 'mittwald' : 'custom');
  let ocrTest = null; // last text recognition test
  let speechTest = null; // last speech recognition test
  let connTest = null; // last model list or connection error
  // how long a failed file waits before it is tried again ('' = the built-in 72h)
  const RETRY = [['', t('Standard: {v}', { v: t('{n} Tage', { n: 3 }) })], ['6h', t('{n} Stunden', { n: 6 })], ['12h', t('{n} Stunden', { n: 12 })],
    ['24h', t('{n} Tag', { n: 1 })], ['72h', t('{n} Tage', { n: 3 })], ['168h', t('{n} Tage', { n: 7 })]];
  function renderAI(el) {
    const preset = aiPreset();
    if (aiKeyField === null) aiKeyField = C.ai.api_key_env ? `$${C.ai.api_key_env}` : (C.ai.api_key || '');
    const langs = C.ai.languages || ['de', 'en'];
    const models = aiModels || [];
    const choice = (name, v, cur, title, text, off) => `<label class="choice ${cur === v ? 'on' : ''}"><input type="radio" name="${name}" value="${v}" ${cur === v ? 'checked' : ''} ${off}>
      <b>${title}</b><span class="muted">${text}</span></label>`;
    const modelField = (id, value, key) => (models.length
      ? `<select id="${id}" ${dis(key)}>${[...new Set([value, ...models])].filter(Boolean).map((m) => `<option ${m === value ? 'selected' : ''}>${esc(m)}</option>`).join('')}</select>`
      : `<input id="${id}" value="${esc(value || '')}" spellcheck="false" ${dis(key)}>`);
    const ocr = C.ai.ocr || 'off';
    // the service can be chosen unless both uses are fixed (e.g. descriptions locked off, text recognition free)
    const svcDis = locked('ai.mode') && locked('ai.ocr') && locked('ai.speech') ? 'disabled' : '';
    el.innerHTML = `
      <section class="card">
        <h2>${t('KI für Beschreibungen und Text?')}</h2>
        <p class="muted">${t('Ein KI-Modell kann zu jedem Foto eine kurze Beschreibung und Stichwörter schreiben (Deutsch und Englisch) und den Text in Dokumenten und Fotos lesen, auch Handschrift. Die Suche findet dann „Hund im Schnee“ oder Wörter auf einem Zettel. Dafür werden die Bilder verkleinert an den gewählten Dienst geschickt. Ohne KI funktioniert alles andere genauso.')}</p>
        <div class="choices">
          ${choice('aim', 'off', preset, t('Aus'), t('Keine Beschreibungen, keine Texterkennung. Später jederzeit einschaltbar.'), svcDis)}
          ${choice('aim', 'mittwald', preset, t('mittwald AI hosting'), t('Gehostete Modelle in Deutschland, braucht einen API-Schlüssel. Texterkennung mit GLM-OCR.'), svcDis)}
          ${choice('aim', 'custom', preset, t('Eigener Dienst'), t('OpenAI-kompatible API, z.B. vLLM oder Ollama im eigenen Netz.'), svcDis)}
        </div>${svcDis ? lockNote('ai.mode') : ''}
      </section>
      ${preset === 'off' ? '' : `<section class="card">
        <h2>${t('Verbindung')}</h2>
        ${preset === 'custom' ? `<label class="field"><span>${t('Adresse der API')}</span><input id="aurl" value="${esc(C.ai.url || '')}" placeholder="http://localhost:8000/v1" spellcheck="false" ${dis('ai.url')}>${lockNote('ai.url')}</label>` : ''}
        <label class="field"><span>${t('API-Schlüssel')}${preset === 'custom' ? ` <span class="muted">(${t('falls nötig')})</span>` : ''}</span>
          <div class="addrow"><input id="akey" type="password" autocomplete="off" spellcheck="false" value="${esc(aiKeyField)}" placeholder="${preset === 'mittwald' ? 'sk-…' : ''}" ${dis('ai.api_key')}>
          ${S.aiKey?.mittwaldEnv && aiKeyField !== '$MITTWALD_LLM_API_KEY' ? `<button class="btn" id="aenv" title="${t('Den Schlüssel aus der Umgebungsvariablen nehmen; er wird dann nicht gespeichert.')}">$MITTWALD_LLM_API_KEY</button>` : ''}</div>
          <span class="muted small">${t('Den Schlüssel selbst eintragen oder eine Umgebungsvariable wie {v}; dann bleibt er nur dort.', { v: '<code>$MITTWALD_LLM_API_KEY</code>' })}</span>
          ${lockNote('ai.api_key')}</label>
        <div class="addrow"><button class="btn" id="amodels">${t('Modelle laden')}</button></div>
        <div class="stat" id="cres"></div>
      </section>
      <section class="card">
        <h2>${t('Fotos beschreiben')}</h2>
        <label class="check"><input type="checkbox" id="adesc" ${C.ai.mode === 'on' ? 'checked' : ''} ${dis('ai.mode')}><span>${t('Jedes Foto und Video bekommt eine Beschreibung und Stichwörter')}</span></label>
        ${C.ai.mode !== 'on' ? '' : `<div class="row2">
          <label class="field"><span>${t('Modell')}</span>${modelField('amodel', C.ai.model, 'ai.model')}${lockNote('ai.model')}</label>
          <label class="field"><span>${t('Gleichzeitige Anfragen')}</span><input type="number" id="awork" min="1" max="16" value="${C.ai.workers || 2}" ${dis('ai.workers')}></label>
        </div>
        <div class="field"><span>${t('Sprachen')}</span>
          <div class="addrow">
            <label class="check"><input type="checkbox" data-lang="de" ${langs.includes('de') ? 'checked' : ''} ${dis('ai.languages')}><span>${t('Deutsch')}</span></label>
            <label class="check"><input type="checkbox" data-lang="en" ${langs.includes('en') ? 'checked' : ''} ${dis('ai.languages')}><span>${t('Englisch')}</span></label>
          </div></div>
        <div class="addrow"><button class="btn primary" id="atest">${t('Beschreibung testen')}</button></div>
        <div class="stat" id="ares"></div>
        <p class="muted small">${t('Der Test schickt ein kleines gezeichnetes Bild, kein Foto. Für Bilder braucht es ein Modell, das Bilder versteht (z.B. {m}).', { m: '<code>Qwen3.8-27B-NVFP4</code>' })}</p>`}
      </section>
      <section class="card">
        <h2>${t('Text erkennen (OCR)')}</h2>
        <p class="muted">${t('Liest den Text in Bildern: Briefe, Rezepte, Notizen, Schilder. Der Text wird durchsucht und in den Details gezeigt. GLM-OCR liest auch Handschrift.')}</p>
        <div class="choices">
          ${choice('aocr', 'off', ocr, t('Aus'), t('Kein Text wird gelesen.'), dis('ai.ocr'))}
          ${choice('aocr', 'documents', ocr, t('Nur Dokumente'), t('Bilder in Dokumente-Ordnern.'), dis('ai.ocr'))}
          ${choice('aocr', 'all', ocr, t('Alle Fotos'), t('Jedes Foto; dauert länger und kostet mehr Tokens.'), dis('ai.ocr'))}
        </div>${lockNote('ai.ocr')}
        ${ocr === 'off' ? '' : `<div class="row2">
          <label class="field"><span>${t('Modell')}</span>${modelField('omodel', C.ai.ocr_model, 'ai.ocr_model')}${lockNote('ai.ocr_model')}</label>
          <label class="field"><span>${t('Gleichzeitige Anfragen')}</span><input type="number" id="owork" min="1" max="16" value="${C.ai.ocr_workers || 2}" ${dis('ai.ocr_workers')}></label>
        </div>
        <div class="addrow"><button class="btn primary" id="otest">${t('Texterkennung testen')}</button></div>
        <div class="stat" id="ores"></div>
        <p class="muted small">${t('Der Test schickt ein kleines gezeichnetes Bild mit Text, kein Foto. Empfohlen: {m}.', { m: '<code>GLM-OCR</code>' })}</p>
        <p class="muted small">${t('Sind Beschreibungen an, übersetzt deren Modell den erkannten Text in ihre Sprachen.')}</p>`}
      </section>
      <section class="card">
        <h2>${t('Sprache erkennen')}</h2>
        <label class="check"><input type="checkbox" id="aspeech" ${speechOn() ? 'checked' : ''} ${dis('ai.speech')}><span>${t('Was in Videos und Live Photos gesagt wird, aufschreiben')}</span></label>${lockNote('ai.speech')}
        <p class="muted">${t('Der Text wird durchsucht und in den Details gezeigt. Stille und Geräusche werden übersprungen.')}</p>
        ${!speechOn() ? '' : `<div class="row2">
          <label class="field"><span>${t('Modell')}</span>${modelField('smodel', C.ai.speech_model, 'ai.speech_model')}${lockNote('ai.speech_model')}</label>
          <label class="field"><span>${t('Gleichzeitige Anfragen')}</span><input type="number" id="swork" min="1" max="16" value="${C.ai.speech_workers || 2}" ${dis('ai.speech_workers')}></label>
        </div>
        <label class="field"><span>${t('Zweiter Versuch, wenn das Modell in lautem Ton nichts hört')}</span>
          ${models.length
            ? `<select id="sfb" ${dis('ai.speech_fallback')}><option value="">${t('keiner')}</option>${[...new Set([C.ai.speech_fallback, ...models])].filter(Boolean).map((m) => `<option ${m === C.ai.speech_fallback ? 'selected' : ''}>${esc(m)}</option>`).join('')}</select>`
            : `<input id="sfb" value="${esc(C.ai.speech_fallback || '')}" placeholder="${t('leer: keiner')}" spellcheck="false" ${dis('ai.speech_fallback')}>`}
          ${lockNote('ai.speech_fallback')}</label>
        <div class="addrow"><button class="btn primary" id="stest">${t('Spracherkennung testen')}</button></div>
        <div class="stat" id="sres"></div>
        <p class="muted small">${t('Der Test schickt eine Sekunde Stille. Empfohlen: {m}.', { m: '<code>whisper-large-v3-turbo</code>' })}</p>`}
      </section>
      <section class="card">
        <details class="more"><summary>${t('Erweitert')}</summary>
          <div class="row2">
            ${numField('ai.image_size', t('Bildgröße für das Modell (lange Seite, Pixel)'), 256, 4096, 64, `${t('Größer: mehr Details, aber mehr Tokens und langsamer.')} ${defaultNote('ai.image_size')}`)}
            <label class="field"><span>${t('Fehlgeschlagenes erneut versuchen nach')}</span><select id="aretry" ${dis('ai.retry_failed')}>
              ${[...RETRY, ...(RETRY.some(([v]) => v === (C.ai.retry_failed || '')) ? [] : [[C.ai.retry_failed, C.ai.retry_failed]])].map(([v, l]) => `<option value="${esc(v)}" ${(C.ai.retry_failed || '') === v ? 'selected' : ''}>${esc(l)}</option>`).join('')}
            </select>${lockNote('ai.retry_failed')}${fieldHelp(t('Gilt für Beschreibungen, Text- und Spracherkennung.'))}</label>
          </div>
          <label class="check"><input type="checkbox" id="athink" ${C.ai.thinking ? 'checked' : ''} ${dis('ai.thinking')}>
            <span><b>${t('Modell erst nachdenken lassen')}</b><br><span class="muted">${t('Nur bei Modellen mit Denkmodus: langsamer und mehr Tokens, selten bessere Antworten.')}</span></span></label>${lockNote('ai.thinking')}
        </details>
      </section>
      ${S.mode === 'settings' ? `<section class="card" id="aprogcard" hidden><h2>${t('Fortschritt')}</h2><div id="aprog"></div></section>` : ''}`}`;
    for (const r of $$('input[name=aim]', el)) r.onchange = () => {
      if (r.value === 'off') {
        if (!locked('ai.mode')) C.ai.mode = 'off';
        if (!locked('ai.ocr')) C.ai.ocr = 'off';
        if (!locked('ai.speech')) C.ai.speech = 'off';
      } else {
        const wasOff = !aiUsed();
        if (r.value === 'mittwald') C.ai.url = MITTWALD;
        else if ((C.ai.url || MITTWALD) === MITTWALD) C.ai.url = 'http://localhost:8000/v1';
        if (wasOff) {
          if (!locked('ai.mode')) C.ai.mode = 'on';
          // text recognition with GLM-OCR at mittwald; elsewhere only when descriptions cannot be switched on
          if (!locked('ai.ocr') && (r.value === 'mittwald' || C.ai.mode !== 'on')) C.ai.ocr = 'documents';
        }
        C.ai.model ||= 'Qwen3.8-27B-NVFP4';
        C.ai.ocr_model ||= 'GLM-OCR';
        C.ai.speech_model ||= 'whisper-large-v3-turbo';
      }
      aiModels = null; aiTest = null; ocrTest = null; speechTest = null;
      renderAI(el);
      validate();
      if (aiUsed()) loadModels(el, true);
    };
    if (preset === 'off') return;
    drawAIProgress(el);
    bindFields(el);
    $('#aretry', el).onchange = (e) => { C.ai.retry_failed = e.target.value; };
    $('#athink', el).onchange = (e) => { C.ai.thinking = e.target.checked; };
    const keyHint = (r) => (r?.hasKey === false && preset === 'mittwald' ? `<div class="muted">${t('Kein API-Schlüssel angegeben.')}</div>` : '');
    // connection: models found, or why not
    const showConn = () => {
      const r = connTest, box = $('#cres', el);
      if (!r || !box) return;
      const res = r.result || {};
      box.innerHTML = res.error ? `<span class="bad">${icon('warn')}${esc(t(res.error))}</span>${keyHint(r)}`
        : res.models ? `<span class="good">${icon('check')}${t('{n} Modelle gefunden', { n: res.models.length })}</span>` : '';
    };
    // description test: the answer for the drawn picture
    const showRes = () => {
      const r = aiTest, box = $('#ares', el);
      if (!r || !box) return;
      if (r.busy) { box.textContent = r.busy; return; }
      const res = r.result || {};
      if (res.answer) {
        const a = res.answer, txt = LANG === 'en' ? (a.en || a.de) : (a.de || a.en);
        const tags = (LANG === 'en' ? a.tagsEn : a.tagsDe) || a.tagsDe || a.tagsEn || [];
        box.innerHTML = `<span class="good">${icon('check')}${t('Antwort in {s} s', { s: (res.ms / 1000).toFixed(1) })}${a.tokens ? ` · ${t('{n} Tokens', { n: num(a.tokens) })}` : ''}</span>
          <div class="muted">„${esc(txt)}“</div><div class="chips">${tags.slice(0, 10).map((x) => `<span class="chip">${esc(x)}</span>`).join('')}</div>`;
      } else if (res.error) {
        box.innerHTML = `<span class="bad">${icon('warn')}${esc(t(res.error))}</span>${keyHint(r)}`;
      }
    };
    // text recognition test: what the model read
    const showOCR = () => {
      const r = ocrTest, box = $('#ores', el);
      if (!r || !box) return;
      if (r.busy) { box.textContent = r.busy; return; }
      const o = r.ocr || {};
      box.innerHTML = o.error ? `<span class="bad">${icon('warn')}${esc(t(o.error))}</span>${keyHint(r)}`
        : `<span class="good">${icon('check')}${t('Antwort in {s} s', { s: ((o.ms || 0) / 1000).toFixed(1) })}</span><div class="muted ocr-test">„${esc(o.text || '')}“</div>`;
    };
    // speech recognition test: the model answered
    const showSpeech = () => {
      const r = speechTest, box = $('#sres', el);
      if (!r || !box) return;
      if (r.busy) { box.textContent = r.busy; return; }
      const o = r.speech || {};
      box.innerHTML = o.error ? `<span class="bad">${icon('warn')}${esc(t(o.error))}</span>${keyHint(r)}`
        : `<span class="good">${icon('check')}${t('Antwort in {s} s', { s: ((o.ms || 0) / 1000).toFixed(1) })}</span>`;
    };
    showConn(); showRes(); showOCR(); showSpeech();
    const url = $('#aurl', el), key = $('#akey', el);
    if (url) url.oninput = () => { C.ai.url = url.value.trim(); aiModels = null; validate(); };
    key.oninput = () => { aiKeyField = key.value.trim(); C.ai.api_key = aiKeyField; };
    const envBtn = $('#aenv', el);
    if (envBtn) envBtn.onclick = () => { aiKeyField = '$MITTWALD_LLM_API_KEY'; C.ai.api_key = aiKeyField; aiModels = null; renderAI(el); loadModels(el, true); };
    $('#amodels', el).onclick = () => loadModels(el, false);
    $('#adesc', el).onchange = (e) => {
      C.ai.mode = e.target.checked ? 'on' : 'off';
      C.ai.model ||= 'Qwen3.8-27B-NVFP4';
      aiTest = null;
      renderAI(el);
      validate();
    };
    const model = $('#amodel', el);
    if (model) {
      model.oninput = model.onchange = () => { C.ai.model = model.value.trim(); validate(); };
      $('#awork', el).oninput = (e) => { C.ai.workers = Math.max(1, Math.min(16, +e.target.value || 1)); };
      for (const c of $$('[data-lang]', el)) c.onchange = () => {
        const l = $$('[data-lang]', el).filter((x) => x.checked).map((x) => x.dataset.lang);
        if (!l.length) { c.checked = true; return; }
        C.ai.languages = l;
      };
      $('#atest', el).onclick = async () => {
        aiTest = { busy: t('Das Modell beschreibt ein Testbild …') };
        showRes();
        aiTest = await api('/api/setup/ai', { url: C.ai.url, key: aiKeyField, model: C.ai.model, describe: true }).catch((e) => ({ result: { error: e.message } }));
        if (aiTest.result?.models?.length) aiModels = aiTest.result.models;
        const keep = aiTest;
        renderAI(el);
        aiTest = keep;
        showRes();
      };
    }
    for (const r of $$('input[name=aocr]', el)) r.onchange = () => {
      C.ai.ocr = r.value;
      C.ai.ocr_model ||= 'GLM-OCR';
      ocrTest = null;
      renderAI(el);
      validate();
    };
    const omodel = $('#omodel', el);
    if (omodel) {
      omodel.oninput = omodel.onchange = () => { C.ai.ocr_model = omodel.value.trim(); validate(); };
      $('#owork', el).oninput = (e) => { C.ai.ocr_workers = Math.max(1, Math.min(16, +e.target.value || 1)); };
      $('#otest', el).onclick = async () => {
        ocrTest = { busy: t('Das Modell liest ein Testbild …') };
        showOCR();
        ocrTest = await api('/api/setup/ai', { url: C.ai.url, key: aiKeyField, model: C.ai.ocr_model, ocr: true }).catch((e) => ({ ocr: { error: e.message } }));
        showOCR();
      };
    }
    $('#aspeech', el).onchange = (e) => {
      C.ai.speech = e.target.checked ? 'on' : 'off';
      C.ai.speech_model ||= 'whisper-large-v3-turbo';
      speechTest = null;
      renderAI(el);
      validate();
    };
    const smodel = $('#smodel', el);
    if (smodel) {
      smodel.oninput = smodel.onchange = () => { C.ai.speech_model = smodel.value.trim(); validate(); };
      $('#swork', el).oninput = (e) => { C.ai.speech_workers = Math.max(1, Math.min(16, +e.target.value || 1)); };
      const sfb = $('#sfb', el);
      sfb.oninput = sfb.onchange = () => { C.ai.speech_fallback = sfb.value.trim(); };
      $('#stest', el).onclick = async () => {
        speechTest = { busy: t('Das Modell hört eine Sekunde Stille …') };
        showSpeech();
        speechTest = await api('/api/setup/ai', { url: C.ai.url, key: aiKeyField, model: C.ai.speech_model, speech: true }).catch((e) => ({ speech: { error: e.message } }));
        showSpeech();
      };
    }
  }
  // progress of the running jobs (settings only), every 5 s while the card
  // is on the page; one timer, so rerenders do not add polls
  let aiProgT = 0;
  async function drawAIProgress(el) {
    clearTimeout(aiProgT);
    const card = $('#aprogcard', el), box = $('#aprog', el);
    if (!card || !document.contains(card)) return;
    const st = (await api('/api/status').catch(() => null))?.status;
    if (!document.contains(card)) return; // replaced by a newer render, which polls itself
    clearTimeout(aiProgT);
    aiProgT = setTimeout(() => drawAIProgress(el), 5000);
    const pl = (n, one, many) => t(n === 1 ? one : many, { n: num(n) });
    const roughly = (h) => {
      const minutes = Math.max(1, Math.round(h * 60));
      if (h < 1) return pl(minutes, '{n} Minute', '{n} Minuten');
      return h < 48 ? pl(Math.round(h), '{n} Stunde', '{n} Stunden') : pl(Math.round(h / 24), '{n} Tag', '{n} Tage');
    };
    // one block per job: ai (descriptions), ocr (text recognition)
    const block = (p, done, stopped) => {
      const lines = [], todo = st[`${p}Todo`], n = st[`${p}Done`], rate = st[`${p}Rate`];
      if (st[`${p}PausedUntil`]) {
        const until = new Date(st[`${p}PausedUntil`] * 1000).toLocaleTimeString(LANG === 'en' ? 'en-GB' : 'de-DE', { hour: '2-digit', minute: '2-digit' });
        lines.push(`<span title="${esc(t(st[`${p}Error`] || ''))}">${t('Dienst ausgelastet, weiter um {t}', { t: until })}</span>`);
      } else if (st[`${p}Error`]) {
        lines.push(`<span class="bad">${icon('warn')}${stopped}: ${esc(t(st[`${p}Error`]))}</span>`);
      } else if (rate > 0 && n < todo) {
        lines.push(`${rate >= 60 ? t('{n} pro Minute', { n: num(Math.round(rate / 60)) }) : t('{n} pro Stunde', { n: num(Math.round(rate)) })} · ${t('noch etwa {d}', { d: roughly((todo - n) / rate) })}`);
      }
      if (st[`${p}Fail`]) lines.push(pl(st[`${p}Fail`], '{n} fehlgeschlagen', '{n} fehlgeschlagen'));
      if (st[`${p}Tokens`]) lines.push(t('{n} Tokens bisher', { n: num(st[`${p}Tokens`]) }));
      return `<p>${done}</p><div class="bar"><i style="width:${Math.min(100, (n / todo) * 100)}%"></i></div>
        ${lines.length ? `<p class="muted small">${lines.join(' · ')}</p>` : ''}`;
    };
    const parts = [];
    if (st?.aiMode === 'on' && st.aiTodo) parts.push(block('ai', t('{a} von {b} Fotos und Videos beschrieben', { a: num(st.aiDone), b: num(st.aiTodo) }), t('Beschreibungen angehalten')));
    if (st?.ocrMode && st.ocrMode !== 'off' && st.ocrTodo) parts.push(block('ocr', t('Text in {a} von {b} Bildern gelesen', { a: num(st.ocrDone), b: num(st.ocrTodo) }), t('Texterkennung angehalten')));
    if (st?.speechOn && st.speechTodo) parts.push(block('speech', t('{a} von {b} Videos und Live Photos angehört', { a: num(st.speechDone), b: num(st.speechTodo) }), t('Spracherkennung angehalten')));
    if (st?.xmpOn && st.xmpTodo) parts.push(block('xmp', t('{a} von {b} Dateien mit allen Angaben beschrieben', { a: num(st.xmpDone), b: num(st.xmpTodo) }), t('Schreiben in Dateien angehalten')));
    card.hidden = !parts.length; // nothing started yet: look again
    box.innerHTML = parts.join('');
  }
  async function loadModels(el, quiet) {
    const r = await api('/api/setup/ai', { url: C.ai.url, key: aiKeyField ?? '', model: C.ai.model, describe: false }).catch((e) => ({ result: { error: e.message } }));
    if (r.result?.models?.length) {
      aiModels = r.result.models;
      if (!aiModels.includes(C.ai.model) && aiModels.includes('Qwen3.8-27B-NVFP4')) C.ai.model = 'Qwen3.8-27B-NVFP4';
      if (!aiModels.includes(C.ai.ocr_model) && aiModels.includes('GLM-OCR')) C.ai.ocr_model = 'GLM-OCR';
      if (!aiModels.includes(C.ai.speech_model) && aiModels.includes('whisper-large-v3-turbo')) C.ai.speech_model = 'whisper-large-v3-turbo';
      if (C.ai.speech_fallback && !aiModels.includes(C.ai.speech_fallback)) C.ai.speech_fallback = ''; // the service does not have it
    }
    if (!quiet || !r.result?.models) connTest = r;
    if (STEPS[step]?.id === 'ki') { renderAI(el); validate(); }
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
        <details class="more" ${C.server.tls_cert ? 'open' : ''}><summary>${t('HTTPS direkt, ohne Reverse Proxy')}</summary>
          <p class="muted small">${t('Zertifikat und Schlüssel als PEM-Dateien, z.B. von Let’s Encrypt. Hinter einem Reverse Proxy, der HTTPS übernimmt, leer lassen. Beim Speichern wird geprüft, ob beide zusammenpassen; danach beginnt die Adresse mit https://.')}</p>
          <div class="row2">
            ${textField('server.tls_cert', t('Zertifikat'), '/etc/letsencrypt/live/…/fullchain.pem')}
            ${textField('server.tls_key', t('Schlüssel'), '/etc/letsencrypt/live/…/privkey.pem')}
          </div>
        </details>
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
        <fieldset class="field logo-pick" ${dis('ui.logo')}><legend>${t('Logo und Symbol im Browser-Tab')}</legend>
          <p class="muted">${t('Mehrere auswählen: Sie wechseln sich ab, in zufälliger Reihenfolge und nie zweimal hintereinander.')}
            <button type="button" class="link" id="logoAll">${t('Alle')}</button></p>
          <div class="logo-grid">${(S.logos || ['aqua']).map((n) => `<label title="${esc(t(LOGOS[n] || n))}"><input type="checkbox" name="logo" value="${esc(n)}" ${logos().includes(n) ? 'checked' : ''}><img src="/assets/logo/${encodeURIComponent(n)}.svg" alt=""><span>${esc(t(LOGOS[n] || n))}</span></label>`).join('')}</div>
          <label class="field" id="logoChangeF"><span>${t('Wechseln')}</span><select id="logoChange" ${dis('ui.logo_change')}>
            ${[['visit', t('bei jedem Aufruf')], ['hourly', t('stündlich')], ['daily', t('täglich')], ['weekly', t('wöchentlich')]].map(([v, l]) => `<option value="${v}" ${(C.ui.logo_change || 'daily') === v ? 'selected' : ''}>${l}</option>`).join('')}</select></label>
        </fieldset>
        <details class="more" ${getKey('ui.map_tiles') !== getKey('ui.map_tiles', S.defaults) ? 'open' : ''}><summary>${t('Karte')}</summary>
          ${textField('ui.map_tiles', t('Kartenkacheln (Adresse mit {z}, {x} und {y})'), getKey('ui.map_tiles', S.defaults), t('Leer: OpenStreetMap. Andere Anbieter brauchen oft einen Schlüssel in der Adresse.'))}
          ${textField('ui.map_attribution', t('Quellenangabe unter der Karte'), getKey('ui.map_attribution', S.defaults))}
        </details>
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
    // the colours of the logo: at least one stays chosen; with several, how often they change
    const logoBoxes = [...el.querySelectorAll('input[name=logo]')];
    const logoSync = () => {
      const on = logoBoxes.filter((b) => b.checked);
      C.ui.logo = on.map((b) => b.value);
      $('#logoChangeF', el).hidden = on.length < 2;
      $('#logoAll', el).textContent = on.length === logoBoxes.length ? t('Nur das erste') : t('Alle');
      for (const b of on) b.disabled = on.length === 1 && !locked('ui.logo'); // the last one cannot be unticked
      for (const b of logoBoxes) if (!b.checked && !locked('ui.logo')) b.disabled = false;
    };
    for (const b of logoBoxes) b.onchange = logoSync;
    $('#logoAll', el).onclick = () => {
      const all = logoBoxes.every((b) => b.checked);
      logoBoxes.forEach((b, i) => { b.checked = all ? i === 0 : true; });
      logoSync();
    };
    $('#logoChange', el).onchange = (e) => { C.ui.logo_change = e.target.value; };
    if (locked('ui.logo')) $('#logoAll', el).hidden = true;
    logoSync();
    const ac = $('#acopy', el);
    if (ac) ac.onclick = () => navigator.clipboard?.writeText($('#alink', el).value).then(() => toast(t('Link kopiert')));
    bindFields(el, () => validate());
  }

  // --- step 6a: first admin --------------------------------------------------------------
  function renderAdmin(el) {
    el.innerHTML = `
      <section class="card">
        <h2>${t('Admin-Konto anlegen')}</h2>
        <p class="muted">${t('Der Admin verwaltet Einstellungen, Konten und Fotos (bearbeiten, drehen, verschieben, löschen). Weitere Konten können später angelegt oder freigegeben werden.')}</p>
        <form id="aform" autocomplete="on" data-nosubmit>
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
        <form id="nform" autocomplete="off" data-nosubmit>
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
        <form id="mform" data-nosubmit>
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
    const goto = { [t('Fotos')]: 'fotos', [t('Cache')]: 'speicher', [t('Vorschauen')]: 'speicher', [t('Hintergrundarbeit')]: 'speicher', [t('Obergrenze')]: 'speicher',
      [t('Gesichter')]: 'gesichter', [t('KI')]: 'ki', [t('Sicherung')]: 'sicherung', [t('Adresse')]: 'zugriff', [t('Konten')]: S.needAdmin ? 'konto' : 'benutzer' };
    const row = (k, v) => tabs() && goto[k]
      ? `<a class="sumrow link" href="#${goto[k]}"><span>${k}</span><b>${v}</b></a>`
      : `<div class="sumrow"><span>${k}</span><b>${v}</b></div>`;
    el.innerHTML = `
      <section class="card">
        <h2>${S.mode === 'install' ? t('Alles bereit') : tabs() ? t('Übersicht') : t('Einstellungen speichern')}</h2>
        <div class="summary">
          ${row(t('Fotos'), f.map((x) => `${esc(x.name)} <span class="muted">${esc(x.path)}</span>`).join('<br>'))}
          ${row(t('Cache'), esc(C.cache.dir) + (importPath ? `<br><span class="muted">${t('übernimmt {x}', { x: esc(importPath) })}</span>` : ''))}
          ${row(t('Vorschauen'), [C.cache.previews ? t('große vorab') : t('große bei Bedarf'), C.media.video_previews ? t('animierte Videos') : t('Videos ohne Animation'), C.media.transcode ? t('HEVC umwandeln') : ''].filter(Boolean).join(' · '))}
          ${row(t('Hintergrundarbeit'), bgSummary())}
          ${C.cache.max_size ? row(t('Obergrenze'), `${esc(C.cache.max_size)} → ${esc(C.cache.overflow_dir || '')}`) : ''}
          ${row(t('Gesichter'), C.faces.mode === 'off' ? t('aus') : C.faces.mode === 'remote' ? `${t('Container')} ${esc(C.faces.url)}` : t('Python auf diesem Rechner'))}
          ${row(t('KI'), !aiUsed() ? t('aus') : [esc(aiPreset() === 'mittwald' ? 'mittwald AI hosting' : C.ai.url),
            C.ai.mode === 'on' ? `${t('Beschreibungen')}: ${esc(C.ai.model)} (${(C.ai.languages || []).join(', ')})` : '',
            ocrOn() ? `${t('Texterkennung')}: ${esc(C.ai.ocr_model)} (${C.ai.ocr === 'all' ? t('alle Fotos') : t('Dokumente')})` : '',
            speechOn() ? `${t('Spracherkennung')}: ${esc(C.ai.speech_model)}${C.ai.speech_fallback ? ` + ${esc(C.ai.speech_fallback)}` : ''}` : ''].filter(Boolean).join(' · '))}
          ${row(t('Sicherung'), C.backup.dir ? `${esc(C.backup.dir)} <span class="muted">${{ '1h': t('stündlich'), '24h': t('täglich'), '168h': t('wöchentlich'), '0': t('auf Knopfdruck') }[C.backup.interval] || esc(C.backup.interval)}</span>` : t('aus'))}
          ${row(t('Adresse'), esc(C.server.listen) + (tokenMode === 'off' ? ` · ${t('ohne Schlüssel')}` : tokenMode === 'new' ? ` · ${t('neuer Schlüssel')}` : ''))}
          ${row(t('Konten'), [S.needAdmin ? `${t('Admin')}: ${esc(admin.name)}` : '', C.access.anonymous ? t('ansehen ohne Konto') : t('nur mit Konto'), C.access.register ? t('Registrieren mit Freigabe') : t('Konten nur durch Admins')].filter(Boolean).join(' · '))}
          ${row(t('Datei'), `<code>${esc(S.configPath)}</code>`)}
        </div>
        <div id="prog" hidden><div class="bar"><i></i></div><p class="muted" id="progText"></p></div>
      </section>`;
  }

  async function save() {
    if (tabs()) return saveTabs();
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
      // HTTPS on or off: the old address stops answering once the switch is done
      // (503: still restarting, e.g. copying a cache; the switch comes after that)
      try { p = await api('/api/setup/progress'); } catch (e) { if (tlsFlip() && e.status !== 503) followTLS('/'); else setTimeout(poll, 1000); return; }
      $('#prog .bar i').style.width = p.total ? `${(p.done / p.total) * 100}%` : p.finished ? '100%' : '30%';
      $('#progText').textContent = `${t(p.phase || '')}${p.total ? ` ${num(p.done)} / ${num(p.total)}` : ''}`;
      if (p.finished) {
        if (p.error) { $('#progText').innerHTML = `<span class="bad">${esc(t(p.error))}</span>`; btn.disabled = false; return; }
        if (tlsFlip()) { followTLS('/'); return; }
        $('#progText').textContent = t('Fertig – die Galerie öffnet sich …');
        setTimeout(() => { location.href = homeLink(p.link); }, 900);
        return;
      }
      setTimeout(poll, 700);
    };
    poll();
  }

  // saveTabs saves the settings of a running instance and stays on the
  // page (the gallery restarts with them), unless the address or the key
  // changed: then it goes where the server says.
  async function saveTabs() {
    const btn = $('#next'), hint = $('#hint');
    // the address as configured (S.listen is the socket, e.g. [::]:8088)
    const stay = tokenMode === 'keep' && C.server.listen === S.config.server.listen &&
      (C.server.tls_cert || '') === (S.config.server.tls_cert || '') && (C.server.tls_key || '') === (S.config.server.tls_key || '');
    const lock = (on) => { saving = on; $('#step').inert = on; $('#steps').inert = on; };
    lock(true);
    btn.disabled = true;
    hint.textContent = t('Speichern …');
    const plan = { config: C, tokenMode, import: importPath };
    if (S.needAdmin) plan.admin = { name: admin.name.trim(), password: admin.pw };
    saveErr = '';
    const fail = (msg) => { lock(false); saveErr = msg || ''; hint.innerHTML = `<span class="bad">${esc(t(msg || ''))}</span>`; btn.disabled = false; };
    const res = await api('/api/setup/save', plan).catch((e) => ({ ok: false, error: e.message }));
    if (!res.ok) return fail(res.error);
    const poll = async () => {
      let p;
      // HTTPS on or off: the old address stops answering once the switch is done
      // (503: still restarting, e.g. copying a cache; the switch comes after that)
      try { p = await api('/api/setup/progress'); } catch (e) { if (tlsFlip() && e.status !== 503) followTLS(`/setup${location.hash}`); else setTimeout(poll, 1000); return; }
      hint.textContent = `${t(p.phase || '')}${p.total ? ` ${num(p.done)} / ${num(p.total)}` : ''}`;
      if (!p.finished) { setTimeout(poll, 700); return; }
      if (p.error) return fail(p.error);
      if (tlsFlip()) { followTLS(`/setup${location.hash}`); return; }
      if (!stay || S.needAdmin) { location.href = homeLink(p.link); return; }
      // the restarted gallery answers again: load what it uses now
      let fresh = null;
      for (let i = 0; i < 30 && !fresh; i++) {
        try { fresh = await api('/api/setup/state'); } catch { await new Promise((r) => setTimeout(r, 1000)); }
      }
      // saved, but what the gallery uses now is unknown: keep the edits
      if (!fresh) return fail('Gespeichert, aber die Galerie antwortet nicht. Bitte die Seite neu laden.');
      S = fresh;
      backupStatus = await api('/api/backup').catch(() => null);
      C = clone(S.config);
      fill();
      importPath = '';
      buildSteps();
      lock(false);
      STEPS[step].render($('#step'));
      saved = snapshot();
      drawSteps();
      validate();
      toast(t('Gespeichert'));
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
  function fill() {
    C.library.folders ||= [];
    C.library.exclude ||= [];
    C.access ||= { anonymous: true, register: true };
    C.ai ||= { mode: 'off', url: MITTWALD, model: 'Qwen3.8-27B-NVFP4', workers: 2, languages: ['de', 'en'] };
  }
  window.skTranslate?.();
  $('#prev').onclick = () => go(step - 1);
  $('#next').onclick = () => (tabs() || step === STEPS.length - 1 ? save() : go(step + 1));
  (async () => {
    try { S = await api('/api/setup/state'); } catch (e) {
      $('#step').innerHTML = `<section class="card"><h2>${t('Kein Zugriff')}</h2><p>${esc(t(e.message))}</p></section>`;
      $('.setup-foot').hidden = true;
      return;
    }
    C = clone(S.config);
    fill();
    buildSteps();
    if (S.mode === 'settings') {
      $('#title').textContent = t('Einstellungen');
      $('#subtitle').textContent = `Schuhkarton ${S.version} · ${S.os}`;
      $('#back').hidden = false;
      $('#back').href = homeLink('');
      document.title = `Schuhkarton – ${t('Einstellungen')}`;
      backupStatus = await api('/api/backup').catch(() => null);
      if (S.needAdmin) $('#subtitle').textContent = t('Noch kein Admin-Konto: bitte jetzt anlegen.');
    }
    let want = location.hash.slice(1) || (S.mode === 'settings' && S.needAdmin ? 'konto' : '');
    if (tabs() && want === 'fertig') want = 'uebersicht';
    const wi = STEPS.findIndex((x) => x.id === want);
    go(wi >= 0 ? wi : 0);
    if (tabs()) {
      saved = snapshot();
      validate();
      // any change on a page: the save button and the hint follow
      for (const ev of ['input', 'change']) $('#step').addEventListener(ev, () => { saveErr = ''; });
      for (const ev of ['input', 'change', 'click']) $('#step').addEventListener(ev, () => setTimeout(validate, 0));
      // changes from the folder picker or answers that arrive later
      setInterval(validate, 1000);
      addEventListener('beforeunload', (e) => { if (!saving && dirty()) e.preventDefault(); });
    }
    addEventListener('hashchange', () => { const i = STEPS.findIndex((x) => x.id === location.hash.slice(1)); if (i >= 0 && i !== step) go(i); });
  })();
})();
