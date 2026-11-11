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
    <symbol id="i-star" viewBox="0 0 24 24"><path d="m12 3.5 2.6 5.3 5.9.9-4.3 4.1 1 5.8L12 16.9l-5.2 2.7 1-5.8-4.3-4.1 5.9-.9z"/></symbol>`);

  // ---------------------------------------------------------------------------
  // data
  // ---------------------------------------------------------------------------
  const D = { groups: [], members: {}, status: null, filter: 'all', loaded: false, keep: new Map() };

  async function load() {
    const r = await api('/api/admin/dupes').catch(() => null);
    if (!r) return false;
    D.groups = r.groups || [];
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
    const r = await api('/api/admin/dupes?summary=1').catch(() => null);
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
  const freedOf = (g) => removeOf(g).reduce((s, id) => s + (D.members[id]?.size || 0), 0);
  const m = (id) => D.members[id] || {};
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
    const todo = groups.map((g) => ({ g, r: { keep: [...keepOf(g)], remove: removeOf(g) } })).filter((x) => x.r.keep.length && x.r.remove.length);
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
          a = await api('/api/admin/dupes/resolve', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ groups: part.map((x) => x.r) }) });
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
      const u = await ui.call('/api/admin/trash/restore', { ids: entries });
      if (!u) return;
      toast(t('Wiederhergestellt'));
      await SK.loadItems();
      await ui.call('/api/admin/dupes/rescan', {});
      await new Promise((r) => setTimeout(r, 1200)); // the server groups again
      for (const g of groups) D.keep.delete(g.id);
      await load();
      if (SK.route().view === 'duplikate') SK.render();
      ui.trashCount();
    }, err ? 20000 : 9000);
    if (r.warnings.length && !err) ui.report(r, t('Mit Hinweisen'));
    SK.loadItems();
    ui.trashCount();
    recount();
    return true;
  }

  async function ignore(g) {
    const r = await ui.call('/api/admin/dupes/ignore', { ids: g.items });
    if (!r) return false;
    D.groups = D.groups.filter((x) => x.id !== g.id);
    recount();
    toast(t('Gemerkt: keine Duplikate'));
    return true;
  }

  // ---------------------------------------------------------------------------
  // the list
  // ---------------------------------------------------------------------------
  let pollT = 0;
  SK.VIEWS.duplikate = async (view) => {
    clearTimeout(pollT);
    if (!admin()) {
      view.innerHTML = `<div class="empty">${icon('dupes')}<h2>${esc(t('Duplikate'))}</h2><p>${esc(t('Nur für Admins.'))}</p></div>`;
      return;
    }
    if (!D.loaded) view.innerHTML = `<div class="head"><div><h1>${esc(t('Duplikate'))}</h1><div class="sub">${esc(t('Wird geladen …'))}</div></div></div>`;
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
    const sureFreed = sure.reduce((s, g) => s + freedOf(g), 0);
    const sureFiles = sure.reduce((s, g) => s + removeOf(g).length, 0);
    const chip = (k, label, n) => `<button class="chip dp-chip ${D.filter === k ? 'on' : ''}" data-f="${k}">${esc(t(label))}${n !== undefined ? ` <em>${num(n)}</em>` : ''}</button>`;
    view.innerHTML = `
      <div class="head"><div><h1>${esc(t('Duplikate'))}</h1>
        <div class="sub">${st.mode === 'off' ? esc(t('Die Suche nach Duplikaten ist ausgeschaltet (Einstellungen → Fotos).'))
          : total ? esc(t('{n} Gruppen · bis zu {s} frei', { n: num(total), s: size(st.save) })) : esc(t('Keine Duplikate gefunden'))}</div></div>
        <div class="tools"><button class="btn" data-rescan title="${esc(t('Alles neu vergleichen'))}">${icon('refresh')}<span>${esc(t('Neu prüfen'))}</span></button></div></div>
      ${scanning ? `<div class="dp-scan"><span>${esc(t('Fotos werden verglichen'))} · ${num(st.done)} / ${num(st.todo)}</span><span class="bar"><i style="width:${st.todo ? (st.done / st.todo) * 100 : 5}%"></i></span></div>` : ''}
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
      await apply(sure, { bulk: true });
      draw(view);
    });
    $('.dp-list', view)?.addEventListener('click', (e) => {
      const c = e.target.closest('[data-g]');
      if (c) compare(list, +c.dataset.g, view);
    });
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
      cmp = null;
      if (view.isConnected) draw(view);
    };
    const next = (d) => {
      const live = visible();
      if (!live.length) { close(); return; }
      i = Math.max(0, Math.min(live.length - 1, i + d));
      list = live;
      render();
    };
    const render = () => {
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
      el.innerHTML = `
        <header class="dp-top">
          <button class="btn ghost icon" data-close title="${esc(t('Schließen (Esc)'))}">${icon('x')}</button>
          <div class="grow"><b><em class="dp-cls ${g.class}">${esc(t(CLASS[g.class]))}</em> ${esc(t('{n} Kopien', { n: num(g.items.length) }))}</b>
            <small>${esc(t(CLASS_HINT[g.class]))}</small></div>
          <span class="dp-pos">${num(i + 1)} / ${num(list.length)}</span>
          <button class="btn ghost icon" data-prev ${i === 0 ? 'disabled' : ''} title="${esc(t('Vorige Gruppe (←)'))}">${icon('left')}</button>
          <button class="btn ghost icon" data-next ${i >= list.length - 1 ? 'disabled' : ''} title="${esc(t('Nächste Gruppe (→)'))}">${icon('right')}</button>
        </header>
        <p class="dp-how">${esc(single ? t('Tippe auf „Behalten“ bei der Kopie, die bleiben soll – vorgeschlagen ist die beste.') : t('Behalte so viele, wie du willst; der Rest kommt in den Papierkorb.'))}</p>
        <div class="dp-cards">${g.items.map((id, n) => {
          const x = m(id), kept = k.has(id);
          const why = g.reasons?.[id];
          return `<article class="dp-card ${kept ? 'keep' : 'drop'}" data-id="${esc(id)}">
            <button class="dp-pic" data-zoom title="${esc(t('Groß ansehen'))}"><img alt="" src="${large(id)}" loading="lazy">
              ${x.kind === 2 ? `<span class="badge br">${icon('play', 'fill')}</span>` : ''}<span class="dp-num">${n + 1}</span></button>
            <button class="btn dp-keep ${kept ? 'on' : ''}" data-keep aria-pressed="${kept}">${icon('star', kept ? 'fill' : '')}<span>${esc(kept ? t('Behalten') : t('Kommt weg'))}</span></button>
            ${why ? `<p class="dp-why">${icon('spark')}${esc(t('Vorschlag: {w}', { w: why.map((w) => t(w)).join(', ') }))}</p>` : ''}
            <table class="dp-meta">${rows.map((r) => cell(r, x)).join('')}</table>
          </article>`;
        }).join('')}</div>
        <footer class="dp-foot">
          <span class="dp-sum">${esc(k.size ? t('{k} bleibt · {r} in den Papierkorb ({s})', { k: num(k.size), r: num(removeN), s: size(freedOf(g)) }) : t('Mindestens eine Kopie muss bleiben'))}</span>
          <span class="grow"></span>
          <button class="btn" data-ignore title="${esc(t('Diese Fotos nicht mehr als Duplikate zeigen (I)'))}">${esc(t('Keine Duplikate'))}</button>
          <button class="btn" data-skip title="${esc(t('Später entscheiden (→)'))}">${esc(t('Später'))}</button>
          <button class="btn primary" data-apply ${!k.size || !removeN ? 'disabled' : ''} title="${esc(t('Übernehmen (Enter)'))}">${icon('trash')}<span>${esc(removeN ? t('{n} löschen', { n: num(removeN) }) : t('Übernehmen'))}</span></button>
        </footer>`;
      $('[data-close]', el).onclick = close;
      $('[data-prev]', el).onclick = () => next(-1);
      $('[data-next]', el).onclick = () => next(1);
      $('[data-skip]', el).onclick = () => (i >= list.length - 1 ? close() : next(1));
      $('[data-ignore]', el).onclick = async () => { if (await ignore(g)) render(); };
      $('[data-apply]', el).onclick = async () => {
        // at once something to see: moving files on a share takes a moment
        const b = $('[data-apply]', el);
        el.classList.add('dp-busy');
        for (const x of $$('.dp-foot button, [data-keep], [data-prev], [data-next]', el)) x.disabled = true;
        b.innerHTML = `<span class="dp-spin"></span><span>${esc(t('Wird gelöscht …'))}</span>`;
        const ok = await apply([g]);
        el.classList.remove('dp-busy');
        render(); // the next group, or this one again after an error
        return ok;
      };
      for (const c of $$('.dp-card', el)) {
        const id = c.dataset.id;
        $('[data-keep]', c).onclick = () => toggleKeep(g, id);
        $('[data-zoom]', c).onclick = () => {
          const items = g.items.map((x) => S.byId.get(x)).filter(Boolean);
          const at = items.findIndex((x) => x.id === id);
          if (items.length) SK.Viewer.open(items, Math.max(0, at));
        };
      }
    };
    const toggleKeep = (g, id) => {
      const k = new Set(keepOf(g));
      if (g.class !== 'aehnlich') { k.clear(); k.add(id); } // one stays
      else if (k.has(id)) { if (k.size > 1) k.delete(id); else { toast(t('Mindestens eine Kopie muss bleiben')); return; } } else k.add(id);
      D.keep.set(g.id, k);
      render();
    };
    const onKey = (e) => {
      if (SK.Viewer.open_ || $('.adm-dlg') || SK.isTyping(e.target)) return;
      const g = list[i];
      if (e.key === 'Escape') close();
      else if (e.key === 'ArrowRight') next(1);
      else if (e.key === 'ArrowLeft') next(-1);
      else if (e.key === 'Enter') { if (!$('[data-apply]', el)?.disabled) $('[data-apply]', el).click(); }
      else if (e.key === 'i' || e.key === 'I') $('[data-ignore]', el).click();
      else if (/^[1-9]$/.test(e.key) && g && g.items[+e.key - 1]) toggleKeep(g, g.items[+e.key - 1]);
      else return;
      e.preventDefault();
      e.stopPropagation();
    };
    addEventListener('keydown', onKey, true);
    render();
    cmp = { close };
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
