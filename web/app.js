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
  const debounce = (fn, ms) => { let t; return (...a) => { clearTimeout(t); t = setTimeout(() => fn(...a), ms); }; };
  const clamp = (v, a, b) => Math.max(a, Math.min(b, v));
  const nf = new Intl.NumberFormat('de-DE');
  const num = (n) => nf.format(n);

  const MONTHS = ['Januar', 'Februar', 'März', 'April', 'Mai', 'Juni', 'Juli', 'August', 'September', 'Oktober', 'November', 'Dezember'];
  const DAYS = ['Sonntag', 'Montag', 'Dienstag', 'Mittwoch', 'Donnerstag', 'Freitag', 'Samstag'];
  const dt = (t) => new Date(t * 1000); // capture times are wall-clock values stored as UTC
  const fmtMonth = (t) => { const d = dt(t); return `${MONTHS[d.getUTCMonth()]} ${d.getUTCFullYear()}`; };
  const fmtDay = (t) => { const d = dt(t); return `${DAYS[d.getUTCDay()].slice(0, 2)}., ${d.getUTCDate()}. ${MONTHS[d.getUTCMonth()]} ${d.getUTCFullYear()}`; };
  const fmtLong = (t) => { const d = dt(t); return `${DAYS[d.getUTCDay()]}, ${d.getUTCDate()}. ${MONTHS[d.getUTCMonth()]} ${d.getUTCFullYear()}`; };
  const fmtTime = (t) => { const d = dt(t); return `${String(d.getUTCHours()).padStart(2, '0')}:${String(d.getUTCMinutes()).padStart(2, '0')}`; };
  const fmtDur = (s) => { s = Math.round(s || 0); const m = Math.floor(s / 60); return m >= 60 ? `${Math.floor(m / 60)}:${String(m % 60).padStart(2, '0')}:${String(s % 60).padStart(2, '0')}` : `${m}:${String(s % 60).padStart(2, '0')}`; };
  const fmtSize = (b) => b > 1e9 ? `${(b / 1e9).toFixed(2)} GB` : b > 1e6 ? `${(b / 1e6).toFixed(1)} MB` : `${Math.round(b / 1e3)} KB`;
  const years = (a, b) => { const x = dt(a).getUTCFullYear(), y = dt(b).getUTCFullYear(); return x === y ? `${x}` : `${x}–${y}`; };
  const plural = (n, one, many) => `${num(n)} ${n === 1 ? one : many}`;
  const markSnip = (s) => esc(s).replace(/\x01/g, '<mark>').replace(/\x02/g, '</mark>');

  let toastT;
  function toast(msg) {
    const el = $('#toast');
    el.textContent = msg;
    el.classList.add('on');
    clearTimeout(toastT);
    toastT = setTimeout(() => el.classList.remove('on'), 2400);
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
    kind: 'all',
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

  const thumbURL = (it) => `/t/${it.id}/s.jpg?v=${it.v}`;
  const largeURL = (it) => `/t/${it.id}/m.jpg?v=${it.v}`;
  const previewURL = (it) => (it.k === 2 ? `/t/${it.id}/p.mp4?v=${it.v}` : it.lv ? `/t/${it.lv}/p.mp4?v=${it.lk}` : '');
  const faceURL = (item, idx) => `/f/${item}/${idx}`;

  async function loadItems() {
    const res = await api('/api/items');
    S.items = res.items;
    S.byId = new Map(S.items.map((it) => [it.id, it]));
    S.version = res.version;
    S.loadedAt = Date.now();
    updateCounts();
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
    const c = { all: S.items.length, videos: 0, favs: 0, gps: 0, places: new Set(), people: S.people.filter((p) => p.name).length || S.people.length };
    for (const it of S.items) {
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
      let p0 = null, longT = 0, startP = null;
      const el = this.el;
      el.addEventListener('pointerdown', (e) => {
        if (e.pointerType !== 'touch') return;
        pts.set(e.pointerId, { x: e.clientX, y: e.clientY });
        if (pts.size === 2 && !this.opts.small) {
          clearTimeout(longT);
          const [a, b] = [...pts.values()];
          p0 = { d: Math.hypot(a.x - b.x, a.y - b.y) || 1, size: S.size };
          return;
        }
        const tile = e.target.closest('.tile');
        if (!tile) return;
        startP = { x: e.clientX, y: e.clientY };
        longT = setTimeout(() => {
          this.suppressClick = true;
          S.selecting = true;
          const id = tile.dataset.id;
          S.sel.has(id) ? S.sel.delete(id) : S.sel.add(id);
          this.lastSel = id;
          navigator.vibrate?.(12);
          selectionChanged();
        }, 480);
      });
      el.addEventListener('pointermove', (e) => {
        if (!pts.has(e.pointerId)) return;
        pts.set(e.pointerId, { x: e.clientX, y: e.clientY });
        if (startP && Math.hypot(e.clientX - startP.x, e.clientY - startP.y) > 10) clearTimeout(longT);
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
      const up = (e) => {
        pts.delete(e.pointerId);
        clearTimeout(longT);
        startP = null;
        if (p0 && pts.size < 2) { p0 = null; saveSize(); Scrub.attach(this); }
      };
      el.addEventListener('pointerup', up);
      el.addEventListener('pointercancel', up);
      el.addEventListener('contextmenu', (e) => { if (e.target.closest('.tile') && isTouch) e.preventDefault(); });
    }

    setItems(items, opts = {}) {
      this.items = items;
      Object.assign(this.opts, opts);
      this.index = new Map(items.map((it, i) => [it.id, i]));
      this.relayout();
    }

    destroy() {
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
        s.meta = [plural(s.items.length, 'Element', 'Elemente'), top.join(', ')].filter(Boolean).join(' · ');
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
      this.el.className = `gallery layout-${mode}`;
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
          <button class="btn ghost selall" data-selall>${icon('select')}<span>Auswählen</span></button></div>`;
      }
      s.el = el;
      this.el.appendChild(el);
    }

    mountBucket(s, b) {
      const els = [];
      const frag = document.createDocumentFragment();
      const t = s.tiles;
      for (let i = 0; i < s.items.length; i++) {
        const ty = t.y[i];
        if (Math.floor(ty / BUCKET) !== b) continue;
        const el = this.tile(s.items[i], t.x[i], ty + s.headH, t.w[i], t.h[i]);
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
      const id = a.dataset.id;
      if (S.selecting || e.target.closest('.check') || e.ctrlKey || e.metaKey || e.shiftKey) {
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
  function selectionChanged() {
    const n = S.sel.size;
    $('#selbar').hidden = n === 0 && !S.selecting;
    $('#selCount').textContent = n ? plural(n, 'ausgewählt', 'ausgewählt') : 'Elemente antippen';
    document.body.classList.toggle('selecting', S.selecting || n > 0);
    S.gallery?.refreshTiles();
    for (const g of extraGalleries) g.refreshTiles();
  }
  function endSelection() { S.sel.clear(); S.selecting = false; selectionChanged(); }
  $('#selectBtn').onclick = () => { S.selecting = !S.selecting; selectionChanged(); };
  $('#selClose').onclick = endSelection;
  $('#selFav').onclick = async () => {
    const ids = [...S.sel];
    const on = !ids.every((id) => S.byId.get(id)?.f);
    await post('/api/fav', { ids, on });
    for (const id of ids) { const it = S.byId.get(id); if (it) it.f = on; }
    toast(on ? `${plural(ids.length, 'Element', 'Elemente')} zu Favoriten` : 'Aus Favoriten entfernt');
    updateCounts();
    endSelection();
    rerender();
  };
  $('#selZip').onclick = () => {
    const f = $('#zipForm');
    f.ids.value = [...S.sel].join(',');
    f.submit();
    toast('Download startet …');
  };

  // ---------------------------------------------------------------------------
  // viewer
  // ---------------------------------------------------------------------------
  const Viewer = {
    el: $('#viewer'), media: $('#vMedia'), stage: $('#vStage'),
    list: [], i: 0, it: null, detail: null,
    showInfo: store.get('info', !isTouch), showFaces: false,
    zoom: { s: 1, x: 0, y: 0 }, slideT: 0, idleT: 0, open_: false,

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
      $('#pop')?.remove();
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
      $('#vPrev').hidden = this.i === 0;
      $('#vNext').hidden = this.i >= this.list.length - 1;
      $('#vTitle').innerHTML = `<b>${fmtLong(it.t)}</b><span>${[it.ap ? 'ungefähr' : fmtTime(it.t), it.p, `${num(this.i + 1)} von ${num(this.list.length)}`].filter(Boolean).map(esc).join(' · ')}</span>`;
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

    renderInfo() {
      const p = $('#vInfoPanel');
      p.hidden = !this.showInfo;
      this.el.classList.toggle('info', this.showInfo);
      $('#vInfo').classList.toggle('on', this.showInfo);
      if (!this.showInfo || !this.detail) return;
      const d = this.detail, it = this.it;
      const exp = d.exposure ? (d.exposure >= 1 ? `${d.exposure} s` : `1/${Math.round(1 / d.exposure)} s`) : '';
      const camera = [d.make, d.model].filter(Boolean).join(' ');
      const shot = [d.fnumber ? `f/${d.fnumber}` : '', exp, d.iso ? `ISO ${d.iso}` : '', d.focal ? `${Math.round(d.focal * 10) / 10} mm` : ''].filter(Boolean).join(' · ');
      const mp = d.w && d.h ? `${d.w} × ${d.h}${d.kind === 1 ? ` · ${(d.w * d.h / 1e6).toFixed(1)} MP` : ''}` : '';
      const q = currentQuery();
      let text = esc(d.desc || '');
      if (q && text) {
        for (const w of q.split(/\s+/).filter((x) => x.length > 2 && !x.includes(':'))) {
          text = text.replace(new RegExp(`(${w.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')})`, 'gi'), '<mark>$1</mark>');
        }
      }
      const faces = (d.faces || []).map((f) => {
        const per = f.person ? S.peopleById.get(f.person) : null;
        const name = f.name || (per ? 'Unbenannt' : 'Unbekannt');
        return `<a href="${f.person ? `#/person/${encodeURIComponent(f.person)}` : '#'}" data-face="${f.idx}"><img src="${faceURL(it.id, f.idx)}" alt="">${esc(name)}</a>`;
      }).join('');
      p.innerHTML = `
        <h3>Aufnahme</h3>
        <div class="big">${fmtLong(d.taken)}</div>
        <div class="muted">${d.takenSrc === 'folder' || d.takenSrc === 'mtime' ? 'Datum ungefähr (aus Ordner bzw. Datei)' : `${fmtTime(d.taken)} Uhr`}</div>
        ${d.place || d.gps ? `<h3>Ort</h3>
          <div class="kv">${icon('pin')}<div>${d.place ? `<a class="link" href="#/ort/${encodeURIComponent(d.place)}">${esc(d.place)}</a>` : 'Ohne Ortsname'}
          ${d.gps ? `<div class="muted">${d.lat.toFixed(5)}, ${d.lon.toFixed(5)}${d.alt ? ` · ${Math.round(d.alt)} m` : ''}</div>` : ''}</div></div>
          ${d.gps ? '<div class="minimap" id="vMap"></div>' : ''}` : ''}
        ${faces ? `<h3>Personen</h3><div class="faces">${faces}</div>` : ''}
        ${d.desc ? `<h3>Text im Bild</h3><div class="text">${text}</div>` : ''}
        ${d.keywords?.length || d.category ? `<h3>Stichwörter</h3><div class="chips">${[d.category, ...(d.keywords || [])].filter((v, i, a) => v && a.indexOf(v) === i).map((k) => `<a class="chip" href="#/suche/${encodeURIComponent(k)}">${icon('tag')}${esc(k)}</a>`).join('')}</div>` : ''}
        ${camera || shot ? `<h3>Kamera</h3><div class="kv">${icon('camera')}<div>${esc(camera || 'Unbekannt')}${d.lens ? `<div class="muted">${esc(d.lens)}</div>` : ''}${shot ? `<div class="muted">${esc(shot)}</div>` : ''}</div></div>` : ''}
        <h3>Datei</h3>
        <div class="kv">${icon('file')}<div>${esc(d.name)}<div class="muted">${[fmtSize(d.size), mp, d.duration ? fmtDur(d.duration) : '', d.codec].filter(Boolean).map(esc).join(' · ')}</div></div></div>
        <div class="kv">${icon('folder')}<div><a class="link" href="#/alben/${encodeURIComponent(d.album)}">${esc(d.album)}</a><div><code>${esc(d.path)}</code></div></div></div>
        <div class="kv">${icon('download')}<div><a class="link" href="/o/${it.id}?dl=1">Original herunterladen</a> · <a class="link" href="/o/${it.id}" target="_blank" rel="noopener">öffnen</a></div></div>`;
      if (d.gps && window.L) {
        const mm = L.map($('#vMap', p), { zoomControl: false, attributionControl: false }).setView([d.lat, d.lon], 13);
        tileLayer().addTo(mm);
        L.circleMarker([d.lat, d.lon], { radius: 7, color: '#fff', weight: 2, fillColor: '#f3b549', fillOpacity: 1 }).addTo(mm);
      }
      for (const a of $$('[data-face]', p)) {
        a.onclick = (e) => {
          const f = d.faces[+a.dataset.face];
          if (!f.person || e.altKey) { e.preventDefault(); this.namePop(a, f); }
          else this.close(true);
        };
      }
    },

    drawFaces() {
      $('.v-faces', this.media)?.remove();
      $('#vFaces').classList.toggle('on', this.showFaces);
      const d = this.detail;
      if (!this.showFaces || !d?.faces?.length || this.it.k !== 1) return;
      const ov = document.createElement('div');
      ov.className = 'v-faces';
      ov.innerHTML = d.faces.map((f) => `<div class="v-face ${f.name ? '' : 'unknown'}" data-idx="${f.idx}" style="left:${f.X * 100}%;top:${f.Y * 100}%;width:${f.W * 100}%;height:${f.H * 100}%"><span>${esc(f.name || (f.person ? 'Unbenannt' : '?'))}</span></div>`).join('');
      this.media.appendChild(ov);
      for (const b of $$('.v-face', ov)) b.onclick = (e) => { e.stopPropagation(); this.namePop(b, d.faces[+b.dataset.idx]); };
    },

    namePop(anchor, face) {
      $('#pop')?.remove();
      const r = anchor.getBoundingClientRect();
      const pop = document.createElement('div');
      pop.className = 'pop';
      pop.id = 'pop';
      const names = [...new Set(S.people.filter((p) => p.name).map((p) => p.name))].sort();
      pop.innerHTML = `<b>Wer ist das?</b>
        <input list="pnames" value="${esc(face.name || '')}" placeholder="Name eingeben">
        <datalist id="pnames">${names.map((n) => `<option value="${esc(n)}">`).join('')}</datalist>
        <div class="row"><button class="primary" data-a="save">Speichern</button><button data-a="not">${face.person ? 'Nicht diese Person' : 'Ausblenden'}</button></div>`;
      document.body.appendChild(pop);
      pop.style.left = `${clamp(r.left, 8, innerWidth - 250)}px`;
      pop.style.top = `${clamp(r.bottom + 8, 8, innerHeight - 160)}px`;
      const inp = $('input', pop);
      inp.focus();
      const save = async (name) => {
        pop.remove();
        await post(`/api/face/${this.it.id}/${face.idx}`, { name });
        toast(name ? `Als ${name} gespeichert` : 'Entfernt');
        await loadPeople();
        const d = await api(`/api/item/${this.it.id}`);
        this.detail = d;
        this.renderInfo();
        this.drawFaces();
        scheduleReload();
      };
      inp.onkeydown = (e) => { if (e.key === 'Enter') save(inp.value.trim()); if (e.key === 'Escape') pop.remove(); e.stopPropagation(); };
      pop.onclick = (e) => {
        const a = e.target.closest('[data-a]')?.dataset.a;
        if (a === 'save' && inp.value.trim()) save(inp.value.trim());
        if (a === 'not') save('');
      };
    },

    // --- zoom & gestures
    resetZoom() { this.zoom = { s: 1, x: 0, y: 0 }; this.applyZoom(); },
    applyZoom() {
      const z = this.zoom;
      this.media.style.transform = z.s === 1 ? '' : `translate(${z.x}px, ${z.y}px) scale(${z.s})`;
      this.stage.style.cursor = z.s > 1 ? 'grab' : '';
    },
    zoomAt(cx, cy, ns) {
      const z = this.zoom;
      const r = this.media.getBoundingClientRect();
      // point under the cursor in unscaled media coordinates
      const px = (cx - r.left) / z.s, py = (cy - r.top) / z.s;
      ns = clamp(ns, 1, 8);
      if (ns === 1) { this.resetZoom(); return; }
      const base = { left: r.left - z.x, top: r.top - z.y };
      z.x = cx - base.left - px * ns;
      z.y = cy - base.top - py * ns;
      z.s = ns;
      this.applyZoom();
    },

    async toggleFav() {
      const it = this.it;
      it.f = !it.f;
      await post('/api/fav', { ids: [it.id], on: it.f });
      $('#vFav').classList.toggle('on', it.f);
      $('#vFav').innerHTML = icon('heart', it.f ? 'fill' : '');
      toast(it.f ? 'Zu Favoriten hinzugefügt' : 'Aus Favoriten entfernt');
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
    $('#vStrip').onclick = (e) => { const b = e.target.closest('[data-j]'); if (b) { V.i = +b.dataset.j; V.step(0); } };
    const live = $('#vLive');
    live.onpointerenter = () => V.playLive(false);
    live.onclick = () => V.playLive(false);

    // idle: hide controls while watching
    V.el.addEventListener('pointermove', () => {
      V.el.classList.remove('idle');
      clearTimeout(V.idleT);
      V.idleT = setTimeout(() => { if (!V.el.matches(':hover .v-top, :hover .v-strip')) V.el.classList.add('idle'); }, 2600);
    });

    // wheel zoom
    V.stage.addEventListener('wheel', (e) => {
      if (V.it?.k !== 1) return;
      e.preventDefault();
      V.zoomAt(e.clientX, e.clientY, V.zoom.s * Math.exp(-e.deltaY * 0.0022));
    }, { passive: false });
    V.stage.addEventListener('dblclick', (e) => {
      if (V.it?.k !== 1) return;
      V.zoom.s > 1 ? V.resetZoom() : V.zoomAt(e.clientX, e.clientY, 2.5);
    });

    // pointer gestures: pan, pinch, swipe
    const pts = new Map();
    let start = null, pinch0 = null, moved = false, lastTap = 0, longT = 0;
    V.stage.addEventListener('pointerdown', (e) => {
      if (e.target.closest('video[controls]') || e.target.closest('.v-face')) return;
      V.stage.setPointerCapture(e.pointerId);
      pts.set(e.pointerId, { x: e.clientX, y: e.clientY });
      moved = false;
      if (pts.size === 1) {
        start = { x: e.clientX, y: e.clientY, zx: V.zoom.x, zy: V.zoom.y, t: Date.now() };
        if (V.it?.lv) longT = setTimeout(() => V.playLive(false), 450);
      } else if (pts.size === 2) {
        const [a, b] = [...pts.values()];
        pinch0 = { d: Math.hypot(a.x - b.x, a.y - b.y), s: V.zoom.s, cx: (a.x + b.x) / 2, cy: (a.y + b.y) / 2 };
        clearTimeout(longT);
      }
    });
    V.stage.addEventListener('pointermove', (e) => {
      if (!pts.has(e.pointerId)) return;
      pts.set(e.pointerId, { x: e.clientX, y: e.clientY });
      if (pts.size === 2 && pinch0) {
        const [a, b] = [...pts.values()];
        V.zoomAt(pinch0.cx, pinch0.cy, pinch0.s * Math.hypot(a.x - b.x, a.y - b.y) / pinch0.d);
        moved = true;
        return;
      }
      if (!start) return;
      const dx = e.clientX - start.x, dy = e.clientY - start.y;
      if (Math.abs(dx) + Math.abs(dy) > 8) { moved = true; clearTimeout(longT); }
      if (V.zoom.s > 1) {
        V.zoom.x = start.zx + dx; V.zoom.y = start.zy + dy;
        V.applyZoom();
      } else if (moved) {
        V.media.style.transform = `translate(${dx}px, ${Math.max(0, dy) * 0.6}px)`;
        V.media.style.opacity = String(1 - Math.min(0.6, Math.max(0, dy) / 600));
      }
    });
    const end = (e) => {
      if (!pts.has(e.pointerId)) return;
      pts.delete(e.pointerId);
      clearTimeout(longT);
      if (pts.size) return;
      pinch0 = null;
      if (start && V.zoom.s === 1) {
        const dx = e.clientX - start.x, dy = e.clientY - start.y;
        V.media.style.transform = '';
        V.media.style.opacity = '';
        if (moved && dy > 120 && Math.abs(dy) > Math.abs(dx)) V.close();
        else if (moved && Math.abs(dx) > 70 && Math.abs(dx) > Math.abs(dy)) V.step(dx < 0 ? 1 : -1);
        else if (!moved && isTouch) {
          const now = Date.now();
          if (now - lastTap < 300) V.zoomAt(e.clientX, e.clientY, 2.5);
          else V.el.classList.toggle('idle');
          lastTap = now;
        }
      }
      start = null;
    };
    V.stage.addEventListener('pointerup', end);
    V.stage.addEventListener('pointercancel', end);
  }

  // ---------------------------------------------------------------------------
  // keyboard
  // ---------------------------------------------------------------------------
  addEventListener('keydown', (e) => {
    const typing = e.target.matches('input, textarea, select');
    if (Viewer.open_) {
      if (typing) return;
      const V = Viewer;
      switch (e.key) {
        case 'Escape': $('#pop') ? $('#pop').remove() : V.close(); break;
        case 'ArrowRight': V.step(1); break;
        case 'ArrowLeft': V.step(-1); break;
        case 'i': case 'I': $('#vInfo').click(); break;
        case 'f': case 'F': V.toggleFav(); break;
        case 'g': case 'G': $('#vFaces').click(); break;
        case 'd': case 'D': location.href = `/o/${V.it.id}?dl=1`; break;
        case ' ': e.preventDefault(); V.slideT ? V.stopSlides() : V.startSlides(); break;
        case '+': case '=': V.zoomAt(innerWidth / 2, innerHeight / 2, V.zoom.s * 1.4); break;
        case '-': V.zoomAt(innerWidth / 2, innerHeight / 2, V.zoom.s / 1.4); break;
        case '0': V.resetZoom(); break;
        default: return;
      }
      e.preventDefault();
      return;
    }
    if (typing) return;
    if (e.key === '/') { e.preventDefault(); $('#q').focus(); $('#q').select(); }
    if (e.key === 'Escape') { if (Filter.open_) Filter.close(); if (S.sel.size || S.selecting) endSelection(); $('#side').classList.remove('open'); }
  });

  // ---------------------------------------------------------------------------
  // search box with suggestions
  // ---------------------------------------------------------------------------
  {
    const q = $('#q'), box = $('#suggest');
    let opts = [], on = -1;
    const TYPES = { ort: 'Ort', person: 'Person', kategorie: 'Kategorie', kamera: 'Kamera', jahr: 'Jahr', monat: 'Monat' };
    const draw = () => {
      if (!q.value.trim()) {
        box.innerHTML = `<div class="hint">Suche in Ort, Personen, Datum, Ordnern, Kamera und im <b>Text in Bildern</b>.<br>
          Filter: <code>ort:</code> <code>person:</code> <code>jahr:2019</code> <code>monat:juni</code> <code>typ:video</code> <code>typ:selfie</code> <code>text:</code> <code>-wort</code></div>`;
      } else {
        box.innerHTML = opts.map((o, i) => `<button data-i="${i}" class="${i === on ? 'on' : ''}"><span class="t">${esc(TYPES[o.type] || o.type)}</span>${esc(o.label)}<span class="n">${o.count ? num(o.count) : ''}</span></button>`).join('')
          + `<button data-i="-1" class="${on === opts.length ? 'on' : ''}"><span class="t">Suche</span>„${esc(q.value)}“ überall</button>`;
      }
      box.hidden = false;
    };
    const fetchS = debounce(async () => {
      const v = q.value.trim();
      if (!v) { opts = []; draw(); return; }
      const last = v.split(/\s+/).pop();
      opts = (await api(`/api/suggest?q=${encodeURIComponent(last)}`).catch(() => [])) || [];
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
    for (const a of $$('[data-v]')) a.classList.toggle('on', a.dataset.v === v || (v === 'ort' && a.dataset.v === 'orte') || (v === 'person' && a.dataset.v === 'personen'));
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
  const FKEYS = { person: 'Person', wer: 'Person', ort: 'Ort', jahr: 'Jahr', monat: 'Monat', jahreszeit: 'Jahreszeit', wochentag: 'Wochentag', tag: 'Tag',
    tageszeit: 'Tageszeit', typ: 'Art', kamera: 'Kamera', kategorie: 'Kategorie', album: 'Ordner', ordner: 'Ordner', text: 'Text im Bild', datum: 'Datum', datei: 'Datei' };
  const KIND_LABEL = { foto: 'Fotos', video: 'Videos', live: 'Live Photos', selfie: 'Selfies', screenshot: 'Screenshots', panorama: 'Panoramen', text: 'Mit Text im Bild', gps: 'Mit Ortsangabe', favorit: 'Favoriten' };
  const cap = (s) => s.charAt(0).toUpperCase() + s.slice(1);

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
      const key = i > 0 ? tok.slice(0, i).toLowerCase() : '';
      if (key && FKEYS[key] && tok.slice(i + 1)) filters.push({ key: key === 'wer' ? 'person' : key === 'ordner' ? 'album' : key, val: tok.slice(i + 1), neg });
      else words.push((neg ? '-' : '') + tok);
    }
    return { filters, words };
  }
  const quote = (v) => (/\s/.test(v) ? `"${v}"` : v);
  function buildQ({ filters, words }) {
    return [...words, ...filters.map((f) => `${f.neg ? '-' : ''}${f.key}:${quote(f.val)}`)].join(' ').trim();
  }
  function filterLabel(f) {
    if (f.key === 'typ') return KIND_LABEL[f.val.toLowerCase()] || cap(f.val);
    if (f.key === 'tag') return cap(f.val);
    if (f.key === 'person') return f.val;
    if (['ort', 'kamera', 'kategorie'].includes(f.key)) return f.val;
    if (['monat', 'jahreszeit', 'wochentag', 'tageszeit'].includes(f.key)) return cap(f.val);
    return `${FKEYS[f.key]}: ${f.val}`;
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
      if (!this.facets) this.facets = await api('/api/facets').catch(() => ({}));
      if (!S.people.length) await loadPeople().catch(() => {});
      this.draw();
    },
    close() { this.open_ = false; this.el.hidden = true; },
    count(n) { this.n = n; const c = $('.count', this.el); if (c) c.textContent = `${num(n)} Treffer`; },
    current() { return route.view === 'suche' ? parseQ(route.arg) : { filters: [], words: [] }; },
    has(p, key, val) { return p.filters.some((f) => f.key === key && f.val.toLowerCase() === val.toLowerCase() && !f.neg); },
    toggle(key, val) {
      const p = this.current();
      const i = p.filters.findIndex((f) => f.key === key && f.val.toLowerCase() === val.toLowerCase());
      if (i >= 0) p.filters.splice(i, 1);
      else p.filters.push({ key, val, neg: false });
      runQuery(buildQ(p));
      setTimeout(() => this.draw(), 0);
    },
    sections() {
      const f = this.facets || {};
      const named = S.people.filter((p) => p.name);
      const DAYS_ORDER = ['Montag', 'Dienstag', 'Mittwoch', 'Donnerstag', 'Freitag', 'Samstag', 'Sonntag'];
      return [
        named.length && { title: 'Personen', items: named.map((p) => ({ key: 'person', val: p.name, label: p.name, img: faceURL(p.cover.item, p.cover.idx), n: p.count })) },
        f.places?.length && { title: 'Orte', search: true, items: f.places.map((x) => ({ key: 'ort', val: x.label, label: x.label, n: x.count })) },
        f.years?.length && { title: 'Jahr', items: f.years.map((x) => ({ key: 'jahr', val: x.label, label: x.label, n: x.count })) },
        { title: 'Monat', items: MONTHS.map((m) => ({ key: 'monat', val: m.toLowerCase(), label: m })) },
        { title: 'Jahreszeit', items: ['Frühling', 'Sommer', 'Herbst', 'Winter'].map((x) => ({ key: 'jahreszeit', val: x.toLowerCase(), label: x })) },
        { title: 'Wochentag', items: [...DAYS_ORDER.map((d) => ({ key: 'wochentag', val: d.toLowerCase(), label: d })), { key: 'tag', val: 'wochenende', label: 'Wochenende' }, { key: 'tag', val: 'werktag', label: 'Werktag' }] },
        { title: 'Tageszeit', items: ['Morgens', 'Mittags', 'Nachmittags', 'Abends', 'Nachts'].map((x) => ({ key: 'tageszeit', val: x.toLowerCase(), label: x })) },
        { title: 'Art', items: Object.entries(KIND_LABEL).map(([v, l]) => ({ key: 'typ', val: v, label: l })) },
        f.cameras?.length && { title: 'Kamera', items: f.cameras.map((x) => ({ key: 'kamera', val: x.label, label: x.label, n: x.count })) },
        f.categories?.length && { title: 'Kategorie', items: f.categories.map((x) => ({ key: 'kategorie', val: x.label, label: x.label, n: x.count })) },
      ].filter(Boolean);
    },
    draw() {
      if (!this.open_) return;
      const p = this.current();
      const chip = (it) => `<button class="chip ${this.has(p, it.key, it.val) ? 'on' : ''}" data-k="${esc(it.key)}" data-v="${esc(it.val)}"
        ${it.n ? `title="${num(it.n)}"` : ''}>${it.img ? `<img src="${it.img}" alt="">` : ''}${esc(it.label)}</button>`;
      const pq = this.placeQ.toLowerCase();
      this.el.innerHTML = `<header><b>Filter</b><span class="count">${this.n !== null && route.view === 'suche' ? `${num(this.n)} Treffer` : ''}</span>
        <span style="flex:1"></span>${p.filters.length ? '<button class="btn ghost" data-reset>Zurücksetzen</button>' : ''}
        <button class="btn ghost icon" data-close title="Schließen">${icon('x')}</button></header>
        ${this.sections().map((sec) => {
          let items = sec.items;
          if (sec.search && pq) items = items.filter((x) => x.label.toLowerCase().includes(pq));
          const showAll = sec.search ? (pq ? 60 : 18) : 200;
          return `<h4>${sec.title}</h4>${sec.search ? `<input class="filt" data-ps placeholder="Ort suchen …" value="${esc(this.placeQ)}">` : ''}
            <div class="chips">${items.slice(0, showAll).map(chip).join('')}</div>`;
        }).join('')}
        <p class="hint">Mehrere Werte einer Art bedeuten „oder“, verschiedene Arten „und“. Tippen geht auch:
          <code>sommer 2019 lübbecke</code>, <code>ort:Dresden</code>, <code>person:Anna</code>, <code>tag:wochenende</code>, <code>typ:video</code>, <code>-typ:screenshot</code>.</p>`;
      for (const b of $$('[data-k]', this.el)) b.onclick = () => this.toggle(b.dataset.k, b.dataset.v);
      $('[data-close]', this.el).onclick = () => this.close();
      $('[data-reset]', this.el)?.addEventListener('click', () => { const q = this.current(); q.filters = []; runQuery(buildQ(q)); setTimeout(() => this.draw(), 0); });
      const ps = $('[data-ps]', this.el);
      if (ps) ps.oninput = () => { this.placeQ = ps.value; const pos = ps.selectionStart; this.draw(); const n = $('[data-ps]', this.el); n.focus(); n.setSelectionRange(pos, pos); };
    },
  };
  $('#filterBtn').onclick = (e) => { e.preventDefault(); e.stopPropagation(); Filter.open_ ? Filter.close() : Filter.open(); };
  document.addEventListener('pointerdown', (e) => {
    if (Filter.open_ && !e.target.closest('#fpanel') && !e.target.closest('#filterBtn') && !e.target.closest('[data-addf]')) Filter.close();
  });

  // shared toolbar for gallery views
  function galleryTools(opts = {}) {
    const lay = (k, ic, t) => `<button data-lay="${k}" class="${S.layout === k ? 'on' : ''}" title="${t}">${icon(ic)}</button>`;
    return `
      ${opts.kinds ? `<div class="seg" data-kinds>${['all:Alle', 'photo:Fotos', 'video:Videos'].map((s) => { const [k, l] = s.split(':'); return `<button data-kind="${k}" class="${S.kind === k ? 'on' : ''}">${l}</button>`; }).join('')}</div>` : ''}
      ${opts.group !== false ? `<select class="sel" data-group title="Gruppieren">
        ${[['year', 'Jahre'], ['month', 'Monate'], ['day', 'Tage']].map(([k, l]) => `<option value="${k}" ${S.group === k ? 'selected' : ''}>${l}</option>`).join('')}</select>` : ''}
      <div class="seg">${lay('rows', 'rows', 'Reihen')}${lay('grid', 'grid', 'Raster')}${lay('masonry', 'masonry', 'Mosaik')}</div>
      <label class="zoom" title="Größe">${icon('photos')}<input type="range" min="70" max="420" step="10" value="${S.size}" data-size></label>
      <button class="btn icon ${S.animate ? 'on' : ''}" data-anim title="Videos und Live Photos animiert zeigen">${icon('play')}</button>`;
  }
  function bindTools(root, g, onKind) {
    for (const b of $$('[data-lay]', root)) b.onclick = () => { S.layout = b.dataset.lay; store.set('layout', S.layout); $$('[data-lay]', root).forEach((x) => x.classList.toggle('on', x === b)); g.relayout(); Scrub.attach(g); };
    const sz = $('[data-size]', root);
    if (sz) sz.oninput = () => { S.size = +sz.value; saveSize(); g.relayout(); Scrub.attach(g); };
    const gr = $('[data-group]', root);
    if (gr) gr.onchange = () => { S.group = gr.value; store.set('group', S.group); g.opts.group = S.group; g.relayout(); Scrub.attach(g); };
    const an = $('[data-anim]', root);
    if (an) an.onclick = () => { S.animate = !S.animate; store.set('animate', S.animate); an.classList.toggle('on', S.animate); g.relayout(); };
    for (const b of $$('[data-kind]', root)) b.onclick = () => { S.kind = b.dataset.kind; $$('[data-kind]', root).forEach((x) => x.classList.toggle('on', x === b)); onKind?.(); };
  }
  const byKind = (items) => (S.kind === 'photo' ? items.filter((it) => it.k === 1) : S.kind === 'video' ? items.filter((it) => it.k === 2) : items);

  function galleryView(view, { title, sub = '', items, group = S.group, kinds = false, before = '', snips = null, autoplay = false, scrub = true, emptyText = 'Keine Fotos gefunden.', tools = {} }) {
    view.innerHTML = `<div class="head"><div><h1>${title}</h1><div class="sub">${sub}</div></div><div class="tools">${galleryTools({ kinds, ...tools })}</div></div>${before}<div data-g></div>`;
    const host = $('[data-g]', view);
    const make = () => {
      const list = kinds ? byKind(items) : items;
      if (!list.length) { host.innerHTML = `<div class="empty">${icon('photos')}<h2>Nichts hier</h2><p>${emptyText}</p></div>`; return; }
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
      const t = dt(it.t);
      if (t.getUTCMonth() === m && t.getUTCDate() === d && t.getUTCFullYear() < y) {
        const k = t.getUTCFullYear();
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
        return `<a class="memory" href="#/erinnerungen" data-yr="${m.yr}"><img src="${thumbURL(c)}" alt="" loading="lazy"><b>${m.ago === 1 ? 'Vor einem Jahr' : `Vor ${m.ago} Jahren`}</b><span>${plural(m.items.length, 'Foto', 'Fotos')} · ${m.yr}</span></a>`;
      }).join('')}</div>` : '';
      const total = S.items.length;
      galleryView(view, {
        title: 'Fotos', sub: total ? `${plural(total, 'Element', 'Elemente')} · ${years(S.items[S.items.length - 1].t, S.items[0].t)}` : 'Wird eingelesen …',
        items: S.items, kinds: true, before: strip, emptyText: 'Die Ordner werden noch eingelesen.',
      });
      for (const a of $$('.memory', view)) a.onclick = (e) => { e.preventDefault(); const m = mem.find((x) => x.yr === +a.dataset.yr); Viewer.open(m.items, 0); };
    },

    videos(view) {
      const items = S.items.filter((it) => it.k === 2);
      galleryView(view, { title: 'Videos', sub: `${plural(items.length, 'Video', 'Videos')} · Vorschau beim Darüberfahren`, items, autoplay: true });
    },

    favoriten(view) {
      const items = S.items.filter((it) => it.f);
      galleryView(view, { title: 'Favoriten', sub: plural(items.length, 'Favorit', 'Favoriten'), items, emptyText: 'Mit dem Herz in der Ansicht markieren.' });
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
      view.innerHTML = `<div class="head"><div><h1>Erinnerungen</h1><div class="sub">${fmtLong(Date.UTC(today.getFullYear(), today.getMonth(), today.getDate()) / 1000)}</div></div>
        <div class="tools"><button class="btn" id="shuffle">${icon('shuffle')}<span>Neu mischen</span></button></div></div>
        ${mem.length ? '' : '<div class="banner">' + icon('cal') + '<div>Heute vor einem Jahr oder länger gibt es keine Fotos – unten die Woche drumherum und zufällige Momente.</div></div>'}
        <div id="memDay"></div><div class="head"><div><h1 style="font-size:20px">Diese Woche in früheren Jahren</h1></div></div><div id="memWeek"></div>
        <div class="head"><div><h1 style="font-size:20px">Zufällige Momente</h1></div></div><div id="memRand"></div>`;
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
      $('#shuffle').onclick = () => rerender();
    },

    suche(view, q) {
      $('#q').value = q;
      view.innerHTML = `<div class="head"><div><h1>Suche</h1><div class="sub">„${esc(q)}“ …</div></div></div>`;
      api(`/api/search?q=${encodeURIComponent(q)}`).then((res) => {
        if (route.view !== 'suche' || route.arg !== q) return;
        const snips = new Map();
        let items = [];
        for (const h of res.hits || []) {
          const it = S.byId.get(h.id);
          if (!it) continue;
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
        const active = parsed.filters.map((f, i) => `<button class="chip" data-rmf="${i}" title="Filter entfernen">${f.neg ? 'ohne ' : ''}${esc(filterLabel(f))}${icon('x')}</button>`)
          .concat(parsed.words.length ? [`<button class="chip" data-rmw title="Suchwörter entfernen">„${esc(parsed.words.join(' '))}“${icon('x')}</button>`] : []).join('');
        galleryView(view, {
          title: 'Suche', sub: `${plural(items.length, 'Treffer', 'Treffer')}${snips.size ? ` · ${num(snips.size)} mit Text im Bild` : ''} · ${res.ms ?? 0} ms`,
          items, group: sort === 'date' ? S.group : 'none', snips, scrub: sort === 'date',
          before: `<div class="active-filters">${active}<button class="chip" data-addf>${icon('sliders')}Filter${parsed.filters.length ? ' ändern' : ' hinzufügen'}</button>
            ${parsed.filters.length + parsed.words.length > 1 ? '<button class="chip" data-clear>Alle entfernen</button>' : ''}</div>
            ${jump ? `<div class="chips" style="margin-bottom:14px">${jump}</div>` : ''}`,
          emptyText: 'Andere Schreibweise, weniger Wörter oder weniger Filter probieren.', tools: { group: false },
        });
        const tools = $('.head .tools', view);
        tools.insertAdjacentHTML('afterbegin', `<div class="seg"><button data-sort="rel" class="${sort === 'rel' ? 'on' : ''}">Relevanz</button><button data-sort="date" class="${sort === 'date' ? 'on' : ''}">Datum</button></div>`);
        for (const b of $$('[data-sort]', tools)) b.onclick = () => { store.set('searchSort', b.dataset.sort); rerender(); };
        for (const b of $$('[data-rmf]', view)) b.onclick = () => { parsed.filters.splice(+b.dataset.rmf, 1); runQuery(buildQ(parsed)); };
        $('[data-rmw]', view)?.addEventListener('click', () => { parsed.words = []; runQuery(buildQ(parsed)); });
        $('[data-clear]', view)?.addEventListener('click', () => runQuery(''));
        $('[data-addf]', view).onclick = () => Filter.open();
        if (route.item) onRoute();
      }).catch((e) => { view.innerHTML = `<div class="empty"><h2>Suche fehlgeschlagen</h2><p>${esc(e.message)}</p></div>`; });
    },

    orte(view) {
      const m = new Map();
      for (const it of S.items) {
        if (!it.p) continue;
        let p = m.get(it.p);
        if (!p) m.set(it.p, (p = { name: it.p, items: [] }));
        p.items.push(it);
      }
      const places = [...m.values()].sort((a, b) => b.items.length - a.items.length);
      view.innerHTML = `<div class="head"><div><h1>Orte</h1><div class="sub">${plural(places.length, 'Ort', 'Orte')}</div></div>
        <div class="tools"><input class="filter-in" id="pf" placeholder="Ort filtern …" type="search"></div></div><div class="cards" id="pc"></div>`;
      const draw = (f = '') => {
        const fl = f.toLowerCase();
        $('#pc', view).innerHTML = places.filter((p) => !fl || p.name.toLowerCase().includes(fl)).map((p) => {
          const c = bestCover(p.items);
          return `<a class="card" href="#/ort/${encodeURIComponent(p.name)}"><div class="cv"><img src="${thumbURL(c)}" alt="" loading="lazy"><span class="n">${num(p.items.length)}</span></div>
            <b>${esc(p.name)}</b><span>${years(p.items[p.items.length - 1].t, p.items[0].t)}</span></a>`;
        }).join('') || '<div class="empty">Keine Orte.</div>';
      };
      draw();
      $('#pf', view).oninput = (e) => draw(e.target.value);
    },

    ort(view, name) {
      const items = S.items.filter((it) => it.p === name);
      const gps = items.filter((it) => it.la !== undefined);
      galleryView(view, {
        title: `<a href="#/orte" class="muted" style="color:var(--muted)">Orte</a> / ${esc(name)}`,
        sub: `${plural(items.length, 'Element', 'Elemente')} · ${items.length ? years(items[items.length - 1].t, items[0].t) : ''}`,
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
      view.innerHTML = `<div class="head"><div><h1>Personen</h1><div class="sub">wird geladen …</div></div></div>`;
      await loadPeople().catch(() => {});
      if (route.view !== 'personen') return;
      const info = S.peopleInfo;
      const named = S.people.filter((p) => p.name), unnamed = S.people.filter((p) => !p.name);
      let banner = '';
      if (S.config.faces === 'off') banner = `<div class="banner">${icon('info')}<div>Gesichtserkennung ist ausgeschaltet. Sie läuft in einem eigenen Container; einschalten unter <a class="link" href="/setup">Einstellungen → Gesichter</a>.</div></div>`;
      else if (!info.detection) banner = `<div class="banner">${icon('info')}<div>Die Gesichtserkennung antwortet nicht${info.error ? `: ${esc(info.error)}` : ''}. Läuft der Container? <a class="link" href="/setup">Einstellungen</a></div></div>`;
      else if (!info.recognition) banner = `<div class="banner">${icon('info')}<div>${num(info.faces)} Fotos auf Gesichter geprüft. Zum <b>Gruppieren nach Personen</b> fehlt der Gesichtserkennung das Modell <code>face_recognition_sface_2021dec.onnx</code>.</div></div>`;
      else if (info.faceTodo > info.faces) banner = `<div class="banner">${icon('info')}<div>Gesichter werden noch gesucht (${num(info.faces)} von ${num(info.faceTodo)}). Die Gruppen werden laufend besser.</div></div>`;
      const card = (p) => `<a class="person" href="#/person/${encodeURIComponent(p.id)}"><span class="face"><img src="${faceURL(p.cover.item, p.cover.idx)}" alt="" loading="lazy"></span>
        <b class="${p.name ? '' : 'unnamed'}">${esc(p.name || 'Name hinzufügen')}</b><span>${plural(p.count, 'Foto', 'Fotos')}</span></a>`;
      view.innerHTML = `<div class="head"><div><h1>Personen</h1><div class="sub">${plural(named.length, 'Person', 'Personen')} mit Namen · ${plural(unnamed.length, 'Gruppe', 'Gruppen')} ohne Namen</div></div></div>
        ${banner}
        ${named.length ? `<div class="people">${named.map(card).join('')}</div>` : ''}
        ${unnamed.length ? `<div class="head"><div><h1 style="font-size:20px">Wer ist das?</h1><div class="sub">Namen vergeben, gleiche Namen werden zusammengeführt</div></div></div><div class="people">${unnamed.map(card).join('')}</div>` : ''}
        ${!S.people.length && info.recognition ? `<div class="empty">${icon('people')}<h2>Noch keine Personen</h2><p>Die Gesichtssuche läuft im Hintergrund.</p></div>` : ''}`;
    },

    async person(view, id) {
      if (!S.peopleById.has(id)) await loadPeople().catch(() => {});
      const p = S.peopleById.get(id);
      if (!p) { view.innerHTML = `<div class="empty"><h2>Person nicht gefunden</h2><p><a class="link" href="#/personen">Zur Übersicht</a></p></div>`; return; }
      const items = S.items.filter((it) => it.pp?.includes(id));
      const names = [...new Set(S.people.filter((x) => x.name && x.id !== id).map((x) => x.name))].sort();
      galleryView(view, {
        title: '', items, kinds: true,
        before: '',
      });
      const head = $('.head', view);
      head.firstElementChild.outerHTML = `<div class="person-head"><span class="face"><img src="${faceURL(p.cover.item, p.cover.idx)}" alt=""></span><div>
        <input class="name-in" id="pname" value="${esc(p.name)}" placeholder="Name hinzufügen" list="pn2"><datalist id="pn2">${names.map((n) => `<option value="${esc(n)}">`).join('')}</datalist>
        <div class="sub">${plural(items.length, 'Foto', 'Fotos')} · ${plural(p.faces, 'Gesicht', 'Gesichter')} · gleicher Name führt Personen zusammen</div></div></div>`;
      $('.head .tools', view).insertAdjacentHTML('afterbegin', `<button class="btn" id="phide" title="Diese Gruppe nicht mehr zeigen">${icon('eye-off')}<span>Ausblenden</span></button>`);
      const inp = $('#pname', view);
      const save = async () => {
        const name = inp.value.trim();
        if (name === p.name) return;
        await post(`/api/people/${encodeURIComponent(id)}`, { name });
        toast(name ? `Gespeichert: ${name}` : 'Name entfernt');
        await loadPeople();
        await loadItems();
        const np = S.people.find((x) => x.name === name);
        location.hash = np ? `#/person/${encodeURIComponent(np.id)}` : '#/personen';
      };
      inp.onkeydown = (e) => { if (e.key === 'Enter') inp.blur(); };
      inp.onchange = save;
      $('#phide', view).onclick = async () => {
        await post(`/api/people/${encodeURIComponent(id)}`, { name: '~' });
        toast('Ausgeblendet');
        await loadPeople();
        location.hash = '#/personen';
      };
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
      if (!node) { view.innerHTML = '<div class="empty"><h2>Ordner nicht gefunden</h2></div>'; return; }
      // single root: skip straight into it
      if (!path && root.kids.size === 1) { node = [...root.kids.values()][0]; }
      const crumbs = ['<a href="#/alben">Alben</a>'];
      if (node.path) {
        let acc = '';
        for (const part of node.path.split('/')) {
          acc = acc ? `${acc}/${part}` : part;
          crumbs.push(`<span class="sep">/</span><a href="#/alben/${encodeURIComponent(acc)}">${esc(part)}</a>`);
        }
      }
      const kids = [...node.kids.values()].sort((a, b) => b.name.localeCompare(a.name, 'de', { numeric: true }));
      const cards = kids.length ? `<div class="cards" style="margin-bottom:26px">${kids.map((k) => `<a class="card folder" href="#/alben/${encodeURIComponent(k.path)}">
        <div class="cv"><img src="${thumbURL(k.cover)}" alt="" loading="lazy"><span class="n">${num(k.total)}</span></div>
        <b>${esc(k.name)}</b><span>${k.from ? years(k.from, k.to) : ''}</span></a>`).join('')}</div>` : '';
      if (node.items.length) {
        galleryView(view, {
          title: esc(node.name || 'Alben'), sub: `<span class="crumbs">${crumbs.join('')}</span>`,
          items: node.items, before: cards, group: kids.length ? 'none' : S.group,
        });
      } else {
        view.innerHTML = `<div class="head"><div><h1>${esc(node.name || 'Alben')}</h1><div class="sub crumbs">${crumbs.join('')}</div></div>
          <div class="tools"><span class="muted">${plural(kids.length, 'Ordner', 'Ordner')} · ${plural(node.total, 'Element', 'Elemente')}</span></div></div>${cards}`;
      }
    },

    karte(view) {
      const gps = S.items.filter((it) => it.la !== undefined);
      view.innerHTML = `<div class="head"><div><h1>Karte</h1><div class="sub">${plural(gps.length, 'Element', 'Elemente')} mit Ortsangabe</div></div></div>
        <div class="mapwrap"><div class="map" id="map"></div><div class="map-side"><header id="mapHead"></header><div class="list" id="mapList"></div></div></div>`;
      if (!window.L) { $('#map', view).innerHTML = '<div class="empty">Karte konnte nicht geladen werden (keine Internetverbindung?).</div>'; return; }
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
      const side = new Gallery($('#mapList', view), { group: 'none', small: true, scrollRoot: $('#mapList', view) });
      extraGalleries.push(side);
      const upd = debounce(() => {
        const b = map.getBounds();
        const inb = gps.filter((it) => b.contains([it.la, it.lo]));
        $('#mapHead', view).innerHTML = `<b>${plural(inb.length, 'Element', 'Elemente')}</b> im Ausschnitt${inb.length > 600 ? ' · die neuesten 600' : ''}`;
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
  let statusT = 0;
  const scheduleReload = debounce(async () => { await loadItems(); if (!Viewer.open_) rerender(); }, 300);
  async function pollStatus() {
    clearTimeout(statusT);
    let busy = false;
    try {
      const res = await api('/api/status');
      const { status: s, scanning } = res;
      S.status = s;
      const rows = [];
      const bar = (label, a, b) => { if (b > 0 && a < b) { busy = true; rows.push(`<div class="row"><span>${label} ${num(a)} / ${num(b)}</span><span class="bar"><i style="width:${(a / b) * 100}%"></i></span></div>`); } };
      if (scanning) {
        busy = true;
        if (s.metaTodo > 0) bar('Metadaten', s.meta, s.metaTodo);
        else rows.push(`<div class="row"><span>${esc(s.phase)} · ${num(s.scanned)}</span></div>`);
      }
      const liveVideos = Math.max(0, S.items.filter((it) => it.lv).length);
      bar('Vorschaubilder', s.thumbs + s.posters, s.photos + s.videos + liveVideos);
      bar('Video-Vorschauen', s.previews, s.videos + liveVideos);
      if (s.faceDetection && s.faceTodo) bar('Gesichter', s.faces, s.faceTodo);
      const bk = res.backup;
      if (bk?.running) { busy = true; rows.push(`<div class="row"><span>Sicherung: ${esc(bk.phase || '')} · ${num(bk.copied)} kopiert</span></div>`); }
      if (!rows.length) rows.push(`<div class="row"><span>${num(s.photos)} Fotos · ${num(s.videos)} Videos${s.people ? ` · ${num(s.people)} Personen` : ''}</span></div>`);
      $('#status').innerHTML = rows.join('');
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

  const Theme = {
    mq: matchMedia('(prefers-color-scheme: light)'),
    mode: (() => { try { const m = localStorage.getItem('schuhkarton-theme'); return m === 'light' || m === 'dark' || m === 'system' ? m : ''; } catch { return ''; } })() || 'system',
    label: { system: 'System', light: 'Hell', dark: 'Dunkel' },
    apply() {
      const t = this.mode === 'system' ? (this.mq.matches ? 'light' : 'dark') : this.mode;
      const changed = document.documentElement.dataset.theme !== t;
      document.documentElement.dataset.theme = t;
      $('#themeColor')?.setAttribute('content', t === 'light' ? '#f5f4f1' : '#0c0d0f');
      $('#theme').innerHTML = icon(this.mode === 'system' ? 'monitor' : this.mode === 'light' ? 'sun' : 'moon') + `<span class="lbl">${this.label[this.mode]}</span>`;
      $('#theme').title = `Design: ${this.label[this.mode]} (wechseln)`;
      return changed;
    },
    cycle() {
      this.mode = { system: 'light', light: 'dark', dark: 'system' }[this.mode];
      try { localStorage.setItem('schuhkarton-theme', this.mode); } catch { /* ignore */ }
      this.apply();
      toast(`Design: ${this.label[this.mode]}`);
    },
  };
  Theme.apply();
  Theme.mq.addEventListener('change', () => { if (Theme.mode === 'system') Theme.apply(); });
  $('#theme').onclick = () => Theme.cycle();
  $('#rescan').onclick = async () => { await post('/api/rescan', {}); toast('Ordner werden neu eingelesen'); pollStatus(); };
  $('#menu').onclick = () => $('#side').classList.toggle('open');
  $('#slideBtn').onclick = () => {
    const list = (S.gallery?.items || S.items).filter((it) => it.k === 1);
    if (!list.length) return;
    Viewer.open(list, 0);
    Viewer.startSlides();
  };
  document.addEventListener('click', (e) => {
    if ($('#side').classList.contains('open') && !e.target.closest('#side') && !e.target.closest('#menu')) $('#side').classList.remove('open');
  });

  // ---------------------------------------------------------------------------
  // start
  // ---------------------------------------------------------------------------
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
    try { await loadItems(); } catch (e) { $('#view').innerHTML = `<div class="empty"><h2>Server nicht erreichbar</h2><p>${esc(e.message)}</p></div>`; }
    loadPeople().catch(() => {});
    onRoute();
    pollStatus();
  })();
})();
