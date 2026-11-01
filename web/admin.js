/* Schuhkarton — admin tools: change date, place, category, text and position,
   rotate, move into other folders, delete into the trash, restore.
   Loaded after app.js and uses its internals through window.SK. Every control
   carries data-need="admin"; the server checks the role again. */
'use strict';
(() => {
  const SK = window.SK;
  if (!SK) return;
  const { t, S, $, $$, esc, icon, api, post, toast, num } = SK;
  const admin = () => SK.Account.can('admin');
  const phone = () => matchMedia('(max-width: 720px)').matches;

  // ---------------------------------------------------------------------------
  // icons
  // ---------------------------------------------------------------------------
  document.querySelector('body > svg')?.insertAdjacentHTML('beforeend', `
    <symbol id="i-edit" viewBox="0 0 24 24"><path d="M4 20h4L19 9l-4-4L4 16z"/><path d="m13.5 6.5 4 4"/></symbol>
    <symbol id="i-rotate" viewBox="0 0 24 24"><path d="M20 12a8 8 0 1 1-2.4-5.7"/><path d="M20 4v5h-5"/></symbol>
    <symbol id="i-rotl" viewBox="0 0 24 24"><path d="M4 12a8 8 0 1 0 2.4-5.7"/><path d="M4 4v5h5"/></symbol>
    <symbol id="i-trash" viewBox="0 0 24 24"><path d="M4 7h16M9.5 7V4.5h5V7M6.5 7l1 13h9l1-13"/><path d="M10 11v5.5M14 11v5.5"/></symbol>
    <symbol id="i-move" viewBox="0 0 24 24"><path d="M3 7.5A2.5 2.5 0 0 1 5.5 5H9l2 2.5h7.5A2.5 2.5 0 0 1 21 10v7.5a2.5 2.5 0 0 1-2.5 2.5h-13A2.5 2.5 0 0 1 3 17.5z"/><path d="M9.5 13.5h6M13 11l2.5 2.5L13 16"/></symbol>
    <symbol id="i-restore" viewBox="0 0 24 24"><path d="M4 12a8 8 0 1 0 2.4-5.7"/><path d="M4 4v5h5"/><path d="M12 8.5V12l2.5 2"/></symbol>
    <symbol id="i-more" viewBox="0 0 24 24"><circle cx="5.5" cy="12" r="1.3"/><circle cx="12" cy="12" r="1.3"/><circle cx="18.5" cy="12" r="1.3"/></symbol>`);

  // ---------------------------------------------------------------------------
  // dialogs, menus, snackbar
  // ---------------------------------------------------------------------------
  function dialog(title, html, { wide = false } = {}) {
    const el = document.createElement('div');
    el.className = 'adm-dlg';
    el.innerHTML = `<div class="adm-box${wide ? ' wide' : ''}" role="dialog" aria-modal="true" aria-label="${esc(title)}">
      <header><b>${esc(title)}</b><button type="button" class="btn ghost icon" data-x title="${esc(t('Schließen'))}">${icon('x')}</button></header>
      <div class="adm-body">${html}</div><footer></footer></div>`;
    document.body.appendChild(el);
    const prev = document.activeElement;
    const close = () => { el.remove(); prev?.focus?.(); };
    // keys stay in the dialog (the viewer listens on window)
    el.addEventListener('keydown', (e) => {
      e.stopPropagation();
      if (e.key === 'Escape') { e.preventDefault(); close(); }
    });
    el.addEventListener('click', (e) => { if (e.target === el || e.target.closest('[data-x]')) close(); });
    return { el, body: $('.adm-body', el), foot: $('footer', el), close };
  }

  function confirmDlg(title, text, okLabel, danger = false) {
    return new Promise((resolve) => {
      const d = dialog(title, `<p class="adm-text">${esc(text)}</p>`);
      d.foot.innerHTML = `<span class="grow"></span><button type="button" class="btn" data-no>${esc(t('Abbrechen'))}</button>
        <button type="button" class="btn ${danger ? 'danger' : 'primary'}" data-ok>${esc(okLabel)}</button>`;
      let done = false;
      const finish = (v) => { if (!done) { done = true; d.close(); resolve(v); } };
      $('[data-ok]', d.foot).onclick = () => finish(true);
      $('[data-no]', d.foot).onclick = () => finish(false);
      new MutationObserver((_, mo) => { if (!d.el.isConnected) { mo.disconnect(); finish(false); } }).observe(document.body, { childList: true });
      $('[data-ok]', d.foot).focus();
    });
  }

  // menu: a small list of actions next to a button
  function menu(anchor, entries) {
    $('.adm-menu')?.remove();
    const m = document.createElement('div');
    m.className = 'adm-menu';
    m.innerHTML = entries.map((e, i) => `<button type="button" data-i="${i}" class="${e.danger ? 'danger' : ''}">${icon(e.icon)}<span>${esc(e.label)}</span></button>`).join('');
    document.body.appendChild(m);
    const r = anchor.getBoundingClientRect();
    const w = m.offsetWidth, h = m.offsetHeight;
    m.style.left = `${SK.clamp(r.right - w, 8, innerWidth - w - 8)}px`;
    m.style.top = `${r.bottom + 6 + h > innerHeight - 8 ? Math.max(8, r.top - h - 6) : r.bottom + 6}px`;
    const off = (e) => { if (!m.contains(e.target)) { m.remove(); removeEventListener('pointerdown', off, true); } };
    setTimeout(() => addEventListener('pointerdown', off, true));
    m.onclick = (e) => {
      const b = e.target.closest('[data-i]');
      if (!b) return;
      m.remove();
      removeEventListener('pointerdown', off, true);
      entries[+b.dataset.i].run();
    };
  }

  let snackT = 0;
  function snack(msg, action, onAction, ms = 6000) {
    let el = $('#admSnack');
    if (!el) {
      el = document.createElement('div');
      el.id = 'admSnack';
      el.className = 'adm-snack';
      document.body.appendChild(el);
    }
    el.innerHTML = `<span>${esc(msg)}</span>${action ? `<button type="button" class="btn ghost">${esc(action)}</button>` : ''}`;
    el.classList.add('on');
    clearTimeout(snackT);
    snackT = setTimeout(() => el.classList.remove('on'), ms);
    const b = $('button', el);
    if (b) b.onclick = () => { el.classList.remove('on'); onAction(); };
  }

  async function call(path, body) {
    try {
      const r = await post(path, body);
      if (!r.ok) throw new Error(r.error || t('Fehler'));
      return r;
    } catch (e) {
      snack(t('Fehler: {e}', { e: t(String(e.message || e).replace(/^\d{3} /, '').trim()) }), '', null, 8000);
      return null;
    }
  }

  function report(r, msg) {
    const w = (r.warnings || []).map((x) => t(x));
    if (!w.length) { toast(msg); return; }
    snack(`${msg} · ${w.length === 1 ? w[0] : t('{n} Hinweise, z. B. {w}', { n: w.length, w: w[0] })}`, '', null, 9000);
  }

  // ---------------------------------------------------------------------------
  // after a change: reload the items, keep the viewer on the same photo
  // ---------------------------------------------------------------------------
  let dirty = false;
  const V = SK.Viewer;
  const close0 = V.close.bind(V);
  V.close = (...a) => {
    close0(...a);
    if (dirty) { dirty = false; SK.rerender(); }
  };
  const hashBase = () => (location.hash.slice(1) || '/fotos').split('|')[0];

  async function refreshAfter(ids = {}, removed = []) {
    await SK.loadItems();
    if (V.open_) {
      dirty = true;
      const gone = new Set(removed);
      const list = V.list.filter((o) => !gone.has(o.id)).map((o) => S.byId.get(ids[o.id] || o.id)).filter(Boolean);
      if (!list.length) { V.close(); return; }
      const cur = V.it && (ids[V.it.id] || V.it.id);
      let i = list.findIndex((o) => o.id === cur);
      if (i < 0) i = Math.min(V.i, list.length - 1);
      V.list = list;
      V.i = i;
      history.replaceState({ viewer: true }, '', `#${hashBase()}|${list[i].id}`);
      V.show();
    } else {
      SK.rerender();
    }
  }

  // the building blocks for social.js and dupes.js
  SK.ui = { dialog, confirmDlg, menu, snack, call, report, refreshAfter: (...a) => refreshAfter(...a), trashCount: () => trashCount() };

  // ---------------------------------------------------------------------------
  // edit: date, place, category, position, text
  // ---------------------------------------------------------------------------
  const wall = (ts) => (ts ? new Date(ts * 1000).toISOString().slice(0, 16) : ''); // stored wall clock is UTC

  async function editDialog(ids) {
    if (!admin() || !ids.length) return;
    const single = ids.length === 1;
    const [d, places, facets] = await Promise.all([
      single ? api(`/api/item/${ids[0]}`).catch(() => null) : null,
      api('/api/places').catch(() => []),
      api('/api/facets').catch(() => ({})),
    ]);
    if (single && !d) { toast(t('Element nicht gefunden')); return; }
    const ed = new Set(d?.edited || []);
    const badge = (f) => (ed.has(f) ? `<em class="adm-badge" title="${esc(t('Von Hand geändert'))}">${esc(t('geändert'))}</em>` : '');
    const placeList = (places || []).map((p) => `<option value="${esc(p.name)}">`).join('');
    const catList = (facets.categories || []).map((c) => `<option value="${esc(c.label)}">`).join('');
    const bulkHint = single ? '' : `placeholder="${esc(t('unverändert'))}"`;
    const clearBox = (name) => (single ? '' : `<label class="adm-clear"><input type="checkbox" name="${name}Clear"> ${esc(t('leeren'))}</label>`);
    const title = single ? t('Bearbeiten') : t('{n} Elemente bearbeiten', { n: num(ids.length) });
    const dlg = dialog(title, `<form class="adm-form" autocomplete="off">
      <div class="adm-f"><span class="lbl">${esc(t('Datum und Uhrzeit'))}${badge('taken')}</span>
        ${single ? `<input type="datetime-local" name="taken" value="${wall(d.taken)}" step="60">`
          : `<div class="seg adm-seg"><button type="button" data-dm="keep" class="on">${esc(t('Unverändert'))}</button><button type="button" data-dm="set">${esc(t('Festlegen'))}</button><button type="button" data-dm="shift">${esc(t('Verschieben'))}</button></div>
          <div class="adm-pane" data-pane="set" hidden><input type="datetime-local" name="taken" step="60"></div>
          <div class="adm-pane adm-shift" data-pane="shift" hidden>
            <select name="sign" aria-label="${esc(t('Richtung'))}"><option value="1">${esc(t('später'))}</option><option value="-1">${esc(t('früher'))}</option></select>
            <label><input type="number" name="sd" min="0" inputmode="numeric" placeholder="0"> ${esc(t('Tage'))}</label>
            <label><input type="number" name="sh" min="0" inputmode="numeric" placeholder="0"> ${esc(t('Std.'))}</label>
            <label><input type="number" name="sm" min="0" inputmode="numeric" placeholder="0"> ${esc(t('Min.'))}</label>
          </div>`}
      </div>
      <div class="adm-f"><span class="lbl">${esc(t('Ort'))}${badge('place')}</span>
        <div class="adm-row2"><input name="place" list="admPlaces" value="${esc(d?.place || '')}" ${bulkHint}>${clearBox('place')}</div>
        <button type="button" class="link adm-usepos" hidden></button>
        <datalist id="admPlaces">${placeList}</datalist></div>
      <div class="adm-f"><span class="lbl">${esc(t('Kategorie'))}${badge('category')}</span>
        <div class="adm-row2"><input name="category" list="admCats" value="${esc(d?.category || '')}" ${bulkHint}>${clearBox('category')}</div>
        <datalist id="admCats">${catList}</datalist></div>
      <div class="adm-f"><span class="lbl">${esc(t('Position'))}${badge('gps')}</span>
        <div class="adm-map" id="admMap"></div>
        <div class="adm-row2"><input name="coords" placeholder="${esc(single ? t('Breite, Länge – oder auf die Karte tippen') : t('unverändert – auf die Karte tippen'))}" inputmode="decimal">
          <button type="button" class="btn" data-gpsclear>${esc(t('Entfernen'))}</button></div></div>
      ${single ? `<div class="adm-f"><span class="lbl">${esc(t('Text im Bild'))}${badge('desc')}</span><textarea name="desc" rows="4">${esc(d.desc || '')}</textarea></div>` : ''}
      <p class="adm-note">${esc(S.config.writeFiles === false ? t('Änderungen bleiben in Schuhkarton; die Dateien bleiben unverändert.') : t('Änderungen werden auch in die Dateien geschrieben (falls möglich).'))}</p>
    </form>`, { wide: true });
    const f = $('form', dlg.body);
    dlg.foot.innerHTML = `<span class="grow"></span><button type="button" class="btn" data-x>${esc(t('Abbrechen'))}</button><button type="button" class="btn primary" data-save>${esc(t('Speichern'))}</button>`;

    // date mode (bulk)
    let dmode = 'keep';
    for (const b of $$('[data-dm]', f)) {
      b.onclick = () => {
        dmode = b.dataset.dm;
        for (const x of $$('[data-dm]', f)) x.classList.toggle('on', x === b);
        for (const p of $$('[data-pane]', f)) p.hidden = p.dataset.pane !== dmode;
      };
    }
    for (const n of ['place', 'category']) {
      const cb = f[`${n}Clear`];
      if (cb) cb.onchange = () => { f[n].disabled = cb.checked; if (cb.checked) f[n].value = ''; };
    }

    // position: map, coordinates, place suggestion
    let gps; // undefined: unchanged, null: remove, {lat, lon}
    const coords = f.coords;
    let map = null, mark = null;
    const fmt = (v) => (Math.round(v * 1e5) / 1e5).toString();
    const setPos = (lat, lon, pan = false) => {
      gps = { lat, lon };
      coords.value = `${fmt(lat)}, ${fmt(lon)}`;
      coords.classList.remove('bad');
      if (map) {
        if (!mark) mark = L.circleMarker([lat, lon], { radius: 8, color: '#fff', weight: 2, fillColor: '#f3b549', fillOpacity: 1 }).addTo(map);
        else mark.setLatLng([lat, lon]);
        if (pan) map.setView([lat, lon], Math.max(map.getZoom(), 12));
      }
    };
    $('[data-gpsclear]', f).onclick = () => {
      gps = null;
      coords.value = '';
      coords.placeholder = t('Position wird entfernt');
      if (mark) { mark.remove(); mark = null; }
    };
    coords.onchange = () => {
      const m = coords.value.trim().match(/^(-?\d+(?:[.,]\d+)?)\s*[,; ]\s*(-?\d+(?:[.,]\d+)?)$/);
      if (!coords.value.trim()) return;
      if (!m) { coords.classList.add('bad'); return; }
      const lat = parseFloat(m[1].replace(',', '.')), lon = parseFloat(m[2].replace(',', '.'));
      if (Math.abs(lat) > 90 || Math.abs(lon) > 180) { coords.classList.add('bad'); return; }
      setPos(lat, lon, true);
    };
    const placeAt = (name) => (places || []).find((p) => p.name.toLowerCase() === String(name).trim().toLowerCase() && (p.lat || p.lon));
    const usePos = $('.adm-usepos', f);
    const offerPos = () => {
      const p = placeAt(f.place.value);
      usePos.hidden = !p || gps !== undefined || (single && d.gps);
      if (p) {
        usePos.textContent = t('Position von {p} übernehmen', { p: p.name });
        usePos.onclick = () => { setPos(p.lat, p.lon, true); usePos.hidden = true; };
      }
    };
    f.place.addEventListener('input', offerPos);
    offerPos();
    if (window.L) {
      let start = [51.2, 10.4], zoom = 5;
      const p = placeAt(d?.place || '');
      if (single && d.gps) { start = [d.lat, d.lon]; zoom = 13; } else if (p) { start = [p.lat, p.lon]; zoom = 11; }
      map = L.map($('#admMap', f), { attributionControl: false }).setView(start, zoom);
      SK.tileLayer().addTo(map);
      map.on('click', (e) => setPos(e.latlng.lat, e.latlng.lng));
      if (single && d.gps) mark = L.circleMarker(start, { radius: 8, color: '#fff', weight: 2, fillColor: '#f3b549', fillOpacity: 1 }).addTo(map);
      setTimeout(() => map.invalidateSize(), 60);
    } else {
      $('#admMap', f).remove();
    }
    if (single && d.gps) coords.value = `${fmt(d.lat)}, ${fmt(d.lon)}`;

    const save = async () => {
      const body = { ids };
      if (single) {
        if (f.taken.value && f.taken.value !== wall(d.taken)) body.taken = f.taken.value;
        if (f.place.value.trim() !== (d.place || '')) body.place = f.place.value.trim();
        if (f.category.value.trim() !== (d.category || '')) body.category = f.category.value.trim();
        if (f.desc.value !== (d.desc || '')) body.desc = f.desc.value;
      } else {
        if (dmode === 'set') {
          if (!f.taken.value) { f.taken.focus(); return; }
          body.taken = f.taken.value;
        } else if (dmode === 'shift') {
          const n = (x) => Math.max(0, parseInt(x.value || '0', 10) || 0);
          const s = (+f.sign.value) * (n(f.sd) * 86400 + n(f.sh) * 3600 + n(f.sm) * 60);
          if (s) body.shift = s;
        }
        for (const k of ['place', 'category']) {
          if (f[`${k}Clear`].checked) body[k] = '';
          else if (f[k].value.trim()) body[k] = f[k].value.trim();
        }
      }
      if (gps === null) body.clearGps = true;
      else if (gps) { body.lat = gps.lat; body.lon = gps.lon; }
      if (Object.keys(body).length === 1) { toast(t('Nichts geändert')); return; }
      const btn = $('[data-save]', dlg.foot);
      btn.disabled = true;
      btn.textContent = t('Speichert …');
      const r = await call('/api/admin/edit', body);
      btn.disabled = false;
      btn.textContent = t('Speichern');
      if (!r) return;
      dlg.close();
      let msg = single ? t('Gespeichert') : t('{n} Elemente geändert', { n: num(r.changed) });
      if (r.moved) msg += ` · ${t('nach den Regeln einsortiert')}`;
      else if (r.queued) msg += ` · ${t('{n} werden im Hintergrund einsortiert', { n: num(r.queued) })}`;
      report(r, msg);
      if (!single) SK.endSelection();
      refreshAfter(r.ids || {});
    };
    $('[data-save]', dlg.foot).onclick = save;
    f.addEventListener('keydown', (e) => { if (e.key === 'Enter' && (e.ctrlKey || e.metaKey)) { e.preventDefault(); save(); } });
    f.onsubmit = (e) => { e.preventDefault(); save(); };
    (single ? f.taken : $('[data-dm="set"]', f))?.focus();
  }

  // ---------------------------------------------------------------------------
  // rotate, delete
  // ---------------------------------------------------------------------------
  let rotating = false;
  async function rotate(ids, deg) {
    if (!admin() || rotating) return;
    rotating = true;
    const r = await call('/api/admin/rotate', { ids, deg });
    rotating = false;
    if (!r) return;
    report(r, ids.length === 1 ? t('Gedreht') : t('{n} Fotos gedreht', { n: num(r.changed) }));
    if (ids.length > 1) SK.endSelection();
    refreshAfter();
  }

  // AI descriptions: forget them so the background job writes new ones
  const aiOn = () => S.config.ai === 'on';
  async function redescribe(ids) {
    if (!admin() || !ids.length) return;
    if (!aiOn()) { toast(t('Bildbeschreibungen sind ausgeschaltet')); return; }
    const r = await call('/api/admin/describe', { ids });
    if (!r) return;
    toast(r.changed === 1 ? t('Wird neu beschrieben') : t('{n} Elemente werden neu beschrieben', { n: num(r.changed) }));
    if (ids.length > 1) SK.endSelection();
  }
  SK.redescribe = redescribe;

  // text recognition: forget the texts so the background job reads them again
  const ocrOn = () => !!S.config.ocr && S.config.ocr !== 'off';
  async function rerecognize(ids) {
    if (!admin() || !ids.length) return;
    if (!ocrOn()) { toast(t('Texterkennung ist ausgeschaltet')); return; }
    const r = await call('/api/admin/ocr', { ids });
    if (!r) return;
    toast(!r.changed ? t('Liegt außerhalb der Texterkennung (nur Dokumente)') : r.changed === 1 ? t('Text wird neu erkannt') : t('Text von {n} Elementen wird neu erkannt', { n: num(r.changed) }));
    if (ids.length > 1) SK.endSelection();
  }
  SK.rerecognize = rerecognize;

  // speech recognition: forget what was heard so the background job listens again
  const speechOn = () => S.config.speech === 'on';
  async function retranscribe(ids) {
    if (!admin() || !ids.length) return;
    if (!speechOn()) { toast(t('Spracherkennung ist ausgeschaltet')); return; }
    const r = await call('/api/admin/speech', { ids });
    if (!r) return;
    toast(!r.changed ? t('Nur Videos und Live Photos haben Ton') : r.changed === 1 ? t('Sprache wird neu erkannt') : t('Sprache von {n} Elementen wird neu erkannt', { n: num(r.changed) }));
    if (ids.length > 1) SK.endSelection();
  }
  SK.retranscribe = retranscribe;

  function rotateMenu(anchor, ids) {
    menu(anchor, [
      { icon: 'rotl', label: t('90° nach links'), run: () => rotate(ids, 270) },
      { icon: 'rotate', label: t('90° nach rechts'), run: () => rotate(ids, 90) },
      { icon: 'refresh', label: t('180°'), run: () => rotate(ids, 180) },
    ]);
  }

  async function remove(ids) {
    if (!admin() || !ids.length) return;
    const days = S.config.trashDays;
    const ok = await confirmDlg(
      ids.length === 1 ? t('In den Papierkorb legen?') : t('{n} Elemente in den Papierkorb legen?', { n: num(ids.length) }),
      days > 0 ? t('Im Papierkorb lässt sich alles {d} Tage lang wiederherstellen; danach wird es endgültig gelöscht.', { d: days })
        : t('Im Papierkorb lässt sich alles wiederherstellen, bis er geleert wird.'),
      t('In den Papierkorb'), true);
    if (!ok) return;
    const r = await call('/api/admin/delete', { ids });
    if (!r) return;
    SK.endSelection();
    const entries = r.entries || [];
    snack(r.changed === 1 ? t('In den Papierkorb gelegt') : t('{n} Elemente in den Papierkorb gelegt', { n: num(r.changed) }), t('Rückgängig'), async () => {
      const u = await call('/api/admin/trash/restore', { ids: entries });
      if (u) { toast(t('Wiederhergestellt')); refreshAfter(); trashCount(); }
    });
    if (r.warnings?.length) report(r, t('Teilweise gelöscht'));
    refreshAfter({}, ids);
    trashCount();
  }

  // ---------------------------------------------------------------------------
  // move: pick a folder (or make a new one)
  // ---------------------------------------------------------------------------
  async function moveDialog(ids) {
    if (!admin() || !ids.length) return;
    const trees = await api('/api/admin/folders').catch((e) => { toast(t('Fehler: {e}', { e: t(String(e.message).replace(/^\d{3} /, '').trim()) })); return null; });
    if (!trees) return;
    const current = new Set(ids.map((id) => S.byId.get(id)?.al).filter(Boolean));
    const rows = [];
    for (const tr of trees) {
      rows.push({ root: tr.root, dir: '', depth: 0, name: tr.root });
      for (const dir of tr.dirs) {
        const parts = dir.split('/');
        rows.push({ root: tr.root, dir, depth: parts.length, name: parts[parts.length - 1] });
      }
    }
    const hasKids = new Set(rows.map((r) => `${r.root}/${r.dir.split('/').slice(0, -1).join('/')}`.replace(/\/$/, '')));
    const open = new Set(trees.length === 1 ? [trees[0].root] : []);
    let sel = null;
    const dlg = dialog(ids.length === 1 ? t('Verschieben') : t('{n} Elemente verschieben', { n: num(ids.length) }), `
      <input class="adm-filter" type="search" placeholder="${esc(t('Ordner suchen'))}" spellcheck="false">
      <div class="adm-tree" role="listbox"></div>
      <label class="adm-f adm-new"><span class="lbl">${esc(t('Neuer Unterordner (optional)'))}</span><input name="sub" placeholder="${esc(t('z. B. Urlaub Ostsee'))}" spellcheck="false"></label>
      <p class="adm-note adm-target"></p>`, { wide: true });
    const tree = $('.adm-tree', dlg.body), filt = $('.adm-filter', dlg.body), sub = $('[name=sub]', dlg.body), target = $('.adm-target', dlg.body);
    dlg.foot.innerHTML = `<span class="grow"></span><button type="button" class="btn" data-x>${esc(t('Abbrechen'))}</button><button type="button" class="btn primary" data-go disabled>${esc(t('Hierher verschieben'))}</button>`;
    const go = $('[data-go]', dlg.foot);
    const key = (r) => (r.dir ? `${r.root}/${r.dir}` : r.root);
    const fold = (s) => s.toLowerCase().normalize('NFD').replace(/[̀-ͯ]/g, '');
    const draw = () => {
      const q = fold(filt.value.trim());
      const vis = rows.filter((r) => {
        if (q) return fold(key(r)).includes(q);
        if (r.depth === 0) return true;
        // visible when every ancestor is open
        const parts = r.dir.split('/');
        for (let i = 0; i < parts.length; i++) {
          if (!open.has(i === 0 ? r.root : `${r.root}/${parts.slice(0, i).join('/')}`)) return false;
        }
        return true;
      }).slice(0, 600);
      tree.innerHTML = vis.map((r) => {
        const k = key(r), kids = hasKids.has(k);
        return `<button type="button" role="option" class="adm-node${sel && key(sel) === k ? ' on' : ''}" data-k="${esc(k)}" style="--d:${q ? 0 : r.depth}" title="${esc(k)}">
          <i class="tw">${kids && !q ? (open.has(k) ? '▾' : '▸') : ''}</i>${icon('folder')}<span>${esc(q ? k : r.name)}</span>${current.has(k) ? `<em>${esc(t('aktuell'))}</em>` : ''}</button>`;
      }).join('') || `<div class="adm-note">${esc(t('Kein Ordner gefunden'))}</div>`;
    };
    const update = () => {
      const s = sub.value.trim().replace(/^\/+|\/+$/g, '');
      if (!sel) { target.textContent = t('Ordner wählen'); go.disabled = true; return; }
      target.textContent = t('Ziel: {p}', { p: [key(sel), s].filter(Boolean).join(' / ') });
      go.disabled = !s && current.size === 1 && current.has(key(sel));
    };
    tree.onclick = (e) => {
      const b = e.target.closest('[data-k]');
      if (!b) return;
      const k = b.dataset.k;
      const wasSel = sel && key(sel) === k;
      sel = rows.find((r) => key(r) === k);
      if (hasKids.has(k) && !filt.value.trim()) {
        if (wasSel && open.has(k)) open.delete(k);
        else open.add(k);
      }
      draw();
      update();
    };
    tree.ondblclick = () => { if (!go.disabled) go.click(); };
    filt.oninput = draw;
    sub.oninput = update;
    go.onclick = async () => {
      const s = sub.value.trim().replace(/^\/+|\/+$/g, '');
      const dir = [sel.dir, s].filter(Boolean).join('/');
      go.disabled = true;
      const r = await call('/api/admin/move', { ids, root: sel.root, dir });
      if (!r) { go.disabled = false; return; }
      dlg.close();
      report(r, r.changed === 1 ? t('Verschoben nach {p}', { p: [sel.root, dir].filter(Boolean).join('/') }) : t('{n} Elemente verschoben', { n: num(r.changed) }));
      SK.endSelection();
      refreshAfter(r.ids || {});
    };
    draw();
    update();
    filt.focus();
  }

  // ---------------------------------------------------------------------------
  // viewer buttons
  // ---------------------------------------------------------------------------
  $('#vInfo')?.insertAdjacentHTML('beforebegin', `
    <button class="vbtn" id="vEdit" data-need="admin" title="${esc(t('Bearbeiten (E)'))}">${icon('edit')}</button>
    <button class="vbtn hide-m" id="vRot" data-need="admin" title="${esc(t('Drehen (R, Umschalt+R: links)'))}">${icon('rotate')}</button>
    <button class="vbtn hide-m" id="vDel" data-need="admin" title="${esc(t('In den Papierkorb (Entf)'))}">${icon('trash')}</button>`);
  const cur = () => (V.it ? [V.it.id] : []);
  const vMenu = (anchor) => menu(anchor, [
    { icon: 'edit', label: t('Datum, Ort, Text …'), run: () => editDialog(cur()) },
    ...(V.it?.k === 1 ? [
      { icon: 'rotl', label: t('90° nach links'), run: () => rotate(cur(), 270) },
      { icon: 'rotate', label: t('90° nach rechts'), run: () => rotate(cur(), 90) },
    ] : []),
    { icon: 'move', label: t('Verschieben'), run: () => moveDialog(cur()) },
    ...(aiOn() ? [{ icon: 'spark', label: t('Neu beschreiben (KI)'), run: () => redescribe(cur()) }] : []),
    ...(ocrOn() && V.it?.k === 1 ? [{ icon: 'file', label: t('Text neu erkennen'), run: () => rerecognize(cur()) }] : []),
    ...(speechOn() && (V.it?.k === 2 || V.it?.lv) ? [{ icon: 'spark', label: t('Sprache neu erkennen'), run: () => retranscribe(cur()) }] : []),
    { icon: 'trash', label: t('In den Papierkorb'), danger: true, run: () => remove(cur()) },
  ]);
  $('#vEdit').onclick = (e) => (phone() ? vMenu(e.currentTarget) : editDialog(cur()));
  $('#vRot').onclick = () => { if (V.it?.k === 1) rotate(cur(), 90); else toast(t('Videos lassen sich nicht drehen')); };
  $('#vRot').oncontextmenu = (e) => { e.preventDefault(); rotateMenu(e.currentTarget, cur()); };
  $('#vDel').onclick = () => remove(cur());

  // ---------------------------------------------------------------------------
  // selection bar
  // ---------------------------------------------------------------------------
  $('#selClose')?.insertAdjacentHTML('beforebegin', `
    <button class="btn" id="selEdit" data-need="admin" title="${esc(t('Datum, Ort, Kategorie, Position'))}">${icon('edit')}<span>${esc(t('Bearbeiten'))}</span></button>
    <button class="btn" id="selRot" data-need="admin" title="${esc(t('Drehen'))}">${icon('rotate')}<span>${esc(t('Drehen'))}</span></button>
    <button class="btn" id="selMove" data-need="admin" title="${esc(t('In einen anderen Ordner'))}">${icon('move')}<span>${esc(t('Verschieben'))}</span></button>
    <button class="btn" id="selAI" data-need="admin" hidden title="${esc(t('Beschreibungen neu schreiben lassen (KI)'))}">${icon('spark')}<span>${esc(t('Beschreiben'))}</span></button>
    <button class="btn adm-del" id="selDel" data-need="admin" title="${esc(t('In den Papierkorb'))}">${icon('trash')}<span>${esc(t('Löschen'))}</span></button>`);
  // short labels for the phone toolbar
  const SHORT = { selFav: t('Favorit'), selZip: t('Laden'), selEdit: t('Ändern'), selRot: t('Drehen'), selMove: t('Ablegen'), selAI: t('KI'), selDel: t('Löschen') };
  for (const [id, label] of Object.entries(SHORT)) $(`#${id}`)?.insertAdjacentHTML('beforeend', `<span class="s">${esc(label)}</span>`);
  const selected = () => [...S.sel];
  const needSel = (fn) => () => { const ids = selected(); if (!ids.length) { toast(t('Erst Elemente auswählen')); return; } fn(ids); };
  $('#selEdit').onclick = needSel(editDialog);
  $('#selRot').onclick = (e) => { const ids = selected(); if (ids.length) rotateMenu(e.currentTarget, ids); else toast(t('Erst Elemente auswählen')); };
  $('#selMove').onclick = needSel(moveDialog);
  $('#selDel').onclick = needSel(remove);
  $('#selAI').onclick = needSel(redescribe);
  // the AI button only when descriptions are on (the config arrives after start)
  const showAI = () => { if ($('#selAI')) $('#selAI').hidden = !aiOn(); };
  showAI();
  if ($('#selbar')) new MutationObserver(showAI).observe($('#selbar'), { attributes: true, attributeFilter: ['hidden'] });

  // ---------------------------------------------------------------------------
  // keys: E edit, R rotate (Shift+R left), Del delete — in the viewer and for a selection
  // ---------------------------------------------------------------------------
  addEventListener('keydown', (e) => {
    if (!admin() || e.ctrlKey || e.metaKey || e.altKey || e.target.matches('input, textarea, select') || $('.adm-dlg')) return;
    const ids = V.open_ ? cur() : selected();
    if (!ids.length) return;
    if (e.key === 'e' || e.key === 'E') editDialog(ids);
    else if (e.key === 'r') rotate(ids, 90);
    else if (e.key === 'R') rotate(ids, 270);
    else if (e.key === 'Delete') remove(ids);
    else return;
    e.preventDefault();
  });

  // ---------------------------------------------------------------------------
  // trash view
  // ---------------------------------------------------------------------------
  $('#nav a[data-v="favoriten"]')?.insertAdjacentHTML('afterend',
    `<a href="#/papierkorb" data-v="papierkorb" data-need="admin">${icon('trash')}<span>${esc(t('Papierkorb'))}</span><em class="count" id="trashCount"></em></a>`);

  async function trashCount() {
    if (!admin()) return;
    const r = await api('/api/admin/trash').catch(() => null);
    const el = $('#trashCount');
    if (r && el) el.textContent = r.entries.length ? num(r.entries.length) : '';
    return r;
  }
  // the role arrives after app.js asked /api/me
  new MutationObserver(() => trashCount()).observe(document.body, { attributes: true, attributeFilter: ['data-role'] });

  const ago = (ts) => {
    const d = Math.floor((Date.now() / 1000 - ts) / 86400);
    return d <= 0 ? t('heute') : d === 1 ? t('gestern') : t('vor {n} Tagen', { n: d });
  };

  SK.VIEWS.papierkorb = async (view) => {
    if (!admin()) {
      view.innerHTML = `<div class="empty">${icon('trash')}<h2>${esc(t('Papierkorb'))}</h2><p>${esc(t('Nur für Admins.'))}</p></div>`;
      return;
    }
    view.innerHTML = `<div class="head"><div><h1>${esc(t('Papierkorb'))}</h1><div class="sub">${esc(t('Wird geladen …'))}</div></div></div>`;
    const res = await trashCount();
    if (!res) { $('.sub', view).textContent = t('Papierkorb nicht erreichbar'); return; }
    const list = res.entries;
    const days = res.days;
    const pick = new Set();
    const sub = list.length
      ? `${t('{n} Elemente', { n: num(list.length) })} · ${days > 0 ? t('werden nach {d} Tagen endgültig gelöscht', { d: days }) : t('bleiben, bis der Papierkorb geleert wird')}`
      : t('Leer');
    view.innerHTML = `<div class="head"><div><h1>${esc(t('Papierkorb'))}</h1><div class="sub">${esc(sub)}</div></div>
      <div class="tools adm-ttools"${list.length ? '' : ' hidden'}>
        <button class="btn" data-all>${icon('select')}<span>${esc(t('Alle auswählen'))}</span></button>
        <button class="btn" data-restore disabled>${icon('restore')}<span>${esc(t('Wiederherstellen'))}</span></button>
        <button class="btn" data-kill disabled>${icon('trash')}<span>${esc(t('Endgültig löschen'))}</span></button>
        <button class="btn ghost" data-empty>${esc(t('Papierkorb leeren'))}</button>
      </div></div>
      ${list.length ? `<div class="adm-trash">${list.map((e) => {
        const name = e.rel.split('/').pop(), dir = e.rel.split('/').slice(0, -1).join('/');
        const left = days > 0 ? Math.max(0, days - Math.floor((Date.now() / 1000 - e.deleted) / 86400)) : null;
        return `<button class="adm-card" data-id="${esc(e.id)}" title="${esc(`${e.root}/${e.rel}`)}">
          <span class="cv">${e.thumb ? `<img loading="lazy" alt="" src="/api/admin/trash/${encodeURIComponent(e.id)}/thumb">` : icon(e.k === 2 ? 'video' : 'photos')}
            ${e.k === 2 ? `<span class="badge br">${icon('play', 'fill')}</span>` : e.live ? `<span class="badge tr live">${icon('live')}</span>` : ''}<span class="check">${icon('check')}</span></span>
          <b>${esc(name)}</b><small>${esc(`${e.root}${dir ? `/${dir}` : ''}`)}</small>
          <small>${esc(ago(e.deleted))}${e.by ? ` · ${esc(e.by)}` : ''}${left !== null ? ` · ${esc(t('noch {n} Tage', { n: left }))}` : ''}</small></button>`;
      }).join('')}</div>` : `<div class="empty">${icon('trash')}<h2>${esc(t('Der Papierkorb ist leer'))}</h2><p>${esc(t('Gelöschte Fotos und Videos landen hier und lassen sich wiederherstellen.'))}</p></div>`}`;
    const btn = (k) => $(`[data-${k}]`, view);
    const sync = () => {
      for (const c of $$('.adm-card', view)) c.classList.toggle('sel', pick.has(c.dataset.id));
      const n = pick.size;
      btn('restore').disabled = btn('kill').disabled = !n;
      $('span', btn('restore')).textContent = n ? t('Wiederherstellen ({n})', { n: num(n) }) : t('Wiederherstellen');
      $('span', btn('kill')).textContent = n ? t('Endgültig löschen ({n})', { n: num(n) }) : t('Endgültig löschen');
      $('span', btn('all')).textContent = n === list.length ? t('Auswahl aufheben') : t('Alle auswählen');
    };
    if (!list.length) return;
    $('.adm-trash', view).onclick = (e) => {
      const c = e.target.closest('.adm-card');
      if (!c) return;
      pick.has(c.dataset.id) ? pick.delete(c.dataset.id) : pick.add(c.dataset.id);
      sync();
    };
    btn('all').onclick = () => { if (pick.size === list.length) pick.clear(); else for (const e of list) pick.add(e.id); sync(); };
    btn('restore').onclick = async () => {
      const r = await call('/api/admin/trash/restore', { ids: [...pick] });
      if (!r) return;
      report(r, t('{n} wiederhergestellt', { n: num(r.changed) }));
      await SK.loadItems();
      SK.render();
    };
    const kill = async (ids, all) => {
      const n = all ? list.length : ids.length;
      const ok = await confirmDlg(all ? t('Papierkorb leeren?') : t('{n} Elemente endgültig löschen?', { n: num(n) }),
        t('Die Dateien werden gelöscht. Das lässt sich nicht rückgängig machen.'), t('Endgültig löschen'), true);
      if (!ok) return;
      const r = await call('/api/admin/trash/empty', all ? { all: true } : { ids });
      if (!r) return;
      report(r, t('{n} endgültig gelöscht', { n: num(r.changed) }));
      SK.render();
    };
    btn('kill').onclick = () => kill([...pick], false);
    btn('empty').onclick = () => kill([], true);
    sync();
  };
})();
