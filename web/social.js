/* Schuhkarton — marks for deletion and notes.
   Marks: every account marks photos it would delete; they disappear for that
   account (view "Vorgemerkt" brings them back), admins decide. Notes: people
   with an account write notes on photos; everybody reads them, the search
   finds them. Loaded after admin.js; uses window.SK. */
'use strict';
(() => {
  const SK = window.SK;
  if (!SK) return;
  const { t, S, $, $$, esc, icon, api, toast, num } = SK;
  const V = SK.Viewer;
  const ui = SK.ui;
  const user = () => SK.Account.can('user');
  const admin = () => SK.Account.can('admin');
  const me = () => SK.Account.me?.name || '';
  const inMarked = () => SK.route().view === 'vorgemerkt';

  document.querySelector('body > svg')?.insertAdjacentHTML('beforeend', `
    <symbol id="i-mark" viewBox="0 0 24 24"><path d="M5 21V4.5h11l-1.8 4 1.8 4H5"/></symbol>
    <symbol id="i-unmark" viewBox="0 0 24 24"><path d="M5 21V4.5h11l-1.8 4 1.8 4H5"/><path d="m3 3 18 18"/></symbol>
    <symbol id="i-note" viewBox="0 0 24 24"><path d="M5 4.5h14v11l-4.5 4.5H5z"/><path d="M14.5 20v-4.5H19M8.5 9h7M8.5 12.5h4"/></symbol>`);

  async function send(method, path, body) {
    try {
      const r = await api(path, { method, headers: { 'Content-Type': 'application/json' }, body: body === undefined ? undefined : JSON.stringify(body) });
      if (!r.ok) throw new Error(r.error || t('Fehler'));
      return r;
    } catch (e) {
      toast(t('Fehler: {e}', { e: t(String(e.message || e).replace(/^\d{3} /, '').trim()) }));
      return null;
    }
  }

  // ---------------------------------------------------------------------------
  // marks for deletion
  // ---------------------------------------------------------------------------
  const markCount = () => {
    const el = $('#markCount');
    if (el) el.textContent = S.marked?.length ? num(S.marked.length) : '';
  };

  async function setMarks(ids, on, { undo = true } = {}) {
    if (!user() || !ids.length) return false;
    const r = await send('POST', '/api/marks', { ids, on });
    if (!r) return false;
    SK.endSelection();
    const msg = on
      ? (ids.length === 1 ? t('Zum Löschen vorgemerkt – für dich ausgeblendet') : t('{n} Elemente zum Löschen vorgemerkt', { n: num(ids.length) }))
      : (ids.length === 1 ? t('Nicht mehr vorgemerkt') : t('{n} Elemente nicht mehr vorgemerkt', { n: num(ids.length) }));
    if (undo) ui.snack(msg, t('Rückgängig'), async () => { if (await send('POST', '/api/marks', { ids, on: !on })) { await ui.refreshAfter(); markCount(); } });
    else toast(msg);
    // gone from this list: the viewer moves on (marking elsewhere, unmarking in "Vorgemerkt")
    const leaves = on !== inMarked();
    await ui.refreshAfter({}, leaves ? ids : []);
    markCount();
    return true;
  }
  SK.setMarks = setMarks;

  // viewer
  $('#vInfo')?.insertAdjacentHTML('beforebegin', `<button class="vbtn" id="vMark" data-need="user" title="${esc(t('Zum Löschen vormerken (M)'))}">${icon('mark')}</button>`);
  const vMark = $('#vMark');
  const syncMark = () => {
    if (!vMark || !V.it) return;
    const on = !!(V.detail?.marked ?? S.byId.get(V.it.id)?.mk);
    vMark.classList.toggle('on', on);
    vMark.innerHTML = icon(on ? 'unmark' : 'mark');
    vMark.title = on ? t('Nicht mehr vormerken (M)') : t('Zum Löschen vormerken (M)');
  };
  if (vMark) vMark.onclick = () => { if (V.it) setMarks([V.it.id], !(V.detail?.marked ?? S.byId.get(V.it.id)?.mk)); };

  // selection bar
  $('#selClose')?.insertAdjacentHTML('beforebegin', `<button class="btn" id="selMark" data-need="user" title="${esc(t('Zum Löschen vormerken'))}">${icon('mark')}<span>${esc(t('Vormerken'))}</span><span class="s">${esc(t('Vormerken'))}</span></button>`);
  const selMark = $('#selMark');
  const syncSelMark = () => {
    if (!selMark) return;
    const back = inMarked();
    selMark.innerHTML = `${icon(back ? 'unmark' : 'mark')}<span>${esc(back ? t('Nicht vormerken') : t('Vormerken'))}</span><span class="s">${esc(back ? t('Zurück') : t('Vormerken'))}</span>`;
    selMark.title = back ? t('Nicht mehr zum Löschen vormerken') : t('Zum Löschen vormerken');
  };
  if (selMark) {
    selMark.onclick = () => {
      const ids = [...S.sel];
      if (!ids.length) { toast(t('Erst Elemente auswählen')); return; }
      setMarks(ids, !inMarked());
    };
    new MutationObserver(syncSelMark).observe($('#selbar'), { attributes: true, attributeFilter: ['hidden'] });
  }

  addEventListener('keydown', (e) => {
    if (!V.open_ || !user() || e.ctrlKey || e.metaKey || e.altKey || SK.isTyping(e.target) || $('.adm-dlg')) return;
    if (e.key === 'm' || e.key === 'M') { e.preventDefault(); vMark?.click(); }
  });

  // sidebar
  $('#nav a[data-v="favoriten"]')?.insertAdjacentHTML('afterend',
    `<a href="#/vorgemerkt" data-v="vorgemerkt" data-need="user">${icon('mark')}<span>${esc(t('Vorgemerkt'))}</span><em class="count" id="markCount"></em></a>`);

  // view: my marks, and for admins everybody's
  SK.VIEWS.vorgemerkt = async (view, arg) => {
    if (!user()) {
      view.innerHTML = `<div class="empty">${icon('mark')}<h2>${esc(t('Vorgemerkt'))}</h2><p>${esc(t('Zum Löschen vormerken geht mit einem Konto.'))}</p><p><a class="btn primary" href="${esc(SK.Account.loginURL())}">${esc(t('Anmelden'))}</a></p></div>`;
      return;
    }
    markCount();
    const all = arg === 'alle' && admin();
    const tabs = admin() ? `<div class="seg mk-tabs"><a class="${all ? '' : 'on'}" href="#/vorgemerkt">${esc(t('Meine'))}</a><a class="${all ? 'on' : ''}" href="#/vorgemerkt/alle">${esc(t('Alle Konten'))}<em id="mkAll"></em></a></div>` : '';
    if (!all) {
      const items = S.marked || [];
      SK.galleryView(view, {
        title: esc(t('Vorgemerkt')),
        sub: (list) => (list.length ? t('{n} zum Löschen vorgemerkt · nur für dich ausgeblendet', { n: num(list.length) }) : t('Nichts vorgemerkt')),
        items, group: 'none', scrub: false,
        before: tabs,
        emptyText: t('Mit der Fahne in der Ansicht oder in der Auswahl merkst du Fotos zum Löschen vor. Sie verschwinden dann für dich; ein Admin entscheidet, ob sie gelöscht werden.'),
        tools: { group: false },
      });
      if (items.length) {
        $('.head .tools', view)?.insertAdjacentHTML('afterbegin', `<button class="btn" data-unall>${icon('unmark')}<span>${esc(t('Alle zurückholen'))}</span></button>`);
        $('[data-unall]', view).onclick = async () => {
          const ok = await ui.confirmDlg(t('Alle Vormerkungen aufheben?'), t('{n} Elemente erscheinen wieder überall.', { n: num(items.length) }), t('Alle zurückholen'));
          if (ok) setMarks(items.map((it) => it.id), false);
        };
      }
      if (admin()) api('/api/admin/marks').then((r) => { const el = $('#mkAll', view); if (el && r.items?.length) el.textContent = num(r.items.length); }).catch(() => {});
      return;
    }
    // admins: everybody's marks, most marked first
    view.innerHTML = `<div class="head"><div><h1>${esc(t('Vorgemerkt'))}</h1><div class="sub">${esc(t('Wird geladen …'))}</div></div></div>${tabs}`;
    const res = await api('/api/admin/marks').catch(() => null);
    if (!res) { $('.sub', view).textContent = t('Nicht erreichbar'); return; }
    const list = (res.items || []).filter((m) => S.byId.has(m.id));
    const when = (ts) => SK.fmtDay(local(ts));
    view.innerHTML = `<div class="head"><div><h1>${esc(t('Vorgemerkt'))}</h1><div class="sub">${esc(list.length ? SK.pl(list.length, '{n} Element von allen Konten zum Löschen vorgemerkt', '{n} Elemente von allen Konten zum Löschen vorgemerkt') : t('Niemand hat etwas vorgemerkt'))}</div></div>
      <div class="tools adm-ttools"${list.length ? '' : ' hidden'}>
        <button class="btn" data-all>${icon('select')}<span>${esc(t('Alle auswählen'))}</span></button>
      </div></div>${tabs}
      ${list.length ? `<div class="adm-trash mk-all">${list.map((m) => {
        const it = S.byId.get(m.id);
        const who = m.marks.map((x) => x.user).join(', ');
        return `<div class="adm-card mk-card" data-id="${esc(m.id)}">
          <span class="cv"><img loading="lazy" alt="" src="${SK.thumbURL(it)}">
            ${it.k === 2 ? `<span class="badge br">${icon('play', 'fill')}</span>` : ''}
            <button class="check" data-pick title="${esc(t('Auswählen'))}">${icon('check')}</button>
            ${m.marks.length > 1 ? `<span class="badge tr mk-n" title="${esc(t('von {n} Konten vorgemerkt', { n: m.marks.length }))}">${icon('mark')}${m.marks.length}</span>` : ''}</span>
          <b title="${esc(who)}">${esc(who)}</b><small>${esc(when(m.marks[m.marks.length - 1].at))}${it.p ? ` · ${esc(it.p)}` : ''}</small></div>`;
      }).join('')}</div>` : `<div class="empty">${icon('mark')}<h2>${esc(t('Nichts vorgemerkt'))}</h2><p>${esc(t('Wenn jemand Fotos zum Löschen vormerkt, entscheidest du hier.'))}</p></div>`}`;
    if (!list.length) return;
    const btn = (k) => $(`[data-${k}]`, view);
    // the selection bar, with the actions for marks: the check (or any click while picking) picks
    const pick = S.sel;
    $('.mk-all', view).onclick = (e) => {
      const c = e.target.closest('.mk-card');
      if (!c) return;
      if (e.target.closest('[data-pick]') || pick.size) {
        pick.has(c.dataset.id) ? pick.delete(c.dataset.id) : pick.add(c.dataset.id);
        S.selecting = true;
        SK.selectionChanged();
        return;
      }
      const items = list.map((m) => S.byId.get(m.id));
      V.open(items, items.findIndex((it) => it.id === c.dataset.id));
    };
    btn('all').onclick = () => {
      if (pick.size === list.length) SK.endSelection();
      else { for (const m of list) pick.add(m.id); S.selecting = true; SK.selectionChanged(); }
    };
    const clear = async (ids) => {
      const r = await ui.call('/api/admin/marks/clear', { ids });
      if (!r) return;
      toast(t('{n} Vormerkungen entfernt', { n: num(r.changed) }));
      await SK.loadItems();
      markCount();
      SK.render();
    };
    const del = async (ids) => {
      const ok = await ui.confirmDlg(t('{n} Elemente in den Papierkorb legen?', { n: num(ids.length) }),
        t('Im Papierkorb lässt sich alles wiederherstellen, bis er geleert wird.'), t('In den Papierkorb'), true);
      if (!ok) return;
      const r = await ui.call('/api/admin/delete', { ids });
      if (!r) return;
      const entries = r.entries || [];
      ui.snack(t('{n} Elemente in den Papierkorb gelegt', { n: num(r.changed) }), t('Rückgängig'), async () => {
        if (await ui.call('/api/admin/trash/restore', { ids: entries })) { await SK.loadItems(); SK.render(); ui.trashCount(); }
      });
      await SK.loadItems();
      ui.trashCount();
      markCount();
      SK.render();
    };
    SK.pageSelection({
      actions: [
        { icon: 'unmark', label: t('Vormerkung entfernen'), short: t('Entfernen'), run: clear },
        { icon: 'trash', label: t('In den Papierkorb'), short: t('Löschen'), danger: true, run: del },
      ],
      sync: () => {
        for (const c of $$('.mk-card', view)) c.classList.toggle('sel', pick.has(c.dataset.id));
        $('span', btn('all')).textContent = pick.size === list.length ? t('Auswahl aufheben') : t('Alle auswählen');
      },
    });
  };

  // ---------------------------------------------------------------------------
  // notes in the viewer details
  // ---------------------------------------------------------------------------
  // note and mark times are real moments; the date helpers expect wall clock as UTC
  const local = (ts) => ts - new Date(ts * 1000).getTimezoneOffset() * 60;
  const fmtWhen = (ts) => `${SK.fmtDay(local(ts))} ${SK.fmtTime(local(ts))}`;
  const linkify = (s) => esc(s).replace(/\n/g, '<br>');

  function notesHTML(d) {
    const notes = d.notes || [];
    const mine = (n) => n.user === me();
    const list = notes.map((n) => `<div class="nt" data-n="${esc(n.id)}">
        <div class="nt-text">${linkify(n.text)}</div>
        <div class="nt-meta"><b>${esc(n.user)}</b> · ${esc(fmtWhen(n.at))}${n.edited ? ` · ${esc(t('bearbeitet'))}` : ''}
          ${mine(n) ? `<button class="link" data-nedit>${esc(t('Ändern'))}</button>` : ''}
          ${mine(n) || admin() ? `<button class="link danger" data-ndel>${esc(t('Löschen'))}</button>` : ''}</div></div>`).join('');
    const composer = user()
      ? `<form class="nt-new"><textarea rows="2" maxlength="2000" placeholder="${esc(t('Notiz hinzufügen … (wer, was, wo)'))}"></textarea>
          <div class="nt-row"><span class="muted">${esc(t('Für alle sichtbar und durchsuchbar'))}</span><button type="submit" class="btn primary" disabled>${esc(t('Speichern'))}</button></div></form>`
      : (notes.length ? '' : `<p class="muted">${esc(t('Noch keine Notizen.'))} <a class="link" href="${esc(SK.Account.loginURL())}">${esc(t('Anmelden'))}</a> ${esc(t('zum Schreiben.'))}</p>`);
    const marked = d.marked ? `<div class="nt-flag">${icon('mark')}<span>${esc(t('Von dir zum Löschen vorgemerkt – für dich ausgeblendet.'))}</span><button class="link" data-unmark>${esc(t('Zurückholen'))}</button></div>` : '';
    const markedBy = admin() && d.markedBy?.length ? `<div class="nt-flag adminflag">${icon('mark')}<span>${esc(t('Zum Löschen vorgemerkt von {w}', { w: d.markedBy.map((m) => m.user).join(', ') }))}</span>
        <button class="link" data-mclear>${esc(t('Vormerkung entfernen'))}</button></div>` : '';
    if (!list && !composer && !marked && !markedBy) return '';
    return `<section class="v-notes"><h3>${esc(t('Notizen'))}${notes.length ? ` <em>${num(notes.length)}</em>` : ''}</h3>${marked}${markedBy}${list}${composer}</section>`;
  }

  async function reloadDetail(it) {
    const d = await api(`/api/item/${it.id}`).catch(() => null);
    if (!d || V.it !== it) return;
    V.detail = d;
    const local = S.byId.get(it.id);
    if (local) local.nt = (d.notes || []).length;
    V.renderInfo();
  }

  function bindNotes(panel) {
    const it = V.it, d = V.detail;
    const sec = $('.v-notes', panel);
    if (!sec || !it || !d) return;
    const form = $('.nt-new', sec);
    if (form) {
      const ta = $('textarea', form), go = $('button[type=submit]', form);
      ta.oninput = () => { go.disabled = !ta.value.trim(); ta.style.height = 'auto'; ta.style.height = `${Math.min(220, ta.scrollHeight + 2)}px`; };
      ta.onkeydown = (e) => { e.stopPropagation(); if (e.key === 'Enter' && (e.ctrlKey || e.metaKey) && !go.disabled) form.requestSubmit(); };
      form.onsubmit = async (e) => {
        e.preventDefault();
        go.disabled = true;
        const r = await send('POST', `/api/notes/${encodeURIComponent(it.id)}`, { text: ta.value });
        if (!r) { go.disabled = false; return; }
        toast(t('Notiz gespeichert'));
        SK.scheduleReload();
        reloadDetail(it);
      };
    }
    sec.onclick = async (e) => {
      const n = e.target.closest('[data-n]');
      if (e.target.closest('[data-unmark]')) { setMarks([it.id], false); return; }
      if (e.target.closest('[data-mclear]')) {
        if (await ui.call('/api/admin/marks/clear', { ids: [it.id] })) { toast(t('Vormerkung entfernt')); reloadDetail(it); }
        return;
      }
      if (!n) return;
      const id = n.dataset.n;
      if (e.target.closest('[data-ndel]')) {
        const ok = await ui.confirmDlg(t('Notiz löschen?'), t('Die Notiz verschwindet für alle.'), t('Löschen'), true);
        if (ok && await send('DELETE', `/api/notes/${encodeURIComponent(it.id)}/${encodeURIComponent(id)}`)) { toast(t('Notiz gelöscht')); SK.scheduleReload(); reloadDetail(it); }
        return;
      }
      if (e.target.closest('[data-nedit]')) {
        const note = d.notes.find((x) => x.id === id);
        n.innerHTML = `<form class="nt-new nt-edit"><textarea rows="3" maxlength="2000">${esc(note.text)}</textarea>
          <div class="nt-row"><button type="button" class="btn" data-cancel>${esc(t('Abbrechen'))}</button><button type="submit" class="btn primary">${esc(t('Speichern'))}</button></div></form>`;
        const f = $('form', n), ta = $('textarea', f);
        ta.focus();
        ta.setSelectionRange(ta.value.length, ta.value.length);
        ta.onkeydown = (ev) => { ev.stopPropagation(); if (ev.key === 'Escape') V.renderInfo(); if (ev.key === 'Enter' && (ev.ctrlKey || ev.metaKey)) f.requestSubmit(); };
        $('[data-cancel]', f).onclick = () => V.renderInfo();
        f.onsubmit = async (ev) => {
          ev.preventDefault();
          if (await send('PATCH', `/api/notes/${encodeURIComponent(it.id)}/${encodeURIComponent(id)}`, { text: ta.value })) { toast(t('Notiz geändert')); SK.scheduleReload(); reloadDetail(it); }
        };
      }
    };
  }

  // the details panel gets the notes
  const render0 = V.renderInfo.bind(V);
  V.renderInfo = function renderInfo(...a) {
    render0(...a);
    const d = this.detail;
    syncMark();
    const panel = $('#vInfoPanel');
    if (!this.showInfo || !d || !panel) return;
    const html = notesHTML(d);
    if (!html) return;
    // after the picture text (or before camera and file details)
    const heads = $$('h3', panel);
    const after = heads.find((h) => h.textContent === t('Text im Bild'));
    const before = heads.find((h) => h.textContent === t('Kamera')) || heads.find((h) => h.textContent === t('Datei'));
    if (after) after.nextElementSibling?.insertAdjacentHTML('afterend', html);
    else if (before) before.insertAdjacentHTML('beforebegin', html);
    else panel.insertAdjacentHTML('beforeend', html);
    bindNotes(panel);
  };
  const show0 = V.show.bind(V);
  V.show = function show(...a) {
    const r = show0(...a);
    syncMark();
    return r;
  };

  // counts once the role and the items are known
  new MutationObserver(() => { markCount(); syncSelMark(); }).observe(document.body, { attributes: true, attributeFilter: ['data-role'] });
  addEventListener('hashchange', syncSelMark);
  const all = document.querySelector('[data-c="all"]');
  if (all) new MutationObserver(markCount).observe(all, { childList: true, characterData: true, subtree: true });
  const load0 = SK.loadItems;
  SK.loadItems = async (...a) => { const r = await load0(...a); markCount(); return r; };
})();
