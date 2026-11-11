/* Schuhkarton — single page app (no build step) */
'use strict';
(() => {
  // ---------------------------------------------------------------------------
  // helpers
  // ---------------------------------------------------------------------------
  const $ = (s, el = document) => el.querySelector(s);
  const $$ = (s, el = document) => [...el.querySelectorAll(s)];
  const esc = (s) => String(s ?? '').replace(/[&<>"']/g, (c) => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c]));
  const icon = (name, cls = '') => `<svg class="i ${cls}"><use href="#i-${name}"/></svg>`;
  const store = {
    get(k, d) { try { const v = localStorage.getItem('schuhkarton-' + k); return v === null ? d : JSON.parse(v); } catch { return d; } },
    set(k, v) { try { localStorage.setItem('schuhkarton-' + k, JSON.stringify(v)); } catch { /* private mode */ } },
  };
  const debounce = (fn, ms) => { let tm; return (...a) => { clearTimeout(tm); tm = setTimeout(() => fn(...a), ms); }; };
  const clamp = (v, a, b) => Math.max(a, Math.min(b, v));
  // language (web/i18n.js): German texts are the keys
  const LANG = window.skLang || 'de';
  const EN = LANG === 'en';
  const t = window.skT || ((s) => s);
  const nf = new Intl.NumberFormat(EN ? 'en-GB' : 'de-DE');
  const num = (n) => nf.format(n);
  // counted text: pl(3, '{n} Foto', '{n} Fotos') → "3 Fotos" / "3 photos"
  const pl = (n, one, many) => t(n === 1 ? one : many, { n: num(n) });

  const MONTHS_DE = ['Januar', 'Februar', 'März', 'April', 'Mai', 'Juni', 'Juli', 'August', 'September', 'Oktober', 'November', 'Dezember'];
  const DAYS_DE = ['Sonntag', 'Montag', 'Dienstag', 'Mittwoch', 'Donnerstag', 'Freitag', 'Samstag'];
  const MONTHS = EN ? ['January', 'February', 'March', 'April', 'May', 'June', 'July', 'August', 'September', 'October', 'November', 'December'] : MONTHS_DE;
  const DAYS = EN ? ['Sunday', 'Monday', 'Tuesday', 'Wednesday', 'Thursday', 'Friday', 'Saturday'] : DAYS_DE;
  const dt = (ts) => new Date(ts * 1000); // capture times are wall-clock values stored as UTC
  const fmtMonth = (ts) => { const d = dt(ts); return `${MONTHS[d.getUTCMonth()]} ${d.getUTCFullYear()}`; };
  const fmtDay = (ts) => {
    const d = dt(ts);
    return EN ? `${DAYS[d.getUTCDay()].slice(0, 3)}, ${d.getUTCDate()} ${MONTHS[d.getUTCMonth()]} ${d.getUTCFullYear()}`
      : `${DAYS[d.getUTCDay()].slice(0, 2)}., ${d.getUTCDate()}. ${MONTHS[d.getUTCMonth()]} ${d.getUTCFullYear()}`;
  };
  const fmtLong = (ts) => {
    const d = dt(ts);
    return EN ? `${DAYS[d.getUTCDay()]}, ${d.getUTCDate()} ${MONTHS[d.getUTCMonth()]} ${d.getUTCFullYear()}`
      : `${DAYS[d.getUTCDay()]}, ${d.getUTCDate()}. ${MONTHS[d.getUTCMonth()]} ${d.getUTCFullYear()}`;
  };
  const fmtTime = (ts) => { const d = dt(ts); return `${String(d.getUTCHours()).padStart(2, '0')}:${String(d.getUTCMinutes()).padStart(2, '0')}`; };
  const fmtDur = (s) => { s = Math.round(s || 0); const m = Math.floor(s / 60); return m >= 60 ? `${Math.floor(m / 60)}:${String(m % 60).padStart(2, '0')}:${String(s % 60).padStart(2, '0')}` : `${m}:${String(s % 60).padStart(2, '0')}`; };
  const fmtSize = (b) => b > 1e9 ? `${(b / 1e9).toFixed(2)} GB` : b > 1e6 ? `${(b / 1e6).toFixed(1)} MB` : `${Math.round(b / 1e3)} KB`;
  const years = (a, b) => { const x = dt(a).getUTCFullYear(), y = dt(b).getUTCFullYear(); return x === y ? `${x}` : `${x}–${y}`; };
  const plural = (n, one, many) => `${num(n)} ${n === 1 ? one : many}`;
  const markSnip = (s) => esc(s).replace(/\x01/g, '<mark>').replace(/\x02/g, '</mark>');

  let toastT;
  // toast(msg) shows a short note; with action and onAction it gets a button (e.g. "Rückgängig").
  // ms: how long it stays (default: longer with a button). The one kind of message everywhere.
  function toast(msg, action, onAction, ms) {
    const el = $('#toast');
    el.innerHTML = `<span>${esc(msg)}</span>${action ? `<button type="button">${esc(action)}</button>` : ''}`;
    el.classList.toggle('act', !!action);
    el.classList.add('on');
    clearTimeout(toastT);
    toastT = setTimeout(() => el.classList.remove('on'), ms || (action ? 10000 : 2400));
    if (action) $('button', el).onclick = () => { el.classList.remove('on'); onAction(); };
  }

  async function api(path, opts) {
    const r = await fetch(path, opts);
    if (!r.ok) throw new Error(`${r.status} ${await r.text()}`);
    return r.json();
  }
  const post = (path, body) => api(path, { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body) });

  // ---------------------------------------------------------------------------
  // state
  // ---------------------------------------------------------------------------
  const S = {
    items: [], byId: new Map(), version: -1, loadedAt: 0,
    people: [], peopleById: new Map(), peopleInfo: {},
    status: null,
    config: {},
    layout: store.get('layout', 'rows'),
    size: 220,
    group: store.get('group', 'month'),
    animate: store.get('animate', false),
    kinds: new Set(['photo', 'video']), // what galleries show: photo, video, doc
    sel: new Set(), selecting: false,
    gallery: null,
  };
  // grid size is remembered per device class
  const devClass = () => (innerWidth <= 720 ? 'phone' : innerWidth <= 1100 ? 'tablet' : 'desktop');
  const DEFAULT_SIZE = { phone: 118, tablet: 170, desktop: 220 };
  S.size = store.get(`size-${devClass()}`, DEFAULT_SIZE[devClass()]);
  const saveSize = () => store.set(`size-${devClass()}`, S.size);
  const canHevc = (() => { const v = document.createElement('video'); return !!(v.canPlayType('video/mp4; codecs="hvc1.1.6.L93.B0"') || v.canPlayType('video/mp4; codecs="hev1.1.6.L93.B0"')); })();
  const isTouch = matchMedia('(hover: none)').matches;
  // keys go to a field that takes text; a slider, a checkbox or a button leaves them to the page
  const isTyping = (el) => !!el.matches?.('textarea, select, [contenteditable], input:not([type=range], [type=checkbox], [type=radio], [type=button], [type=submit], [type=color])');
  // a popup that knows how to place itself moves when the keyboard comes or goes
  window.visualViewport?.addEventListener('resize', () => $('#pop')?.position?.());
  // the grip of a bottom sheet: dragging sets the height (set), end(height, start height, speed)
  // follows on release (speed in px per ms, positive = down)
  const gripDrag = (grip, get, set, end) => {
    let drag = null;
    const viewer = () => grip.closest('.viewer') || grip.parentElement;
    grip.addEventListener('pointerdown', (e) => {
      if (e.button) return;
      e.preventDefault();
      grip.setPointerCapture(e.pointerId);
      drag = { y: e.clientY, h: get(), ly: e.clientY, lt: e.timeStamp, v: 0 };
      viewer()?.classList.add('sheet-drag');
    });
    grip.addEventListener('pointermove', (e) => {
      if (!drag) return;
      const dt = e.timeStamp - drag.lt;
      if (dt > 0) drag.v = (e.clientY - drag.ly) / dt;
      drag.ly = e.clientY;
      drag.lt = e.timeStamp;
      set(drag.h - (e.clientY - drag.y));
    });
    const up = (e) => {
      if (!drag) return;
      const { h, lt } = drag;
      const v = e.timeStamp - lt > 120 ? 0 : drag.v; // held still before letting go: no flick
      drag = null;
      viewer()?.classList.remove('sheet-drag');
      end(get(), h, v);
    };
    grip.addEventListener('pointerup', up);
    grip.addEventListener('pointercancel', up);
  };
  // a sheet dragged this low or flicked down closes
  const sheetGone = (now, h, v) => now < 110 || (v > 1.2 && now < h);
  // a dialog as a bottom sheet on a phone: a grip on top sets any height (remembered per kind
  // of dialog, key), a flick down or dragging it nearly closed calls onClose. The grip only
  // shows on a phone; elsewhere the dialog stays as it is.
  const phoneMQ = matchMedia('(max-width: 720px)');
  function sheetGrip(box, { key, onClose, min = 160 }) {
    const grip = document.createElement('div');
    grip.className = 'sheet-grip';
    grip.title = t('Ziehen: Höhe ändern');
    box.prepend(grip);
    const set = (h) => {
      box.style.height = `${Math.round(clamp(h, 0, innerHeight - 24))}px`;
      box.style.maxHeight = 'none';
    };
    const saved = key ? +store.get(`dlg-h-${key}`, 0) : 0;
    if (saved && phoneMQ.matches) set(Math.max(saved * innerHeight, min));
    gripDrag(grip, () => box.offsetHeight, set, (now, h, v) => {
      if (sheetGone(now, h, v)) { set(h); onClose(); return; }
      const keep = Math.max(now, min);
      set(keep);
      if (key) store.set(`dlg-h-${key}`, Math.round((keep / innerHeight) * 1000) / 1000);
    });
    return grip;
  }
  // menu: a small list of actions next to a button (a phone: a sheet at the bottom). entries:
  // { icon, label, run, danger }, or { head, sub } for a line on top that is not an action
  function menu(anchor, entries) {
    $('.adm-menu')?.remove();
    const m = document.createElement('div');
    m.className = 'adm-menu';
    m.setAttribute('role', 'menu');
    m.innerHTML = entries.map((e, i) => (e.head !== undefined ? `<div class="mh"><b>${esc(e.head)}</b>${e.sub ? `<small>${esc(e.sub)}</small>` : ''}</div>`
      : `<button type="button" role="menuitem" data-i="${i}" class="${e.danger ? 'danger' : ''}">${icon(e.icon)}<span>${esc(e.label)}</span></button>`)).join('');
    document.body.appendChild(m);
    const off = (e) => { if (!m.contains(e.target)) { m.remove(); removeEventListener('pointerdown', off, true); } };
    sheetGrip(m, { onClose: () => { m.remove(); removeEventListener('pointerdown', off, true); }, min: 0 });
    if (!phoneMQ.matches) {
      const r = anchor.getBoundingClientRect();
      const w = m.offsetWidth, h = m.offsetHeight;
      // under the button, right-aligned; a button in the left sidebar: left-aligned
      const left = r.left < innerWidth / 3 ? r.left : r.right - w;
      m.style.left = `${clamp(left, 8, innerWidth - w - 8)}px`;
      m.style.top = `${r.bottom + 6 + h > innerHeight - 8 ? Math.max(8, r.top - h - 6) : r.bottom + 6}px`;
    }
    setTimeout(() => addEventListener('pointerdown', off, true));
    m.onclick = (e) => {
      const b = e.target.closest('[data-i]');
      if (!b) return;
      m.remove();
      removeEventListener('pointerdown', off, true);
      entries[+b.dataset.i].run();
    };
    m.onkeydown = (e) => { if (e.key === 'Escape') { e.stopPropagation(); m.remove(); removeEventListener('pointerdown', off, true); anchor.focus?.(); } };
    $('[data-i]', m)?.focus({ preventScroll: true });
  }

  const thumbURL = (it) => `/t/${it.id}/s.jpg?v=${it.v}`;
  const largeURL = (it) => `/t/${it.id}/m.jpg?v=${it.v}`;
  const previewURL = (it) => (it.k === 2 ? `/t/${it.id}/p.mp4?v=${it.v}` : it.lv ? `/t/${it.lv}/p.mp4?v=${it.lk}` : '');
  const faceURL = (item, idx) => `/f/${item}/${idx}`;

  // Documents (items in folders like "Dokumente", library.documents) are a kind
  // of their own: hidden by default, any mix of photos, videos and documents.
  const KINDS = ['photo', 'video', 'doc'];
  {
    const saved = store.get('kinds', null);
    if (Array.isArray(saved) && saved.some((k) => KINDS.includes(k))) S.kinds = new Set(saved.filter((k) => KINDS.includes(k)));
  }
  const kindOf = (it) => (it.dc ? 'doc' : it.k === 2 ? 'video' : 'photo');
  const shown = (it) => S.kinds.has(kindOf(it));

  async function loadItems() {
    const res = await api('/api/items');
    for (const it of res.items) it.dc = !!it.dc; // the server knows the document folders
    // what this account marked for deletion is hidden everywhere but in "Vorgemerkt"
    S.items = res.items.filter((it) => !it.mk);
    S.marked = res.items.filter((it) => it.mk);
    S.byId = new Map(res.items.map((it) => [it.id, it]));
    S.version = res.version;
    S.loadedAt = Date.now();
    updateCounts();
  }

  // showHidden shows a hidden group again; back opens the person when it exists again
  async function showHidden(hid, back) {
    try {
      await post(`/api/people/hidden/${encodeURIComponent(hid)}/show`, {});
    } catch (e) {
      toast(t('Fehler: {e}', { e: String(e.message || e) }));
      return false;
    }
    await loadPeople().catch(() => {});
    await loadItems().catch(() => {});
    toast(t('Wieder sichtbar'));
    if (back && S.peopleById.has(back)) location.replace(`#/person/${encodeURIComponent(back)}`);
    return true;
  }
  // people and places: cards in an order and a size of their own, remembered per page
  const CARD = { people: { min: 88, max: 240, step: 8, def: 128 }, places: { min: 150, max: 420, step: 10, def: 210 } };
  function cardTools(kind, sorts) {
    const c = CARD[kind], sort = store.get(`sort-${kind}`, sorts[0][0]);
    return `<select class="sel" data-sort title="${t('Sortieren')}">${sorts.map(([k, l]) => `<option value="${k}" ${k === sort ? 'selected' : ''}>${t(l)}</option>`).join('')}</select>
      <label class="zoom" title="${t('Größe')}">${icon(kind === 'people' ? 'people' : 'photos')}<input type="range" min="${c.min}" max="${c.max}" step="${c.step}" value="${store.get(`csize-${kind}`, c.def)}" data-csize aria-label="${t('Größe')}"></label>`;
  }
  // the size goes to the cards in box; a new order draws them again
  function bindCardTools(head, box, kind, redraw) {
    const apply = (v) => box.style.setProperty('--cardw', `${v}px`);
    apply(store.get(`csize-${kind}`, CARD[kind].def));
    $('[data-csize]', head).oninput = (e) => { apply(+e.target.value); store.set(`csize-${kind}`, +e.target.value); };
    $('[data-sort]', head).onchange = (e) => { store.set(`sort-${kind}`, e.target.value); redraw(); };
  }

  // a hidden group with its "Wieder zeigen" button
  const hiddenCard = (h) => `<div class="hperson" data-hid="${esc(h.id)}">
    <span class="face"><img src="${faceURL(h.cover.item, h.cover.idx)}" alt="" loading="lazy"></span>
    <div class="hinfo"><b class="${h.name || h.loose ? '' : 'unnamed'}">${esc(h.loose ? t('Einzelne Gesichter') : h.name || t('Ohne Namen'))}</b>
      <span>${h.loose ? pl(h.faces, '{n} Gesicht', '{n} Gesichter') : pl(h.count, '{n} Foto', '{n} Fotos')} · ${h.at ? t('ausgeblendet am {d}', { d: fmtLong(h.at) }) : t('früher ausgeblendet')}</span>
      <div class="hsample">${h.sample.map((f) => `<img src="${faceURL(f.item, f.idx)}" alt="" loading="lazy">`).join('')}</div></div>
    <button class="btn" type="button" data-show>${icon('eye')}<span>${t('Wieder zeigen')}</span></button></div>`;
  // the buttons of the hidden groups in root; after: when the last one is shown again (each:
  // after every one)
  function bindShowHidden(root, after, each = false) {
    for (const b of $$('[data-show]', root)) {
      b.onclick = async () => {
        const row = b.closest('.hperson');
        b.disabled = true;
        if (await showHidden(row.dataset.hid)) {
          row.remove();
          if (each || !$('.hperson', root)) after();
        } else b.disabled = false;
      };
    }
  }

  async function loadPeople() {
    const res = await api('/api/people');
    S.people = res.people || [];
    S.faceMode = S.config.faces;
    S.peopleById = new Map(S.people.map((p) => [p.id, p]));
    S.peopleInfo = res;
    updateCounts();
    return S.people;
  }

  function updateCounts() {
    const c = { all: S.items.filter(shown).length, videos: 0, favs: 0, gps: 0, places: new Set(), people: S.people.filter((p) => p.name).length || S.people.length };
    for (const it of S.items) {
      if (!shown(it)) continue;
      if (it.k === 2) c.videos++;
      if (it.f) c.favs++;
      if (it.la !== undefined) c.gps++;
      if (it.p) c.places.add(it.p);
    }
    c.places = c.places.size;
    for (const el of $$('[data-c]')) {
      const v = c[el.dataset.c];
      el.textContent = v ? num(v) : '';
    }
  }

  // ---------------------------------------------------------------------------
  // gallery: virtualized justified rows / square grid / masonry
  // ---------------------------------------------------------------------------
  const BUCKET = 1200;

  class Gallery {
    constructor(host, opts) {
      this.host = host;
      this.opts = Object.assign({ group: 'month', snips: null, header: true, small: false }, opts);
      this.el = document.createElement('div');
      this.el.className = 'gallery';
      host.appendChild(this.el);
      this.items = [];
      this.sections = [];
      this.width = 0;
      this.raf = 0;
      this.onScroll = () => { if (!this.raf) this.raf = requestAnimationFrame(() => { this.raf = 0; this.render(); }); };
      this.scrollRoot = this.opts.scrollRoot || window;
      this.scrollRoot.addEventListener('scroll', this.onScroll, { passive: true });
      this.ro = new ResizeObserver(() => {
        const w = this.el.clientWidth;
        if (w && Math.abs(w - this.width) > 1) this.relayout();
      });
      this.ro.observe(this.el);
      this.el.addEventListener('click', (e) => this.click(e));
      this.el.addEventListener('pointerover', (e) => this.hover(e, true));
      this.el.addEventListener('pointerout', (e) => this.hover(e, false));
      this.io = new IntersectionObserver((ents) => this.visibleVideos(ents), { root: this.opts.scrollRoot ? this.opts.scrollRoot : null, threshold: 0.6 });
      this.playing = new Map();
      this.touch();
    }

    // touch: two-finger pinch changes the tile size, long press selects
    touch() {
      const pts = new Map();
      let p0 = null, longT = 0, press = null;
      const el = this.el;
      const cancelLong = () => { clearTimeout(longT); longT = 0; press = null; };
      el.addEventListener('pointerdown', (e) => {
        this.suppressClick = false; // a flag left over from an earlier press must not swallow this tap
        if (e.pointerType !== 'touch') return;
        pts.set(e.pointerId, { x: e.clientX, y: e.clientY });
        if (pts.size > 1) {
          cancelLong();
          if (pts.size === 2 && !this.opts.small) {
            const [a, b] = [...pts.values()];
            p0 = { d: Math.hypot(a.x - b.x, a.y - b.y) || 1, size: S.size };
          }
          return;
        }
        const tile = e.target.closest('.tile');
        if (!tile) return;
        press = { id: e.pointerId, x: e.clientX, y: e.clientY, tile, sy: this.scrollPos(), t: Date.now() };
        longT = setTimeout(() => {
          const p = press;
          longT = 0;
          press = null;
          // only a finger that is still down on the same tile, alone, and the page did not scroll
          if (!p || pts.size !== 1 || !pts.has(p.id) || !p.tile.isConnected || Viewer.open_ || Math.abs(this.scrollPos() - p.sy) > 2) return;
          this.suppressClick = true;
          S.selecting = true;
          const id = p.tile.dataset.id;
          S.sel.has(id) ? S.sel.delete(id) : S.sel.add(id);
          this.lastSel = id;
          navigator.vibrate?.(12);
          selectionChanged();
        }, 550);
      });
      el.addEventListener('pointermove', (e) => {
        if (!pts.has(e.pointerId)) return;
        pts.set(e.pointerId, { x: e.clientX, y: e.clientY });
        if (press && Math.hypot(e.clientX - press.x, e.clientY - press.y) > 10) cancelLong();
        if (p0 && pts.size === 2) {
          const [a, b] = [...pts.values()];
          const ns = clamp(Math.round((p0.size * Math.hypot(a.x - b.x, a.y - b.y)) / p0.d / 10) * 10, 70, 420);
          if (ns !== S.size && !this.pinchRaf) {
            this.pinchRaf = requestAnimationFrame(() => {
              this.pinchRaf = 0;
              const frac = this.height ? this.viewport().top / this.height : 0;
              S.size = ns;
              this.relayout();
              if (frac > 0) scrollTo({ top: this.el.getBoundingClientRect().top + scrollY + frac * this.height });
              const sz = $('[data-size]');
              if (sz) sz.value = ns;
            });
          }
        }
      });
      // releases are watched on the window: the tile under the finger may be gone by then
      const up = (e) => {
        if (!pts.delete(e.pointerId)) return;
        const p = press;
        cancelLong();
        // a short, still tap opens the tile right away: on some phones the click after
        // closing the viewer never arrives, so the first tap was lost
        if (e.type === 'pointerup' && p && p.id === e.pointerId && !pts.size && p.tile.isConnected &&
            Date.now() - p.t < 500 && Math.abs(this.scrollPos() - p.sy) <= 2 && !Viewer.open_) {
          this.swallowClick();
          this.activate(p.tile, e);
        }
        if (p0 && pts.size < 2) { p0 = null; saveSize(); Scrub.attach(this); }
      };
      const onScroll = () => { if (press) cancelLong(); };
      addEventListener('pointerup', up);
      addEventListener('pointercancel', up);
      addEventListener('blur', cancelLong);
      this.scrollRoot.addEventListener('scroll', onScroll, { passive: true });
      this.untouch = () => {
        cancelLong();
        removeEventListener('pointerup', up);
        removeEventListener('pointercancel', up);
        removeEventListener('blur', cancelLong);
        this.scrollRoot.removeEventListener('scroll', onScroll);
      };
      el.addEventListener('contextmenu', (e) => { if (e.target.closest('.tile') && isTouch) e.preventDefault(); });
    }

    setItems(items, opts = {}) {
      this.items = items;
      Object.assign(this.opts, opts);
      this.index = new Map(items.map((it, i) => [it.id, i]));
      this.relayout();
    }

    scrollPos() { return this.scrollRoot === window ? scrollY : this.scrollRoot.scrollTop; }

    destroy() {
      this.untouch?.();
      this.scrollRoot.removeEventListener('scroll', this.onScroll);
      this.ro.disconnect();
      this.io.disconnect();
      this.el.remove();
    }

    groups() {
      const g = this.opts.group;
      if (g === 'none') {
        return [{ key: 'all', title: '', items: this.items }];
      }
      const out = [];
      let cur = null;
      for (const it of this.items) {
        const d = dt(it.t);
        const key = g === 'year' ? d.getUTCFullYear() : g === 'day' ? `${d.getUTCFullYear()}-${d.getUTCMonth()}-${d.getUTCDate()}` : `${d.getUTCFullYear()}-${d.getUTCMonth()}`;
        if (!cur || cur.key !== key) {
          cur = { key, title: g === 'year' ? String(d.getUTCFullYear()) : g === 'day' ? fmtDay(it.t) : fmtMonth(it.t), items: [] };
          out.push(cur);
        }
        cur.items.push(it);
      }
      for (const s of out) {
        const places = new Map();
        for (const it of s.items) if (it.p) places.set(it.p, (places.get(it.p) || 0) + 1);
        const top = [...places.entries()].sort((a, b) => b[1] - a[1]).slice(0, 3).map((e) => e[0]);
        s.meta = [pl(s.items.length, '{n} Element', '{n} Elemente'), top.join(', ')].filter(Boolean).join(' · ');
      }
      return out;
    }

    relayout() {
      const w = this.el.clientWidth;
      if (!w) return;
      this.width = w;
      const gap = this.opts.small ? 3 : 4;
      const size = this.opts.small ? 110 : S.size;
      const mode = this.opts.small ? 'rows' : S.layout;
      const headH = this.opts.group === 'none' ? 0 : 54;
      let y = 0;
      for (const s of this.sections) s.el?.remove();
      this.sections = this.groups().map((g) => {
        const tiles = layoutTiles(g.items, w, size, gap, mode);
        const h = headH + (tiles.length ? tiles.height : 0) + (headH ? 18 : 0);
        const sec = { ...g, y, h, headH, tiles, el: null, buckets: new Map() };
        y += h;
        return sec;
      });
      this.height = y;
      this.el.style.height = `${y}px`;
      this.el.className = `gallery layout-${mode}${size <= 80 ? ' tiny' : ''}`; // the two smallest sizes: the check circle in the middle
      this.render();
      this.opts.onLayout?.(this);
    }

    viewport() {
      if (this.opts.scrollRoot) {
        const r = this.opts.scrollRoot;
        return { top: r.scrollTop - this.el.offsetTop, h: r.clientHeight };
      }
      const top = this.el.getBoundingClientRect().top;
      return { top: -top, h: innerHeight };
    }

    render() {
      const { top, h } = this.viewport();
      const from = top - h * 1.2, to = top + h * 2.2;
      for (const s of this.sections) {
        const vis = s.y + s.h > from && s.y < to;
        if (!vis) {
          if (s.el) { s.el.remove(); s.el = null; s.buckets.clear(); }
          continue;
        }
        if (!s.el) this.mountSection(s);
        const rel0 = from - s.y - s.headH, rel1 = to - s.y - s.headH;
        const nb = Math.ceil((s.tiles.height || 0) / BUCKET) + 1;
        for (let b = 0; b < nb; b++) {
          const bv = (b + 1) * BUCKET + 600 > rel0 && b * BUCKET < rel1;
          const have = s.buckets.get(b);
          if (bv && !have) this.mountBucket(s, b);
          else if (!bv && have) { for (const el of have) { this.io.unobserve(el); el.remove(); } s.buckets.delete(b); }
        }
      }
    }

    mountSection(s) {
      const el = document.createElement('section');
      el.className = 'sec';
      el.style.top = `${s.y}px`;
      el.style.height = `${s.h}px`;
      if (s.headH) {
        el.innerHTML = `<div class="sec-head"><h2>${esc(s.title)}</h2><span class="meta">${esc(s.meta)}</span>
          <button class="btn ghost selall" data-selall>${icon('select')}<span>${t('Auswählen')}</span></button></div>`;
      }
      s.el = el;
      this.el.appendChild(el);
    }

    mountBucket(s, b) {
      const els = [];
      const frag = document.createDocumentFragment();
      const tl = s.tiles;
      for (let i = 0; i < s.items.length; i++) {
        const ty = tl.y[i];
        if (Math.floor(ty / BUCKET) !== b) continue;
        const el = this.tile(s.items[i], tl.x[i], ty + s.headH, tl.w[i], tl.h[i]);
        frag.appendChild(el);
        els.push(el);
      }
      s.el.appendChild(frag);
      s.buckets.set(b, els);
    }

    tile(it, x, y, w, h) {
      const a = document.createElement('a');
      a.className = 'tile' + (S.sel.has(it.id) ? ' sel' : '');
      a.dataset.id = it.id;
      a.href = `#${routeBase()}|${it.id}`;
      a.style.cssText = `left:${x}px;top:${y}px;width:${w}px;height:${h}px`;
      let html = `<img alt="" loading="lazy" decoding="async" src="${thumbURL(it)}">`;
      if (it.k === 2) html += `<span class="badge br">${icon('play', 'fill')}${fmtDur(it.d)}</span>`;
      else if (it.lv) html += `<span class="badge tr live">${icon('live')}</span>`;
      if (it.f) html += `<span class="badge bl">${icon('heart', 'fill')}</span>`;
      html += `<span class="check">${icon('check')}</span>`;
      const snip = this.opts.snips?.get(it.id);
      if (snip && w > 120 && h > 100) html += `<span class="snip">${markSnip(snip)}</span>`;
      a.innerHTML = html;
      const img = a.firstChild;
      if (img.complete && img.naturalWidth) img.classList.add('ok');
      else img.onload = () => img.classList.add('ok');
      img.onerror = () => { img.remove(); };
      if (previewURL(it) && (S.animate || (isTouch && it.k === 2 && this.opts.autoplay))) this.io.observe(a);
      return a;
    }

    // swallowClick drops the click that follows a tap handled on pointerup, wherever it lands
    swallowClick() {
      const stop = (ev) => { ev.preventDefault(); ev.stopPropagation(); done(); };
      const done = () => { clearTimeout(timer); removeEventListener('click', stop, true); };
      const timer = setTimeout(done, 700);
      addEventListener('click', stop, true);
    }

    click(e) {
      const sa = e.target.closest('[data-selall]');
      if (sa) {
        e.preventDefault();
        const sec = this.sections.find((s) => s.el && s.el.contains(sa));
        if (sec) {
          const all = sec.items.every((it) => S.sel.has(it.id));
          for (const it of sec.items) all ? S.sel.delete(it.id) : S.sel.add(it.id);
          selectionChanged();
        }
        return;
      }
      const a = e.target.closest('.tile');
      if (!a) return;
      e.preventDefault();
      if (this.suppressClick) { this.suppressClick = false; return; }
      this.activate(a, e);
    }

    // activate selects (selection mode, check, modifier keys) or opens the tile
    activate(a, e) {
      const id = a.dataset.id;
      // the check circle selects with a mouse; on touch only a long press starts selecting
      const touch = e.pointerType ? e.pointerType === 'touch' : isTouch;
      if (S.selecting || (e.target.closest('.check') && !touch) || e.ctrlKey || e.metaKey || e.shiftKey) {
        if (e.shiftKey && this.lastSel !== undefined) {
          const i0 = this.index.get(this.lastSel), i1 = this.index.get(id);
          const [lo, hi] = i0 < i1 ? [i0, i1] : [i1, i0];
          for (let i = lo; i <= hi; i++) S.sel.add(this.items[i].id);
        } else if (S.sel.has(id)) S.sel.delete(id);
        else S.sel.add(id);
        this.lastSel = id;
        selectionChanged();
        return;
      }
      Viewer.open(this.items, this.index.get(id));
    }

    hover(e, on) {
      if (isTouch) return;
      const a = e.target.closest('.tile');
      if (!a || (e.relatedTarget && a.contains(e.relatedTarget))) return;
      const it = S.byId.get(a.dataset.id);
      if (!it || !previewURL(it)) return;
      clearTimeout(a._hov);
      if (on) a._hov = setTimeout(() => this.play(a, it), it.k === 2 ? 120 : 250);
      else if (!S.animate) this.stop(a);
    }

    play(a, it) {
      if (a.querySelector('video')) return;
      const v = document.createElement('video');
      v.muted = true; v.loop = true; v.playsInline = true; v.preload = 'auto';
      v.src = previewURL(it);
      v.oncanplay = () => v.classList.add('on');
      v.onerror = () => v.remove();
      a.insertBefore(v, a.firstChild.nextSibling);
      v.play().catch(() => {});
    }

    stop(a) {
      const v = a.querySelector('video');
      if (v) { v.pause(); v.removeAttribute('src'); v.load(); v.remove(); }
    }

    visibleVideos(entries) {
      for (const e of entries) {
        const it = S.byId.get(e.target.dataset.id);
        if (!it) continue;
        if (e.isIntersecting) {
          if (this.playing.size >= 10) continue;
          this.playing.set(e.target, true);
          this.play(e.target, it);
        } else if (this.playing.has(e.target)) {
          this.playing.delete(e.target);
          this.stop(e.target);
        }
      }
    }

    refreshTiles() {
      for (const s of this.sections) {
        if (!s.el) continue;
        for (const el of s.el.querySelectorAll('.tile')) el.classList.toggle('sel', S.sel.has(el.dataset.id));
      }
    }

    scrollToItem(id) {
      const i = this.index.get(id);
      if (i === undefined) return;
      for (const s of this.sections) {
        const j = s.items.findIndex((it) => it.id === id);
        if (j >= 0) {
          const y = this.el.getBoundingClientRect().top + scrollY + s.y + s.headH + s.tiles.y[j];
          const vis = y > scrollY + 60 && y + s.tiles.h[j] < scrollY + innerHeight;
          if (!vis) scrollTo({ top: y - innerHeight / 3 });
          return;
        }
      }
    }

    // year marks for the scrubber: [{label, y}], y relative to the gallery top
    marks() {
      const out = [];
      let last = null;
      for (const s of this.sections) {
        if (!s.items.length) continue;
        const yr = dt(s.items[0].t).getUTCFullYear();
        if (yr !== last) { out.push({ label: String(yr), y: s.y }); last = yr; }
      }
      return out;
    }
  }

  function layoutTiles(items, W, size, gap, mode) {
    const n = items.length;
    const x = new Float32Array(n), y = new Float32Array(n), w = new Float32Array(n), h = new Float32Array(n);
    const ar = (it) => clamp(it.a || 1.333, 0.35, 3.2);
    let height = 0;
    if (mode === 'grid') {
      const cols = Math.max(2, Math.round(W / size));
      const c = (W - gap * (cols - 1)) / cols;
      for (let i = 0; i < n; i++) {
        x[i] = (i % cols) * (c + gap);
        y[i] = Math.floor(i / cols) * (c + gap);
        w[i] = h[i] = c;
      }
      height = n ? Math.ceil(n / cols) * (c + gap) - gap : 0;
    } else if (mode === 'masonry') {
      const cols = Math.max(2, Math.round(W / (size * 1.05)));
      const c = (W - gap * (cols - 1)) / cols;
      const colY = new Array(cols).fill(0);
      for (let i = 0; i < n; i++) {
        let k = 0;
        for (let j = 1; j < cols; j++) if (colY[j] < colY[k] - 1) k = j;
        const th = c / ar(items[i]);
        x[i] = k * (c + gap); y[i] = colY[k]; w[i] = c; h[i] = th;
        colY[k] += th + gap;
      }
      height = Math.max(0, ...colY) - gap;
    } else {
      let row = [], rowW = 0, top = 0;
      const flush = (last) => {
        if (!row.length) return;
        const avail = W - gap * (row.length - 1);
        let rh = avail / rowW;
        if (last && rh > size * 1.15) rh = size;
        let left = 0;
        for (const i of row) {
          const tw = ar(items[i]) * rh;
          x[i] = left; y[i] = top; w[i] = tw; h[i] = rh;
          left += tw + gap;
        }
        if (!last || rh !== size) {
          // close rounding gaps on the right edge
          const lastI = row[row.length - 1];
          w[lastI] = W - x[lastI];
        }
        top += rh + gap;
        row = []; rowW = 0;
      };
      for (let i = 0; i < n; i++) {
        row.push(i);
        rowW += ar(items[i]);
        if (rowW * size + gap * (row.length - 1) >= W) flush(false);
      }
      flush(true);
      height = top - gap;
    }
    return { x, y, w, h, height, length: n };
  }

  // ---------------------------------------------------------------------------
  // scrubber (year rail on the right)
  // ---------------------------------------------------------------------------
  const Scrub = {
    el: $('#scrubber'),
    g: null,
    attach(g) {
      this.g = g;
      const marks = g.marks();
      if (g.height < innerHeight * 2.5 || marks.length < 2) { this.detach(); return; }
      document.body.classList.add('has-scrubber');
      this.el.hidden = false;
      const H = this.el.clientHeight;
      const total = g.height;
      let html = '';
      let lastY = -100;
      for (const m of marks) {
        const y = (m.y / total) * H;
        html += y - lastY > 18 ? `<span class="y" style="top:${y}px">${m.label}</span>` : `<span class="dot" style="top:${y}px"></span>`;
        if (y - lastY > 18) lastY = y;
      }
      this.el.innerHTML = html + '<i class="thumb"></i><span class="tip"></span>';
      this.update();
    },
    detach() {
      this.g = null;
      this.el.hidden = true;
      document.body.classList.remove('has-scrubber');
    },
    galleryTop() { return this.g.el.getBoundingClientRect().top + scrollY; },
    update() {
      if (!this.g || this.el.hidden) return;
      const H = this.el.clientHeight;
      const frac = clamp((scrollY - this.galleryTop() + innerHeight / 3) / this.g.height, 0, 1);
      $('.thumb', this.el).style.top = `${frac * H}px`;
    },
    at(clientY) {
      const r = this.el.getBoundingClientRect();
      const frac = clamp((clientY - r.top) / r.height, 0, 1);
      const y = frac * this.g.height;
      let sec = this.g.sections[0];
      for (const s of this.g.sections) { if (s.y <= y) sec = s; else break; }
      const tip = $('.tip', this.el);
      tip.style.top = `${clientY - r.top}px`;
      tip.textContent = sec?.items.length ? fmtMonth(sec.items[0].t) : '';
      return y;
    },
  };
  {
    const el = Scrub.el;
    let drag = false;
    el.addEventListener('pointermove', (e) => { if (Scrub.g) { const y = Scrub.at(e.clientY); if (drag) scrollTo({ top: Scrub.galleryTop() + y - 40 }); } });
    el.addEventListener('pointerdown', (e) => { if (!Scrub.g) return; drag = true; el.classList.add('drag'); el.setPointerCapture(e.pointerId); scrollTo({ top: Scrub.galleryTop() + Scrub.at(e.clientY) - 40 }); });
    el.addEventListener('pointerup', () => { drag = false; el.classList.remove('drag'); });
    addEventListener('scroll', () => Scrub.update(), { passive: true });
  }

  // ---------------------------------------------------------------------------
  // selection
  // ---------------------------------------------------------------------------
  // a page with its own things to select (the trash, everybody's marks) uses the selection bar
  // too, with its actions instead of the ones for photos: { actions: [{ icon, label, short, danger,
  // run(ids) }], sync() shows the selection on the page }. It ends with the page.
  let pageSel = null;
  function pageSelection(ps) {
    pageSel = ps;
    S.sel.clear();
    S.selecting = false;
    for (const b of $$('#selbar .pg')) b.remove();
    if (ps) {
      $('#selClose').insertAdjacentHTML('beforebegin', ps.actions.map((a, i) => `<button class="btn pg${a.danger ? ' adm-del' : ''}" data-pg="${i}">${icon(a.icon)}<span>${esc(a.label)}</span><span class="s">${esc(a.short || a.label)}</span></button>`).join(''));
      for (const b of $$('#selbar .pg')) b.onclick = () => { const ids = [...S.sel]; if (ids.length) ps.actions[+b.dataset.pg].run(ids); };
      cleanup.push(() => { if (pageSel === ps) pageSelection(null); });
    }
    $('#selbar').classList.toggle('page', !!ps);
    selectionChanged();
  }
  function selectionChanged() {
    const n = S.sel.size;
    $('#selbar').hidden = n === 0 && !S.selecting;
    $('#selCount').textContent = n ? t('{n} ausgewählt', { n: num(n) }) : isTouch ? t('Elemente antippen') : t('Elemente anklicken');
    document.body.classList.toggle('selecting', S.selecting || n > 0);
    S.gallery?.refreshTiles();
    for (const g of extraGalleries) g.refreshTiles();
    pageSel?.sync();
    if (n && !pageSel) SelPeople.load();
    else SelPeople.clear();
  }
  function endSelection() { S.sel.clear(); S.selecting = false; selectionChanged(); }
  // swiped away: the selection ends, the toast brings it back
  function dropSelection() {
    const ids = [...S.sel];
    endSelection();
    if (ids.length) toast(pl(ids.length, '{n} Element abgewählt', '{n} Elemente abgewählt'), t('Rückgängig'), () => { for (const id of ids) S.sel.add(id); S.selecting = true; selectionChanged(); });
  }
  sheetGrip($('#selbar'), { key: 'sel', onClose: dropSelection, min: 120 });
  new ResizeObserver(() => document.body.style.setProperty('--selh', `${$('#selbar').offsetHeight}px`)).observe($('#selbar'));
  // the people in the selection: a chip per person with the number of its photos there; a tap
  // asks who that is in all of them at once (another name, not this person, not a face)
  const SelPeople = {
    el: $('#selPeople'), seq: 0,
    clear() { this.seq++; this.el.hidden = true; this.el.innerHTML = ''; },
    // the chips belong to the selection they were made for: off at once until the new ones come
    load() {
      this.seq++;
      for (const b of $$('.sp', this.el)) b.disabled = true;
      this.later();
    },
    later: debounce(() => SelPeople.fetch(), 250),
    async fetch() {
      const ids = [...S.sel], seq = ++this.seq;
      if (!ids.length || S.config?.faces === 'off') { this.clear(); return; }
      const res = await post('/api/items/people', { ids }).catch(() => null);
      if (seq !== this.seq) return;
      const people = res?.people || [];
      this.el.hidden = !people.length;
      this.el.innerHTML = people.length ? `<h4>${t('Personen')}</h4><div class="sp-list">${people.map((p, i) => `<button type="button" class="sp" data-i="${i}"
        title="${t('{items} · {faces}', { items: pl(p.items, '{n} Foto', '{n} Fotos'), faces: pl(p.faces, '{n} Gesicht', '{n} Gesichter') })}"><img src="${faceURL(p.cover.item, p.cover.idx)}" alt="" loading="lazy"><span class="${p.name ? '' : 'unnamed'}">${esc(p.name || t('Unbenannt'))}</span><small>${num(p.items)}</small></button>`).join('')}</div>` : '';
      for (const b of $$('.sp', this.el)) {
        b.onclick = () => {
          const p = people[+b.dataset.i];
          Viewer.namePop(b, { name: p.name, person: p.id, multi: { ids, person: p.id, items: p.items, cover: p.cover } });
        };
      }
    },
  };
  $('#selectBtn').onclick = () => { S.selecting = !S.selecting; selectionChanged(); };
  $('#selClose').onclick = endSelection;
  $('#selFav').onclick = async () => {
    const ids = [...S.sel];
    const on = !ids.every((id) => S.byId.get(id)?.f);
    await post('/api/fav', { ids, on });
    for (const id of ids) { const it = S.byId.get(id); if (it) it.f = on; }
    toast(on ? pl(ids.length, '{n} Element zu Favoriten', '{n} Elemente zu Favoriten') : t('Aus Favoriten entfernt'));
    updateCounts();
    endSelection();
    rerender();
  };
  $('#selZip').onclick = () => {
    const f = $('#zipForm');
    f.ids.value = [...S.sel].join(',');
    f.submit();
    toast(t('Download startet …'));
  };

  // ---------------------------------------------------------------------------
  // viewer
  // ---------------------------------------------------------------------------
  const Viewer = {
    el: $('#viewer'), media: $('#vMedia'), stage: $('#vStage'),
    list: [], i: 0, it: null, detail: null,
    showInfo: store.get('info', !isTouch), showFaces: false,
    zoom: { s: 1, x: 0, y: 0 }, slideT: 0, idleT: 0, open_: false,
    mark: null, // a face being marked by hand: { key(e), end() }

    open(list, i, push = true) {
      this.list = list;
      this.i = clamp(i, 0, list.length - 1);
      if (!this.open_) {
        this.open_ = true;
        this.el.hidden = false;
        document.body.classList.add('locked');
        if (push) history.pushState({ viewer: true }, '', `#${routeBase()}|${this.list[this.i].id}`);
      }
      this.show();
    },

    close(fromPop = false) {
      if (!this.open_) return;
      this.open_ = false;
      this.stopSlides();
      this.el.hidden = true;
      this.media.innerHTML = '';
      document.body.classList.remove('locked');
      this.mark?.end();
      $('#pop')?.remove();
      if (FS.auto) FS.exit();
      if (!fromPop) history.back();
      if (this.it) S.gallery?.scrollToItem(this.it.id);
    },

    step(d) {
      const n = this.i + d;
      if (n < 0 || n >= this.list.length) return;
      this.i = n;
      history.replaceState({ viewer: true }, '', `#${routeBase()}|${this.list[this.i].id}`);
      this.show();
    },

    async show() {
      const it = (this.it = this.list[this.i]);
      this.mark?.end();
      this.resetZoom();
      $('#pop')?.remove();
      const m = this.media;
      m.innerHTML = '';
      if (it.k === 2) {
        const v = document.createElement('video');
        v.controls = true; v.autoplay = true; v.playsInline = true;
        v.poster = largeURL(it);
        let triedH264 = false;
        const h264 = () => { if (triedH264) return; triedH264 = true; v.src = `/v/${it.id}?h264=1`; v.play().catch(() => {}); };
        v.onerror = h264;
        v.src = `/v/${it.id}`;
        for (const ev of ['pause', 'play', 'seeked']) v.addEventListener(ev, () => { if (this.it === it) this.drawFaces(); });
        m.appendChild(v);
        // HEVC without browser support: transcode right away
        api(`/api/item/${it.id}`).then((d) => { if (/hev|hvc|h265/i.test(d.codec || '') && !canHevc && this.it === it) h264(); });
      } else {
        const img = new Image();
        img.className = 'lo';
        img.alt = '';
        img.src = thumbURL(it);
        img.draggable = false;
        m.appendChild(img);
        const big = new Image();
        big.onload = () => { if (this.it === it) { img.src = big.src; img.classList.remove('lo'); this.drawFaces(); } };
        big.src = largeURL(it);
      }
      $('#vLive').hidden = !it.lv;
      $('#vFav').classList.toggle('on', !!it.f);
      $('#vFav').innerHTML = icon('heart', it.f ? 'fill' : '');
      $('#vDl').href = `/o/${it.id}?dl=1`;
      $('#vPrint').hidden = it.k === 2;
      $('#vPrev').hidden = this.i === 0;
      $('#vNext').hidden = this.i >= this.list.length - 1;
      $('#vTitle').innerHTML = `<b>${fmtLong(it.t)}</b><span>${[it.ap ? t('ungefähr') : fmtTime(it.t), it.p, t('{i} von {n}', { i: num(this.i + 1), n: num(this.list.length) })].filter(Boolean).map(esc).join(' · ')}</span>`;
      this.strip();
      this.preload();
      this.detail = null;
      const d = await api(`/api/item/${it.id}`).catch(() => null);
      if (this.it !== it || !d) return;
      this.detail = d;
      this.renderInfo();
      this.drawFaces();
      if (it.lv && store.get('livePlay', true)) this.playLive(true);
    },

    preload() {
      for (const d of [1, -1, 2]) {
        const n = this.list[this.i + d];
        if (n && n.k === 1) { const im = new Image(); im.src = largeURL(n); }
      }
    },

    strip() {
      const el = $('#vStrip');
      const from = Math.max(0, this.i - 8), to = Math.min(this.list.length, this.i + 9);
      let html = '';
      for (let j = from; j < to; j++) {
        html += `<button data-j="${j}" class="${j === this.i ? 'on' : ''}"><img loading="lazy" src="${thumbURL(this.list[j])}" alt=""></button>`;
      }
      el.innerHTML = html;
    },

    playLive(once) {
      const it = this.it;
      if (!it?.lv) return;
      let v = $('.live-v', this.media);
      if (!v) {
        v = document.createElement('video');
        v.className = 'live-v';
        v.muted = once; v.playsInline = true;
        v.src = canHevc ? `/v/${it.lv}` : `/v/${it.lv}?h264=1`;
        v.onerror = () => { if (!v.src.includes('h264')) v.src = `/v/${it.lv}?h264=1`; };
        v.onended = () => v.classList.remove('on');
        v.oncanplay = () => { v.classList.add('on'); };
        this.media.appendChild(v);
      } else {
        v.currentTime = 0;
        v.muted = once;
        v.classList.add('on');
      }
      v.play().catch(() => {});
    },

    // details as a bottom sheet (phones, tablets): free height, remembered per device class
    sheetMode: matchMedia('(max-width: 900px)'),
    sheetH() { return parseFloat(this.el.style.getPropertyValue('--sheet')) || innerHeight * 0.5; },
    setSheet(h) {
      h = clamp(h, 0, innerHeight - 56);
      this.el.style.setProperty('--sheet', `${Math.round(h)}px`);
    },
    restoreSheet() {
      const f = clamp(+store.get(`sheet-${devClass()}`, 0.5) || 0.5, 0.18, 0.92);
      this.setSheet(innerHeight * f);
    },
    renderInfo() {
      const p = $('#vInfoPanel');
      p.hidden = !this.showInfo;
      if (this.showInfo && this.sheetMode.matches && !this.el.style.getPropertyValue('--sheet')) this.restoreSheet();
      this.el.classList.toggle('info', this.showInfo);
      $('#vInfo').classList.toggle('on', this.showInfo);
      if (!this.showInfo || !this.detail) return;
      const d = this.detail, it = this.it;
      const exp = d.exposure ? (d.exposure >= 1 ? `${d.exposure} s` : `1/${Math.round(1 / d.exposure)} s`) : '';
      const camera = [d.make, d.model].filter(Boolean).join(' ');
      const shot = [d.fnumber ? `f/${d.fnumber}` : '', exp, d.iso ? `ISO ${d.iso}` : '', d.focal ? `${Math.round(d.focal * 10) / 10} mm` : ''].filter(Boolean).join(' · ');
      const mp = d.w && d.h ? `${d.w} × ${d.h}${d.kind === 1 ? ` · ${(d.w * d.h / 1e6).toFixed(1)} MP` : ''}` : '';
      const q = currentQuery();
      const marked = (raw) => {
        let h = esc(raw || '');
        if (q && h) {
          for (const w of q.split(/\s+/).filter((x) => x.length > 2 && !x.includes(':'))) {
            h = h.replace(new RegExp(`(${w.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')})`, 'gi'), '<mark>$1</mark>');
          }
        }
        return h;
      };
      // text in the picture in the viewer's language (a translation), the original below it
      const ocrShown = (LANG === 'en' ? d.ocrEn : d.ocrDe) || d.ocr, ocrTr = !!d.ocr && ocrShown !== d.ocr;
      const text = marked(d.desc), ocrText = marked(ocrShown), speechText = marked(d.speech);
      // AI description in the viewer's language (the other one as fallback)
      const ai = d.ai || null;
      let aiText = '', aiTags = [];
      if (ai) {
        aiText = esc(LANG === 'en' ? (ai.en || ai.de) : (ai.de || ai.en));
        aiTags = (LANG === 'en' ? (ai.tagsEn?.length ? ai.tagsEn : ai.tagsDe) : (ai.tagsDe?.length ? ai.tagsDe : ai.tagsEn)) || [];
        if (q) {
          for (const w of q.split(/\s+/).filter((x) => x.length > 2 && !x.includes(':'))) {
            aiText = aiText.replace(new RegExp(`(${w.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')})`, 'gi'), '<mark>$1</mark>');
          }
        }
      }
      const faces = (d.faces || []).map((f) => {
        const per = f.person ? S.peopleById.get(f.person) : null;
        const name = f.name || (per ? t('Unbenannt') : t('Unbekannt'));
        const at = it.k === 2 ? `<small class="face-t" data-seek title="${t('Im Video zeigen')}">${icon('play', 'fill')}${fmtDur(f.t)}</small>` : '';
        return `<a href="${f.person ? `#/person/${encodeURIComponent(f.person)}` : '#'}" data-face="${f.idx}"><img src="${faceURL(it.id, f.idx)}" alt="">${esc(name)}${at}</a>`;
      }).join('');
      p.innerHTML = `
        <h3>${t('Aufnahme')}</h3>
        <div class="big">${fmtLong(d.taken)}</div>
        <div class="muted">${d.takenSrc === 'folder' || d.takenSrc === 'mtime' ? t('Datum ungefähr (aus Ordner bzw. Datei)') : t('{time} Uhr', { time: fmtTime(d.taken) })}</div>
        ${d.place || d.gps ? `<h3>${t('Ort')}</h3>
          <div class="kv">${icon('pin')}<div>${d.place ? `<a class="link" href="#/ort/${encodeURIComponent(d.place)}">${esc(d.place)}</a>` : t('Ohne Ortsname')}
          ${d.gps ? `<div class="muted">${d.lat.toFixed(5)}, ${d.lon.toFixed(5)}${d.alt ? ` · ${Math.round(d.alt)} m` : ''}</div>` : ''}</div></div>
          ${d.gps ? '<div class="minimap" id="vMap"></div>' : ''}` : ''}
        ${faces || Account.can('user') ? `<h3>${t('Personen')}${Account.can('user') ? ` <button class="link ai-redo mark-face" data-markface title="${t('Ein Gesicht selbst einrahmen, z.B. wenn es nicht erkannt wurde')}">${icon('face-plus')}<span>${t('Gesicht markieren')}</span></button>` : ''}</h3>${faces ? `<div class="faces">${faces}</div>` : ''}` : ''}
        ${ai ? `<h3>${t('Beschreibung')} <span class="ai-badge" title="${t('Von einem KI-Modell geschrieben')}">${t('KI')}</span></h3>
          ${aiText ? `<div class="ai-desc">${aiText}</div>` : ''}
          ${aiTags.length ? `<div class="chips">${aiTags.map((k) => `<a class="chip" href="#/suche/${encodeURIComponent(k)}">${esc(k)}</a>`).join('')}</div>` : ''}
          ${Account.can('admin') && S.config.ai === 'on' ? `<button class="link ai-redo" data-redesc>${t('Neu beschreiben')}</button>` : ''}` : ''}
        ${d.ocr ? `<h3>${t('Text im Bild')} <span class="ai-badge" title="${t('Von einem KI-Modell gelesen')}">${t('erkannt')}</span>${ocrTr ? `<span class="ai-badge" title="${t('Von einem KI-Modell übersetzt')}">${t('übersetzt')}</span>` : ''}</h3><div class="text ocr">${ocrText}</div>
          ${ocrTr ? `<details class="ocr-orig"><summary>${t('Original')}</summary><div class="text ocr">${marked(d.ocr)}</div></details>` : ''}
          ${Account.can('admin') && d.ocrOn ? `<button class="link ai-redo" data-reocr>${t('Neu erkennen')}</button>` : ''}`
          : Account.can('admin') && d.ocrOn ? `<button class="link ai-redo" data-reocr>${t('Text neu erkennen')}</button>` : ''}
        ${d.speech ? `<h3>${t('Gesprochen')} <span class="ai-badge" title="${t('Von einem KI-Modell gehört')}">${t('erkannt')}</span></h3><div class="text ocr">${speechText}</div>
          ${Account.can('admin') && d.speechOn ? `<button class="link ai-redo" data-respeech>${t('Neu anhören')}</button>` : ''}`
          : Account.can('admin') && d.speechOn ? `<button class="link ai-redo" data-respeech>${t('Sprache neu erkennen')}</button>` : ''}
        ${d.desc ? `<h3>${d.ocr ? t('Text aus der Datei') : t('Text im Bild')}</h3><div class="text">${text}</div>` : ''}
        ${d.keywords?.length || d.category ? `<h3>${t('Stichwörter')}</h3><div class="chips">${[d.category, ...(d.keywords || [])].filter((v, i, a) => v && a.indexOf(v) === i).map((k) => `<a class="chip" href="#/suche/${encodeURIComponent(k)}">${icon('tag')}${esc(k)}</a>`).join('')}</div>` : ''}
        ${camera || shot ? `<h3>${t('Kamera')}</h3><div class="kv">${icon('camera')}<div>${esc(camera || t('Unbekannt'))}${d.lens ? `<div class="muted">${esc(d.lens)}</div>` : ''}${shot ? `<div class="muted">${esc(shot)}</div>` : ''}</div></div>` : ''}
        <h3>${t('Datei')}</h3>
        <div class="kv">${icon('file')}<div>${esc(d.name)}<div class="muted">${[fmtSize(d.size), mp, d.duration ? fmtDur(d.duration) : '', d.codec].filter(Boolean).map(esc).join(' · ')}</div></div></div>
        <div class="kv">${icon('folder')}<div><a class="link" href="#/alben/${encodeURIComponent(d.album)}">${esc(d.album)}</a><div><code>${esc(d.path)}</code></div></div></div>
        <div class="kv">${icon('download')}<div><a class="link" href="/o/${it.id}?dl=1">${t('Original herunterladen')}</a> · <a class="link" href="/o/${it.id}" target="_blank" rel="noopener">${t('öffnen')}</a>${it.k === 2 ? '' : ` · <a class="link" href="/print/${it.id}?v=${it.v}" data-print>${t('drucken')}</a>`}</div></div>`;
      if (d.gps && window.L) {
        const mm = L.map($('#vMap', p), { zoomControl: false, attributionControl: false }).setView([d.lat, d.lon], 13);
        tileLayer().addTo(mm);
        L.circleMarker([d.lat, d.lon], { radius: 7, color: '#fff', weight: 2, fillColor: '#f3b549', fillOpacity: 1 }).addTo(mm);
      }
      $('[data-markface]', p)?.addEventListener('click', () => this.markFace());
      $('[data-redesc]', p)?.addEventListener('click', () => window.SK?.redescribe?.([it.id]));
      $('[data-reocr]', p)?.addEventListener('click', () => window.SK?.rerecognize?.([it.id]));
      $('[data-respeech]', p)?.addEventListener('click', () => window.SK?.retranscribe?.([it.id]));
      $('[data-print]', p)?.addEventListener('click', (e) => { e.preventDefault(); printItem(it, d); });
      // a person: a tap opens the person (no person yet: asks for the name); a long press or
      // a right click opens the face's menu
      for (const a of $$('[data-face]', p)) {
        const f = d.faces.find((x) => x.idx === +a.dataset.face);
        let longT = 0, press = null, held = false;
        a.onpointerdown = (e) => {
          held = false;
          if (e.pointerType === 'mouse') return;
          press = { x: e.clientX, y: e.clientY };
          clearTimeout(longT);
          longT = setTimeout(() => { held = true; press = null; this.faceInfoMenu(a, f, e.clientX, e.clientY, true); }, 450);
        };
        a.onpointermove = (e) => { if (press && Math.abs(e.clientX - press.x) + Math.abs(e.clientY - press.y) > 8) { clearTimeout(longT); press = null; } };
        a.onpointerup = a.onpointercancel = () => { clearTimeout(longT); press = null; };
        a.oncontextmenu = (e) => {
          e.preventDefault();
          if (!press && !held) this.faceInfoMenu(a, f, e.clientX, e.clientY, false); // a mouse; a finger has the timer
        };
        a.onclick = (e) => {
          if (held) { e.preventDefault(); held = false; return; } // the finger lifting off after a long press
          if (e.target.closest('[data-seek]')) { e.preventDefault(); this.seekFace(f); return; }
          if (!f.person || e.altKey) { e.preventDefault(); this.namePop(a, f); }
          else this.close(true);
        };
      }
    },

    drawFaces() {
      $('.v-faces', this.media)?.remove();
      $('#vFaces').classList.toggle('on', this.showFaces);
      this.el.classList.toggle('faces-on', this.showFaces);
      const d = this.detail;
      if (!this.showFaces || !d?.faces?.length || (this.it.k !== 1 && this.it.k !== 2)) return;
      let faces = d.faces;
      if (this.it.k === 2) { // a video: the faces of the frame it is paused at
        const v = $('video', this.media);
        if (!v || !v.paused) return;
        faces = faces.filter((f) => Math.abs((f.t || 0) - v.currentTime) < 0.25);
        if (!faces.length) return;
      }
      const ov = document.createElement('div');
      ov.className = 'v-faces';
      ov.innerHTML = faces.map((f) => `<div class="v-face ${f.name ? '' : 'unknown'}" data-idx="${f.idx}" style="left:${f.X * 100}%;top:${f.Y * 100}%;width:${f.W * 100}%;height:${f.H * 100}%"><span>${esc(f.name || (f.person ? t('Unbenannt') : '?'))}</span></div>`).join('');
      this.media.appendChild(ov);
      for (const b of $$('.v-face', ov)) b.onclick = (e) => { e.stopPropagation(); this.namePop(b, d.faces.find((x) => x.idx === +b.dataset.idx)); };
    },

    // a face of a video: pause at the second it was found at and show its box
    seekFace(f) {
      const v = $('video', this.media);
      if (!v) return;
      v.pause();
      this.showFaces = true;
      if (this.sheetMode.matches) this.setSheet(Math.min(this.sheetH(), innerHeight * 0.3)); // room for the picture
      if (Math.abs(v.currentTime - (f.t || 0)) < 0.01) this.drawFaces();
      else v.currentTime = f.t || 0; // 'seeked' draws the boxes
    },

    // a long press (a right click with a mouse) on a photo while its face boxes are shown:
    // a menu at that spot (above a finger, so it is not covered). False when there is none
    // (e.g. beside the picture).
    faceMenu(x, y, touch = false) {
      if (!this.showFaces || this.it?.k !== 1 || this.mark || !Account.can('user')) return false;
      const r = this.media.getBoundingClientRect();
      if (x < r.left || x > r.right || y < r.top || y > r.bottom) return false;
      const at = { x: (x - r.left) / r.width, y: (y - r.top) / r.height };
      this.ctxMenu(x, y, touch, [['face-plus', t('Gesicht hinzufügen'), () => this.markFace(at)]]);
      return true;
    },

    // a small menu at a point, above a finger so it is not covered. entries: [icon, label, action]
    ctxMenu(x, y, touch, entries) {
      $('#pop')?.remove();
      const menu = document.createElement('div');
      menu.className = 'pop v-ctx';
      menu.id = 'pop';
      menu.innerHTML = entries.map(([ic, label], i) => `<button type="button" data-i="${i}">${icon(ic)}<span>${esc(label)}</span></button>`).join('');
      document.body.appendChild(menu);
      menu.style.left = `${clamp(x, 8, innerWidth - menu.offsetWidth - 8)}px`;
      menu.style.top = `${clamp(touch ? y - menu.offsetHeight - 24 : y, 8, innerHeight - menu.offsetHeight - 8)}px`;
      const off = (e) => { if (!menu.contains(e.target)) close(); };
      const close = () => { menu.remove(); removeEventListener('pointerdown', off, true); };
      addEventListener('pointerdown', off, true);
      menu.onclick = (e) => {
        const b = e.target.closest('[data-i]');
        if (!b) return;
        close();
        entries[+b.dataset.i][2]();
      };
      navigator.vibrate?.(12);
    },

    // the menu of a person in the details (long press, right click)
    faceInfoMenu(a, f, x, y, touch) {
      const user = Account.can('user'), video = this.it?.k === 2;
      this.ctxMenu(x, y, touch, [
        f.person && ['people', f.name ? t('Fotos von {name}', { name: f.name }) : t('Fotos dieser Person'), () => { this.close(true); location.hash = `#/person/${encodeURIComponent(f.person)}`; }],
        [video ? 'play' : 'face', video ? t('Im Video zeigen') : t('Im Bild zeigen'), () => this.showFace(f)],
        user && ['user', f.name ? t('Name ändern …') : t('Benennen …'), () => this.namePop(a, f)],
        user && ['eye-off', f.person ? t('Nicht diese Person') : t('Ausblenden'), () => this.saveFace(f, '')],
        user && ['face-x', t('Kein Gesicht'), () => this.saveFace(f, '-')],
      ].filter(Boolean));
    },

    // shows a face's box in the picture for a moment (a video: at the second it was found at)
    showFace(f) {
      if (this.it?.k === 2) { this.seekFace(f); return; }
      this.resetZoom();
      this.showFaces = true;
      if (this.sheetMode.matches) this.setSheet(Math.min(this.sheetH(), innerHeight * 0.3)); // room for the picture
      this.drawFaces();
      const b = $(`.v-face[data-idx="${f.idx}"]`, this.media);
      if (!b) return;
      b.classList.remove('flash');
      void b.offsetWidth; // restarts the animation
      b.classList.add('flash');
    },

    // the photo's details again after a change of its faces
    async refreshItem(it) {
      await loadPeople();
      const d = await api(`/api/item/${it.id}`);
      if (this.it !== it) return;
      this.detail = d;
      this.renderInfo();
      this.drawFaces();
      scheduleReload();
    },

    // names a face: '' not this person (no person: hidden), '-' not a face (can be undone).
    // An added face (face.add) is saved with its box.
    async saveFace(face, name) {
      const it = this.it;
      if (name && name !== '-') store.set('recent-names', [name, ...store.get('recent-names', []).filter((n) => n !== name)].slice(0, 8));
      if (face.multi) { // a person in all photos of a selection
        const m = face.multi;
        const res = await post(`/api/items/people/${encodeURIComponent(m.person)}`, { ids: m.ids, name }).catch((e) => ({ error: e.message }));
        if (res.error) { toast(t('Fehler: {e}', { e: String(res.error).replace(/^\d{3} /, '') })); return; }
        const faces = pl(res.n, '{n} Gesicht', '{n} Gesichter');
        const msg = name === '-' ? pl(res.n, '{n} Gesicht entfernt', '{n} Gesichter entfernt')
          : name ? t('{faces} als {name} gespeichert', { faces, name })
            : face.name ? t('{faces}: nicht {name}', { faces, name: face.name }) : t('{faces} ausgeblendet', { faces });
        const after = async () => { await loadPeople(); scheduleReload(); SelPeople.load(); };
        toast(msg, t('Rückgängig'), async () => { await post('/api/faces/labels', { labels: res.undo }); await after(); toast(t('Wiederhergestellt')); });
        await after();
        return;
      }
      if (face.add) {
        const b = face.box;
        toast(t('Wird gespeichert …'));
        const res = await post(`/api/face/${it.id}/add`, { X: b.x, Y: b.y, W: b.w, H: b.h, t: face.t || 0, name }).catch((e) => ({ error: e.message }));
        if (res.error) { toast(t('Fehler: {e}', { e: String(res.error).replace(/^\d{3} /, '') })); return; }
        toast(name ? t('Als {name} gespeichert', { name }) : t('Gesicht markiert'));
        await this.refreshItem(it);
        return;
      }
      const res = await post(`/api/face/${it.id}/${face.idx}`, { name });
      if (name === '-') {
        toast(t('Kein Gesicht'), t('Rückgängig'), async () => { await post('/api/faces/labels', { labels: res.undo }); await this.refreshItem(it); });
      } else toast(name ? t('Als {name} gespeichert', { name }) : t('Entfernt'));
      await this.refreshItem(it);
    },

    // draws a box around a face by hand (a paused video: on its frame). The box can then be
    // moved and resized before the name is asked for. at: a point of the picture (0..1) that
    // gets a box right away instead of drawing one.
    markFace(at) {
      if (!Account.need('user')) return;
      const it = this.it, m = this.media;
      const v = it.k === 2 ? $('video', m) : null;
      const time = v ? (v.pause(), v.currentTime) : 0;
      const lv = $('.live-v', m); // a Live Photo: the box is drawn on the still photo
      if (lv) { lv.pause(); lv.classList.remove('on'); }
      this.mark?.end();
      this.resetZoom();
      if (this.sheetMode.matches) this.setSheet(Math.min(this.sheetH(), innerHeight * 0.3)); // room for the picture
      $('#pop')?.remove();
      const ov = document.createElement('div');
      ov.className = 'v-mark';
      ov.innerHTML = `<div class="v-mark-hint"><span></span><button type="button" data-cancel>${t('Abbrechen')}</button></div>`;
      m.appendChild(ov);
      const hint = $('.v-mark-hint span', ov);
      const box = document.createElement('div');
      box.className = 'v-face mark';
      // size and confirm buttons next to the box; outside the picture, so they also fit beside a small one
      const tools = document.createElement('div');
      tools.className = 'v-mark-tools';
      tools.innerHTML = `<button type="button" data-k="-" title="${t('Kleiner')} (−)">${icon('minus')}</button>
        <button type="button" data-k="+" title="${t('Größer')} (+)">${icon('plus')}</button>
        <button type="button" class="ok" data-k="ok" title="${t('Weiter')} (Enter)">${icon('check')}<span>${t('Weiter')}</span></button>`;
      this.el.classList.add('marking'); // bars out of the way
      let b = null, drag = null;
      // the bar goes below the picture (no room: above it; a picture that fills the stage: at
      // the stage's bottom edge), under the middle of the box, so a growing box does not run
      // into it. It stays where it is while the box is resized: − and + can be tapped again
      // and again at the same spot.
      const placeTools = () => {
        if (!tools.isConnected) return;
        const p = ov.getBoundingClientRect(), s = this.stage.getBoundingClientRect(), tw = tools.offsetWidth, th = tools.offsetHeight;
        const lo = Math.max(s.top, 0) + 8, hi = Math.min(s.bottom, innerHeight) - th - 8;
        let top = p.bottom + 10;
        if (top > hi) top = p.top - th - 10 >= lo ? p.top - th - 10 : hi;
        const r = box.isConnected ? box.getBoundingClientRect() : p;
        tools.style.left = `${clamp(r.left + r.width / 2 - tw / 2, 8, innerWidth - tw - 8)}px`;
        tools.style.top = `${clamp(top, 8, innerHeight - th - 8)}px`;
      };
      const ro = new ResizeObserver(placeTools); // the picture changes size (sheet, rotation)
      ro.observe(ov);
      const done = () => {
        ov.remove();
        tools.remove();
        ro.disconnect();
        $('#pop')?.remove();
        this.el.classList.remove('marking');
        if (this.mark === mark) this.mark = null;
      };
      // with a box: it can be moved and resized; without: one is drawn
      const adjusting = (on) => {
        hint.textContent = on ? t('Rahmen auf das Gesicht schieben, Größe mit − und +') : t('Rahmen um das Gesicht ziehen oder auf das Gesicht tippen');
        if (!on) { tools.remove(); return; }
        $('#pop')?.remove();
        document.body.appendChild(tools);
        placeTools();
      };
      // keepTools: the bar stays (resizing)
      const place = (nb, keepTools = false) => {
        const w = clamp(nb.w, 0, 1), h = clamp(nb.h, 0, 1);
        b = { x: clamp(nb.x, 0, 1 - w), y: clamp(nb.y, 0, 1 - h), w, h };
        if (!box.isConnected) ov.appendChild(box);
        Object.assign(box.style, { left: `${b.x * 100}%`, top: `${b.y * 100}%`, width: `${w * 100}%`, height: `${h * 100}%` });
        if (!keepTools) placeTools();
      };
      // a square box around a point: given size (fractions), or a fifth of the picture's shorter side
      const around = (p, size) => {
        const r = ov.getBoundingClientRect(), side = Math.min(r.width, r.height) * 0.2;
        const w = size?.w ?? side / r.width, h = size?.h ?? side / r.height;
        place({ x: p.x - w / 2, y: p.y - h / 2, w, h });
      };
      // grows or shrinks the box around its middle, at most to the whole picture
      const resize = (f) => {
        if (!b) return;
        const r = ov.getBoundingClientRect();
        f = Math.min(f, 1 / b.w, 1 / b.h);
        if (f < 1 && Math.min(b.w * r.width, b.h * r.height) * f < 16) return;
        const w = b.w * f, h = b.h * f;
        place({ x: b.x - (w - b.w) / 2, y: b.y - (h - b.h) / 2, w, h }, true);
      };
      const ok = () => {
        if (!b) return;
        tools.remove();
        this.namePop(box, { add: true, box: b, t: time, back: () => adjusting(true) }, done);
      };
      const mark = (this.mark = {
        end: done,
        // keys while marking: true when used
        key: (e) => {
          if ($('#pop')) { // the name: Escape goes back to the box
            if (e.key !== 'Escape') return false;
            adjusting(true);
            return true;
          }
          const step = e.shiftKey ? 0.05 : 0.01;
          switch (e.key) {
            case 'Escape': done(); break;
            case 'Enter': ok(); break;
            case '+': case '=': resize(1.2); break;
            case '-': resize(1 / 1.2); break;
            case 'ArrowLeft': if (b) place({ ...b, x: b.x - step }); break;
            case 'ArrowRight': if (b) place({ ...b, x: b.x + step }); break;
            case 'ArrowUp': if (b) place({ ...b, y: b.y - step }); break;
            case 'ArrowDown': if (b) place({ ...b, y: b.y + step }); break;
            default: return false;
          }
          return true;
        },
      });
      $('[data-cancel]', ov).onclick = (e) => { e.stopPropagation(); done(); };
      tools.onclick = (e) => {
        const k = e.target.closest('[data-k]')?.dataset.k;
        if (k === '+') resize(1.2);
        if (k === '-') resize(1 / 1.2);
        if (k === 'ok') ok();
      };
      const pos = (e) => {
        const r = ov.getBoundingClientRect();
        return { x: clamp((e.clientX - r.left) / r.width, 0, 1), y: clamp((e.clientY - r.top) / r.height, 0, 1) };
      };
      // on the box, or a few pixels around it (a finger covers more than the edge)
      const onBox = (e) => {
        if (!b) return false;
        const r = ov.getBoundingClientRect(), p = pos(e), sx = 12 / r.width, sy = 12 / r.height;
        return p.x >= b.x - sx && p.x <= b.x + b.w + sx && p.y >= b.y - sy && p.y <= b.y + b.h + sy;
      };
      const draw = (a, c) => place({ x: Math.min(a.x, c.x), y: Math.min(a.y, c.y), w: Math.abs(a.x - c.x), h: Math.abs(a.y - c.y) });
      ov.onpointerdown = (e) => {
        if (e.target.closest('.v-mark-hint')) return;
        e.preventDefault(); e.stopPropagation();
        ov.setPointerCapture(e.pointerId);
        const p = pos(e);
        if (onBox(e)) { drag = { p, b0: b, move: true }; adjusting(true); return; }
        drag = { p, prev: b };
        $('#pop')?.remove();
        tools.remove();
        draw(p, p);
      };
      ov.onpointermove = (e) => {
        if (!drag) {
          if (e.pointerType === 'mouse') ov.style.cursor = onBox(e) ? 'move' : '';
          return;
        }
        e.stopPropagation();
        const p = pos(e);
        if (drag.move) place({ ...drag.b0, x: drag.b0.x + p.x - drag.p.x, y: drag.b0.y + p.y - drag.p.y });
        else draw(drag.p, p);
      };
      ov.onpointerup = ov.onpointercancel = (e) => {
        if (!drag) return;
        e.stopPropagation();
        const d = drag;
        drag = null;
        // a tap: the box goes there, keeping its size; the first one gets a size of its own
        if (!d.move && (b.w < 0.02 || b.h < 0.02)) around(d.p, d.prev);
        adjusting(true);
      };
      ov.addEventListener('wheel', (e) => {
        e.preventDefault(); e.stopPropagation();
        if (!b) return;
        if (!tools.isConnected) adjusting(true);
        resize(Math.exp(-e.deltaY * 0.0022));
      }, { passive: false });
      for (const ev of ['click', 'dblclick', 'touchstart']) ov.addEventListener(ev, (e) => e.stopPropagation());
      if (at) around(at);
      adjusting(!!at);
    },

    // asks who a face is. A tap on a person saves; a new name is typed only after "Neue
    // Person" (so on a phone the keyboard comes up only then; there is no search there, the
    // list scrolls instead). On a narrow screen a bottom sheet like the details: its grip sets
    // the height (the picture stays whole above it), a flick down closes it. face.add: a box
    // marked by hand (face.box, face.t, face.back: back to adjusting the box); onClose: called
    // when the question goes away.
    namePop(anchor, face, onClose) {
      if (!Account.need('user')) return;
      $('#pop')?.remove();
      // one entry per name; the group with the most photos gives the picture
      const people = new Map();
      for (const p of S.people) if (p.name && !(people.get(p.name)?.count >= p.count)) people.set(p.name, p);
      const names = [...people.keys()].sort((a, b) => a.localeCompare(b));
      const recent = store.get('recent-names', []).filter((n) => people.has(n)).slice(0, 4);
      const img = (n) => { const c = people.get(n).cover; return c ? `<img src="${faceURL(c.item, c.idx)}" alt="" loading="lazy">` : '<i></i>'; };
      const person = (n) => `<button type="button" data-name="${esc(n)}"${n === face.name ? ' class="cur"' : ''}>${img(n)}<span>${esc(n)}</span>${n === face.name ? icon('check') : ''}</button>`;
      const sheet = this.sheetMode.matches;
      const pop = document.createElement('div');
      pop.className = `pop name-pop${isTouch ? ' touch' : ''}${sheet ? ' sheet' : ''}${face.multi ? ' themed' : ''}`; // themed: over the page, not the viewer
      pop.id = 'pop';
      // the face asked about; a person in a selection: its picture, and in how many photos
      const m = face.multi;
      const pic = m ? faceURL(m.cover.item, m.cover.idx) : face.add ? '' : faceURL(this.it.id, face.idx);
      pop.innerHTML = `<div class="np-head"${sheet ? ` title="${t('Ziehen: Höhe ändern')}"` : ''}>${pic ? `<img src="${pic}" alt="">` : ''}<div><b>${face.name ? t('Ist das {name}?', { name: esc(face.name) }) : t('Wer ist das?')}</b>${m ? `<small>${pl(m.items, 'in {n} Foto', 'in {n} Fotos')}</small>` : ''}</div>${sheet ? '' : `<button type="button" class="btn ghost icon np-x" data-a="cancel" title="${t('Schließen')}">${icon('x')}</button>`}</div>
        ${recent.length ? `<div class="np-recent">${recent.map((n) => `<button type="button" data-name="${esc(n)}">${img(n)}<span>${esc(n)}</span></button>`).join('')}</div>` : ''}
        ${names.length && !isTouch ? `<input type="search" class="np-search" placeholder="${t('Person suchen')}" autocomplete="off" enterkeyhint="done">` : ''}
        ${names.length ? `<div class="np-list">${names.map(person).join('')}<button type="button" class="np-typed" data-a="typed" hidden>${icon('plus')}<span></span></button></div>` : ''}
        <button type="button" class="np-add" data-a="new">${icon('plus')}<span>${t('Neue Person …')}</span></button>
        <form class="np-form" hidden><input class="np-name" placeholder="${t('Name der neuen Person')}" autocomplete="off" enterkeyhint="done">
          <div class="row"><button class="primary">${t('Speichern')}</button><button type="button" data-a="back">${t('Zurück')}</button></div></form>
        ${face.add ? `<div class="row np-acts"><button type="button" data-a="noname">${t('Ohne Namen speichern')}</button><button type="button" data-a="cancel">${t('Abbrechen')}</button></div>`
          : `<div class="row np-acts"><button type="button" data-a="not">${face.person ? t('Nicht diese Person') : t('Ausblenden')}</button>
            <button type="button" data-a="noface" title="${t('Falsch erkannt: kein Gesicht. Wird nicht mehr gezeigt, andere Gesichter bleiben.')}">${icon('face-x')}<span>${t('Kein Gesicht')}</span></button></div>`}`;
      // a sheet in the viewer shares it with the picture; one for a selection lies over the page
      (sheet && !m ? this.el : document.body).appendChild(pop);
      const search = $('.np-search', pop), list = $('.np-list', pop), typed = $('.np-typed', pop), recentEl = $('.np-recent', pop);
      const form = $('.np-form', pop), nameIn = $('.np-name', pop);
      const adding = () => pop.classList.contains('adding');
      // the sheet's height, remembered per device class like the details'
      const sheetKey = `name-sheet-${devClass()}`;
      let sheetH = innerHeight * clamp(+store.get(sheetKey, 0.55) || 0.55, 0.2, 0.92);
      // below the face; no room: above it, else beside it; above the keyboard while it is up.
      // On a touch screen as tall as the larger room above or below the face allows, the list
      // scrolling inside (too little room: the whole screen). The sheet: its height, above the
      // keyboard; the picture gets the room above it.
      const position = () => {
        if (!pop.isConnected) return;
        const vv = window.visualViewport;
        if (sheet) {
          const kb = vv ? Math.max(0, innerHeight - vv.height - vv.offsetTop) : 0;
          const h = Math.round(clamp(sheetH, 0, innerHeight - kb - 56));
          pop.style.height = `${h}px`;
          pop.style.bottom = `${kb}px`;
          this.el.style.setProperty('--nsheet', `${h + kb}px`);
          return;
        }
        // the face with its name label under it: the question does not cover the name
        const r0 = anchor.getBoundingClientRect(), lab = anchor.matches?.('.v-face') ? $(':scope > span', anchor)?.getBoundingClientRect() : null;
        const r = lab ? { left: r0.left, right: Math.max(r0.right, lab.right), top: r0.top, bottom: Math.max(r0.bottom, lab.bottom), width: r0.width } : r0;
        const top0 = vv ? vv.offsetTop : 0, h = vv ? vv.height : innerHeight, pw = pop.offsetWidth;
        const below = top0 + h - 8 - (r.bottom + 8), above = r.top - 8 - (top0 + 8);
        if (isTouch) {
          const room = Math.max(below, above);
          pop.style.maxHeight = `${room >= 260 ? room : h - 16}px`;
        }
        const ph = pop.offsetHeight;
        let top = r.bottom + 8, left = r.left + r.width / 2 - pw / 2;
        if (ph > below) {
          if (ph <= above) top = r.top - 8 - ph;
          else if (r.right + 8 + pw <= innerWidth - 8) [left, top] = [r.right + 8, r.top];
          else if (r.left - 8 - pw >= 8) [left, top] = [r.left - 8 - pw, r.top];
        }
        pop.style.left = `${clamp(left, 8, innerWidth - pw - 8)}px`;
        pop.style.top = `${clamp(top, top0 + 8, top0 + h - ph - 8)}px`;
      };
      pop.position = position;
      position();
      if (sheet) {
        gripDrag($('.np-head', pop), () => pop.offsetHeight, (h) => { sheetH = h; position(); }, (now, h, v) => {
          if (sheetGone(now, h, v)) { dismiss(); return; }
          sheetH = Math.max(now, 200); // a lower one would hide the list
          position();
          store.set(sheetKey, Math.round((sheetH / innerHeight) * 1000) / 1000);
        });
      }
      // the people of the list whose name contains q. While a new name is typed: only those,
      // as a hint (none: no list); in the search: also the typed name as a new person.
      const filter = (q) => {
        const ql = q.toLowerCase();
        let any = false, exact = false;
        for (const b of list ? $$('[data-name]', list) : []) {
          const n = b.dataset.name.toLowerCase();
          b.hidden = !n.includes(ql);
          any ||= !b.hidden;
          exact ||= n === ql;
        }
        // the person Enter in the search takes, marked so the choice is never a surprise:
        // the exact name, else the first match
        const shown = list ? $$('[data-name]', list).filter((b) => !b.hidden) : [];
        const hit = q && search && !adding() ? shown.find((b) => b.dataset.name.toLowerCase() === ql) || shown[0] : null;
        for (const b of shown) b.classList.toggle('hit', b === hit);
        hit?.scrollIntoView({ block: 'nearest' });
        if (typed) {
          typed.hidden = !q || exact || adding();
          $('span', typed).textContent = t('„{name}“ als neue Person', { name: q });
        }
        if (list) list.hidden = adding() && (!q || !any);
        if (recentEl) recentEl.hidden = !!q;
        if (adding()) position();
      };
      const setAdding = (on) => {
        pop.classList.toggle('adding', on);
        form.hidden = !on;
        if (on) {
          nameIn.value = search?.value.trim() || '';
          nameIn.focus();
        }
        filter(on ? nameIn.value.trim() : search?.value.trim() || '');
        position();
        if (!on && search && !isTouch) search.focus();
      };
      // the keys go to it (Escape, arrows), also without a search field
      pop.tabIndex = -1;
      if (search && !isTouch) search.focus();
      else pop.focus({ preventScroll: true });
      const close = () => { pop.remove(); onClose?.(); };
      // swiped away: a box marked by hand can still be adjusted
      const dismiss = () => { if (face.back) { pop.remove(); face.back(); } else close(); };
      // a press beside it puts it away (a box marked by hand: back to the box)
      const off = (e) => {
        if (pop.isConnected && (pop.contains(e.target) || e.target.closest('.sp'))) return;
        removeEventListener('pointerdown', off, true);
        if (pop.isConnected) dismiss();
      };
      addEventListener('pointerdown', off, true);
      const save = (name) => { close(); this.saveFace(face, name); };
      // the people shown and the new-person entry, for the arrow keys
      const choices = () => $$('[data-name], .np-typed', pop).filter((b) => !b.hidden && !b.closest('[hidden]'));
      if (search) {
        search.oninput = () => filter(search.value.trim());
        search.onkeydown = (e) => {
          e.stopPropagation();
          if (e.key === 'Escape') close();
          if (e.key === 'ArrowDown') { e.preventDefault(); choices().find((b) => b.parentNode === list)?.focus(); }
          if (e.key === 'Enter') {
            const q = search.value.trim(), hit = $('[data-name].hit:not([hidden])', list);
            if (q) save(hit ? hit.dataset.name : q);
          }
        };
      }
      nameIn.oninput = () => filter(nameIn.value.trim());
      nameIn.onkeydown = (e) => {
        e.stopPropagation();
        if (e.key === 'Escape') setAdding(false);
      };
      form.onsubmit = (e) => {
        e.preventDefault();
        const name = nameIn.value.trim();
        if (name) save(name);
        else nameIn.focus();
      };
      pop.onkeydown = (e) => {
        if (e.key === 'Escape' && !adding()) { e.stopPropagation(); dismiss(); return; }
        if (e.key !== 'ArrowDown' && e.key !== 'ArrowUp') return;
        const bs = choices(), i = bs.indexOf(document.activeElement);
        if (i < 0) return;
        e.preventDefault();
        e.stopPropagation();
        if (i === 0 && e.key === 'ArrowUp' && search) search.focus();
        else bs[clamp(i + (e.key === 'ArrowDown' ? 1 : -1), 0, bs.length - 1)].focus();
      };
      pop.onclick = (e) => {
        const n = e.target.closest('[data-name]')?.dataset.name;
        if (n) { save(n); return; }
        const a = e.target.closest('[data-a]')?.dataset.a;
        if (a === 'new') setAdding(true);
        if (a === 'back') setAdding(false);
        if (a === 'typed') save(search.value.trim());
        if (a === 'noname' || a === 'not') save('');
        if (a === 'cancel') close();
        if (a === 'noface') save('-');
      };
    },

    // --- zoom & gestures (transform-origin 0 0: screen = layout box + translate + scale)
    resetZoom(animate = false) {
      this.zoom = { s: 1, x: 0, y: 0 };
      if (animate) this.animateZoom();
      this.applyZoom();
    },
    // the next transform change glides instead of jumping
    animateZoom() {
      const m = this.media;
      m.style.transition = 'transform .22s cubic-bezier(.2, .8, .2, 1), opacity .22s';
      clearTimeout(this.animT);
      this.animT = setTimeout(() => { m.style.transition = ''; }, 260);
    },
    // stage and untransformed media box in screen pixels (offsets ignore transforms)
    geom() {
      const st = this.stage.getBoundingClientRect(), m = this.media;
      return { sx: st.left, sy: st.top, sw: st.width, sh: st.height, bx: st.left + m.offsetLeft, by: st.top + m.offsetTop, w: m.offsetWidth, h: m.offsetHeight };
    },
    // keeps the picture on the stage: centred while smaller, no empty margin where it could fill the stage
    clampZoom() {
      const z = this.zoom, g = this.geom();
      const fit = (pos, base, size, s0, ss) => {
        const sz = size * z.s;
        return sz <= ss ? s0 + (ss - sz) / 2 - base : clamp(pos, s0 + ss - sz - base, s0 - base);
      };
      z.x = fit(z.x, g.bx, g.w, g.sx, g.sw);
      z.y = fit(z.y, g.by, g.h, g.sy, g.sh);
    },
    applyZoom() {
      const z = this.zoom;
      if (z.s === 1) z.x = z.y = 0;
      else this.clampZoom();
      this.media.style.transform = z.s === 1 ? '' : `translate(${z.x}px, ${z.y}px) scale(${z.s})`;
      this.stage.style.cursor = z.s > 1 ? 'grab' : '';
    },
    // scales by ns around the screen point (cx, cy) and moves that point to (tx, ty); pinches may dip below 1 and snap back
    zoomAt(cx, cy, ns, tx = cx, ty = cy, min = 1) {
      const z = this.zoom, g = this.geom();
      const px = (cx - g.bx - z.x) / z.s, py = (cy - g.by - z.y) / z.s;
      ns = clamp(ns, min, 8);
      if (ns === 1 && min === 1) { this.resetZoom(); return; }
      z.x = tx - g.bx - px * ns;
      z.y = ty - g.by - py * ns;
      z.s = ns;
      this.applyZoom();
    },

    async toggleFav() {
      if (!Account.need('user')) return;
      const it = this.it;
      it.f = !it.f;
      await post('/api/fav', { ids: [it.id], on: it.f });
      $('#vFav').classList.toggle('on', it.f);
      $('#vFav').innerHTML = icon('heart', it.f ? 'fill' : '');
      toast(it.f ? t('Zu Favoriten hinzugefügt') : t('Aus Favoriten entfernt'));
      updateCounts();
      scheduleRerender();
    },

    startSlides() {
      this.stopSlides();
      $('#vPlay').innerHTML = icon('pause');
      $('#vPlay').classList.add('on');
      this.slideT = setInterval(() => {
        if (this.i >= this.list.length - 1) { this.stopSlides(); return; }
        this.media.style.transition = 'opacity .5s';
        this.media.style.opacity = 0;
        setTimeout(() => { this.step(1); this.media.style.opacity = 1; setTimeout(() => (this.media.style.transition = ''), 500); }, 450);
      }, 4500);
    },
    stopSlides() {
      clearInterval(this.slideT);
      this.slideT = 0;
      $('#vPlay').innerHTML = icon('play');
      $('#vPlay').classList.remove('on');
    },
  };

  {
    const V = Viewer;
    $('#vClose').onclick = () => V.close();
    $('#vPrev').onclick = () => V.step(-1);
    $('#vNext').onclick = () => V.step(1);
    $('#vInfo').onclick = () => { V.showInfo = !V.showInfo; store.set('info', V.showInfo); V.renderInfo(); };
    $('#vFaces').onclick = () => { V.showFaces = !V.showFaces; V.drawFaces(); };
    $('#vFav').onclick = () => V.toggleFav();
    $('#vPlay').onclick = () => (V.slideT ? V.stopSlides() : V.startSlides());
    $('#vPrint').onclick = () => printItem(V.it, V.detail);
    $('#vStrip').onclick = (e) => { const b = e.target.closest('[data-j]'); if (b) { V.i = +b.dataset.j; V.step(0); } };
    const live = $('#vLive');
    live.onpointerenter = () => V.playLive(false);
    live.onclick = () => V.playLive(false);

    // the picture fits the stage: its size follows the stage (details panel, sheet height, rotation)
    new ResizeObserver(() => {
      const r = V.stage.getBoundingClientRect();
      if (!r.width || !r.height) return;
      V.el.style.setProperty('--sw', `${Math.round(r.width)}px`);
      V.el.style.setProperty('--sh', `${Math.round(r.height)}px`);
      if (V.zoom.s !== 1) V.applyZoom();
    }).observe(V.stage);
    V.sheetMode.addEventListener('change', () => { if (V.sheetMode.matches && V.showInfo) V.restoreSheet(); });
    addEventListener('resize', () => { if (V.sheetMode.matches && V.showInfo) V.setSheet(V.sheetH()); });

    // sheet grip: drag to any height; a short sheet or a quick flick down closes the details
    gripDrag($('#vGrip'), () => V.sheetH(), (h) => V.setSheet(h), (now, h, v) => {
      if (sheetGone(now, h, v)) {
        // closed: the next opening starts at the last kept height
        V.setSheet(h);
        V.showInfo = false;
        store.set('info', false);
        V.renderInfo();
        return;
      }
      store.set(`sheet-${devClass()}`, Math.round((now / innerHeight) * 1000) / 1000);
    });

    // idle: the mouse shows the controls. They stay while it is on the picture or on them, and
    // fade 2.6 s after it left (beside the picture, out of the window); on touch a tap toggles them.
    const onContent = (x, y) => {
      const r = V.media.getBoundingClientRect();
      return (x >= r.left && x <= r.right && y >= r.top && y <= r.bottom) || !!V.el.querySelector('.v-top:hover, .v-strip:hover, .v-nav:hover');
    };
    const fadeLater = () => {
      clearTimeout(V.idleT);
      V.idleT = setTimeout(() => V.el.classList.add('idle'), 2600);
    };
    V.el.addEventListener('pointermove', (e) => {
      if (e.pointerType !== 'mouse') return;
      V.el.classList.remove('idle');
      if (onContent(e.clientX, e.clientY)) clearTimeout(V.idleT);
      else fadeLater();
    });
    V.el.addEventListener('pointerleave', (e) => { if (e.pointerType === 'mouse') fadeLater(); });

    // wheel zoom
    V.stage.addEventListener('wheel', (e) => {
      if (V.it?.k !== 1) return;
      e.preventDefault();
      V.zoomAt(e.clientX, e.clientY, V.zoom.s * Math.exp(-e.deltaY * 0.0022));
    }, { passive: false });

    // pointer gestures: pan (zoomed), pinch, swipe (fitted), taps
    const pts = new Map();
    let start = null, pinch = null, moved = false, multi = false, held = false, menued = false, lastTap = null, tapT = 0, longT = 0, lastType = 'mouse';
    V.stage.addEventListener('dblclick', (e) => {
      if (V.it?.k !== 1 || lastType !== 'mouse') return; // touch double taps are handled in end()
      V.animateZoom();
      V.zoom.s > 1 ? V.resetZoom() : V.zoomAt(e.clientX, e.clientY, 2.5);
    });
    V.stage.addEventListener('pointerdown', (e) => {
      lastType = e.pointerType;
      if (e.target.closest('video[controls]') || e.target.closest('.v-face')) return;
      V.stage.setPointerCapture(e.pointerId);
      pts.set(e.pointerId, { x: e.clientX, y: e.clientY });
      V.media.style.transition = '';
      if (pts.size === 1) {
        moved = multi = held = menued = false;
        start = { x: e.clientX, y: e.clientY, zx: V.zoom.x, zy: V.zoom.y };
        // a long press: the face menu while the face boxes are shown, else a Live Photo plays
        const x = e.clientX, y = e.clientY;
        if (e.button === 0) {
          longT = setTimeout(() => {
            if (V.faceMenu(x, y, lastType !== 'mouse')) held = menued = true;
            else if (V.it?.lv) { held = true; V.playLive(false); }
          }, 450);
        }
      } else if (pts.size === 2) {
        const [a, b] = [...pts.values()];
        multi = moved = true;
        clearTimeout(longT);
        clearTimeout(tapT);
        lastTap = null;
        if (V.zoom.s === 1) { V.media.style.transform = ''; V.media.style.opacity = ''; } // drop a started swipe
        pinch = V.it?.k === 1 ? { d: Math.hypot(a.x - b.x, a.y - b.y) || 1, cx: (a.x + b.x) / 2, cy: (a.y + b.y) / 2 } : null;
      }
    });
    V.stage.addEventListener('pointermove', (e) => {
      if (!pts.has(e.pointerId)) return;
      pts.set(e.pointerId, { x: e.clientX, y: e.clientY });
      if (pts.size >= 2) {
        if (!pinch) return;
        const [a, b] = [...pts.values()];
        const d = Math.hypot(a.x - b.x, a.y - b.y) || 1, cx = (a.x + b.x) / 2, cy = (a.y + b.y) / 2;
        // the point between the fingers stays between the fingers
        V.zoomAt(pinch.cx, pinch.cy, V.zoom.s * d / pinch.d, cx, cy, 0.8);
        pinch = { d, cx, cy };
        return;
      }
      if (!start || menued) return; // the menu is open: the finger lifting off must not swipe
      const dx = e.clientX - start.x, dy = e.clientY - start.y;
      if (!moved && Math.abs(dx) + Math.abs(dy) > 8) { moved = true; clearTimeout(longT); }
      if (!moved) return;
      if (V.zoom.s > 1) {
        V.zoom.x = start.zx + dx;
        V.zoom.y = start.zy + dy;
        V.applyZoom();
      } else if (!multi && V.zoom.s === 1) {
        // fitted picture: it is not panned, only previews the swipe (sideways: neighbours, down: close)
        const side = Math.abs(dx) >= Math.abs(dy);
        V.media.style.transform = side ? `translateX(${dx}px)` : `translateY(${Math.max(0, dy) * 0.6}px)`;
        V.media.style.opacity = side ? '' : String(1 - Math.min(0.6, Math.max(0, dy) / 600));
      }
    });
    const end = (e) => {
      if (!pts.delete(e.pointerId)) return;
      clearTimeout(longT);
      if (pts.size === 1) {
        // one finger of a pinch lifted: go on panning from where the other finger is
        const [p] = [...pts.values()];
        start = { x: p.x, y: p.y, zx: V.zoom.x, zy: V.zoom.y };
        pinch = null;
        return;
      }
      if (pts.size) return;
      pinch = null;
      if (V.zoom.s !== 1 && V.zoom.s < 1.1) V.resetZoom(true); // nearly fitted: snap back
      if (V.zoom.s === 1 && start && moved) {
        const dx = e.clientX - start.x, dy = e.clientY - start.y;
        const swipe = !multi && e.type !== 'pointercancel';
        if (swipe && dy > 120 && dy > Math.abs(dx)) { V.media.style.transform = ''; V.media.style.opacity = ''; V.close(); }
        else if (swipe && Math.abs(dx) > 70 && Math.abs(dx) > Math.abs(dy)) { V.media.style.transform = ''; V.media.style.opacity = ''; V.step(dx < 0 ? 1 : -1); }
        else { V.animateZoom(); V.media.style.transform = ''; V.media.style.opacity = ''; }
      }
      if (start && !moved && !multi && !held && e.type !== 'pointercancel' && e.pointerType !== 'mouse') {
        // touch: one tap toggles the controls (a little later, so a double tap does not), two taps zoom
        const now = Date.now(), x = e.clientX, y = e.clientY;
        clearTimeout(tapT);
        if (lastTap && now - lastTap.t < 320 && Math.hypot(x - lastTap.x, y - lastTap.y) < 40) {
          lastTap = null;
          if (V.it?.k === 1) { V.animateZoom(); V.zoom.s > 1 ? V.resetZoom() : V.zoomAt(x, y, 2.5); }
        } else {
          lastTap = { t: now, x, y };
          tapT = setTimeout(() => { lastTap = null; V.el.classList.toggle('idle'); }, 280);
        }
      }
      start = null;
    };
    V.stage.addEventListener('pointerup', end);
    V.stage.addEventListener('pointercancel', end);
    // a right click opens the face menu; on touch the long press above does, the browser's menu stays away
    V.stage.addEventListener('contextmenu', (e) => {
      if (!V.showFaces || V.it?.k !== 1 || !Account.can('user')) return;
      e.preventDefault();
      if (lastType === 'mouse') V.faceMenu(e.clientX, e.clientY);
    });

    // Videos keep their own controls (tap to play, seek bar). A sideways swipe on the
    // picture still goes to the neighbours and a swipe down closes, also while playing;
    // touches on the control bar at the bottom are left alone.
    let vs = null;
    const vreset = (animate) => {
      if (animate) V.animateZoom();
      V.media.style.transform = '';
      V.media.style.opacity = '';
    };
    V.stage.addEventListener('pointerdown', (e) => {
      const v = e.target.closest('video[controls]');
      vs = null;
      if (!v || e.pointerType === 'mouse') return;
      if (e.clientY > v.getBoundingClientRect().bottom - 72) return;
      vs = { id: e.pointerId, x: e.clientX, y: e.clientY, dir: '' };
    });
    V.stage.addEventListener('pointermove', (e) => {
      if (!vs || e.pointerId !== vs.id) return;
      const dx = e.clientX - vs.x, dy = e.clientY - vs.y;
      if (!vs.dir && Math.abs(dx) > 12 && Math.abs(dx) > 1.5 * Math.abs(dy)) vs.dir = 'x';
      else if (!vs.dir && dy > 16 && dy > 1.5 * Math.abs(dx)) vs.dir = 'y';
      if (vs.dir === 'x') V.media.style.transform = `translateX(${dx}px)`;
      else if (vs.dir === 'y') {
        V.media.style.transform = `translateY(${Math.max(0, dy) * 0.6}px)`;
        V.media.style.opacity = String(1 - Math.min(0.6, Math.max(0, dy) / 600));
      }
    });
    const vend = (e) => {
      if (!vs || e.pointerId !== vs.id) return;
      const g = vs;
      vs = null;
      if (!g.dir) return;
      const dx = e.clientX - g.x, dy = e.clientY - g.y;
      if (e.type === 'pointerup' && g.dir === 'x' && Math.abs(dx) > 70) { vreset(); V.step(dx < 0 ? 1 : -1); }
      else if (e.type === 'pointerup' && g.dir === 'y' && dy > 120) { vreset(); V.close(); }
      else vreset(true);
    };
    V.stage.addEventListener('pointerup', vend);
    V.stage.addEventListener('pointercancel', vend);
  }

  // printing: the picture alone in a hidden frame, as large as the page
  // allows, landscape pictures on landscape pages. The server sends a JPEG
  // turned like in the viewer (browsers print neither HEIC nor RAW).
  function printItem(it, d) {
    if (!it || it.k === 2) return;
    $('#printFrame')?.remove();
    toast(t('Druck wird vorbereitet …'));
    const f = document.createElement('iframe');
    f.id = 'printFrame';
    f.setAttribute('aria-hidden', 'true');
    f.tabIndex = -1;
    f.style.cssText = 'position:fixed;right:0;bottom:0;width:0;height:0;border:0';
    f.srcdoc = `<!doctype html><meta charset="utf-8"><title>${esc(d?.name || 'Schuhkarton')}</title>
<style>@page{margin:8mm}html,body{margin:0;height:100%;overflow:hidden}img{display:block;width:100%;height:100%;object-fit:contain}</style>
<img src="/print/${encodeURIComponent(it.id)}?v=${encodeURIComponent(it.v)}" alt="">`;
    f.onload = () => {
      const w = f.contentWindow, doc = f.contentDocument, img = doc?.querySelector('img');
      if (!img?.naturalWidth) { toast(t('Drucken fehlgeschlagen')); f.remove(); return; }
      if (img.naturalWidth > img.naturalHeight) doc.head.insertAdjacentHTML('beforeend', '<style>@page{size:landscape}</style>');
      w.addEventListener('afterprint', () => setTimeout(() => f.remove(), 100));
      w.focus();
      w.print();
    };
    document.body.appendChild(f);
  }

  // ---------------------------------------------------------------------------
  // keyboard
  // ---------------------------------------------------------------------------
  addEventListener('keydown', (e) => {
    const typing = isTyping(e.target);
    if (Viewer.open_) {
      if (typing) return;
      const V = Viewer;
      if (V.mark?.key(e)) { e.preventDefault(); return; }
      switch (e.key) {
        case 'Escape': $('#pop') ? $('#pop').remove() : V.close(); break;
        case 'ArrowRight': V.step(1); break;
        case 'ArrowLeft': V.step(-1); break;
        case 'i': case 'I': $('#vInfo').click(); break;
        case 'f': case 'F': V.toggleFav(); break;
        case 'g': case 'G': $('#vFaces').click(); break;
        case 'd': case 'D': location.href = `/o/${V.it.id}?dl=1`; break;
        case 'p': case 'P': if (V.it.k === 2) return; printItem(V.it, V.detail); break; // Ctrl+P too: the picture, not the page
        case ' ': e.preventDefault(); V.slideT ? V.stopSlides() : V.startSlides(); break;
        case '+': case '=': V.zoomAt(innerWidth / 2, innerHeight / 2, V.zoom.s * 1.4); break;
        case '-': V.zoomAt(innerWidth / 2, innerHeight / 2, V.zoom.s / 1.4); break;
        case '0': V.resetZoom(); break;
        case 'v': case 'V': FS.toggle(); break;
        default: return;
      }
      e.preventDefault();
      return;
    }
    if (typing) return;
    if (e.key === '/') { e.preventDefault(); $('#q').focus(); $('#q').select(); }
    if ((e.key === 'v' || e.key === 'V') && !e.ctrlKey && !e.metaKey && !e.altKey) FS.toggle();
    if (e.key === 'Escape' && $('#pop')) { $('#pop').remove(); return; } // a popup first, the selection stays
    if (e.key === 'Escape') { ViewMenu.close(); if (Filter.open_) Filter.close(); if (S.sel.size || S.selecting) endSelection(); $('#side').classList.remove('open'); }
  });

  // ---------------------------------------------------------------------------
  // search box with suggestions
  // ---------------------------------------------------------------------------
  {
    const q = $('#q'), box = $('#suggest');
    let opts = [], on = -1;
    const TYPES = { ort: 'Ort', person: 'Person', kategorie: 'Kategorie', kamera: 'Kamera', jahr: 'Jahr', monat: 'Monat', wochentag: 'Wochentag', jahreszeit: 'Jahreszeit', tageszeit: 'Tageszeit', tag: 'Stichwort' };
    const SYNTAX = EN ? ['place:', 'person:', 'year:2019', 'month:june', 'type:video', 'type:selfie', 'text:', '-word'] : ['ort:', 'person:', 'jahr:2019', 'monat:juni', 'typ:video', 'typ:selfie', 'text:', '-wort'];
    const draw = () => {
      if (!q.value.trim()) {
        box.innerHTML = `<div class="hint">${t('Suche in Ort, Personen, Datum, Ordnern, Kamera und im {b}.', { b: `<b>${t('Text in Bildern')}</b>` })}<br>
          ${t('Filter')}: ${SYNTAX.map((x) => `<code>${x}</code>`).join(' ')}</div>`;
      } else {
        box.innerHTML = opts.map((o, i) => `<button data-i="${i}" class="${i === on ? 'on' : ''}"><span class="t">${esc(TYPES[o.type] ? t(TYPES[o.type]) : o.type)}</span>${esc(o.label)}<span class="n">${o.count ? num(o.count) : ''}</span></button>`).join('')
          + `<button data-i="-1" class="${on === opts.length ? 'on' : ''}"><span class="t">${t('Suche')}</span>${t('„{q}“ überall', { q: esc(q.value) })}</button>`;
      }
      box.hidden = false;
    };
    const fetchS = debounce(async () => {
      const v = q.value.trim();
      if (!v) { opts = []; draw(); return; }
      const last = v.split(/\s+/).pop();
      opts = (await api(`/api/suggest?q=${encodeURIComponent(last)}&lang=${LANG}`).catch(() => [])) || [];
      on = -1;
      if (document.activeElement === q) draw();
    }, 120);
    const go = (query) => {
      box.hidden = true;
      q.blur();
      if (query.trim()) location.hash = `#/suche/${encodeURIComponent(query.trim())}`;
    };
    const pick = (i) => {
      if (i < 0 || i >= opts.length) { go(q.value); return; }
      const words = q.value.trim().split(/\s+/);
      words.pop();
      words.push(opts[i].query);
      q.value = words.join(' ');
      go(q.value);
    };
    q.addEventListener('input', fetchS);
    q.addEventListener('focus', () => { fetchS(); draw(); });
    q.addEventListener('blur', () => setTimeout(() => (box.hidden = true), 150));
    q.addEventListener('keydown', (e) => {
      if (e.key === 'ArrowDown') { on = Math.min(on + 1, opts.length); draw(); e.preventDefault(); }
      else if (e.key === 'ArrowUp') { on = Math.max(on - 1, -1); draw(); e.preventDefault(); }
      else if (e.key === 'Enter') { pick(on); e.preventDefault(); }
      else if (e.key === 'Escape') { box.hidden = true; q.blur(); }
    });
    box.addEventListener('mousedown', (e) => {
      const b = e.target.closest('[data-i]');
      if (b) { e.preventDefault(); pick(+b.dataset.i); }
    });
  }

  // ---------------------------------------------------------------------------
  // routing
  // ---------------------------------------------------------------------------
  let route = { view: 'fotos', arg: '' };
  let cleanup = [];
  const extraGalleries = [];

  function parseHash() {
    const h = decodeURIComponent(location.hash.slice(1) || '/fotos');
    const [path, item] = h.split('|');
    const parts = path.replace(/^\/+/, '').split('/');
    return { view: parts[0] || 'fotos', arg: parts.slice(1).join('/'), item, path };
  }
  function routeBase() {
    const h = location.hash.slice(1) || '/fotos';
    return h.split('|')[0];
  }
  function currentQuery() { return route.view === 'suche' ? route.arg : ''; }

  function onRoute() {
    const r = parseHash();
    const same = r.view === route.view && r.arg === route.arg && S.gallery;
    route = r;
    if (!r.item && Viewer.open_) Viewer.close(true);
    if (!same) render();
    if (r.item && !Viewer.open_) {
      const list = S.gallery?.items || S.items;
      const i = list.findIndex((it) => it.id === r.item);
      if (i >= 0) Viewer.open(list, i, false);
      else if (S.byId.has(r.item)) Viewer.open([S.byId.get(r.item)], 0, false);
    }
  }
  addEventListener('hashchange', onRoute);
  addEventListener('popstate', () => { if (!parseHash().item && Viewer.open_) Viewer.close(true); });

  function teardown() {
    for (const fn of cleanup) fn();
    cleanup = [];
    S.gallery?.destroy();
    S.gallery = null;
    for (const g of extraGalleries.splice(0)) g.destroy();
    Scrub.detach();
  }

  function render() {
    teardown();
    const v = route.view;
    for (const a of $$('[data-v]')) a.classList.toggle('on', a.dataset.v === v || (v === 'ort' && a.dataset.v === 'orte') || ((v === 'person' || v === 'ausgeblendet') && a.dataset.v === 'personen'));
    $('#side').classList.remove('open');
    if (v !== 'suche') $('#q').value = '';
    const view = $('#view');
    view.innerHTML = '';
    scrollTo({ top: 0 });
    const fn = VIEWS[v] || VIEWS.fotos;
    fn(view, route.arg);
  }
  function rerender() {
    const y = scrollY;
    render();
    scrollTo({ top: y });
  }
  const scheduleRerender = debounce(() => { if (!Viewer.open_) rerender(); }, 400);

  // ---------------------------------------------------------------------------
  // filters: clickable for everyone, typed syntax for power users
  // ---------------------------------------------------------------------------
  // filter keys are kept in German inside; queries use the syntax of the page language
  const FKEYS = { person: 'Person', ort: 'Ort', jahr: 'Jahr', monat: 'Monat', jahreszeit: 'Jahreszeit', wochentag: 'Wochentag', tag: 'Tag',
    tageszeit: 'Tageszeit', typ: 'Art', kamera: 'Kamera', kategorie: 'Kategorie', album: 'Ordner', text: 'Text im Bild', datum: 'Datum', datei: 'Datei' };
  const KEY_ALIAS = { wer: 'person', who: 'person', ordner: 'album', folder: 'album', place: 'ort', year: 'jahr', month: 'monat', season: 'jahreszeit',
    weekday: 'wochentag', day: 'tag', time: 'tageszeit', zeit: 'tageszeit', type: 'typ', kind: 'typ', camera: 'kamera', category: 'kategorie', date: 'datum', file: 'datei' };
  const KEY_EN = { ort: 'place', jahr: 'year', monat: 'month', jahreszeit: 'season', wochentag: 'weekday', tag: 'day', tageszeit: 'time', typ: 'type',
    kamera: 'camera', kategorie: 'category', album: 'folder', datum: 'date', datei: 'file' };
  const KIND_LABEL = { foto: 'Fotos', video: 'Videos', live: 'Live Photos', selfie: 'Selfies', screenshot: 'Screenshots', panorama: 'Panoramen', text: 'Mit Text im Bild', notiz: 'Mit Notiz', gps: 'Mit Ortsangabe', favorit: 'Favoriten' };
  // words of the date and kind filters: German (canonical) → English
  const WORD_EN = {
    ...Object.fromEntries(MONTHS_DE.map((m, i) => [m.toLowerCase(), ['january', 'february', 'march', 'april', 'may', 'june', 'july', 'august', 'september', 'october', 'november', 'december'][i]])),
    ...Object.fromEntries(DAYS_DE.map((d, i) => [d.toLowerCase(), ['sunday', 'monday', 'tuesday', 'wednesday', 'thursday', 'friday', 'saturday'][i]])),
    frühling: 'spring', sommer: 'summer', herbst: 'autumn', winter: 'winter', wochenende: 'weekend', werktag: 'workday',
    morgens: 'morning', mittags: 'noon', nachmittags: 'afternoon', abends: 'evening', nachts: 'night',
    foto: 'photo', video: 'video', live: 'live', selfie: 'selfie', screenshot: 'screenshot', panorama: 'panorama', text: 'text', notiz: 'note', gps: 'gps', favorit: 'favorite',
  };
  const WORD_DE = Object.fromEntries(Object.entries(WORD_EN).map(([de, en]) => [en, de]));
  Object.assign(WORD_DE, { fall: 'herbst', weekday: 'werktag', midday: 'mittags', photos: 'foto', favourite: 'favorit', favorites: 'favorit', fruhling: 'frühling', marz: 'märz' });
  const WORD_KEYS = new Set(['monat', 'jahreszeit', 'wochentag', 'tag', 'tageszeit', 'typ']);
  const WORD_LABEL = { frühling: 'Frühling', sommer: 'Sommer', herbst: 'Herbst', winter: 'Winter', wochenende: 'Wochenende', werktag: 'Werktag',
    morgens: 'Morgens', mittags: 'Mittags', nachmittags: 'Nachmittags', abends: 'Abends', nachts: 'Nachts' };
  const cap = (s) => s.charAt(0).toUpperCase() + s.slice(1);
  // canonical (German, lower case) value of a filter word: "June" → "juni"
  const canonVal = (key, v) => { const l = v.toLowerCase(); return WORD_KEYS.has(key) ? WORD_DE[l] || l : l; };
  const wordLabel = (cv) => {
    const mi = MONTHS_DE.findIndex((m) => m.toLowerCase() === cv);
    if (mi >= 0) return MONTHS[mi];
    const di = DAYS_DE.findIndex((d) => d.toLowerCase() === cv);
    if (di >= 0) return DAYS[di];
    return WORD_LABEL[cv] ? t(WORD_LABEL[cv]) : cap(cv);
  };

  function tokenizeQ(q) {
    const out = [];
    let cur = '', inQ = false;
    for (const ch of q) {
      if (ch === '"') inQ = !inQ;
      else if (/\s/.test(ch) && !inQ) { if (cur) { out.push(cur); cur = ''; } }
      else cur += ch;
    }
    if (cur) out.push(cur);
    return out;
  }
  function parseQ(q) {
    const filters = [], words = [];
    for (let tok of tokenizeQ(q || '')) {
      let neg = false;
      if (tok.startsWith('-') && tok.length > 1) { neg = true; tok = tok.slice(1); }
      const i = tok.indexOf(':');
      const raw = i > 0 ? tok.slice(0, i).toLowerCase() : '';
      const key = FKEYS[raw] ? raw : KEY_ALIAS[raw] || '';
      const val = tok.slice(i + 1);
      if (key && val) filters.push({ key, val, cv: canonVal(key, val), neg });
      else words.push((neg ? '-' : '') + tok);
    }
    return { filters, words };
  }
  const quote = (v) => (/\s/.test(v) ? `"${v}"` : v);
  // a filter in the syntax of the page language: {key: 'monat', cv: 'juni'} → month:june
  function filterQ(f) {
    const key = EN ? KEY_EN[f.key] || f.key : f.key;
    const known = WORD_KEYS.has(f.key) && WORD_EN[f.cv];
    const val = known ? (EN ? WORD_EN[f.cv] : f.cv) : f.val;
    return `${f.neg ? '-' : ''}${key}:${quote(val)}`;
  }
  function buildQ({ filters, words }) {
    return [...words, ...filters.map(filterQ)].join(' ').trim();
  }
  function filterLabel(f) {
    if (f.key === 'typ') return KIND_LABEL[f.cv] ? t(KIND_LABEL[f.cv]) : cap(f.val);
    if (f.key === 'person') return f.val;
    if (['ort', 'kamera', 'kategorie'].includes(f.key)) return f.val;
    if (WORD_KEYS.has(f.key)) return wordLabel(f.cv);
    return `${t(FKEYS[f.key])}: ${f.val}`;
  }
  function runQuery(q) {
    const target = q ? `#/suche/${encodeURIComponent(q)}` : '#/fotos';
    if (route.view === 'suche') history.replaceState(null, '', target), onRoute();
    else location.hash = target;
  }

  const Filter = {
    el: $('#fpanel'), facets: null, open_: false, n: null, placeQ: '',
    async open() {
      this.open_ = true;
      this.el.hidden = false;
      this.mark(true);
      this.place();
      if (!this.facets) this.facets = await api(`/api/facets?lang=${LANG}`).catch(() => ({}));
      if (!S.people.length) await loadPeople().catch(() => {});
      this.draw();
    },
    close() { this.open_ = false; this.el.hidden = true; this.mark(false); },
    // the filter button folds the panel up again while it is open
    mark(on) {
      $('#search').classList.toggle('fopen', on);
      const b = $('#filterBtn');
      b.classList.toggle('open', on);
      b.innerHTML = on ? icon('down', 'flip') : icon('sliders');
      b.title = on ? t('Filter schließen') : t('Filter');
    },
    // the lower part of the search field (a phone: the screen's width under the top bar)
    place() {
      if (!this.open_) return;
      const f = $('#search .field').getBoundingClientRect(), s = this.el.style;
      if (phoneMQ.matches) Object.assign(s, { left: '6px', width: `${innerWidth - 12}px`, top: `${Math.round($('.top').getBoundingClientRect().bottom)}px` });
      else Object.assign(s, { left: `${Math.round(f.left)}px`, width: `${Math.round(f.width)}px`, top: `${Math.round(f.bottom - 1)}px` });
      s.maxHeight = `${Math.round(innerHeight - parseFloat(s.top) - 16)}px`;
    },
    count(n) { this.n = n; const c = $('.count', this.el); if (c) c.textContent = pl(n, '{n} Treffer', '{n} Treffer'); },
    current() { return route.view === 'suche' ? parseQ(route.arg) : { filters: [], words: [] }; },
    has(p, key, val) { return p.filters.some((f) => f.key === key && f.cv === val.toLowerCase() && !f.neg); },
    toggle(key, val) {
      const p = this.current();
      const i = p.filters.findIndex((f) => f.key === key && f.cv === val.toLowerCase());
      if (i >= 0) p.filters.splice(i, 1);
      else p.filters.push({ key, val, cv: val.toLowerCase(), neg: false });
      runQuery(buildQ(p));
      setTimeout(() => this.draw(), 0);
    },
    sections() {
      const f = this.facets || {};
      const named = S.people.filter((p) => p.name);
      const word = (key, cv) => ({ key, val: cv, label: wordLabel(cv) });
      const weekOrder = [1, 2, 3, 4, 5, 6, 0].map((i) => DAYS_DE[i].toLowerCase());
      return [
        named.length && { title: t('Personen'), items: named.map((p) => ({ key: 'person', val: p.name, label: p.name, img: faceURL(p.cover.item, p.cover.idx), n: p.count })) },
        f.places?.length && { title: t('Orte'), search: true, items: f.places.map((x) => ({ key: 'ort', val: x.label, label: x.label, n: x.count })) },
        f.years?.length && { title: t('Jahr'), items: f.years.map((x) => ({ key: 'jahr', val: x.label, label: x.label, n: x.count })) },
        { title: t('Monat'), items: MONTHS_DE.map((m) => word('monat', m.toLowerCase())) },
        { title: t('Jahreszeit'), items: ['frühling', 'sommer', 'herbst', 'winter'].map((x) => word('jahreszeit', x)) },
        { title: t('Wochentag'), items: [...weekOrder.map((d) => word('wochentag', d)), word('tag', 'wochenende'), word('tag', 'werktag')] },
        { title: t('Tageszeit'), items: ['morgens', 'mittags', 'nachmittags', 'abends', 'nachts'].map((x) => word('tageszeit', x)) },
        { title: t('Art'), items: Object.entries(KIND_LABEL).map(([v, l]) => ({ key: 'typ', val: v, label: t(l) })) },
        f.cameras?.length && { title: t('Kamera'), items: f.cameras.map((x) => ({ key: 'kamera', val: x.label, label: x.label, n: x.count })) },
        f.categories?.length && { title: t('Kategorie'), items: f.categories.map((x) => ({ key: 'kategorie', val: x.label, label: x.label, n: x.count })) },
      ].filter(Boolean);
    },
    draw() {
      if (!this.open_) return;
      const p = this.current();
      const chip = (it) => `<button class="chip ${this.has(p, it.key, it.val) ? 'on' : ''}" data-k="${esc(it.key)}" data-v="${esc(it.val)}"
        ${it.n ? `title="${num(it.n)}"` : ''}>${it.img ? `<img src="${it.img}" alt="">` : ''}${esc(it.label)}</button>`;
      const pq = this.placeQ.toLowerCase();
      this.el.innerHTML = `<header><b>${t('Filter')}</b><span class="count">${this.n !== null && route.view === 'suche' ? pl(this.n, '{n} Treffer', '{n} Treffer') : ''}</span>
        <span style="flex:1"></span>${p.filters.length ? `<button class="btn ghost" data-reset>${t('Zurücksetzen')}</button>` : ''}
        <button class="btn ghost icon" data-close title="${t('Schließen')}">${icon('x')}</button></header>
        ${this.sections().map((sec) => {
          let items = sec.items;
          if (sec.search && pq) items = items.filter((x) => x.label.toLowerCase().includes(pq));
          const showAll = sec.search ? (pq ? 60 : 18) : 200;
          return `<h4>${sec.title}</h4>${sec.search ? `<input class="filt" data-ps placeholder="${t('Ort suchen …')}" value="${esc(this.placeQ)}">` : ''}
            <div class="chips">${items.slice(0, showAll).map(chip).join('')}</div>`;
        }).join('')}
        <p class="hint">${t('Mehrere Werte einer Art bedeuten „oder“, verschiedene Arten „und“. Tippen geht auch:')}
          ${(EN ? ['summer 2019 dresden', 'place:Dresden', 'person:Anna', 'day:weekend', 'type:video', '-type:screenshot'] : ['sommer 2019 lübbecke', 'ort:Dresden', 'person:Anna', 'tag:wochenende', 'typ:video', '-typ:screenshot']).map((x) => `<code>${x}</code>`).join(', ')}.</p>`;
      for (const b of $$('[data-k]', this.el)) b.onclick = () => this.toggle(b.dataset.k, b.dataset.v);
      $('[data-close]', this.el).onclick = () => this.close();
      $('[data-reset]', this.el)?.addEventListener('click', () => { const q = this.current(); q.filters = []; runQuery(buildQ(q)); setTimeout(() => this.draw(), 0); });
      const ps = $('[data-ps]', this.el);
      if (ps) ps.oninput = () => { this.placeQ = ps.value; const pos = ps.selectionStart; this.draw(); const n = $('[data-ps]', this.el); n.focus(); n.setSelectionRange(pos, pos); };
    },
  };
  $('#filterBtn').onclick = (e) => { e.preventDefault(); e.stopPropagation(); Filter.open_ ? Filter.close() : Filter.open(); };
  addEventListener('resize', () => Filter.place());
  document.addEventListener('pointerdown', (e) => {
    if (Filter.open_ && !e.target.closest('#fpanel') && !e.target.closest('#filterBtn') && !e.target.closest('[data-addf]')) Filter.close();
  });

  // shared toolbar for gallery views
  function galleryTools(opts = {}) {
    const lay = (k, title) => `<button data-lay="${k}" class="${S.layout === k ? 'on' : ''}" title="${t(title)}">${icon(k)}</button>`;
    return `
      ${opts.kinds ? `<div class="kindset" data-kinds role="group" aria-label="${t('Anzeigen')}">${[['photo', 'Fotos', 'photos'], ['video', 'Videos', 'video'], ['doc', 'Dokumente', 'file']].map(([k, l, ic]) =>
        `<button data-kind="${k}" class="${S.kinds.has(k) ? 'on' : ''}" aria-pressed="${S.kinds.has(k)}" title="${t('{x} zeigen oder ausblenden', { x: t(l) })}">${icon(ic)}<span>${t(l)}</span></button>`).join('')}</div>` : ''}
      ${opts.group !== false ? `<select class="sel" data-group title="${t('Gruppieren')}">
        ${[['year', 'Jahre'], ['month', 'Monate'], ['day', 'Tage']].map(([k, l]) => `<option value="${k}" ${S.group === k ? 'selected' : ''}>${t(l)}</option>`).join('')}</select>` : ''}
      <div class="seg lay-seg">${lay('rows', 'Reihen')}${lay('grid', 'Raster')}${lay('masonry', 'Mosaik')}</div>
      <label class="zoom" title="${t('Größe')}">${icon('photos')}<input type="range" min="70" max="420" step="10" value="${S.size}" data-size></label>
      <button class="btn icon ${S.animate ? 'on' : ''}" data-anim title="${t('Videos und Live Photos animiert zeigen')}">${icon('play')}</button>
      <button class="btn view-btn" data-vmenu title="${t('Ansicht')}" aria-haspopup="dialog">${icon(S.layout)}${icon('down')}</button>`;
  }
  // one change of layout, grouping, size or animation, mirrored in the toolbar and the view menu
  function setView(g, what, v) {
    if (what === 'layout') { S.layout = v; store.set('layout', v); }
    else if (what === 'group') { S.group = v; store.set('group', v); g.opts.group = v; }
    else if (what === 'size') { S.size = v; saveSize(); }
    else if (what === 'animate') { S.animate = v; store.set('animate', v); }
    for (const b of $$('[data-lay]')) b.classList.toggle('on', b.dataset.lay === S.layout);
    for (const b of $$('[data-grp]')) b.classList.toggle('on', b.dataset.grp === S.group);
    for (const s of $$('[data-group]')) s.value = S.group;
    for (const r of $$('[data-size]')) if (r.value !== String(S.size)) r.value = S.size;
    for (const b of $$('[data-anim]')) b.classList.toggle('on', S.animate);
    for (const b of $$('[data-vmenu]')) b.innerHTML = icon(S.layout) + icon('down');
    // every gallery of the page (memories have three); a gallery with a scrubber renews it in onLayout
    g?.relayout();
    for (const x of extraGalleries) if (x !== g) x.relayout();
  }
  function bindTools(root, g, onKind) {
    for (const b of $$('[data-lay]', root)) b.onclick = () => setView(g, 'layout', b.dataset.lay);
    for (const b of $$('[data-grp]', root)) b.onclick = () => setView(g, 'group', b.dataset.grp);
    for (const r of $$('[data-size]', root)) r.oninput = () => setView(g, 'size', +r.value);
    const gr = $('[data-group]', root);
    if (gr) gr.onchange = () => setView(g, 'group', gr.value);
    for (const b of $$('[data-anim]', root)) b.onclick = () => setView(g, 'animate', !S.animate);
    for (const b of $$('[data-kind]', root)) b.onclick = () => {
      const k = b.dataset.kind;
      if (S.kinds.has(k)) {
        if (S.kinds.size === 1) { toast(t('Mindestens eine Art bleibt sichtbar')); return; }
        S.kinds.delete(k);
      } else S.kinds.add(k);
      store.set('kinds', [...S.kinds]);
      for (const x of $$('[data-kind]', root)) { const on = S.kinds.has(x.dataset.kind); x.classList.toggle('on', on); x.setAttribute('aria-pressed', on); }
      updateCounts();
      onKind?.();
    };
    const vm = $('[data-vmenu]', root);
    if (vm) vm.onclick = (e) => { e.stopPropagation(); ViewMenu.el ? ViewMenu.close() : ViewMenu.open(g, !!gr); };
  }
  // phones: layout, grouping, size and animation in one sheet instead of a toolbar wider than the screen
  const ViewMenu = {
    el: null,
    open(g, group) {
      this.close();
      const lay = (k, l) => `<button data-lay="${k}" class="${S.layout === k ? 'on' : ''}">${icon(k)}<span>${t(l)}</span></button>`;
      const m = document.createElement('div');
      m.className = 'vmenu';
      m.setAttribute('role', 'dialog');
      m.innerHTML = `<header><b>${t('Ansicht')}</b><button class="btn ghost icon" data-close title="${t('Schließen')}">${icon('x')}</button></header>
        <h4>${t('Anordnung')}</h4><div class="seg wide">${lay('rows', 'Reihen')}${lay('grid', 'Raster')}${lay('masonry', 'Mosaik')}</div>
        ${group ? `<h4>${t('Gruppieren nach')}</h4><div class="seg wide">${[['year', 'Jahren'], ['month', 'Monaten'], ['day', 'Tagen']].map(([k, l]) => `<button data-grp="${k}" class="${S.group === k ? 'on' : ''}">${t(l)}</button>`).join('')}</div>` : ''}
        <h4>${t('Größe')}</h4><input type="range" min="70" max="420" step="10" value="${S.size}" data-size aria-label="${t('Größe')}">
        <button class="btn vrow ${S.animate ? 'on' : ''}" data-anim>${icon('play')}<span>${t('Videos und Live Photos animiert zeigen')}</span></button>`;
      document.body.appendChild(m);
      this.el = m;
      sheetGrip(m, { key: 'view', onClose: () => this.close() });
      bindTools(m, g);
      $('[data-close]', m).onclick = () => this.close();
      this.off = (e) => { if (!m.contains(e.target) && !e.target.closest('[data-vmenu]')) this.close(); };
      document.addEventListener('pointerdown', this.off, true);
      cleanup.push(() => this.close());
    },
    close() {
      if (!this.el) return;
      this.el.remove();
      this.el = null;
      document.removeEventListener('pointerdown', this.off, true);
    },
  };
  const byKind = (items) => items.filter(shown);

  function galleryView(view, { title, sub = '', items, group = S.group, kinds = false, before = '', snips = null, autoplay = false, scrub = true, emptyText = t('Keine Fotos gefunden.'), tools = {} }) {
    // sub may be a function of the shown items, so it follows the kind switches
    const subOf = (list) => (typeof sub === 'function' ? sub(list) : sub);
    view.innerHTML = `<div class="head"><div><h1>${title}</h1><div class="sub">${subOf(kinds ? byKind(items) : items)}</div></div><div class="tools">${galleryTools({ kinds, ...tools })}</div></div>${before}<div data-g></div>`;
    const host = $('[data-g]', view);
    const make = () => {
      const list = kinds ? byKind(items) : items;
      if (typeof sub === 'function') $('.head .sub', view).innerHTML = subOf(list);
      if (!list.length) { host.innerHTML = `<div class="empty">${icon('photos')}<h2>${t('Nichts hier')}</h2><p>${emptyText}</p></div>`; return; }
      host.innerHTML = '';
      S.gallery?.destroy();
      const g = new Gallery(host, { group, snips, autoplay, onLayout: (gg) => scrub && group !== 'none' && Scrub.attach(gg) });
      S.gallery = g;
      g.setItems(list);
      bindTools(view, g, make);
    };
    make();
  }

  // ---------------------------------------------------------------------------
  // views
  // ---------------------------------------------------------------------------
  function memoriesFor(day) {
    const m = day.getMonth(), d = day.getDate(), y = day.getFullYear();
    const by = new Map();
    for (const it of S.items) {
      if (!shown(it)) continue;
      const dd = dt(it.t);
      if (dd.getUTCMonth() === m && dd.getUTCDate() === d && dd.getUTCFullYear() < y) {
        const k = dd.getUTCFullYear();
        if (!by.has(k)) by.set(k, []);
        by.get(k).push(it);
      }
    }
    return [...by.entries()].sort((a, b) => b[0] - a[0]).map(([yr, items]) => ({ yr, items, ago: y - yr }));
  }

  function bestCover(items) {
    // prefer photos with people, then any photo
    return items.find((it) => it.k === 1 && it.pp?.length) || items.find((it) => it.k === 1) || items[0];
  }

  const VIEWS = {
    fotos(view) {
      const mem = memoriesFor(new Date());
      const strip = mem.length ? `<div class="memories">${mem.map((m) => {
        const c = bestCover(m.items);
        return `<a class="memory" href="#/erinnerungen" data-yr="${m.yr}"><img src="${thumbURL(c)}" alt="" loading="lazy"><b>${pl(m.ago, 'Vor einem Jahr', 'Vor {n} Jahren')}</b><span>${pl(m.items.length, '{n} Foto', '{n} Fotos')} · ${m.yr}</span></a>`;
      }).join('')}</div>` : '';
      galleryView(view, {
        title: t('Fotos'), sub: (list) => (list.length ? `${pl(list.length, '{n} Element', '{n} Elemente')} · ${years(list[list.length - 1].t, list[0].t)}` : S.items.length ? '' : t('Wird eingelesen …')),
        items: S.items, kinds: true, before: strip, emptyText: t('Die Ordner werden noch eingelesen.'),
      });
      for (const a of $$('.memory', view)) a.onclick = (e) => { e.preventDefault(); const m = mem.find((x) => x.yr === +a.dataset.yr); Viewer.open(m.items, 0); };
    },

    videos(view) {
      const items = S.items.filter((it) => it.k === 2);
      galleryView(view, { title: t('Videos'), sub: `${pl(items.length, '{n} Video', '{n} Videos')} · ${t('Vorschau beim Darüberfahren')}`, items, autoplay: true });
    },

    favoriten(view) {
      if (!Account.can('user')) {
        view.innerHTML = `<div class="empty"><h2>${t('Favoriten')}</h2><p>${t('Favoriten gehören zu einem Konto.')}</p><p><a class="btn primary" href="${esc(Account.loginURL())}">${t('Anmelden')}</a></p></div>`;
        return;
      }
      const items = S.items.filter((it) => it.f);
      galleryView(view, { title: t('Favoriten'), sub: (list) => pl(list.length, '{n} Favorit', '{n} Favoriten'), items, kinds: true, emptyText: t('Mit dem Herz in der Ansicht markieren.') });
    },

    erinnerungen(view) {
      const today = new Date();
      const mem = memoriesFor(today);
      const week = [];
      for (let k = -3; k <= 3; k++) {
        if (!k) continue;
        const d = new Date(today); d.setDate(d.getDate() + k);
        for (const m of memoriesFor(d)) week.push(...m.items);
      }
      week.sort((a, b) => b.t - a.t);
      const shuffled = S.items.filter((it) => it.k === 1).sort(() => Math.random() - 0.5).slice(0, 40).sort((a, b) => b.t - a.t);
      view.innerHTML = `<div class="head"><div><h1>${t('Erinnerungen')}</h1><div class="sub">${fmtLong(Date.UTC(today.getFullYear(), today.getMonth(), today.getDate()) / 1000)}</div></div>
        <div class="tools"><button class="btn" id="shuffle">${icon('shuffle')}<span>${t('Neu mischen')}</span></button>${galleryTools({ group: false })}</div></div>
        ${mem.length ? '' : `<div class="banner">${icon('cal')}<div>${t('Heute vor einem Jahr oder länger gibt es keine Fotos – unten die Woche drumherum und zufällige Momente.')}</div></div>`}
        <div id="memDay"></div><div class="head"><div><h1 style="font-size:20px">${t('Diese Woche in früheren Jahren')}</h1></div></div><div id="memWeek"></div>
        <div class="head"><div><h1 style="font-size:20px">${t('Zufällige Momente')}</h1></div></div><div id="memRand"></div>`;
      const mk = (sel, items, group) => {
        if (!items.length) return;
        const g = new Gallery($(sel, view), { group });
        g.setItems(items);
        extraGalleries.push(g);
      };
      mk('#memDay', mem.flatMap((m) => m.items), 'year');
      mk('#memWeek', week, 'day');
      mk('#memRand', shuffled, 'none');
      S.gallery = extraGalleries[0] || null;
      extraGalleries.shift();
      bindTools(view, S.gallery); // layout and size for all three
      $('#shuffle').onclick = () => rerender();
    },

    suche(view, q) {
      $('#q').value = q;
      view.innerHTML = `<div class="head compact"><div><h1>${t('Suche')}</h1><div class="sub">${t('„{q}“ …', { q: esc(q) })}</div></div></div>`;
      api(`/api/search?q=${encodeURIComponent(q)}&lang=${LANG}`).then((res) => {
        if (route.view !== 'suche' || route.arg !== q) return;
        const snips = new Map();
        let items = [];
        for (const h of res.hits || []) {
          const it = S.byId.get(h.id);
          if (!it || it.mk) continue;
          items.push(it);
          if (h.snip) snips.set(h.id, h.snip);
        }
        Filter.count(items.length);
        const sort = store.get('searchSort', 'rel');
        if (sort === 'date') items.sort((a, b) => b.t - a.t);
        const parsed = parseQ(q);
        const text = parsed.words.join(' ').toLowerCase();
        const people = text ? S.people.filter((p) => p.name && p.name.toLowerCase().includes(text)).slice(0, 6) : [];
        const places = text ? [...new Set(items.map((it) => it.p).filter((p) => p && p.toLowerCase().includes(text)))].slice(0, 6) : [];
        const jump = [...people.map((p) => `<a class="chip" href="#/person/${encodeURIComponent(p.id)}"><img src="${faceURL(p.cover.item, p.cover.idx)}" alt="">${esc(p.name)}</a>`),
          ...places.map((p) => `<a class="chip" href="#/ort/${encodeURIComponent(p)}">${icon('pin')}${esc(p)}</a>`)].join('');
        const active = parsed.filters.map((f, i) => `<button class="chip" data-rmf="${i}" title="${t('Filter entfernen')}">${f.neg ? `${t('ohne')} ` : ''}${esc(filterLabel(f))}${icon('x')}</button>`)
          .concat(parsed.words.length ? [`<button class="chip" data-rmw title="${t('Suchwörter entfernen')}">${t('„{q}“', { q: esc(parsed.words.join(' ')) })}${icon('x')}</button>`] : []).join('');
        // asking for documents shows them; otherwise the kind switches apply, with a hint
        const docQuery = /(^|\s)(typ|type):(dokument|document)|(^|\s)(kategorie|category):|(^|\s)(album|ordner|folder):\S*(dokument|document|scan|beleg)/i.test(q);
        const hiddenDocs = !docQuery && !S.kinds.has('doc') ? items.filter((it) => it.dc).length : 0;
        galleryView(view, {
          title: t('Suche'), sub: (list) => `${pl(list.length, '{n} Treffer', '{n} Treffer')}${snips.size ? ` · ${t('{n} mit Text im Bild', { n: num(list.filter((it) => snips.has(it.id)).length) })}` : ''}<span class="hide-m"> · ${res.ms ?? 0} ms</span>`,
          items, kinds: !docQuery, group: sort === 'date' ? S.group : 'none', snips, scrub: sort === 'date',
          before: `<div class="active-filters">${active}${hiddenDocs ? `<button class="chip docs-hint" data-showdocs>${icon('file')}${pl(hiddenDocs, '{n} Dokument ausgeblendet', '{n} Dokumente ausgeblendet')} · ${t('zeigen')}</button>` : ''}<button class="chip" data-addf>${icon('sliders')}<span class="hide-m">${parsed.filters.length ? t('Filter ändern') : t('Filter hinzufügen')}</span><span class="only-m">${t('Filter')}</span></button>
            ${parsed.filters.length + parsed.words.length > 1 ? `<button class="chip" data-clear>${t('Alle entfernen')}</button>` : ''}</div>
            ${jump ? `<div class="chips" style="margin-bottom:14px">${jump}</div>` : ''}`,
          emptyText: t('Andere Schreibweise, weniger Wörter oder weniger Filter probieren.'), tools: { group: false },
        });
        $('.head', view).classList.add('compact');
        const tools = $('.head .tools', view);
        tools.insertAdjacentHTML('afterbegin', `<div class="seg"><button data-sort="rel" class="${sort === 'rel' ? 'on' : ''}">${t('Relevanz')}</button><button data-sort="date" class="${sort === 'date' ? 'on' : ''}">${t('Datum')}</button></div>`);
        for (const b of $$('[data-sort]', tools)) b.onclick = () => { store.set('searchSort', b.dataset.sort); rerender(); };
        for (const b of $$('[data-rmf]', view)) b.onclick = () => { parsed.filters.splice(+b.dataset.rmf, 1); runQuery(buildQ(parsed)); };
        $('[data-rmw]', view)?.addEventListener('click', () => { parsed.words = []; runQuery(buildQ(parsed)); });
        $('[data-clear]', view)?.addEventListener('click', () => runQuery(''));
        $('[data-addf]', view).onclick = () => Filter.open();
        $('[data-showdocs]', view)?.addEventListener('click', () => { S.kinds.add('doc'); store.set('kinds', [...S.kinds]); updateCounts(); rerender(); });
        if (route.item) onRoute();
      }).catch((e) => { view.innerHTML = `<div class="empty"><h2>${t('Suche fehlgeschlagen')}</h2><p>${esc(e.message)}</p></div>`; });
    },

    orte(view) {
      const m = new Map();
      for (const it of S.items) {
        if (!it.p) continue;
        let p = m.get(it.p);
        if (!p) m.set(it.p, (p = { name: it.p, items: [] }));
        p.items.push(it);
      }
      const order = { count: (a, b) => b.items.length - a.items.length, name: (a, b) => a.name.localeCompare(b.name), new: (a, b) => b.items[0].t - a.items[0].t };
      const places = [...m.values()];
      view.innerHTML = `<div class="head"><div><h1>${t('Orte')}</h1><div class="sub">${pl(places.length, '{n} Ort', '{n} Orte')}</div></div>
        <div class="tools grow"><input class="filter-in" id="pf" placeholder="${t('Ort filtern …')}" type="search">${cardTools('places', [['count', 'Meiste Fotos'], ['new', 'Neueste'], ['name', 'Name']])}</div></div><div class="cards" id="pc"></div>`;
      const draw = (f = $('#pf', view).value) => {
        const fl = f.toLowerCase();
        places.sort(order[store.get('sort-places', 'count')] || order.count);
        $('#pc', view).innerHTML = places.filter((p) => !fl || p.name.toLowerCase().includes(fl)).map((p) => {
          const c = bestCover(p.items);
          return `<a class="card" href="#/ort/${encodeURIComponent(p.name)}"><div class="cv"><img src="${thumbURL(c)}" alt="" loading="lazy"><span class="n">${num(p.items.length)}</span></div>
            <b>${esc(p.name)}</b><span>${years(p.items[p.items.length - 1].t, p.items[0].t)}</span></a>`;
        }).join('') || `<div class="empty">${t('Keine Orte.')}</div>`;
      };
      bindCardTools($('.head', view), $('#pc', view), 'places', () => draw());
      draw();
      $('#pf', view).oninput = (e) => draw(e.target.value);
    },

    ort(view, name) {
      const items = S.items.filter((it) => it.p === name);
      const gps = items.filter((it) => it.la !== undefined);
      galleryView(view, {
        title: `<a href="#/orte" class="muted" style="color:var(--muted)">${t('Orte')}</a> / ${esc(name)}`,
        sub: `${pl(items.length, '{n} Element', '{n} Elemente')} · ${items.length ? years(items[items.length - 1].t, items[0].t) : ''}`,
        items, kinds: true, before: gps.length ? '<div class="minimap" id="placeMap" style="height:200px;margin-bottom:16px"></div>' : '',
      });
      if (gps.length && window.L) {
        const mm = L.map($('#placeMap', view), { scrollWheelZoom: false, attributionControl: false });
        tileLayer().addTo(mm);
        const b = L.latLngBounds(gps.map((it) => [it.la, it.lo]));
        for (const it of gps.slice(0, 400)) L.circleMarker([it.la, it.lo], { radius: 4, weight: 1, color: '#fff', fillColor: '#f3b549', fillOpacity: 0.9 }).addTo(mm).on('click', () => Viewer.open(gps, gps.indexOf(it)));
        mm.fitBounds(b.pad(0.2), { maxZoom: 14 });
      }
    },

    async personen(view) {
      view.innerHTML = `<div class="head"><div><h1>${t('Personen')}</h1><div class="sub">${t('wird geladen …')}</div></div></div>`;
      await loadPeople().catch(() => {});
      if (route.view !== 'personen') return;
      const info = S.peopleInfo;
      const named = S.people.filter((p) => p.name), unnamed = S.people.filter((p) => !p.name);
      let banner = '';
      const settings = `<a class="link" href="/setup">${t('Einstellungen')}</a>`;
      if (S.config.faces === 'off') banner = `<div class="banner">${icon('info')}<div>${t('Gesichtserkennung ist ausgeschaltet. Sie läuft in einem eigenen Container; einschalten unter {link}.', { link: `<a class="link" href="/setup">${t('Einstellungen → Gesichter')}</a>` })}</div></div>`;
      else if (!info.detection) banner = `<div class="banner">${icon('info')}<div>${t('Die Gesichtserkennung antwortet nicht')}${info.error ? `: ${esc(info.error)}` : ''}. ${t('Läuft der Container?')} ${settings}</div></div>`;
      else if (!info.recognition) banner = `<div class="banner">${icon('info')}<div>${t('{n} Fotos auf Gesichter geprüft. Zum {b} fehlt der Gesichtserkennung das Modell {m}.', { n: num(info.faces), b: `<b>${t('Gruppieren nach Personen')}</b>`, m: '<code>face_recognition_sface_2021dec.onnx</code>' })}</div></div>`;
      else if (info.faceTodo > info.faces) banner = `<div class="banner">${icon('info')}<div>${t('Gesichter werden noch gesucht ({a} von {b}). Die Gruppen werden laufend besser.', { a: num(info.faces), b: num(info.faceTodo) })}</div></div>`;
      const card = (p) => `<a class="person" href="#/person/${encodeURIComponent(p.id)}"><span class="face"><img src="${faceURL(p.cover.item, p.cover.idx)}" alt="" loading="lazy"></span>
        <b class="${p.name ? '' : 'unnamed'}">${esc(p.name || t('Name hinzufügen'))}</b><span>${pl(p.count, '{n} Foto', '{n} Fotos')}</span></a>`;
      // which groups show, switched like the kinds of a gallery: with names, without, hidden
      const user = Account.can('user');
      const kinds = [['named', 'Mit Namen', 'people'], ['unnamed', 'Ohne Namen', 'face'], ...(user ? [['hidden', 'Ausgeblendet', 'eye-off']] : [])];
      const show = new Set(store.get('people-show', ['named', 'unnamed']).filter((k) => kinds.some(([x]) => x === k)));
      if (!show.size) show.add('named');
      view.innerHTML = `<div class="head"><div><h1>${t('Personen')}</h1><div class="sub">${t('{a} mit Namen · {b} ohne Namen', { a: pl(named.length, '{n} Person', '{n} Personen'), b: pl(unnamed.length, '{n} Gruppe', '{n} Gruppen') })}</div></div>
        <div class="tools">${cardTools('people', [['count', 'Meiste Fotos'], ['name', 'Name']])}<div class="kindset" role="group" aria-label="${t('Anzeigen')}">${kinds.map(([k, l, ic]) =>
          `<button data-pshow="${k}" class="${show.has(k) ? 'on' : ''}" aria-pressed="${show.has(k)}" title="${t('{x} zeigen oder ausblenden', { x: t(l) })}">${icon(ic)}<span>${t(l)}</span></button>`).join('')}</div></div></div>
        ${banner}<div data-psec></div>
        ${!S.people.length && info.recognition ? `<div class="empty">${icon('people')}<h2>${t('Noch keine Personen')}</h2><p>${t('Die Gesichtssuche läuft im Hintergrund.')}</p></div>` : ''}`;
      const sec = $('[data-psec]', view);
      let hidden = null; // loaded when first shown
      const draw = async () => {
        const part = (title, sub, html) => `<div class="head"><div><h1 style="font-size:20px">${title}</h1>${sub ? `<div class="sub">${sub}</div>` : ''}</div></div>${html}`;
        const parts = () => [
          show.has('named') && named.length && `<div class="people">${(store.get('sort-people', 'count') === 'name' ? [...named].sort((a, b) => a.name.localeCompare(b.name)) : named).map(card).join('')}</div>`,
          show.has('unnamed') && unnamed.length && part(t('Wer ist das?'), t('Namen vergeben, gleiche Namen werden zusammengeführt'), `<div class="people">${unnamed.map(card).join('')}</div>`),
          show.has('hidden') && part(t('Ausgeblendet'), hidden ? t('Die Gesichter bleiben erhalten und lassen sich wieder zeigen.') : t('wird geladen …'),
            hidden ? (hidden.length ? `<div class="hpeople">${hidden.map(hiddenCard).join('')}</div>` : `<div class="empty">${icon('eye')}<h2>${t('Nichts ausgeblendet')}</h2></div>`) : ''),
        ].filter(Boolean).join('');
        sec.innerHTML = parts();
        if (show.has('hidden') && !hidden) {
          hidden = await api('/api/people/hidden').then((r) => r.hidden || []).catch(() => []);
          if (route.view !== 'personen' || !sec.isConnected) return;
          sec.innerHTML = parts();
        }
        bindShowHidden(sec, rerender, true); // the group is back among the others
      };
      bindCardTools($('.head', view), sec, 'people', () => draw());
      for (const b of $$('[data-pshow]', view)) {
        b.onclick = () => {
          const k = b.dataset.pshow;
          if (show.has(k)) {
            if (show.size === 1) { toast(t('Mindestens eine Art bleibt sichtbar')); return; }
            show.delete(k);
          } else show.add(k);
          store.set('people-show', [...show]);
          for (const x of $$('[data-pshow]', view)) { const on = show.has(x.dataset.pshow); x.classList.toggle('on', on); x.setAttribute('aria-pressed', on); }
          draw();
        };
      }
      draw();
    },

    async person(view, id) {
      if (!S.peopleById.has(id)) await loadPeople().catch(() => {});
      const p = S.peopleById.get(id);
      if (!p) {
        // a group that was named or merged since (an old link, the history): its faces are elsewhere now
        const m = /^c-([0-9a-f]+)-(\d+)$/.exec(id);
        if (m) {
          const it = await api(`/api/item/${m[1]}`).catch(() => null);
          const to = it?.faces?.find((f) => f.idx === +m[2])?.person;
          if (route.view !== 'person' || route.arg !== id) return;
          if (to && to !== id && S.peopleById.has(to)) { location.replace(`#/person/${encodeURIComponent(to)}`); return; }
        }
        view.innerHTML = `<div class="empty"><h2>${t('Person nicht gefunden')}</h2><p><a class="link" href="#/personen">${t('Zur Übersicht')}</a></p></div>`;
        return;
      }
      const items = S.items.filter((it) => it.pp?.includes(id));
      const names = [...new Set(S.people.filter((x) => x.name && x.id !== id).map((x) => x.name))].sort();
      galleryView(view, {
        title: '', items, kinds: true,
        before: '',
      });
      const head = $('.head', view);
      head.firstElementChild.outerHTML = `<div class="person-head"><span class="face"><img src="${faceURL(p.cover.item, p.cover.idx)}" alt=""></span><div>
        <input class="name-in" id="pname" value="${esc(p.name)}" placeholder="${Account.can('user') ? t('Name hinzufügen') : t('Unbenannt')}" list="pn2" ${Account.can('user') ? '' : 'readonly'}><datalist id="pn2">${names.map((n) => `<option value="${esc(n)}">`).join('')}</datalist>
        <div class="sub">${pl(items.length, '{n} Foto', '{n} Fotos')}</div></div></div>`;
      $('.head .tools', view).insertAdjacentHTML('afterbegin', `<button class="btn" id="phide" data-need="user" title="${t('Diese Gruppe nicht mehr zeigen')}">${icon('eye-off')}<span>${t('Ausblenden')}</span></button>
        <button class="btn" id="pnoface" data-need="user" title="${t('Falsch erkannt: das sind keine Gesichter. Sie werden nicht mehr gezeigt und nicht wieder gesucht.')}">${icon('face-x')}<span>${t('Kein Gesicht')}</span></button>`);
      const inp = $('#pname', view);
      const save = async () => {
        const name = inp.value.trim();
        if (name === p.name) return;
        await post(`/api/people/${encodeURIComponent(id)}`, { name });
        toast(name ? t('Gespeichert: {name}', { name }) : t('Name entfernt'));
        await loadPeople();
        await loadItems();
        const np = S.people.find((x) => x.name === name);
        // the group has a new id now: replace the page in the history, so "back" does not lead to the old one
        location.replace(np ? `#/person/${encodeURIComponent(np.id)}` : '#/personen');
      };
      inp.onkeydown = (e) => { if (e.key === 'Enter') inp.blur(); };
      inp.onchange = save;
      $('#pnoface', view).onclick = async () => {
        const res = await post(`/api/people/${encodeURIComponent(id)}`, { name: '-' });
        await loadPeople();
        await loadItems();
        location.replace('#/personen');
        toast(pl(Object.keys(res.undo || {}).length, '{n} Gesicht entfernt', '{n} Gesichter entfernt'), t('Rückgängig'), async () => {
          await post('/api/faces/labels', { labels: res.undo });
          await loadPeople();
          await loadItems();
          toast(t('Wiederhergestellt'));
        });
      };
      $('#phide', view).onclick = async () => {
        const res = await post(`/api/people/${encodeURIComponent(id)}`, { name: '~' });
        await loadPeople();
        location.replace('#/personen'); // the hidden group is gone: "back" must not lead to it
        if (res.hidden) toast(t('Ausgeblendet'), t('Rückgängig'), () => showHidden(res.hidden, id));
        else toast(t('Ausgeblendet'));
      };
    },

    async ausgeblendet(view) {
      const head = (sub) => `<div class="head"><div><h1>${t('Ausgeblendete Personen')}</h1><div class="sub">${sub}</div></div>
        <div class="tools"><a class="btn" href="#/personen">${icon('people')}<span>${t('Personen')}</span></a></div></div>`;
      if (!Account.can('user')) {
        view.innerHTML = `<div class="empty"><h2>${t('Ausgeblendete Personen')}</h2><p>${t('Ausblenden und wieder zeigen geht mit einem Konto.')}</p><p><a class="btn primary" href="${esc(Account.loginURL())}">${t('Anmelden')}</a></p></div>`;
        return;
      }
      view.innerHTML = head(t('wird geladen …'));
      let list;
      try {
        list = (await api('/api/people/hidden')).hidden || [];
      } catch (e) {
        view.innerHTML = head(esc(String(e.message || e)));
        return;
      }
      if (route.view !== 'ausgeblendet') return;
      view.innerHTML = head(list.length ? t('{n} – die Gesichter bleiben erhalten und lassen sich wieder zeigen.', { n: pl(list.length, '{n} Gruppe', '{n} Gruppen') }) : '')
        + (list.length ? `<div class="hpeople">${list.map(hiddenCard).join('')}</div>`
          : `<div class="empty">${icon('eye')}<h2>${t('Nichts ausgeblendet')}</h2><p>${t('Ausgeblendete Personen erscheinen hier.')}</p></div>`);
      bindShowHidden(view, rerender);
    },

    alben(view, path) {
      // folder tree from the item list
      const root = { name: '', path: '', kids: new Map(), items: [], total: 0 };
      for (const it of S.items) {
        let n = root;
        n.total++;
        if (!n.cover) n.cover = it;
        for (const part of it.al.split('/')) {
          let k = n.kids.get(part);
          if (!k) n.kids.set(part, (k = { name: part, path: n.path ? `${n.path}/${part}` : part, kids: new Map(), items: [], total: 0 }));
          n = k;
          n.total++;
          if (!n.cover) n.cover = it;
          n.from = it.t;
          n.to ??= it.t;
        }
        n.items.push(it);
      }
      let node = root;
      if (path) for (const part of path.split('/')) node = node?.kids.get(part);
      if (!node) { view.innerHTML = `<div class="empty"><h2>${t('Ordner nicht gefunden')}</h2></div>`; return; }
      // single root: skip straight into it
      if (!path && root.kids.size === 1) { node = [...root.kids.values()][0]; }
      const crumbs = [`<a href="#/alben">${t('Alben')}</a>`];
      if (node.path) {
        let acc = '';
        for (const part of node.path.split('/')) {
          acc = acc ? `${acc}/${part}` : part;
          crumbs.push(`<span class="sep">/</span><a href="#/alben/${encodeURIComponent(acc)}">${esc(part)}</a>`);
        }
      }
      const kids = [...node.kids.values()].sort((a, b) => b.name.localeCompare(a.name, 'de', { numeric: true }));
      // a year folder named like its years shows its subfolders instead of "2025 / 2025"
      const albumMeta = (k) => { const y = k.from ? years(k.from, k.to) : ''; return y && y !== k.name ? y : k.kids.size ? pl(k.kids.size, '{n} Ordner', '{n} Ordner') : y; };
      const cards = kids.length ? `<div class="cards" style="margin-bottom:26px">${kids.map((k) => `<a class="card folder" href="#/alben/${encodeURIComponent(k.path)}">
        <div class="cv"><img src="${thumbURL(k.cover)}" alt="" loading="lazy"><span class="n">${num(k.total)}</span></div>
        <b>${esc(k.name)}</b><span>${albumMeta(k)}</span></a>`).join('')}</div>` : '';
      if (node.items.length) {
        galleryView(view, {
          title: esc(node.name || t('Alben')), sub: `<span class="crumbs">${crumbs.join('')}</span>`,
          items: node.items, before: cards, group: kids.length ? 'none' : S.group,
        });
      } else {
        view.innerHTML = `<div class="head"><div><h1>${esc(node.name || t('Alben'))}</h1><div class="sub crumbs">${crumbs.join('')}</div></div>
          <div class="tools"><span class="muted">${pl(kids.length, '{n} Ordner', '{n} Ordner')} · ${pl(node.total, '{n} Element', '{n} Elemente')}</span></div></div>${cards}`;
      }
    },

    karte(view) {
      const gps = S.items.filter((it) => it.la !== undefined && shown(it));
      view.innerHTML = `<div class="head"><div><h1>${t('Karte')}</h1><div class="sub">${t('{a} mit Ortsangabe', { a: pl(gps.length, '{n} Element', '{n} Elemente') })}</div></div>
        <div class="tools">${galleryTools({ kinds: true, group: false })}</div></div>
        <div class="mapwrap"><div class="map" id="map"></div><div class="map-side"><header id="mapHead"></header><div class="list" id="mapList"></div></div></div>`;
      if (!window.L) { $('#map', view).innerHTML = `<div class="empty">${t('Karte konnte nicht geladen werden (keine Internetverbindung?).')}</div>`; return; }
      const map = L.map($('#map', view), { worldCopyJump: true, preferCanvas: true });
      tileLayer().addTo(map);
      const cluster = L.markerClusterGroup({
        maxClusterRadius: 70, showCoverageOnHover: false, spiderfyOnMaxZoom: true, chunkedLoading: true,
        iconCreateFunction: (c) => {
          const ms = c.getAllChildMarkers();
          let best = ms[0].it;
          for (const m of ms) if (m.it.t > best.t) best = m.it;
          return L.divIcon({ html: `<div class="pin-wrap"><div class="pin"><img src="${thumbURL(best)}" alt=""></div><span class="n">${num(c.getChildCount())}</span></div>`, className: '', iconSize: [48, 48], iconAnchor: [24, 24] });
        },
      });
      const markers = gps.map((it) => {
        const m = L.marker([it.la, it.lo], { icon: L.divIcon({ html: `<div class="pin"><img src="${thumbURL(it)}" alt="" loading="lazy"></div>`, className: '', iconSize: [48, 48], iconAnchor: [24, 24] }) });
        m.it = it;
        m.on('click', () => Viewer.open(gps, gps.indexOf(it)));
        return m;
      });
      cluster.addLayers(markers);
      cluster.on('clusterclick', (e) => {
        if (map.getZoom() >= map.getMaxZoom() - 1) return;
      });
      map.addLayer(cluster);
      if (gps.length) map.fitBounds(L.latLngBounds(gps.map((it) => [it.la, it.lo])).pad(0.05));
      else map.setView([51, 10], 5);
      // the list beside the map follows the layout and size of the toolbar; the kinds filter the map
      const side = new Gallery($('#mapList', view), { group: 'none', scrollRoot: $('#mapList', view) });
      extraGalleries.push(side);
      bindTools(view, side, () => rerender());
      const upd = debounce(() => {
        const b = map.getBounds();
        const inb = gps.filter((it) => b.contains([it.la, it.lo]));
        $('#mapHead', view).innerHTML = `<b>${pl(inb.length, '{n} Element', '{n} Elemente')}</b> ${t('im Ausschnitt')}${inb.length > 600 ? ` · ${t('die neuesten 600')}` : ''}`;
        side.setItems(inb.slice(0, 600));
      }, 200);
      map.on('moveend', upd);
      upd();
      cleanup.push(() => map.remove());
    },
  };

  function tileLayer() {
    // OpenStreetMap tiles (or the configured ones); dark mode inverts them in CSS
    return L.tileLayer(S.config.mapTiles || 'https://tile.openstreetmap.org/{z}/{x}/{y}.png', { maxZoom: 19, attribution: S.config.mapAttribution || '© OpenStreetMap' });
  }

  // ---------------------------------------------------------------------------
  // status, theme, menu
  // ---------------------------------------------------------------------------
  // how long something takes, roughly: "40 Min.", "5 Std.", "3 Tage"
  function roughly(hours) {
    const minutes = Math.max(1, Math.round(hours * 60));
    if (hours < 1) return pl(minutes, '{n} Minute', '{n} Minuten');
    if (hours < 48) return pl(Math.round(hours), '{n} Stunde', '{n} Stunden');
    return pl(Math.round(hours / 24), '{n} Tag', '{n} Tage');
  }

  // the line below a remote job's bar (descriptions: ai, text recognition: ocr):
  // paused until …, or speed and time left, and failures
  function jobNote(s, p) {
    const parts = [], paused = s[`${p}PausedUntil`], rate = s[`${p}Rate`], fail = s[`${p}Fail`];
    if (paused) {
      const until = new Date(paused * 1000).toLocaleTimeString(LANG === 'en' ? 'en-GB' : 'de-DE', { hour: '2-digit', minute: '2-digit' });
      parts.push(`<span title="${esc(t(s[`${p}Error`] || ''))}">${t('Dienst ausgelastet, weiter um {t}', { t: until })}</span>`);
    } else if (rate > 0) {
      const left = (s[`${p}Todo`] - s[`${p}Done`]) / rate;
      parts.push(`<span>${rate >= 60 ? t('{n} pro Minute', { n: num(Math.round(rate / 60)) }) : t('{n} pro Stunde', { n: num(Math.round(rate)) })} · ${t('noch etwa {d}', { d: roughly(left) })}</span>`);
    }
    if (fail) parts.push(`<span class="faint" title="${esc(t('Diese Dateien ließen sich nicht lesen; sie werden später noch einmal versucht.'))}">${pl(fail, '{n} fehlgeschlagen', '{n} fehlgeschlagen')}</span>`);
    return parts.join('');
  }

  let statusT = 0;
  const stalled = {}; // progress label -> last count and since when
  const scheduleReload = debounce(async () => { await loadItems(); if (!Viewer.open_) rerender(); }, 300);
  // trying unreadable files again: a second click within a few seconds, since every try may freeze the share
  // (the deadline lives here: the status panel is drawn anew every few seconds)
  let unreadArmed = 0;
  const unreadLabel = () => t(Date.now() < unreadArmed ? 'Wirklich? Noch einmal klicken' : 'Erneut versuchen');
  const unreadRelabel = () => { for (const b of $$('[data-unread-retry]')) b.textContent = unreadLabel(); };
  // the background work panel: folded to one line by default, open or not is kept per device
  let statusView = null; // what pollStatus found last: { rows, head, frac, bad }
  let statusOpen = store.get('statusOpen', false); // kept here too: storage may be unavailable
  function drawStatus() {
    const v = statusView;
    if (!v) return;
    if (!v.head) { $('#status').innerHTML = v.rows.join(''); return; }
    const open = statusOpen;
    // drawn anew every few seconds: a focused toggle keeps the focus
    const focused = document.activeElement?.matches?.('[data-status-toggle]');
    $('#status').innerHTML = `<button class="status-head" data-status-toggle aria-expanded="${open}" title="${esc(t(open ? 'Einklappen' : 'Ausklappen'))}">
        <span class="txt">${v.head}</span>${v.bad ? `<span class="bad n" title="${esc(t('Probleme, Details beim Ausklappen'))}">${icon('warn')}</span>` : ''}${icon('down', 'chev')}</button>
      ${open ? v.rows.join('') : v.frac === null ? '' : `<span class="bar"><i style="width:${v.frac * 100}%"></i></span>`}`;
    if (focused) $('[data-status-toggle]')?.focus();
  }
  $('#status').addEventListener('click', (e) => {
    if (!e.target.closest('[data-status-toggle]')) return;
    statusOpen = !statusOpen;
    store.set('statusOpen', statusOpen);
    drawStatus();
  });
  $('#status').addEventListener('click', async (e) => {
    if (!e.target.closest('[data-unread-retry]')) return;
    if (Date.now() >= unreadArmed) {
      unreadArmed = Date.now() + 5000;
      unreadRelabel();
      setTimeout(unreadRelabel, 5100);
      return;
    }
    unreadArmed = 0;
    try { await post('/api/admin/unreadable/retry', {}); toast(t('Wird noch einmal versucht')); } catch (err) { toast(err.message); }
    pollStatus();
  });

  async function pollStatus() {
    clearTimeout(statusT);
    let busy = false;
    try {
      const res = await api('/api/status');
      const { status: s, scanning } = res;
      S.status = s;
      const rows = [];
      // files that cannot be read (broken videos …) count as done: from the server (…Fail), or when nothing moved for 90 s
      let missing = 0, noAI = 0, noOCR = 0, first = null; // first: share done of the first running job, for the ring in the narrow sidebar
      let jobs = 0, fracSum = 0; // running jobs and their shares done, for the folded panel
      // steady: a slow job (descriptions) that is never taken for unreadable files; note: a line below the bar
      // outside the background window nothing moves: no bar counts as stuck
      const waiting = !!s.windowNext;
      // a kind of work whose window is closed while others run (a window for some work only)
      const loc0 = LANG === 'en' ? 'en-GB' : 'de-DE';
      const jobWait = (job) => {
        if (!job || !s.windowWait || !(job in s.windowWait)) return '';
        const at = s.windowWait[job];
        if (!at) return t('wartet, kein Zeitfenster dafür');
        const when = new Date(at * 1000);
        return t('wartet bis {t}', { t: (when.toDateString() === new Date().toDateString() ? '' : when.toLocaleDateString(loc0, { weekday: 'short' }) + ' ') +
          when.toLocaleTimeString(loc0, { hour: '2-digit', minute: '2-digit' }) });
      };
      const bar = (label, a, b, fail = 0, { steady = false, note = '', job = '' } = {}) => {
        if (!(b > 0)) return;
        if (a + fail >= b) { missing = Math.max(missing, fail); return; }
        const st = stalled[label], now = Date.now();
        const jw = jobWait(job);
        if (jw) note = note ? `${note} · ${jw}` : jw;
        if (!st || st.a !== a || st.b !== b || waiting || jw) stalled[label] = { a, b, t: now };
        else if (!steady && !scanning && now - st.t > 90000) { missing = Math.max(missing, b - a); return; }
        if (!jw) busy = true; // a kind of work waiting for its window: nothing to watch closely
        first ??= a / b;
        jobs++;
        fracSum += a / b;
        rows.push(`<div class="row"><span>${label} ${num(a)} / ${num(b)}</span><span class="bar"><i style="width:${(a / b) * 100}%"></i></span></div>`);
        if (note) rows.push(`<div class="row sub">${note}</div>`);
      };
      if (scanning) {
        busy = true;
        if (s.metaTodo > 0) bar(t('Metadaten'), s.meta, s.metaTodo);
        else rows.push(`<div class="row"><span>${esc(t(s.phase || ''))} · ${num(s.scanned)}</span></div>`);
      }
      const liveVideos = Math.max(0, S.items.filter((it) => it.lv).length);
      bar(t('Vorschaubilder'), s.thumbs + s.posters, s.photos + s.videos + liveVideos, s.thumbFail || 0, { job: 'media' });
      bar(t('Video-Vorschauen'), s.previews, s.videos + liveVideos, s.previewFail || 0, { job: 'media' });
      if (s.faceDetection && s.faceTodo) bar(t('Gesichter'), s.faces, s.faceTodo, 0, { job: 'media' });
      // remote jobs: descriptions and text recognition; failures go into the summary once all is tried
      const remote = (p, label, stopped) => {
        if (s[`${p}Error`] && !s[`${p}PausedUntil`]) {
          if (Account.can('admin')) rows.push(`<div class="row"><span class="bad" title="${esc(t(s[`${p}Error`]))}">${stopped}: ${esc(t(s[`${p}Error`]))}</span></div>`);
          return 0;
        }
        const wasBusy = busy;
        bar(label, s[`${p}Done`], s[`${p}Todo`], 0, { steady: true, note: jobNote(s, p), job: p === 'xmp' ? 'metadata' : p });
        if (s[`${p}PausedUntil`]) busy = wasBusy; // nothing moves until the pause ends
        return s[`${p}Done`] >= s[`${p}Todo`] && Account.can('admin') ? s[`${p}Fail`] || 0 : 0;
      };
      if (s.aiMode === 'on') noAI = remote('ai', t('Beschreibungen'), t('Beschreibungen angehalten'));
      if (s.ocrMode && s.ocrMode !== 'off') noOCR = remote('ocr', t('Texterkennung'), t('Texterkennung angehalten'));
      if (s.speechOn) remote('speech', t('Spracherkennung'), t('Spracherkennung angehalten'));
      if (s.xmpOn && Account.can('admin')) remote('xmp', t('In Dateien schreiben'), t('Schreiben in Dateien angehalten'));
      if (s.dupeMode === 'on' && Account.can('admin')) bar(t('Duplikate prüfen'), s.dupeDone, s.dupeTodo, 0, { job: 'dupes' });
      if (s.rulesRunning && Account.can('admin')) { busy = true; bar(t('Einsortieren'), s.rulesDone, s.rulesTodo, s.rulesFail || 0); }
      if (s.offline?.length) rows.push(`<div class="row"><span class="bad" title="${esc(t('Der bisherige Stand bleibt, bis der Ordner wieder da ist.'))}">${t('Nicht erreichbar')}: ${esc(s.offline.join(', '))}</span></div>`);
      if (s.import && Account.can('admin')) {
        if (s.import.waiting) rows.push(`<div class="row"><span>${t('Import')}: ${pl(s.import.waiting, '{n} Datei wird noch geschrieben', '{n} Dateien werden noch geschrieben')}</span></div>`);
        if (s.import.error) rows.push(`<div class="row"><span class="bad" title="${esc(t(s.import.error))}">${t('Import')}: ${esc(t(s.import.error))}</span></div>`);
      }
      let head = jobs ? esc(t('Hintergrundarbeit')) : '';
      if (waiting && rows.length) {
        const when = new Date(s.windowNext * 1000), loc = LANG === 'en' ? 'en-GB' : 'de-DE';
        // the next window may be on another day (weekly windows)
        const at = (when.toDateString() === new Date().toDateString() ? '' : when.toLocaleDateString(loc, { weekday: 'short' }) + ' ') +
          when.toLocaleTimeString(loc, { hour: '2-digit', minute: '2-digit' });
        const ab = t('Hintergrundarbeit ab {t}', { t: at });
        const tip = esc(t('Zeitfenster für Hintergrundarbeit: {w}', { w: s.window }));
        if (jobs) head = `<span title="${tip}">${esc(ab)}</span>`; // the head says it
        else rows.unshift(`<div class="row"><span class="faint" title="${tip}">${ab}</span></div>`);
        busy = scanning; // nothing to watch closely until then
      }
      if (head) head += ` <em class="cnt">${num(jobs)}</em>`;
      const bk = res.backup;
      if (bk?.running) { busy = true; rows.push(`<div class="row"><span>${t('Sicherung')}: ${esc(t(bk.phase || ''))} · ${t('{n} kopiert', { n: num(bk.copied) })}</span></div>`); }
      if (!rows.length) rows.push(`<div class="row"><span>${pl(s.photos, '{n} Foto', '{n} Fotos')} · ${pl(s.videos, '{n} Video', '{n} Videos')}${s.people ? ` · ${pl(s.people, '{n} Person', '{n} Personen')}` : ''}</span>${missing ? `<span class="faint" title="${t('Diese Dateien lassen sich nicht lesen')}">${num(missing)} ${t('ohne Vorschau')}</span>` : ''}${noAI ? `<span class="faint" title="${esc(t('Diese Dateien ließen sich nicht lesen; sie werden später noch einmal versucht.'))}">${num(noAI)} ${t('ohne Beschreibung')}</span>` : ''}${noOCR ? `<span class="faint" title="${esc(t('Diese Dateien ließen sich nicht lesen; sie werden später noch einmal versucht.'))}">${num(noOCR)} ${t('ohne Texterkennung')}</span>` : ''}</div>`);
      if (s.unreadable?.length && Account.can('admin')) {
        // files the share hangs on: left out until they change or are tried again here (below the summary, which stays)
        const list = s.unreadable.map((f) => f.name + (f.aside ? '' : ` (${t('wird später noch einmal versucht')})`)).join('\n');
        rows.push(`<div class="row"><span class="bad" title="${esc(t('Das Netzlaufwerk hängt beim Lesen dieser Dateien. Sie werden ausgelassen, bis sie sich ändern.') + '\n\n' + list)}">${t('Nicht lesbar')}: ${pl(s.unreadable.length, '{n} Datei', '{n} Dateien')}</span><button class="link" data-unread-retry title="${esc(t('Jeder Versuch kann das Netzlaufwerk für einige Minuten anhalten.'))}">${unreadLabel()}</button></div>`);
      }
      // folded while work runs (one line, a bar for all of it); idle, the summary shows as it is
      statusView = { rows, head, frac: jobs ? fracSum / jobs : null, bad: rows.filter((r) => r.includes('class="bad"')).length };
      drawStatus();
      const mini = $('#statusMini');
      mini.hidden = first === null;
      mini.style.setProperty('--p', Math.round((first ?? 0) * 100));
      const all = document.createElement('div');
      all.innerHTML = rows.join('');
      mini.title = first === null ? '' : $$('.row', all).map((r) => [...r.children].map((c) => c.textContent).filter(Boolean).join(' · ')).join('\n');
      if (s.version !== S.version) {
        const age = Date.now() - S.loadedAt;
        if (!S.items.length || age > (busy ? 30000 : 3000)) {
          const first = !S.items.length;
          await loadItems();
          if (first || (!Viewer.open_ && !S.sel.size && ['fotos', 'videos', 'favoriten', 'orte', 'karte', 'alben'].includes(route.view) && scrollY < 200)) rerender();
        }
      }
    } catch { busy = true; }
    statusT = setTimeout(pollStatus, busy ? 3000 : 20000);
  }

  // fullscreen for the whole app (header, menu on phones) and in the viewer; iPhones cannot, so no buttons there
  const FS = {
    ok: !!(document.fullscreenEnabled || document.webkitFullscreenEnabled),
    auto: false, // entered by the slideshow: left again when the viewer closes
    get on() { return !!(document.fullscreenElement || document.webkitFullscreenElement); },
    enter() {
      const el = document.documentElement, fn = el.requestFullscreen || el.webkitRequestFullscreen;
      try { fn?.call(el, { navigationUI: 'hide' })?.catch?.(() => {}); } catch { /* not allowed here */ }
    },
    exit() {
      this.auto = false;
      const fn = document.exitFullscreen || document.webkitExitFullscreen;
      if (this.on && fn) { try { fn.call(document)?.catch?.(() => {}); } catch { /* ignore */ } }
    },
    toggle() { if (!this.ok) return; this.auto = false; this.on ? this.exit() : this.enter(); },
    sync() {
      const on = this.on;
      if (!on) this.auto = false;
      for (const b of $$('[data-fs]')) {
        b.hidden = !this.ok;
        b.title = on ? t('Vollbild beenden (V)') : t('Vollbild (V)');
        b.setAttribute('aria-label', on ? t('Vollbild beenden') : t('Vollbild'));
        $('use', b)?.setAttribute('href', on ? '#i-shrink' : '#i-expand');
      }
    },
  };
  for (const b of $$('[data-fs]')) b.onclick = () => FS.toggle();
  document.addEventListener('fullscreenchange', () => FS.sync());
  document.addEventListener('webkitfullscreenchange', () => FS.sync());
  FS.sync();

  const Theme = {
    mq: matchMedia('(prefers-color-scheme: light)'),
    mode: (() => { try { const m = localStorage.getItem('schuhkarton-theme'); return m === 'light' || m === 'dark' || m === 'system' ? m : ''; } catch { return ''; } })() || 'system',
    label: { system: 'System', light: 'Hell', dark: 'Dunkel' },
    apply() {
      const th = this.mode === 'system' ? (this.mq.matches ? 'light' : 'dark') : this.mode;
      const changed = document.documentElement.dataset.theme !== th;
      const lbl = t(this.label[this.mode]);
      document.documentElement.dataset.theme = th;
      $('#themeColor')?.setAttribute('content', th === 'light' ? '#f5f4f1' : '#0c0d0f');
      $('#theme').innerHTML = icon(this.mode === 'system' ? 'monitor' : this.mode === 'light' ? 'sun' : 'moon') + `<span class="lbl">${lbl}</span>`;
      $('#theme').title = t('Design: {x} (wechseln)', { x: lbl });
      return changed;
    },
    cycle() {
      this.mode = { system: 'light', light: 'dark', dark: 'system' }[this.mode];
      try { localStorage.setItem('schuhkarton-theme', this.mode); } catch { /* ignore */ }
      this.apply();
      toast(t('Design: {x}', { x: t(this.label[this.mode]) }));
    },
  };
  Theme.apply();
  Theme.mq.addEventListener('change', () => { if (Theme.mode === 'system') Theme.apply(); });
  // desktop: the sidebar folds to the icon rail of narrower screens, and stays so
  const sideMini = (on) => {
    document.body.classList.toggle('side-mini', on);
    $('#sideToggle').title = on ? t('Seitenleiste ausklappen') : t('Seitenleiste einklappen');
  };
  sideMini(!!store.get('side-mini', false));
  $('#sideToggle').onclick = () => { const on = !document.body.classList.contains('side-mini'); store.set('side-mini', on); sideMini(on); };
  $('#theme').onclick = () => Theme.cycle();
  $('#rescan').onclick = async () => { await post('/api/rescan', {}); toast(t('Ordner werden neu eingelesen')); pollStatus(); };
  $('#menu').onclick = () => $('#side').classList.toggle('open');
  $('#slideBtn').onclick = () => {
    const list = (S.gallery?.items || S.items).filter((it) => it.k === 1);
    if (!list.length) return;
    if (FS.ok && !FS.on) { FS.enter(); FS.auto = true; }
    Viewer.open(list, 0);
    Viewer.startSlides();
  };
  document.addEventListener('click', (e) => {
    if ($('#side').classList.contains('open') && !e.target.closest('#side') && !e.target.closest('#menu') && !e.target.closest('.pop')) $('#side').classList.remove('open');
  });

  // ---------------------------------------------------------------------------
  // account: anonymous viewers look, accounts personalise, admins manage
  // ---------------------------------------------------------------------------
  const Account = {
    me: { role: 'anon' },
    async load() {
      this.me = await api('/api/me').catch(() => ({ role: 'anon' }));
      const r = this.me.role || 'anon';
      document.body.dataset.role = r;
      S.me = this.me;
      this.render();
    },
    can(what) {
      const r = this.me.role;
      if (what === 'admin') return r === 'admin';
      if (what === 'user') return r === 'admin' || r === 'user';
      return true;
    },
    // need returns true when allowed; otherwise it explains and offers the login
    need(what) {
      if (this.can(what)) return true;
      toast(this.me.role === 'pending' ? t('Dein Konto wartet noch auf Freigabe') : what === 'admin' ? t('Nur für Admins') : t('Dafür bitte anmelden'));
      return false;
    },
    passwordDialog() {
      $('#pop')?.remove();
      const pop = document.createElement('div');
      pop.className = 'pop pw-pop themed';
      pop.id = 'pop';
      pop.innerHTML = `<b>${t('Passwort ändern')}</b>
        <form onsubmit="return false"><input type="text" autocomplete="username" value="${esc(this.me.name)}" hidden>
        <input type="password" id="pwOld" autocomplete="current-password" placeholder="${t('Bisheriges Passwort')}">
        <input type="password" id="pwNew" autocomplete="new-password" placeholder="${t('Neues Passwort (mind. 8 Zeichen)')}">
        <div class="row"><button class="primary" data-a="save">${t('Speichern')}</button><button data-a="cancel">${t('Abbrechen')}</button></div></form>`;
      document.body.appendChild(pop);
      sheetGrip(pop, { onClose: () => pop.remove() });
      if (!phoneMQ.matches) { // a phone: a sheet at the bottom
        const r = $('#account').getBoundingClientRect();
        pop.style.left = `${clamp(r.left, 8, innerWidth - 270)}px`;
        pop.style.top = `${clamp(r.top - 190, 8, innerHeight - 200)}px`;
      }
      $('#pwOld', pop).focus();
      pop.onclick = async (e) => {
        const a = e.target.closest('[data-a]')?.dataset.a;
        if (a === 'cancel') pop.remove();
        if (a !== 'save') return;
        const res = await post('/api/me/password', { old: $('#pwOld', pop).value, new: $('#pwNew', pop).value }).catch((err) => ({ ok: false, error: err.message }));
        if (!res.ok) { toast(t(res.error || '')); return; }
        pop.remove();
        toast(t('Passwort geändert'));
      };
    },
    loginURL() { return `/login?next=${encodeURIComponent(location.pathname + location.hash)}`; },
    render() {
      const el = $('#account');
      if (!el) return;
      const m = this.me;
      if (m.role === 'anon') {
        el.innerHTML = `<a class="acc-login" href="${esc(this.loginURL())}">${icon('user')}<span>${t('Anmelden')}</span></a>`;
        return;
      }
      const role = t({ admin: 'Admin', user: 'Konto', pending: 'wartet auf Freigabe' }[m.role] || '');
      // the account: one button, its menu has the password and the logout
      el.innerHTML = `<button type="button" class="acc-me" id="accBtn" aria-haspopup="menu" title="${esc(m.name)} · ${esc(role)}">${icon('user')}<span><b>${esc(m.name)}</b><small>${esc(role)}</small></span>${icon('down', 'acc-dn')}</button>
        ${m.pendingUsers ? `<a class="acc-pending" href="/setup#benutzer" data-n="${m.pendingUsers}">${pl(m.pendingUsers, '{n} Konto wartet auf Freigabe', '{n} Konten warten auf Freigabe')}</a>` : ''}`;
      $('#accBtn').onclick = (e) => menu(e.currentTarget, [
        { head: m.name, sub: role },
        { icon: 'key', label: t('Passwort ändern'), run: () => this.passwordDialog() },
        { icon: 'logout', label: t('Abmelden'), run: async () => { await post('/api/auth/logout', {}).catch(() => {}); location.reload(); } },
      ]);
    },
  };

  // internals for admin.js (admin tools) — not a public API
  window.SK = {
    t, LANG, S, VIEWS, Viewer, Gallery, Account, api, post, toast, esc, icon, $, $$, num, plural, pl, clamp, debounce,
    fmtLong, fmtTime, fmtMonth, thumbURL, largeURL, tileLayer, galleryView,
    route: () => route, render, rerender, loadItems, loadPeople, scheduleReload, selectionChanged, endSelection, updateCounts, sheetGrip, phoneMQ,
    fmtSize, fmtDay, shown, isTyping, menu, pageSelection, pageSelActive: () => !!pageSel,
  };

  // ---------------------------------------------------------------------------
  // start
  // ---------------------------------------------------------------------------
  window.skTranslate?.();
  $('#langBtn').innerHTML = `${icon('globe')}<span class="lbl">${LANG.toUpperCase()}</span>`;
  $('#langBtn').title = LANG === 'de' ? 'Sprache: Deutsch – switch to English' : 'Language: English – auf Deutsch wechseln';
  $('#langBtn').onclick = () => window.skSetLang?.(LANG === 'de' ? 'en' : 'de');

  (async () => {
    S.config = await api('/api/config').catch(() => ({}));
    if (S.config.title) {
      document.title = S.config.title;
      $('.brand span').textContent = S.config.title;
    }
    if (S.config.theme && (() => { try { return !localStorage.getItem('schuhkarton-theme'); } catch { return true; } })()) {
      Theme.mode = S.config.theme;
      Theme.apply();
    }
    await Account.load();
    try { await loadItems(); } catch (e) { $('#view').innerHTML = `<div class="empty"><h2>${t('Server nicht erreichbar')}</h2><p>${esc(e.message)}</p></div>`; }
    loadPeople().catch(() => {});
    onRoute();
    pollStatus();
  })();
})();
