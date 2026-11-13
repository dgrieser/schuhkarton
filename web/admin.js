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
    SK.sheetGrip?.($('.adm-box', el), { key: 'dlg', onClose: close });
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

  // menu: a small list of actions next to a button (app.js)
  const menu = SK.menu;

  // a message with an optional button: the app's toast, so every message looks the same
  const snack = (msg, action, onAction, ms = 6000) => toast(msg, action, onAction, ms);

  // call posts and returns the result; work that runs in the background queue is waited for
  // (the page goes on meanwhile, and the work also when the page closes)
  async function call(path, body) {
    try {
      const r = await untilDone(await post(path, body));
      if (!r.ok) throw new Error(r.error || t('Fehler'));
      return r;
    } catch (e) {
      snack(t('Fehler: {e}', { e: t(String(e.message || e).replace(/^\d{3} /, '').trim()) }), '', null, 8000);
      return null;
    }
  }

  async function untilDone(r) {
    while (r?.ok && r.pending) {
      try {
        r = await api(`/api/ops/${encodeURIComponent(r.op)}?wait=20`);
      } catch (e) {
        const m = String(e.message);
        if (m.startsWith('404')) return { ok: false, error: t('Aufgabe unbekannt') };
        // signed out or not allowed: asking again does not help (the work goes on)
        if (/^4\d\d/.test(m)) return { ok: false, error: m.replace(/^\d{3} /, '').trim() || t('Fehler') };
        await new Promise((res) => setTimeout(res, 2000)); // offline for a moment, or a restart: ask again
      }
    }
    return r;
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
  const hashBase = () => (location.hash.slice(1) || '/fotos').split(/\||%7C/i)[0];

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

  // an edit shows on the page at once; the files are written behind
  function showEdit(ids, body) {
    const wallTs = (v) => { const ms = Date.parse(`${v}Z`); return Number.isNaN(ms) ? null : Math.floor(ms / 1000); };
    for (const id of ids) {
      const it = S.byId.get(id);
      if (!it) continue;
      if (body.place !== undefined) it.p = body.place;
      if (body.category !== undefined) it.c = body.category;
      if (body.taken) { const ts = wallTs(body.taken); if (ts !== null) it.t = ts; }
      if (body.shift) it.t += body.shift;
      if (V.open_ && V.it?.id === id && V.detail) {
        const d = V.detail;
        if (body.place !== undefined) d.place = body.place;
        if (body.category !== undefined) d.category = body.category;
        if (body.desc !== undefined) d.desc = body.desc;
        d.taken = it.t;
        if (body.clearGps) d.gps = false;
        else if (body.lat !== undefined) Object.assign(d, { gps: true, lat: body.lat, lon: body.lon });
      }
    }
    if (V.open_) V.renderInfo();
    else SK.rerender();
  }

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
      dlg.close();
      if (!single) SK.endSelection();
      showEdit(ids, body);
      const r = await call('/api/admin/edit', body);
      if (!r) { refreshAfter(); return; } // the old values come back
      let msg = single ? t('Gespeichert') : t('{n} Elemente geändert', { n: num(r.changed) });
      if (r.moved) msg += ` · ${t('nach den Regeln einsortiert')}`;
      else if (r.queued) msg += ` · ${t('{n} werden im Hintergrund einsortiert', { n: num(r.queued) })}`;
      report(r, msg);
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
  // rotate: turned on the page at once (the pictures turn by CSS until the new ones come)
  const spin = new Map(); // id -> degrees turned, not done yet
  const showSpin = () => {
    for (const [id, d] of spin) {
      for (const img of $$(`.tile[data-id="${CSS.escape(id)}"] img`)) img.style.rotate = `${d}deg`;
      if (V.open_ && V.it?.id === id) for (const img of $$('img', V.media)) img.style.rotate = `${d}deg`;
    }
  };
  async function rotate(ids, deg) {
    if (!admin() || !ids.length) return;
    for (const id of ids) spin.set(id, (spin.get(id) || 0) + deg);
    showSpin();
    if (ids.length > 1) SK.endSelection();
    const r = await call('/api/admin/rotate', { ids, deg });
    for (const id of ids) {
      const left = (spin.get(id) || 0) - deg;
      if (left) spin.set(id, left);
      else spin.delete(id);
    }
    if (r) report(r, ids.length === 1 ? t('Gedreht') : t('{n} Fotos gedreht', { n: num(r.changed) }));
    await refreshAfter();
    showSpin(); // others still turning
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

  // the items leave the page at once; the server catches up behind (a failure brings them back)
  function dropLocal(ids) {
    const gone = new Set(ids);
    S.items = S.items.filter((it) => !gone.has(it.id));
    for (const id of ids) S.byId.delete(id);
    SK.updateCounts();
    if (!V.open_) { SK.rerender(); return; }
    dirty = true;
    const list = V.list.filter((o) => !gone.has(o.id));
    if (!list.length) { V.close(); return; }
    let i = list.findIndex((o) => o.id === V.it?.id);
    if (i < 0) i = Math.min(V.i, list.length - 1);
    V.list = list;
    V.i = i;
    history.replaceState({ viewer: true }, '', `#${hashBase()}|${list[i].id}`);
    V.show();
  }

  // delete: every account, into the trash (kept for the days set, then deleted for good)
  async function remove(ids) {
    if (!SK.Account.can('user') || !ids.length) return;
    const days = S.config.trashDays;
    if (ids.length >= 10) { // a slip of the finger is undone at once; many are asked for
      const ok = await confirmDlg(t('{n} Elemente in den Papierkorb legen?', { n: num(ids.length) }),
        days > 0 ? t('Im Papierkorb lässt sich alles {d} Tage lang wiederherstellen; danach wird es endgültig gelöscht.', { d: days })
          : t('Im Papierkorb lässt sich alles wiederherstellen.'),
        t('In den Papierkorb'), true);
      if (!ok) return;
    }
    SK.endSelection();
    dropLocal(ids);
    showTrashCount(trashN + ids.length);
    const pending = call('/api/trash/delete', { ids }).then((r) => {
      if (!r) { refreshAfter(); trashCount(); return null; } // they come back
      if (r.warnings?.length) report(r, t('Teilweise gelöscht'));
      SK.scheduleReload();
      trashCount();
      return r.entries || [];
    });
    snack(ids.length === 1 ? t('In den Papierkorb gelegt') : t('{n} Elemente in den Papierkorb gelegt', { n: num(ids.length) }), t('Rückgängig'), async () => {
      const entries = await pending; // also before the deletion arrived
      if (!entries?.length) return;
      const u = await call('/api/trash/restore', { ids: entries });
      if (u) { toast(t('Wiederhergestellt')); refreshAfter(); trashCount(); }
    });
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
      // the dialog closes at once; the files move behind (also when the page closes)
      dlg.close();
      SK.endSelection();
      toast(ids.length === 1 ? t('Wird verschoben …') : t('{n} werden verschoben …', { n: num(ids.length) }));
      const r = await call('/api/admin/move', { ids, root: sel.root, dir });
      if (!r) { refreshAfter(); return; }
      report(r, r.changed === 1 ? t('Verschoben nach {p}', { p: [sel.root, dir].filter(Boolean).join('/') }) : t('{n} Elemente verschoben', { n: num(r.changed) }));
      refreshAfter(r.ids || {});
    };
    draw();
    update();
    filt.focus();
  }

  // ---------------------------------------------------------------------------
  // viewer buttons
  // ---------------------------------------------------------------------------
  // one button for the photo's changes in the corner of its details (the sidebar, a phone: the
  // sheet): every account can move it to the trash, admins change more (keys E, R, Entf act directly)
  const cur = () => (V.it ? [V.it.id] : []);
  const vMenu = (anchor) => menu(anchor, [
    ...(admin() ? [{ icon: 'edit', label: t('Datum, Ort, Text …'), run: () => editDialog(cur()) },
    ...(V.it?.k === 1 ? [
      { icon: 'rotl', label: t('90° nach links'), run: () => rotate(cur(), 270) },
      { icon: 'rotate', label: t('90° nach rechts'), run: () => rotate(cur(), 90) },
    ] : []),
    { icon: 'move', label: t('Verschieben'), run: () => moveDialog(cur()) },
    ...(aiOn() ? [{ icon: 'spark', label: t('Neu beschreiben (KI)'), run: () => redescribe(cur()) }] : []),
    ...(ocrOn() && V.it?.k === 1 ? [{ icon: 'file', label: t('Text neu erkennen'), run: () => rerecognize(cur()) }] : []),
    ...(speechOn() && (V.it?.k === 2 || V.it?.lv) ? [{ icon: 'spark', label: t('Sprache neu erkennen'), run: () => retranscribe(cur()) }] : []),
    ] : []),
    { icon: 'trash', label: t('In den Papierkorb'), danger: true, run: () => remove(cur()) },
  ]);
  const renderInfo0 = V.renderInfo.bind(V);
  V.renderInfo = function renderInfo(...a) {
    renderInfo0(...a);
    const panel = $('#vInfoPanel');
    if (!this.showInfo || !this.detail || !panel || !user() || $('.v-corner', panel)) return;
    panel.insertAdjacentHTML('afterbegin', `<div class="v-corner">
      <button class="vbtn" id="vEdit" title="${esc(t('Bearbeiten'))}" aria-haspopup="menu">${icon('edit')}</button></div>`);
    $('#vEdit', panel).onclick = (e) => vMenu(e.currentTarget);
  };

  // ---------------------------------------------------------------------------
  // selection bar
  // ---------------------------------------------------------------------------
  $('#selClose')?.insertAdjacentHTML('beforebegin', `
    <button class="btn" id="selEdit" data-need="admin" title="${esc(t('Datum, Ort, Kategorie, Position'))}">${icon('edit')}<span>${esc(t('Bearbeiten'))}</span></button>
    <button class="btn" id="selRot" data-need="admin" title="${esc(t('Drehen'))}">${icon('rotate')}<span>${esc(t('Drehen'))}</span></button>
    <button class="btn" id="selMove" data-need="admin" title="${esc(t('In einen anderen Ordner'))}">${icon('move')}<span>${esc(t('Verschieben'))}</span></button>
    <button class="btn" id="selAI" data-need="admin" hidden title="${esc(t('Beschreibungen neu schreiben lassen (KI)'))}">${icon('spark')}<span>${esc(t('Beschreiben'))}</span></button>
    <button class="btn adm-del" id="selDel" data-need="user" title="${esc(t('In den Papierkorb'))}">${icon('trash')}<span>${esc(t('Löschen'))}</span></button>`);
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
    if (!SK.Account.can('user') || e.ctrlKey || e.metaKey || e.altKey || SK.isTyping(e.target) || $('.adm-dlg') || (!V.open_ && SK.pageSelActive())) return; // a trash selection holds no photos
    if (e.key === 'Delete') { // every account
      const ids = V.open_ ? cur() : selected();
      if (ids.length) { e.preventDefault(); remove(ids); }
      return;
    }
    if (!admin()) return;
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
  // trash: every account sees what it deleted (admins everything) and brings it
  // back. Emptying only hides: files are deleted for good after the days kept.
  // ---------------------------------------------------------------------------
  $('#nav a[data-v="favoriten"]')?.insertAdjacentHTML('afterend',
    `<a href="#/papierkorb" data-v="papierkorb" data-need="user">${icon('trash')}<span>${esc(t('Papierkorb'))}</span><em class="count" id="trashCount"></em></a>
     <a href="#/aenderungen" data-v="aenderungen" data-need="admin">${icon('restore')}<span>${esc(t('Änderungen'))}</span></a>`);

  const user = () => SK.Account.can('user');
  let trashN = 0;
  const showTrashCount = (n) => {
    trashN = Math.max(0, n);
    const el = $('#trashCount');
    if (el) el.textContent = trashN ? num(trashN) : '';
  };
  async function trashCount() {
    if (!user()) return null;
    const r = await api('/api/trash').catch(() => null);
    if (r) showTrashCount(r.entries.length);
    return r;
  }
  // the role arrives after app.js asked /api/me
  new MutationObserver(() => trashCount()).observe(document.body, { attributes: true, attributeFilter: ['data-role'] });

  const ago = (ts) => {
    const d = Math.floor((Date.now() / 1000 - ts) / 86400);
    return d <= 0 ? t('heute') : d === 1 ? t('gestern') : t('vor {n} Tagen', { n: d });
  };

  SK.VIEWS.papierkorb = async (view) => {
    if (!user()) {
      view.innerHTML = `<div class="empty">${icon('trash')}<h2>${esc(t('Papierkorb'))}</h2><p>${esc(t('Löschen und wiederherstellen geht mit einem Konto.'))}</p>
        <p><a class="btn primary" href="${esc(SK.Account.loginURL())}">${esc(t('Anmelden'))}</a></p></div>`;
      return;
    }
    view.innerHTML = `<div class="head"><div><h1>${esc(t('Papierkorb'))}</h1><div class="sub">${esc(t('Wird geladen …'))}</div></div></div>`;
    const res = await trashCount();
    if (!res) { $('.sub', view).textContent = t('Papierkorb nicht erreichbar'); return; }
    if (SK.route().view !== 'papierkorb') return;
    let list = res.entries;
    const days = res.days;
    const subText = () => (list.length
      ? `${t(list.length === 1 ? '{n} Element' : '{n} Elemente', { n: num(list.length) })} · ${days > 0 ? t('werden nach {d} Tagen endgültig gelöscht', { d: days }) : t('bleiben, bis sie jemand wiederherstellt')}`
      : t('Leer'));
    const card = (e) => {
      const name = e.rel.split('/').pop(), dir = e.rel.split('/').slice(0, -1).join('/');
      const left = days > 0 ? Math.max(0, days - Math.floor((Date.now() / 1000 - e.deleted) / 86400)) : null;
      return `<button class="adm-card" data-id="${esc(e.id)}" title="${esc(`${e.root}/${e.rel}`)}">
        <span class="cv">${e.thumb ? `<img loading="lazy" alt="" src="/api/trash/${encodeURIComponent(e.id)}/thumb">` : icon(e.k === 2 ? 'video' : 'photos')}
          ${e.k === 2 ? `<span class="badge br">${icon('play', 'fill')}</span>` : e.live ? `<span class="badge tr live">${icon('live')}</span>` : ''}<span class="check">${icon('check')}</span></span>
        <b>${esc(name)}</b><small>${esc(`${e.root}${dir ? `/${dir}` : ''}`)}</small>
        <small>${esc(ago(e.deleted))}${e.by && admin() ? ` · ${esc(e.by)}` : ''}${left !== null ? ` · ${esc(t('noch {n} Tage', { n: left }))}` : ''}</small></button>`;
    };
    const empty = `<div class="empty">${icon('trash')}<h2>${esc(t('Der Papierkorb ist leer'))}</h2><p>${esc(t('Gelöschte Fotos und Videos landen hier und lassen sich wiederherstellen.'))}</p></div>`;
    view.innerHTML = `<div class="head"><div><h1>${esc(t('Papierkorb'))}</h1><div class="sub">${esc(subText())}</div></div>
      <div class="tools adm-ttools"${list.length ? '' : ' hidden'}>
        <button class="btn" data-all>${icon('select')}<span>${esc(t('Alle auswählen'))}</span></button>
        <button class="btn ghost" data-empty title="${esc(t('Blendet alles aus deinem Papierkorb aus; endgültig gelöscht wird erst nach der Frist.'))}">${esc(t('Papierkorb leeren'))}</button>
      </div></div>
      <div data-tb>${list.length ? `<div class="adm-trash">${list.map(card).join('')}</div>` : empty}</div>`;
    const btn = (k) => $(`[data-${k}]`, view);
    const pick = S.sel;
    // entries leave the page at once; the server catches up behind
    const drop = (ids) => {
      const gone = new Set(ids);
      list = list.filter((e) => !gone.has(e.id));
      for (const id of ids) $(`.adm-card[data-id="${CSS.escape(id)}"]`, view)?.remove();
      for (const id of ids) pick.delete(id);
      SK.selectionChanged();
      $('.sub', view).textContent = subText();
      showTrashCount(trashN - ids.length);
      if (!list.length) { $('[data-tb]', view).innerHTML = empty; $('.adm-ttools', view).hidden = true; SK.endSelection(); }
    };
    const back = () => { if (SK.route().view === 'papierkorb') SK.rerender(); else trashCount(); };
    if (!list.length) return;
    $('[data-tb]', view).onclick = (e) => {
      const c = e.target.closest('.adm-card');
      if (!c) return;
      pick.has(c.dataset.id) ? pick.delete(c.dataset.id) : pick.add(c.dataset.id);
      S.selecting = true;
      SK.selectionChanged();
    };
    btn('all').onclick = () => {
      if (pick.size === list.length) SK.endSelection();
      else { for (const e of list) pick.add(e.id); S.selecting = true; SK.selectionChanged(); }
    };
    const restore = async (ids) => {
      drop(ids);
      toast(ids.length === 1 ? t('Wird wiederhergestellt …') : t('{n} werden wiederhergestellt …', { n: num(ids.length) }));
      const r = await call('/api/trash/restore', { ids });
      if (!r) { back(); return; }
      report(r, t('{n} wiederhergestellt', { n: num(r.changed) }));
      if (Object.keys(r.ids || {}).length < ids.length) back(); // some stay in the trash: shown again
      SK.loadItems().then(() => SK.updateCounts?.()).catch(() => {});
    };
    // emptying hides: gone from this trash, deleted for good only after the days kept
    const hide = async (ids) => {
      const all = !ids;
      ids = ids || list.map((e) => e.id);
      drop(ids);
      const r = await call('/api/trash/clear', all ? {} : { ids });
      if (!r) { back(); return; }
      const hidden = r.entries || [];
      if (hidden.length < ids.length) back(); // some were not removed (gone meanwhile, or not one's own)
      snack(days > 0 ? t('{n} aus dem Papierkorb entfernt – endgültig gelöscht nach {d} Tagen', { n: num(hidden.length), d: days }) : t('{n} aus dem Papierkorb entfernt', { n: num(hidden.length) }), t('Rückgängig'), async () => {
        if (await call('/api/trash/unhide', { ids: hidden })) back();
      });
    };
    btn('empty').onclick = () => hide(null);
    SK.pageSelection({
      actions: [
        { icon: 'restore', label: t('Wiederherstellen'), run: restore },
        { icon: 'x', label: t('Aus dem Papierkorb entfernen'), short: t('Entfernen'), run: (ids) => hide(ids) },
      ],
      sync: () => {
        for (const c of $$('.adm-card', view)) c.classList.toggle('sel', pick.has(c.dataset.id));
        $('span', btn('all')).textContent = pick.size === list.length ? t('Auswahl aufheben') : t('Alle auswählen');
      },
    });
  };

  // ---------------------------------------------------------------------------
  // changes: everything that changed in the last days, by whom, and undo
  // ---------------------------------------------------------------------------
  const KINDS = [
    ['', 'Alle Änderungen'], ['edit', 'Bearbeitet'], ['rotate', 'Gedreht'], ['delete,restore,purge,binclear', 'Papierkorb'],
    ['move', 'Verschoben'], ['faces,hide,show', 'Personen'], ['note', 'Notizen'],
    ['fav', 'Favoriten'], ['ai,ocr,speech', 'KI und Texterkennung'],
  ];
  const KIND_ICON = { edit: 'edit', rotate: 'rotate', delete: 'trash', restore: 'restore', purge: 'trash', move: 'move', faces: 'face',
    hide: 'eye-off', show: 'eye', note: 'text', mark: 'mark', unmarkall: 'unmark', binclear: 'x', fav: 'heart', ai: 'spark', ocr: 'file', speech: 'spark' };
  // a real time as the wall clock, which the date helpers show (they read UTC)
  const realLocal = (ts) => ts - new Date(ts * 1000).getTimezoneOffset() * 60;
  const FIELD = { taken: 'Datum', place: 'Ort', category: 'Kategorie', desc: 'Text', gps: 'Position' };
  // what happened, in words
  const changeText = (c) => {
    const i = c.info || {}, n = c.items?.length || c.names?.length || i.n || 0;
    const count = n > 1 ? ` (${num(n)})` : '';
    switch (c.kind) {
      case 'edit': {
        const parts = (i.fields || []).filter((f, k, a) => a.indexOf(f) === k).map((f) => {
          if (f === 'place' && i.place) return `${t('Ort')}: ${i.place}`;
          if (f === 'category' && i.category) return `${t('Kategorie')}: ${i.category}`;
          if (f === 'taken' && i.taken) return `${t('Datum')}: ${i.taken.replace('T', ' ')}`;
          if (f === 'taken' && i.shift) return t('Datum um {h} Std. verschoben', { h: Math.round(i.shift / 360) / 10 });
          if (f === 'gps' && i.gpsClear) return t('Position entfernt');
          return t(FIELD[f] || f);
        });
        return t('geändert: {what}', { what: parts.join(', ') }) + count;
      }
      case 'rotate': return (i.deg === 90 ? t('nach rechts gedreht') : i.deg === 270 ? t('nach links gedreht') : t('um {d}° gedreht', { d: i.deg })) + count;
      case 'delete': return (i.dupes ? t('Duplikate in den Papierkorb gelegt') : t('in den Papierkorb gelegt')) + count;
      case 'restore': return t('aus dem Papierkorb geholt') + count;
      case 'purge': return (c.by ? t('endgültig gelöscht') : t('nach {d} Tagen endgültig gelöscht', { d: i.days || '?' })) + count;
      case 'move': return (i.rules ? t('nach den Regeln einsortiert') : t('verschoben nach {to}', { to: i.to || '/' })) + count;
      case 'faces':
        switch (i.op) {
          case 'add': return i.to ? t('Gesicht markiert: {name}', { name: i.to }) : t('Gesicht markiert');
          case 'not': return t('nicht {name}', { name: i.from || t('diese Person') }) + count;
          case 'noface': return t('kein Gesicht') + count;
          case 'hide': return t('Gesicht ausgeblendet') + count;
          case 'restore': return t('Gesichter zurückgesetzt') + count;
          default: return (i.from && i.from !== i.to ? t('{from} heißt jetzt {to}', { from: i.from, to: i.to }) : t('benannt: {to}', { to: i.to })) + count;
        }
      case 'hide': return t('{name} ausgeblendet', { name: i.from || t('Person') });
      case 'show': return t('ausgeblendete Person wieder gezeigt');
      case 'note':
        if (i.op === 'add') return t('Notiz geschrieben: „{text}“', { text: i.text });
        return i.op === 'edit' ? t('Notiz geändert: „{text}“', { text: i.text }) : t('Notiz gelöscht: „{text}“', { text: i.text });
      case 'mark': return (i.on ? t('zum Löschen vorgemerkt') : t('Vormerkung entfernt')) + count;
      case 'unmarkall': return t('alle Vormerkungen entfernt') + count;
      case 'binclear': return t('Papierkorb geleert (nur ausgeblendet)') + count;
      case 'fav': return (i.on ? t('als Favorit markiert') : t('aus den Favoriten genommen')) + count;
      case 'ai': return t('Beschreibung neu angefordert') + count;
      case 'ocr': return t('Text im Bild neu erkennen lassen') + count;
      case 'speech': return t('Sprache neu erkennen lassen') + count;
    }
    return c.kind;
  };

  SK.VIEWS.aenderungen = async (view) => {
    if (!admin()) {
      view.innerHTML = `<div class="empty">${icon('restore')}<h2>${esc(t('Änderungen'))}</h2><p>${esc(t('Nur für Admins.'))}</p></div>`;
      return;
    }
    let kind = '', by = '', list = [], more = false, gen = 0; // gen: only the newest request counts
    const people = new Set();
    view.innerHTML = `<div class="head"><div><h1>${esc(t('Änderungen'))}</h1><div class="sub">${esc(t('Wird geladen …'))}</div></div>
      <div class="tools"><select class="sel" data-kind title="${esc(t('Art'))}">${KINDS.map(([k, l]) => `<option value="${k}">${esc(t(l))}</option>`).join('')}</select>
        <select class="sel" data-by title="${esc(t('Wer'))}"><option value="">${esc(t('Alle'))}</option><option value="-">${esc(t('Schuhkarton'))}</option></select></div></div>
      <div class="hx" data-hx></div><div class="hx-more"><button class="btn" data-more hidden>${esc(t('Ältere laden'))}</button></div>`;
    const box = $('[data-hx]', view);
    // the photos it was about; gone ones from the trash, else their names
    const thumbs = (c) => {
      const out = [];
      for (const id of c.items || []) {
        const it = S.byId.get(id);
        if (it) out.push(`<a href="#/fotos|${esc(id)}" class="hx-th" data-open="${esc(id)}"><img loading="lazy" alt="" src="${SK.thumbURL(it)}"></a>`);
        if (out.length >= 8) break;
      }
      if (!out.length) {
        for (const e of (c.trash || []).slice(0, 8)) out.push(`<span class="hx-th"><img loading="lazy" alt="" src="/api/trash/${encodeURIComponent(e)}/thumb"></span>`);
      }
      const total = Math.max(c.items?.length || 0, c.trash?.length || 0, c.names?.length || 0);
      if (!out.length && c.names?.length) return `<div class="hx-names">${c.names.slice(0, 6).map(esc).join(' · ')}${c.names.length > 6 ? ` · +${num(c.names.length - 6)}` : ''}</div>`;
      return out.length ? `<div class="hx-thumbs">${out.join('')}${total > out.length ? `<span class="hx-th more">+${num(total - out.length)}</span>` : ''}</div>` : '';
    };
    const row = (c) => {
      const undone = c.undone ? t('rückgängig gemacht von {who}, {d} {t}', { who: c.undoneBy || '?', d: SK.fmtDay(realLocal(c.undone)), t: SK.fmtTime(realLocal(c.undone)) }) : '';
      const why = !c.canUndo && !c.undone && c.why ? t(c.why) : '';
      return `<div class="hx-row${c.undone ? ' undone' : ''}" data-id="${esc(c.id)}">
        <span class="hx-ic">${icon(KIND_ICON[c.kind] || 'edit')}</span>
        <div class="hx-main"><div class="hx-t"><b class="${c.by ? '' : 'sys'}">${esc(c.by || t('Schuhkarton'))}</b> ${esc(changeText(c))}</div>
          <div class="hx-meta">${esc(SK.fmtTime(realLocal(c.at)))}${undone ? ` · ${esc(undone)}` : ''}${why ? ` · ${esc(why)}` : ''}</div>${thumbs(c)}</div>
        ${c.canUndo ? `<button class="btn" data-undo>${icon('restore')}<span>${esc(t('Rückgängig'))}</span></button>` : ''}</div>`;
    };
    const draw = () => {
      let day = '', html = '';
      for (const c of list) {
        const d = SK.fmtDay(realLocal(c.at));
        if (d !== day) { day = d; html += `<h3 class="hx-day">${esc(d)}</h3>`; }
        html += row(c);
      }
      box.innerHTML = html || `<div class="empty">${icon('restore')}<h2>${esc(t('Keine Änderungen'))}</h2><p>${esc(t('Was jemand ändert, erscheint hier und lässt sich zurücknehmen.'))}</p></div>`;
      $('[data-more]', view).hidden = !more;
      const sel = $('[data-by]', view);
      for (const u of [...people].sort()) if (![...sel.options].some((o) => o.value === u)) sel.insertAdjacentHTML('beforeend', `<option value="${esc(u)}">${esc(u)}</option>`);
    };
    const load = async (append = false) => {
      const my = ++gen;
      const before = append && list.length ? list[list.length - 1].id : '';
      const q = new URLSearchParams({ limit: '100' });
      if (before) q.set('before', String(before));
      if (kind) q.set('kind', kind);
      if (by) q.set('by', by);
      const r = await api(`/api/admin/history?${q}`).catch(() => null);
      if (!r || my !== gen || SK.route().view !== 'aenderungen') return; // a newer filter or page came meanwhile
      list = append ? list.concat(r.changes) : r.changes;
      more = r.more;
      for (const c of r.changes) if (c.by) people.add(c.by);
      $('.head .sub', view).textContent = r.days > 0 ? t('Die letzten {d} Tage. Was älter ist, lässt sich nicht mehr zurücknehmen.', { d: r.days })
        : t('Das letzte Jahr. Was älter ist, lässt sich nicht mehr zurücknehmen.');
      draw();
    };
    $('[data-kind]', view).onchange = (e) => { kind = e.target.value; load(); };
    $('[data-by]', view).onchange = (e) => { by = e.target.value; load(); };
    $('[data-more]', view).onclick = () => load(true);
    box.onclick = async (e) => {
      const open = e.target.closest('[data-open]');
      if (open) {
        e.preventDefault();
        const it = S.byId.get(open.dataset.open);
        if (it) SK.Viewer.open([it], 0);
        return;
      }
      const b = e.target.closest('[data-undo]');
      if (!b) return;
      b.disabled = true;
      const r = await call(`/api/admin/history/${encodeURIComponent(b.closest('.hx-row').dataset.id)}/undo`, {});
      if (!r) { b.disabled = false; return; }
      report(r, t('Rückgängig gemacht'));
      await SK.loadItems().catch(() => {});
      await SK.loadPeople().catch(() => {});
      trashCount();
      load();
    };
    load();
  };
})();
