/* Schuhkarton — duplicates (admins). The server groups identical files,
   the same picture in other copies (smaller, recompressed, turned, other
   format) and series of nearly the same shot, and suggests which copy to
   keep. Here admins look, compare and decide; deleted copies go to the
   trash, what they knew (favorites, names, place, notes …) moves to the
   copy that stays. Loaded after admin.js; uses window.SK. */
'use strict';
(() => {
  const SK = window.SK;
  if (!SK) return;
  const { t, S, $, $$, esc, icon, api, toast, num } = SK;
  const ui = SK.ui;
  const admin = () => SK.Account.can('admin');
  const size = (b) => SK.fmtSize(b || 0);
  const CLASS = { identisch: 'Identisch', gleich: 'Gleich', aehnlich: 'Ähnlich' };
  const CLASS_HINT = {
    identisch: 'Byte für Byte dieselbe Datei',
    gleich: 'Dasselbe Bild: verkleinert, neu gespeichert, gedreht oder in anderem Format',
    aehnlich: 'Serie: fast dasselbe Motiv, kurz nacheinander aufgenommen',
  };
  const SRC = { exif: 'Kamera', edit: 'von Hand', name: 'Dateiname', folder: 'Ordner (ungefähr)', mtime: 'Dateidatum' };

  document.querySelector('body > svg')?.insertAdjacentHTML('beforeend', `
    <symbol id="i-dupes" viewBox="0 0 24 24"><rect x="3.5" y="7.5" width="12" height="12" rx="2.5"/><path d="M8.5 7.5V6a2.5 2.5 0 0 1 2.5-2.5h7A2.5 2.5 0 0 1 20.5 6v7a2.5 2.5 0 0 1-2.5 2.5h-2.5"/></symbol>
    <symbol id="i-star" viewBox="0 0 24 24"><path d="m12 3.5 2.6 5.3 5.9.9-4.3 4.1 1 5.8L12 16.9l-5.2 2.7 1-5.8-4.3-4.1 5.9-.9z"/></symbol>
    <symbol id="i-pin-off" viewBox="0 0 24 24"><path d="M12 21s-7-6.1-7-11.5A7 7 0 0 1 19 9.5C19 14.9 12 21 12 21z"/><path d="M4 4l16 16"/></symbol>`);

  // ---------------------------------------------------------------------------
  // data
  // ---------------------------------------------------------------------------
  // pending: groups decided whose files still move in the background (not shown again meanwhile)
  const D = { groups: [], members: {}, status: null, filter: 'all', loaded: false, keep: new Map(), pending: new Set(), later: new Map() };
  // a quick round has at most this many groups (as many as a quick round of faces); the groups
  // put off for later come last, the one put off last at the end
  const ROUND = 12;
  let laterN = 0;
  const roundIds = () => {
    const ids = visible().map((g) => g.id);
    const off = ids.filter((id) => D.later.has(id)).sort((a, b) => D.later.get(a) - D.later.get(b));
    return [...ids.filter((id) => !D.later.has(id)), ...off].slice(0, ROUND);
  };

  async function load() {
    const r = await api('/api/admin/dupes').catch(() => null);
    if (!r) return false;
    D.groups = (r.groups || []).filter((g) => !D.pending.has(g.id));
    D.members = r.members || {};
    D.status = r.status;
    D.loaded = true;
    for (const g of D.groups) if (!D.keep.has(g.id)) D.keep.set(g.id, new Set(g.keep));
    count();
    return true;
  }

  function count(st = D.status) {
    const el = $('#dupeCount');
    if (!el || !st) return;
    const n = (st.counts?.identisch || 0) + (st.counts?.gleich || 0) + (st.counts?.aehnlich || 0);
    el.textContent = n ? num(n) : '';
  }
  async function summary() {
    if (!admin()) return;
    const r = await api('/api/admin/dupes?summary=1', { quiet: true }).catch(() => null);
    if (r) { D.status = r.status; count(); }
  }

  // counts after local changes (the server agrees on its next pass)
  function recount() {
    if (!D.status) return;
    const c = { identisch: 0, gleich: 0, aehnlich: 0 };
    let save = 0;
    for (const g of D.groups) {
      c[g.class]++;
      if (g.class !== 'aehnlich') save += g.save;
    }
    D.status.counts = c;
    D.status.save = save;
    count();
  }

  const visible = () => D.groups.filter((g) => D.filter === 'all' || g.class === D.filter);
  const keepOf = (g) => D.keep.get(g.id) || new Set(g.keep);
  const removeOf = (g) => g.items.filter((id) => !keepOf(g).has(id));
  const freedOf = (g, keep = keepOf(g)) => g.items.filter((id) => !keep.has(id)).reduce((s, id) => s + (D.members[id]?.size || 0), 0);
  // the bulk cleanup promises that the best copy stays: a group left with nothing kept (decided
  // in the full screen view, then skipped) falls back to the suggestion there
  const bulkKeep = (g) => (keepOf(g).size ? keepOf(g) : new Set(g.keep));
  const m = (id) => D.members[id] || {};
  const sumOf = (g) => {
    const k = keepOf(g).size, r = removeOf(g).length, s = size(freedOf(g));
    return k ? t('{k} bleibt · {r} in den Papierkorb ({s})', { k: num(k), r: num(r), s }) : t('Alle {r} in den Papierkorb ({s})', { r: num(r), s });
  };
  const thumb = (id) => { const it = S.byId.get(id); return it ? SK.thumbURL(it) : `/t/${id}/s.jpg?v=${m(id).v || ''}`; };
  const large = (id) => { const it = S.byId.get(id); return it ? SK.largeURL(it) : `/t/${id}/m.jpg?v=${m(id).v || ''}`; };

  // ---------------------------------------------------------------------------
  // deciding
  // ---------------------------------------------------------------------------
  // a few groups per request: moving hundreds of files on a share takes
  // minutes, longer than a proxy keeps a request open, and the page shows
  // how far it got
  const BATCH = 25;
  let busy = false;
  async function apply(groups, { bulk = false } = {}) {
    const todo = groups.map((g) => {
      const keep = bulk ? bulkKeep(g) : keepOf(g);
      return { g, r: { keep: [...keep], remove: g.items.filter((id) => !keep.has(id)) } };
    }).filter((x) => x.r.remove.length);
    if (!todo.length) { toast(t('Nichts zu löschen')); return false; }
    if (busy) return false;
    busy = true;
    if (bulk) clearTimeout(pollT); // the list must not be drawn anew meanwhile (draw arms it again)
    const r = { changed: 0, freed: 0, entries: [], warnings: [] };
    const done = new Set();
    let err = '';
    try {
      for (let i = 0; i < todo.length && !err; i += BATCH) {
        if (todo.length > BATCH) {
          ui.snack(t('Wird bereinigt … {d} / {n} Gruppen', { d: num(i), n: num(todo.length) }), '', null, 10 * 60000);
        }
        const part = todo.slice(i, i + BATCH);
        let a = null;
        try {
          // moving files on a share can take longer than the server waits: then the work is
          // queued, and what it did (the count, the space freed) comes when it is done
          a = await ui.untilDone(await api('/api/admin/dupes/resolve', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ groups: part.map((x) => x.r) }) }));
          if (!a.ok) err = a.error || t('Fehler');
        } catch (e) {
          err = String(e.message || e).replace(/^\d{3} /, '').replace(/<[^>]*>/g, ' ').replace(/\s+/g, ' ').trim().slice(0, 160) || t('Fehler');
        }
        if (!a) break;
        // also after an error: what went to the trash stays undoable
        if (a.ok || a.entries?.length) for (const x of part) done.add(x.g.id);
        r.changed += a.changed || 0;
        r.freed += a.freed || 0;
        r.entries.push(...(a.entries || []));
        r.warnings.push(...(a.warnings || []));
      }
    } finally {
      busy = false;
    }
    if (!done.size) {
      ui.snack(t('Fehler: {e}', { e: t(err) }), '', null, 8000);
      return false;
    }
    D.groups = D.groups.filter((g) => !done.has(g.id));
    groups = groups.filter((g) => done.has(g.id));
    const entries = r.entries;
    const hints = r.warnings.length ? ` · ${t('{n} Hinweise, z. B. {w}', { n: r.warnings.length, w: t(r.warnings[0]) })}` : '';
    ui.snack(err ? t('{n} Gruppen bereinigt, dann Fehler: {e}', { n: num(groups.length), e: t(err) }) + hints
      : bulk || groups.length > 1
        ? t('{n} Gruppen bereinigt · {s} frei', { n: num(groups.length), s: size(r.freed) })
        : t('{n} in den Papierkorb · {s} frei', { n: num(r.changed), s: size(r.freed) }),
    t('Rückgängig'), async () => {
      const u = await ui.call('/api/trash/restore', { ids: entries });
      if (!u) return;
      toast(t('Wiederhergestellt'));
      await SK.loadItems();
      await ui.call('/api/admin/dupes/rescan', {});
      await new Promise((r) => setTimeout(r, 1200)); // the server groups again
      for (const g of groups) D.keep.delete(g.id);
      await load();
      if (SK.route().view === 'duplikate' && !cmp && !full) SK.render();
      redraw();
      ui.trashCount();
    }, err ? 20000 : 9000);
    if (r.warnings.length && !err) ui.report(r, t('Mit Hinweisen'));
    SK.loadItems();
    ui.trashCount();
    recount();
    return true;
  }

  // a decision on one group: off the list at once (the next group is there right away), the
  // files move in the background one group after the other; back in the list when that failed
  let queue = Promise.resolve();
  const later = (fn) => (queue = queue.catch(() => {}).then(fn));
  const redraws = new Set(); // what shows the groups and needs drawing anew after a change
  const redraw = () => { for (const f of [...redraws]) f(); };
  function take(g) {
    const at = D.groups.indexOf(g);
    if (at >= 0) D.groups.splice(at, 1);
    D.pending.add(g.id);
    recount();
    return Math.max(0, at);
  }
  function settle(g, at, ok) {
    D.pending.delete(g.id);
    if (ok || D.groups.some((x) => x.id === g.id)) return;
    D.groups.splice(Math.min(at, D.groups.length), 0, g);
    recount();
    redraw();
  }
  function resolveSoon(g) {
    if (!removeOf(g).length) { toast(t('Nichts zu löschen')); return false; }
    const at = take(g);
    later(async () => settle(g, at, await apply([g]).catch(() => false)));
    return true;
  }
  function ignoreSoon(g) {
    const at = take(g);
    later(async () => {
      const r = await ui.call('/api/admin/dupes/ignore', { ids: g.items }).catch(() => null);
      settle(g, at, !!r);
      if (r) toast(t('Gemerkt: keine Duplikate'));
    });
    return true;
  }
  // which copies stay: one for the same picture, any number for a series; none kept puts every
  // copy in the trash
  function toggleKeep(g, id) {
    const k = new Set(keepOf(g));
    if (g.class !== 'aehnlich') { k.clear(); k.add(id); } // one stays
    else if (k.has(id)) k.delete(id); else k.add(id);
    D.keep.set(g.id, k);
    return true;
  }

  // ---------------------------------------------------------------------------
  // the list
  // ---------------------------------------------------------------------------
  let pollT = 0, listRedraw = null;
  SK.VIEWS.duplikate = async (view) => {
    clearTimeout(pollT);
    if (!admin()) {
      view.innerHTML = `<div class="empty">${icon('dupes')}<h2>${esc(t('Duplikate'))}</h2><p>${esc(t('Nur für Admins.'))}</p></div>`;
      return;
    }
    if (!D.loaded) view.innerHTML = `<div class="head"><div><h1>${esc(t('Duplikate'))}</h1><div class="sub">${esc(t('Wird geladen …'))}</div></div></div>${SK.loading()}`;
    if (!(await load())) { $('.sub', view) && ($('.sub', view).textContent = t('Nicht erreichbar')); return; }
    draw(view);
  };

  function draw(view) {
    const st = D.status || { counts: {} };
    const c = st.counts || {};
    const total = (c.identisch || 0) + (c.gleich || 0) + (c.aehnlich || 0);
    const scanning = st.mode === 'on' && (st.running || st.done < st.todo);
    const list = visible();
    const sure = D.groups.filter((g) => g.class !== 'aehnlich' && (D.filter === 'all' || D.filter === g.class));
    const sureFreed = sure.reduce((s, g) => s + freedOf(g, bulkKeep(g)), 0);
    const sureFiles = sure.reduce((s, g) => s + g.items.filter((id) => !bulkKeep(g).has(id)).length, 0);
    const chip = (k, label, n) => `<button class="chip dp-chip ${D.filter === k ? 'on' : ''}" data-f="${k}">${esc(t(label))}${n !== undefined ? ` <em>${num(n)}</em>` : ''}</button>`;
    view.innerHTML = `
      <div class="head"><div><h1>${esc(t('Duplikate'))}</h1>
        <div class="sub">${st.mode === 'off' ? esc(t('Die Suche nach Duplikaten ist ausgeschaltet (Einstellungen → Fotos).'))
          : total ? esc(t('{n} Gruppen · bis zu {s} frei', { n: num(total), s: size(st.save) })) : esc(t('Keine Duplikate gefunden'))}</div></div>
        <div class="tools">${list.length ? `<button class="btn primary" data-round title="${esc(t('Gruppe für Gruppe im Vollbild entscheiden'))}">${icon('dupes')}<span>${esc(t('Schnell prüfen'))}</span></button>` : ''}<button class="btn dp-rescan" data-rescan title="${esc(t('Alles neu vergleichen'))}">${icon('refresh')}<span>${esc(t('Neu prüfen'))}</span></button></div></div>
      ${scanning ? `<div class="dp-scan">${SK.ring(st.todo ? st.done / st.todo : 0)}<span>${esc(t('Fotos werden verglichen'))} · ${num(st.done)} / ${num(st.todo)}</span></div>` : ''}
      <div class="dp-chips">${chip('all', 'Alle', total)}${chip('identisch', 'Identisch', c.identisch || 0)}${chip('gleich', 'Gleich', c.gleich || 0)}${chip('aehnlich', 'Ähnlich', c.aehnlich || 0)}</div>
      ${D.filter !== 'all' ? `<p class="dp-hint">${esc(t(CLASS_HINT[D.filter]))}</p>` : ''}
      ${sure.length > 1 ? `<div class="dp-bulk">${icon('spark')}<div class="grow"><b>${esc(t('Alle Vorschläge übernehmen'))}</b>
        <span>${esc(t('{g} Gruppen (identisch und gleich): {f} Kopien in den Papierkorb, {s} frei. Die beste Kopie bleibt jeweils.', { g: num(sure.length), f: num(sureFiles), s: size(sureFreed) }))}</span></div>
        <button class="btn primary" data-bulk>${esc(t('Übernehmen'))}</button></div>` : ''}
      ${list.length ? `<div class="dp-list">${list.map((g, i) => card(g, i)).join('')}</div>`
        : `<div class="empty">${icon('dupes')}<h2>${esc(scanning ? t('Wird noch verglichen …') : t('Alles aufgeräumt'))}</h2><p>${esc(scanning ? t('Neue Gruppen erscheinen hier, sobald der Vergleich durch ist.') : t('Hier erscheinen doppelte Fotos und Videos, sobald welche auftauchen.'))}</p></div>`}`;
    for (const b of $$('[data-f]', view)) b.onclick = () => { D.filter = b.dataset.f; draw(view); };
    $('[data-rescan]', view).onclick = async () => {
      if (await ui.call('/api/admin/dupes/rescan', {})) { toast(t('Wird neu verglichen')); setTimeout(() => SK.VIEWS.duplikate(view), 1500); }
    };
    $('[data-bulk]', view)?.addEventListener('click', async () => {
      const ok = await ui.confirmDlg(t('{n} Gruppen bereinigen?', { n: num(sure.length) }),
        t('{f} Kopien kommen in den Papierkorb ({s}); pro Gruppe bleibt die vorgeschlagene beste Kopie. Favoriten, Namen, Ort und Notizen gehen auf sie über. Rückgängig im Papierkorb.', { f: num(sureFiles), s: size(sureFreed) }),
        t('Bereinigen'), true);
      if (!ok) return;
      const b = $('[data-bulk]', view);
      if (b) b.disabled = true;
      await later(() => apply(sure, { bulk: true }));
      draw(view);
    });
    $('[data-round]', view)?.addEventListener('click', () => fullView(roundIds(), 0, { round: true }));
    $('.dp-list', view)?.addEventListener('click', (e) => {
      const c = e.target.closest('[data-g]');
      if (c) compare(list, +c.dataset.g, view);
    });
    redraws.delete(listRedraw);
    listRedraw = () => { if (view.isConnected && !cmp && !full && SK.route().view === 'duplikate') draw(view); };
    redraws.add(listRedraw);
    if (scanning) pollT = setTimeout(() => { if (SK.route().view === 'duplikate') SK.VIEWS.duplikate(view); }, 4000);
  }

  function card(g, i) {
    const k = keepOf(g);
    const shown = g.items.slice(0, 4);
    const more = g.items.length - shown.length;
    const first = m(g.items[0]);
    return `<button class="dp-group" data-g="${i}" title="${esc(t(CLASS_HINT[g.class]))}">
      <span class="dp-strip">${shown.map((id) => `<span class="dp-th${k.has(id) ? ' keep' : ''}"><img loading="lazy" alt="" src="${thumb(id)}">${k.has(id) ? `<i class="dp-star">${icon('star', 'fill')}</i>` : ''}${m(id).kind === 2 ? `<span class="badge br">${icon('play', 'fill')}</span>` : ''}</span>`).join('')}
        ${more > 0 ? `<span class="dp-more">+${more}</span>` : ''}</span>
      <span class="dp-line"><em class="dp-cls ${g.class}">${esc(t(CLASS[g.class]))}</em>${esc(t('{n} Kopien', { n: num(g.items.length) }))}
        <span class="grow"></span><b>${g.class === 'aehnlich' ? esc(t('Serie')) : `−${esc(size(freedOf(g)))}`}</b></span>
      <span class="dp-line muted">${first.taken ? esc(SK.fmtDay(first.taken)) : ''}${first.album ? ` · ${esc(first.album)}` : ''}</span></button>`;
  }

  // ---------------------------------------------------------------------------
  // compare one group
  // ---------------------------------------------------------------------------
  let cmp = null;
  function compare(list, idx, view) {
    cmp?.close();
    const el = document.createElement('div');
    el.className = 'dp-cmp';
    el.setAttribute('role', 'dialog');
    el.setAttribute('aria-modal', 'true');
    document.body.appendChild(el);
    document.body.classList.add('locked');
    let i = idx;
    const close = () => {
      el.remove();
      document.body.classList.remove('locked');
      removeEventListener('keydown', onKey, true);
      redraws.delete(render);
      cmp = null;
      if (view.isConnected) draw(view);
    };
    SK.layer(el, close);
    const next = (d) => {
      const live = visible();
      if (!live.length) { close(); return; }
      i = Math.max(0, Math.min(live.length - 1, i + d));
      list = live;
      render();
    };
    const render = () => {
      if (full) return; // drawn when the full screen view closes
      list = visible();
      if (!list.length) { close(); return; }
      i = Math.min(i, list.length - 1);
      const g = list[i];
      const k = keepOf(g);
      const single = g.class !== 'aehnlich';
      const ms = g.items.map(m);
      // rows: label, value per member, which value is best
      const rows = [
        [t('Auflösung'), (x) => (x.w ? `${x.w} × ${x.h}` : '–'), (x) => (x.w || 0) * (x.h || 0)],
        [t('Format'), (x) => x.ext || '–', (x) => ({ HEIC: 4, HEIF: 4, AVIF: 4, PNG: 3, TIF: 3, TIFF: 3, DNG: 5, CR2: 5, NEF: 5, ARW: 5, JPG: 2, JPEG: 2, MOV: 3 }[x.ext] || 1)],
        [t('Größe'), (x) => size(x.size), (x) => x.size || 0],
        [t('Aufnahme'), (x) => (x.taken ? `${SK.fmtDay(x.taken)} ${SK.fmtTime(x.taken)}` : '–'), null],
        [t('Datum aus'), (x) => t(SRC[x.takenSrc] || x.takenSrc || '–'), (x) => ({ exif: 3, edit: 3, name: 2, folder: 1 }[x.takenSrc] || 0)],
        [t('Ort'), (x) => x.place || (x.gps ? t('nur Position') : '–'), (x) => (x.place ? 1 : 0) + (x.gps ? 1 : 0)],
        [t('Ordner'), (x) => x.album || '–', null],
        [t('Datei'), (x) => x.name || '–', null],
        [t('Kamera'), (x) => x.camera || '–', null],
        ...(ms.some((x) => x.kind === 2) ? [[t('Dauer'), (x) => (x.duration ? `${Math.round(x.duration)} s` : '–'), null]] : []),
        ...(ms.some((x) => x.favs) ? [[t('Favorit bei'), (x) => (x.favs ? t('{n} Konten', { n: x.favs }) : '–'), (x) => x.favs]] : []),
        ...(ms.some((x) => x.named) ? [[t('Benannte Personen'), (x) => String(x.named || '–'), (x) => x.named]] : []),
        ...(ms.some((x) => x.edited?.length) ? [[t('Von Hand geändert'), (x) => (x.edited?.length ? x.edited.join(', ') : '–'), null]] : []),
        ...(ms.some((x) => x.live) ? [[t('Live Photo'), (x) => (x.live ? t('ja') : '–'), (x) => (x.live ? 1 : 0)]] : []),
      ];
      const cell = (row, x) => {
        const [, val, score] = row;
        const vals = ms.map(val);
        const differs = new Set(vals).size > 1;
        let cls = differs ? 'diff' : '';
        if (differs && score) {
          const best = Math.max(...ms.map(score));
          if (score(x) === best && ms.filter((y) => score(y) === best).length < ms.length) cls += ' best';
        }
        return `<tr class="${cls}"><th>${esc(row[0])}</th><td>${esc(val(x))}</td></tr>`;
      };
      const removeN = removeOf(g).length;
      // the same group drawn anew (a copy kept or not): the cards stay where they were scrolled to
      const was = $('.dp-cards', el);
      const scroll = was && was.dataset.g === g.id ? [was.scrollLeft, was.scrollTop] : null;
      el.innerHTML = `
        <header class="dp-top">
          <button class="btn ghost icon" data-close title="${esc(t('Schließen (Esc)'))}">${icon('x')}</button>
          <div class="grow"><b><em class="dp-cls ${g.class}">${esc(t(CLASS[g.class]))}</em> ${esc(t('{n} Kopien', { n: num(g.items.length) }))}</b>
            <small>${esc(t(CLASS_HINT[g.class]))}</small></div>
          ${SK.Busy.mark()}<span class="dp-pos">${num(i + 1)} / ${num(list.length)}</span>
          <button class="btn ghost icon" data-prev ${i === 0 ? 'disabled' : ''} title="${esc(t('Vorige Gruppe (←)'))}">${icon('left')}</button>
          <button class="btn ghost icon" data-next ${i >= list.length - 1 ? 'disabled' : ''} title="${esc(t('Nächste Gruppe (→)'))}">${icon('right')}</button>
        </header>
        <p class="dp-how">${esc(single ? t('Tippe auf „Behalten“ bei der Kopie, die bleiben soll – vorgeschlagen ist die beste.') : t('Behalte so viele, wie du willst; der Rest kommt in den Papierkorb.'))}</p>
        <div class="dp-cards" data-g="${esc(g.id)}">${g.items.map((id, n) => {
          const x = m(id), kept = k.has(id);
          const why = g.reasons?.[id];
          return `<article class="dp-card ${kept ? 'keep' : 'drop'}" data-id="${esc(id)}">
            <button class="dp-pic" data-zoom title="${esc(t('Im Vollbild vergleichen'))}"><img alt="" src="${large(id)}" loading="lazy">
              ${x.kind === 2 ? `<span class="badge br">${icon('play', 'fill')}</span>` : ''}<span class="dp-num">${n + 1}</span></button>
            <button class="btn dp-keep ${kept ? 'on' : ''}" data-keep aria-pressed="${kept}">${icon('star', kept ? 'fill' : '')}<span>${esc(kept ? t('Behalten') : t('Kommt weg'))}</span></button>
            ${why ? `<p class="dp-why">${icon('spark')}${esc(t('Vorschlag: {w}', { w: why.map((w) => t(w)).join(', ') }))}</p>` : ''}
            <table class="dp-meta">${rows.map((r) => cell(r, x)).join('')}</table>
          </article>`;
        }).join('')}</div>
        <footer class="dp-foot">
          <span class="dp-sum">${esc(sumOf(g))}</span>
          <span class="grow"></span>
          <button class="btn" data-ignore title="${esc(t('Diese Fotos nicht mehr als Duplikate zeigen (I)'))}">${esc(t('Keine Duplikate'))}</button>
          <button class="btn" data-skip title="${esc(t('Später entscheiden (→)'))}">${esc(t('Später'))}</button>
          <button class="btn primary" data-apply ${!removeN ? 'disabled' : ''} title="${esc(t('Übernehmen (Enter)'))}">${icon('trash')}<span>${esc(removeN ? t('{n} löschen', { n: num(removeN) }) : t('Übernehmen'))}</span></button>
        </footer>`;
      if (scroll) { const c = $('.dp-cards', el); c.scrollLeft = scroll[0]; c.scrollTop = scroll[1]; }
      $('[data-close]', el).onclick = close;
      $('[data-prev]', el).onclick = () => next(-1);
      $('[data-next]', el).onclick = () => next(1);
      $('[data-skip]', el).onclick = () => (i >= list.length - 1 ? close() : next(1));
      $('[data-ignore]', el).onclick = () => { if (ignoreSoon(g)) render(); };
      // the next group at once, the files move in the background
      $('[data-apply]', el).onclick = () => { if (resolveSoon(g)) render(); };
      for (const c of $$('.dp-card', el)) {
        const id = c.dataset.id;
        $('[data-keep]', c).onclick = () => { if (toggleKeep(g, id)) render(); };
        // full screen goes on through the following groups; back here on the group it was on
        $('[data-zoom]', c).onclick = () => fullView(list.map((x) => x.id), i, {
          member: Math.max(0, g.items.indexOf(id)),
          onClose: (rest) => {
            const live = visible();
            const at = rest.map((gid) => live.findIndex((x) => x.id === gid)).find((n) => n >= 0);
            if (at !== undefined) i = at;
            render();
          },
        });
      }
    };
    redraws.add(render);
    const onKey = (e) => {
      if (full || SK.Viewer.open_ || $('.adm-dlg') || SK.isTyping(e.target)) return;
      const g = list[i];
      if (e.key === 'Escape') close();
      else if (e.key === 'ArrowRight') next(1);
      else if (e.key === 'ArrowLeft') next(-1);
      else if (e.key === 'Enter') { if (!$('[data-apply]', el)?.disabled) $('[data-apply]', el).click(); }
      else if (e.key === 'i' || e.key === 'I') $('[data-ignore]', el).click();
      else if (/^[1-9]$/.test(e.key) && g && g.items[+e.key - 1]) { if (toggleKeep(g, g.items[+e.key - 1])) render(); }
      else return;
      e.preventDefault();
      e.stopPropagation();
    };
    addEventListener('keydown', onKey, true);
    render();
    cmp = { close };
  }

  // ---------------------------------------------------------------------------
  // full screen: one copy at a time, as big as it goes; a tap shows the next copy in
  // the same place, without a transition, so differences jump out. It goes through the
  // groups ids: a quick round (round), or the groups from a comparison on.
  // ---------------------------------------------------------------------------
  let full = null;
  function fullView(ids, at, { round = false, member = 0, onClose = null } = {}) {
    full?.close();
    const el = document.createElement('div');
    el.className = 'quiz dp-full';
    el.setAttribute('role', 'dialog');
    el.setAttribute('aria-modal', 'true');
    document.body.appendChild(el);
    document.body.classList.add('locked');
    let pos = at, mi = member, shown = null, ended = false;
    // The badge says what happens to the copy shown: kept alone ("Nur dieses behalten"), kept
    // with others ("Behalten") or deleted ("Kommt weg"). A tap goes keep, keep only this one,
    // delete and keep again: a deleted copy joins the ones kept (one kept alone before is then
    // kept with it), "keep only this one" deletes the others and remembers them, keeping this
    // copy again after "delete" brings them back. A copy kept alone goes to delete and back.
    let mem = null; // { g, id, others }: what "keep only this one" put aside
    const markOf = (g, id) => {
      const k = keepOf(g);
      if (!k.has(id)) return 'drop';
      return k.size === 1 && g.items.length > 1 ? 'only' : 'keep';
    };
    const cycle = (g, id) => {
      const k = keepOf(g), st = markOf(g, id);
      if (mem && (mem.g !== g.id || mem.id !== id)) mem = null; // another copy decided meanwhile
      if (st === 'keep') { mem = { g: g.id, id, others: [...k].filter((x) => x !== id) }; D.keep.set(g.id, new Set([id])); }
      else if (st === 'only') { const n = new Set(k); n.delete(id); D.keep.set(g.id, n); }
      else { D.keep.set(g.id, new Set([...k, ...(mem?.others || []), id])); mem = null; }
    };
    // what was decided this round: group id -> { what: apply, ignore or later, freed }; a group
    // whose decision failed in the background is in the list again and does not count
    const decided = new Map();
    const stats = () => {
      const s = { done: 0, freed: 0, ignored: 0, later: 0 };
      for (const [id, d] of decided) {
        if (d.what === 'later') s.later++;
        else if (D.groups.some((g) => g.id === id)) continue;
        else if (d.what === 'apply') { s.done++; s.freed += d.freed; } else s.ignored++;
      }
      return s;
    };
    const group = () => D.groups.find((g) => g.id === ids[pos]);
    const close = () => {
      el.remove();
      removeEventListener('keydown', onKey, true);
      watch.disconnect();
      redraws.delete(render);
      full = null;
      if (!cmp) document.body.classList.remove('locked');
      if (onClose) onClose(ids.slice(pos)); // the group shown and the ones after it
      else redraw();
    };
    SK.layer(el, close);
    // the next group still there (from pos on, d: +1 or -1); none: the end
    const step = (d) => {
      for (let p = pos + d; p >= 0 && p < ids.length; p += d) {
        if (D.groups.some((g) => g.id === ids[p])) { pos = p; mi = 0; render(); return; }
      }
      if (d > 0) end();
    };
    const end = () => {
      if (!round) { close(); return; }
      ended = true;
      shown = null; // the pictures are gone
      const left = visible().length, st = stats();
      el.innerHTML = `<div class="qz-top"><b>${esc(t('Duplikate schnell prüfen'))}</b><span class="qz-prog"></span>${SK.Busy.mark()}
          <button class="btn ghost icon qz-x" data-x title="${esc(t('Schließen (Esc)'))}">${icon('x')}</button></div>
        <div class="qz-body"><div class="qz-end">${icon('check')}<h2>${esc(t('Runde fertig'))}</h2>
          <p>${esc([st.done && (st.done === 1 ? t('1 Gruppe bereinigt · {s} frei', { s: size(st.freed) }) : t('{n} Gruppen bereinigt · {s} frei', { n: num(st.done), s: size(st.freed) })), st.ignored && t('{n} keine Duplikate', { n: num(st.ignored) }), st.later && t('{n} für später', { n: num(st.later) })].filter(Boolean).join(' · ') || t('Nichts entschieden'))}</p>
          <div class="qz-endacts">${left ? `<button type="button" class="primary" data-again>${icon('dupes')}<span>${esc(t('Noch eine Runde ({n})', { n: num(left) }))}</span></button>` : ''}
            <button type="button" data-x>${esc(t('Schließen'))}</button></div></div></div>`;
      for (const b of $$('[data-x]', el)) b.onclick = close;
      $('[data-again]', el)?.addEventListener('click', () => {
        ids = roundIds();
        pos = 0; mi = 0; ended = false; shown = null;
        decided.clear();
        if (ids.length) render(); else end();
      });
    };
    const decide = (what) => {
      const g = group();
      if (!g) return;
      const freed = freedOf(g);
      if (what === 'apply' && !resolveSoon(g)) return;
      if (what === 'ignore') ignoreSoon(g);
      decided.set(g.id, { what, freed });
      if (round && what === 'later') D.later.set(g.id, ++laterN);
      step(1);
    };
    const render = () => {
      if (ended) { end(); return; } // a decision failed meanwhile: the summary anew
      const g = group();
      if (!g) { step(1); return; }
      document.body.classList.add('locked'); // the viewer, closing over it, unlocks
      mi = Math.min(mi, g.items.length - 1);
      const k = keepOf(g), id = g.items[mi], x = m(id), kept = k.has(id), removeN = removeOf(g).length, st = markOf(g, id);
      const ms = g.items.map(m);
      const best = (f) => { const v = ms.map(f), top = Math.max(...v); return new Set(v).size > 1 && f(x) === top; };
      const it = S.byId.get(id);
      const when = x.taken ? `${SK.fmtDay(x.taken)} ${SK.fmtTime(x.taken)}` : '';
      // the pictures stay when only the copy shown changes: switching is instant
      if (shown !== g.id) {
        shown = g.id;
        el.innerHTML = `<div class="qz-top"><b>${esc(round ? t('Duplikate schnell prüfen') : t('Vergleichen'))}</b><span class="qz-prog"></span>${SK.Busy.mark()}
            <button class="btn ghost icon qz-x" data-x title="${esc(t('Schließen (Esc)'))}">${icon('x')}</button></div>
          <div class="dp-stage" title="${esc(t('Tippen: nächste Kopie (Leertaste)'))}">${g.items.map((y) => `<img alt="" draggable="false" src="${large(y)}" data-id="${esc(y)}">`).join('')}
            <div class="dp-info"><div class="grow"><b></b><small></small></div>
              <button type="button" class="dp-open" data-open title="${esc(t('Im Betrachter öffnen, z. B. zum Drehen (O)'))}">${icon('expand')}</button></div>
            <div class="dp-sfoot"><span class="dp-tag"><em class="dp-cls ${g.class}">${esc(t(CLASS[g.class]))}</em><b></b></span>
              <button type="button" class="dp-mark" data-mark></button></div></div>
          <div class="dp-thumbs">${g.items.map((y, n) => `<button type="button" data-m="${n}" title="${esc(t('Kopie {n}', { n: n + 1 }))} (${n + 1})"><img alt="" src="${thumb(y)}"><i></i></button>`).join('')}</div>
          <div class="dp-qacts"><span class="dp-sum"></span>
            <div class="dp-qbtns"><button type="button" data-ignore title="${esc(t('Diese Fotos nicht mehr als Duplikate zeigen (I)'))}">${esc(t('Keine Duplikate'))}</button>
            <button type="button" data-later title="${esc(t('Später entscheiden (→)'))}">${esc(t('Später'))}</button>
            <button type="button" class="primary" data-apply title="${esc(t('Übernehmen (Enter)'))}"></button></div></div>`;
        $('[data-x]', el).onclick = close;
        // a tap on the picture (not on its buttons, whose content is drawn anew meanwhile)
        $('.dp-stage', el).addEventListener('click', (e) => {
          if (!e.composedPath().every((n) => n.tagName !== 'BUTTON')) return;
          mi = (mi + 1) % group().items.length;
          render();
        });
        $('[data-mark]', el).onclick = () => { const gg = group(); if (gg) { cycle(gg, gg.items[mi]); render(); } };
        $('[data-open]', el).onclick = () => openViewer();
        $('.dp-thumbs', el).onclick = (e) => { const b = e.target.closest('[data-m]'); if (b) { mi = +b.dataset.m; render(); } };
        $('[data-ignore]', el).onclick = () => decide('ignore');
        $('[data-later]', el).onclick = () => decide('later');
        $('[data-apply]', el).onclick = () => decide('apply');
      }
      $('.qz-prog', el).textContent = ids.length > 1 ? `${num(Math.min(pos + 1, ids.length))} / ${num(ids.length)}` : '';
      for (const img of $$('.dp-stage img', el)) {
        img.hidden = img.dataset.id !== id;
        if (it && img.dataset.id === id && img.getAttribute('src') !== large(id)) img.src = large(id); // turned meanwhile
      }
      const stage = $('.dp-stage', el);
      stage.classList.toggle('keep', kept);
      stage.classList.toggle('drop', !kept);
      $('.dp-tag b', el).textContent = `${mi + 1} / ${g.items.length}`;
      const mark = $('[data-mark]', el);
      mark.className = `dp-mark ${kept ? 'keep' : 'drop'}`;
      mark.innerHTML = `${icon(kept ? 'star' : 'trash', kept ? 'fill' : '')}<span>${esc({ keep: t('Behalten'), drop: t('Kommt weg'), only: t('Nur dieses behalten') }[st])}</span>`;
      mark.title = `${{ keep: t('Behalten – tippen: nur dieses behalten'), drop: t('Kommt weg – tippen: behalten'), only: t('Nur dieses behalten – tippen: kommt weg') }[st]} (K)`;
      mark.setAttribute('aria-pressed', String(kept));
      $('.dp-info b', el).innerHTML = [
        `<span class="${best((y) => (y.w || 0) * (y.h || 0)) ? 'best' : ''}">${esc(x.w ? `${x.w} × ${x.h}` : '–')}</span>`,
        esc(x.ext || ''),
        `<span class="${best((y) => y.size || 0) ? 'best' : ''}">${esc(size(x.size))}</span>`,
        `<span class="dp-gps ${x.gps || x.place ? 'on' : ''}" title="${esc(x.gps || x.place ? t('Mit Ortsangabe') : t('Ohne Ortsangabe'))}">${icon(x.gps || x.place ? 'pin' : 'pin-off')}</span>`,
      ].filter(Boolean).join('<i>·</i>');
      $('.dp-info small', el).textContent = [when, x.album, x.name].filter(Boolean).join(' · ');
      $$('.dp-thumbs [data-m]', el).forEach((b, n) => {
        b.classList.toggle('on', n === mi);
        b.classList.toggle('keep', k.has(g.items[n]));
        b.querySelector('i').innerHTML = icon(k.has(g.items[n]) ? 'star' : 'trash', k.has(g.items[n]) ? 'fill' : '');
      });
      $('.dp-sum', el).textContent = sumOf(g);
      const ap = $('[data-apply]', el);
      ap.disabled = !removeN;
      ap.innerHTML = `${icon('trash')}<span>${esc(removeN ? t('{n} löschen', { n: num(removeN) }) : t('Übernehmen'))}</span>`;
    };
    const openViewer = () => {
      const g = group();
      if (!g) return;
      const items = g.items.map((y) => S.byId.get(y)).filter(Boolean);
      const at2 = items.findIndex((y) => y.id === g.items[mi]);
      if (items.length) SK.Viewer.open(items, Math.max(0, at2));
    };
    // back from the viewer (turned a copy?): the pictures anew
    const watch = new MutationObserver(() => { if (!SK.Viewer.open_) { shown = null; render(); } });
    if (SK.Viewer.el) watch.observe(SK.Viewer.el, { attributes: true, attributeFilter: ['hidden'] });
    const onKey = (e) => {
      if (SK.Viewer.open_ || $('.adm-dlg') || $('.adm-menu') || SK.isTyping(e.target)) return;
      const g = group();
      if (e.key === 'Escape') close();
      else if (ended) return;
      else if (e.key === ' ' && g) { mi = (mi + (e.shiftKey ? g.items.length - 1 : 1)) % g.items.length; render(); }
      else if ((e.key === 'k' || e.key === 'K') && g) { cycle(g, g.items[mi]); render(); }
      else if (/^[1-9]$/.test(e.key) && g && g.items[+e.key - 1]) { mi = +e.key - 1; render(); }
      else if (e.key === 'Enter') { if (!$('[data-apply]', el)?.disabled) decide('apply'); }
      else if (e.key === 'i' || e.key === 'I') decide('ignore');
      else if (e.key === 'ArrowRight') decide('later');
      else if (e.key === 'ArrowLeft') step(-1);
      else if (e.key === 'o' || e.key === 'O') openViewer();
      else return;
      e.preventDefault();
      e.stopPropagation();
    };
    addEventListener('keydown', onKey, true);
    redraws.add(render);
    full = { close };
    if (ids.length) render(); else end();
  }

  // ---------------------------------------------------------------------------
  // sidebar
  // ---------------------------------------------------------------------------
  const anchor = $('#nav a[data-v="papierkorb"]') || $('#nav a[data-v="favoriten"]');
  anchor?.insertAdjacentHTML('afterend',
    `<a href="#/duplikate" data-v="duplikate" data-need="admin">${icon('dupes')}<span>${esc(t('Duplikate'))}</span><em class="count" id="dupeCount"></em></a>`);
  new MutationObserver(() => summary()).observe(document.body, { attributes: true, attributeFilter: ['data-role'] });
  setInterval(() => { if (admin() && !document.hidden && SK.route().view !== 'duplikate') summary(); }, 120000);
})();
