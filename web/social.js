/* Schuhkarton — notes: people with an account write notes on photos;
   everybody reads them, the search finds them. Loaded after admin.js; uses
   window.SK. (Deleting is for every account now: the trash, admin.js.) */
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
  // notes in the viewer details
  // ---------------------------------------------------------------------------
  // note times are real moments; the date helpers expect wall clock as UTC
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
    if (!list && !composer) return '';
    return `<section class="v-notes"><h3>${esc(t('Notizen'))}${notes.length ? ` <em>${num(notes.length)}</em>` : ''}</h3>${list}${composer}</section>`;
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
})();
