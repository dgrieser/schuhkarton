/* Schuhkarton — filing rules (admins). Rules say where files live, from
   their metadata: a condition in the search syntax and a folder template
   like {jahr}/{datum:MM MMMM} {ort}. Without rules nothing moves. The page
   shows live what each rule would do before anything is saved; applying
   moves the files in the background. Loaded after admin.js; uses window.SK. */
'use strict';
(() => {
  const SK = window.SK;
  if (!SK) return;
  const { t, $, $$, esc, icon, api, post, toast, num, pl, debounce } = SK;
  const ui = SK.ui;
  const admin = () => SK.Account.can('admin');

  document.querySelector('body > svg')?.insertAdjacentHTML('beforeend', `
    <symbol id="i-rules" viewBox="0 0 24 24"><path d="M4 5.5h6M4 5.5v13a1.5 1.5 0 0 0 1.5 1.5H8M4 12h4"/><rect x="11" y="3.5" width="9" height="4.5" rx="1.5"/><rect x="11" y="9.75" width="9" height="4.5" rx="1.5"/><rect x="11" y="16" width="9" height="4.5" rx="1.5"/></symbol>
    <symbol id="i-up" viewBox="0 0 24 24"><path d="m6 15 6-6 6 6"/></symbol>
    <symbol id="i-plus" viewBox="0 0 24 24"><path d="M12 5v14M5 12h14"/></symbol>`);

  // the layout Schuhkarton was built around, as rules
  const LAYOUT = [
    { name: 'Dokumente', when: 'typ:dokument', path: 'Dokumente/{jahr}/{datum:MM MMMM} {kategorie}' },
    { name: 'Fotos und Videos', when: '', path: '{jahr}/{datum:MM MMMM} {ort|Kamera}' },
  ];
  // German field names, or English ones on an English page (both work)
  const EN = SK.LANG === 'en';
  const F = (de, en) => (EN ? en : de);
  const CHIPS = [
    [F('{jahr}', '{year}'), 'Jahr'], [F('{datum:MM}', '{date:MM}'), 'Monat (07)'], [F('{datum:MMMM}', '{date:MMMM}'), 'Monatsname'],
    [F('{datum:MMM}', '{date:MMM}'), 'Monat kurz'], [F('{datum:dd}', '{date:dd}'), 'Tag'], [F('{datum:yyyy-MM-dd}', '{date:yyyy-MM-dd}'), 'Datum'],
    [F('{ort}', '{place}'), 'Ort'], [F('{kategorie}', '{category}'), 'Kategorie'], [F('{kamera}', '{camera}'), 'Kamera'],
    [F('{typ}', '{kind}'), 'Typ'], [F('{schlagwort}', '{keyword}'), 'Schlagwort'], ['{person}', 'Personen'], ['{album}', 'Bisheriger Ordner'],
  ];
  const SAMPLE = F('{jahr}/{datum:MM MMMM} {ort}', '{year}/{date:MM MMMM} {place}');
  const FORMATS = [
    ['yyyy', '2025'], ['yy', '25'], ['MM', '07'], ['M', '7'], ['MMMM', 'Juli'], ['MMM', 'Jul'], ['dd', '05'], ['d', '5'],
    ['EEEE', 'Samstag'], ['EEE', 'Sa'], ['QQQ', 'Q3'], ['ww', '27'], ["'KW' ww", 'KW 27'],
  ];

  const R = { rules: null, saved: '', locales: [], locked: false, preview: null, seq: 0, job: null };
  let pollT = 0;

  async function load() {
    const r = await api('/api/admin/rules').catch(() => null);
    if (!r) return false;
    R.rules = { locale: r.rules.locale || 'de', auto: r.rules.auto !== false, list: (r.rules.list || []).map((x) => ({ name: x.name || '', when: x.when || '', path: x.path || '' })) };
    R.saved = JSON.stringify(R.rules);
    R.locales = r.locales || [];
    R.locked = !!r.locked;
    R.job = { running: r.running, done: r.done, todo: r.todo };
    return true;
  }
  const dirty = () => JSON.stringify(R.rules) !== R.saved;

  SK.VIEWS.ablage = async (view) => {
    clearTimeout(pollT);
    if (!admin()) {
      view.innerHTML = `<div class="empty">${icon('rules')}<h2>${esc(t('Ablage'))}</h2><p>${esc(t('Nur für Admins.'))}</p></div>`;
      return;
    }
    view.innerHTML = `<div class="head"><div><h1>${esc(t('Ablage'))}</h1><div class="sub">${esc(t('Wird geladen …'))}</div></div></div>`;
    if (!(await load())) { $('.sub', view).textContent = t('Nicht erreichbar'); return; }
    draw(view);
    preview(view);
    poll(view);
  };

  // ---------------------------------------------------------------------------
  // page
  // ---------------------------------------------------------------------------
  function draw(view) {
    const rs = R.rules;
    view.innerHTML = `
      <div class="head"><div><h1>${esc(t('Ablage'))}</h1>
        <div class="sub">${esc(t('Regeln, in welchem Ordner eine Datei liegt. Ohne Regeln bleibt alles, wie es ist.'))}</div></div>
        <div class="tools"><button class="btn" data-layout title="${esc(t('Regeln für die bisherige Ablage einsetzen'))}">${icon('spark')}<span>${esc(t('Wie bisher'))}</span></button></div></div>
      ${R.locked ? `<div class="banner">${icon('info')}<div>${esc(t('Die Regeln sind über Umgebungsvariablen festgelegt und lassen sich hier nicht ändern.'))}</div></div>` : ''}
      <div class="ru-job" hidden></div>
      <div class="ru-how">${icon('info')}<div>${t('Jede Datei folgt der <b>ersten</b> Regel, deren Bedingung passt. Die Bedingung ist eine Suche wie im Suchfeld (leer: alle Dateien), der Ordner eine Vorlage mit Feldern in geschweiften Klammern. Dateinamen bleiben, Live-Photo-Videos ziehen mit ihrem Foto um, leere Ordner verschwinden.')}</div></div>
      <div class="ru-opts">
        <label class="ru-opt"><span>${esc(t('Sprache der Monatsnamen'))}</span>
          <select data-locale>${R.locales.map((l) => `<option value="${esc(l.code)}" ${l.code === rs.locale ? 'selected' : ''}>${esc(l.name)}</option>`).join('')}</select></label>
        <label class="ru-opt ru-check"><input type="checkbox" data-auto ${rs.auto ? 'checked' : ''}><span>${esc(t('Nach dem Bearbeiten von Datum, Ort oder Kategorie sofort einsortieren'))}</span></label>
      </div>
      <div class="ru-list">${rs.list.map(ruleCard).join('') || `<div class="ru-none">${icon('rules')}<b>${esc(t('Keine Regeln'))}</b><span>${esc(t('Alle Dateien bleiben, wo sie sind. „Wie bisher“ setzt Regeln für Jahr/Monat Ort und Dokumente ein.'))}</span></div>`}</div>
      <button class="btn ru-add" data-add>${icon('plus')}<span>${esc(t('Regel hinzufügen'))}</span></button>
      <details class="ru-help"><summary>${esc(t('Felder und Datumsformate'))}</summary>
        <p>${t('{a} setzt einen Wert ein, {b} nimmt den Text, wenn der Wert fehlt, {c} formatiert das Aufnahmedatum mit Unicode-Datumssymbolen (wie in ICU, Java oder Excel). Text in einfachen Anführungszeichen bleibt, wie er ist.',
          { a: `<code>${F('{feld}', '{field}')}</code>`, b: `<code>${F('{feld|Text}', '{field|text}')}</code>`, c: `<code>${F('{datum:Format}', '{date:format}')}</code>` })}</p>
        <div class="ru-fmt">${FORMATS.map(([f, ex]) => `<code>${esc(f)}</code><span>${esc(ex)}</span>`).join('')}</div>
        <p class="faint">${esc(t('Felder: jahr, monat, datum, ort, kategorie, kamera, typ, schlagwort, person, album – auch englisch (year, month, date, place, category …).'))}</p>
      </details>
      <div class="ru-bar"><div class="ru-sum"></div>
        <button class="btn" data-save>${esc(t('Speichern'))}</button>
        <button class="btn primary" data-apply>${icon('rules')}<span>${esc(t('Jetzt einsortieren'))}</span></button></div>`;
    wire(view);
    drawPreview(view);
    drawJob(view);
  }

  function ruleCard(r, i) {
    const n = R.rules.list.length;
    return `<section class="ru-rule" data-i="${i}">
      <header><span class="ru-no">${i + 1}</span>
        <input class="ru-name" data-k="name" value="${esc(r.name)}" placeholder="${esc(t('Name (frei wählbar)'))}" ${R.locked ? 'disabled' : ''}>
        <button class="btn ghost icon" data-up title="${esc(t('Nach oben'))}" ${i === 0 ? 'disabled' : ''}>${icon('up')}</button>
        <button class="btn ghost icon" data-down title="${esc(t('Nach unten'))}" ${i === n - 1 ? 'disabled' : ''}>${icon('down')}</button>
        <button class="btn ghost icon" data-del title="${esc(t('Regel löschen'))}">${icon('trash')}</button></header>
      <label class="ru-f"><span>${esc(t('Wenn'))}</span><input data-k="when" value="${esc(r.when)}" placeholder="${esc(t('alle Dateien – oder z. B. typ:dokument, ort:Berlin'))}" spellcheck="false" autocapitalize="off" ${R.locked ? 'disabled' : ''}></label>
      <label class="ru-f"><span>${esc(t('Ordner'))}</span><input class="mono" data-k="path" value="${esc(r.path)}" placeholder="${esc(SAMPLE)}" spellcheck="false" autocapitalize="off" ${R.locked ? 'disabled' : ''}></label>
      <div class="ru-chips">${CHIPS.map(([v, l]) => `<button type="button" class="chip" data-ins="${esc(v)}" title="${esc(v)}">${esc(t(l))}</button>`).join('')}</div>
      <div class="ru-prev"></div>
    </section>`;
  }

  function wire(view) {
    const rs = R.rules;
    const later = debounce(() => preview(view), 350);
    const changed = () => { updateBar(view); later(); };
    $('[data-locale]', view).onchange = (e) => { rs.locale = e.target.value; changed(); };
    $('[data-auto]', view).onchange = (e) => { rs.auto = e.target.checked; updateBar(view); };
    for (const card of $$('.ru-rule', view)) {
      const i = +card.dataset.i;
      for (const inp of $$('input[data-k]', card)) inp.oninput = () => { rs.list[i][inp.dataset.k] = inp.value; changed(); };
      const pathIn = $('input[data-k="path"]', card);
      for (const c of $$('[data-ins]', card)) {
        c.onmousedown = (e) => e.preventDefault(); // keep the cursor in the field
        c.onclick = () => {
          const v = c.dataset.ins;
          const a = pathIn.selectionStart ?? pathIn.value.length, b = pathIn.selectionEnd ?? a;
          pathIn.value = pathIn.value.slice(0, a) + v + pathIn.value.slice(b);
          pathIn.focus();
          pathIn.setSelectionRange(a + v.length, a + v.length);
          rs.list[i].path = pathIn.value;
          changed();
        };
      }
      $('[data-up]', card).onclick = () => { [rs.list[i - 1], rs.list[i]] = [rs.list[i], rs.list[i - 1]]; redraw(view); };
      $('[data-down]', card).onclick = () => { [rs.list[i + 1], rs.list[i]] = [rs.list[i], rs.list[i + 1]]; redraw(view); };
      $('[data-del]', card).onclick = () => { rs.list.splice(i, 1); redraw(view); };
    }
    $('[data-add]', view).onclick = () => {
      rs.list.push({ name: '', when: '', path: rs.list.length ? '' : SAMPLE });
      redraw(view);
      $$('.ru-rule input[data-k="path"]', view).pop()?.focus();
    };
    $('[data-layout]', view).onclick = async () => {
      if (rs.list.length && !(await ui.confirmDlg(t('Regeln ersetzen?'), t('Die Regeln werden durch die bisherige Ablage ersetzt: Dokumente nach Jahr und Monat mit Kategorie, alles andere nach Jahr und Monat mit Ort. Gespeichert wird erst mit „Speichern“.'), t('Ersetzen')))) return;
      rs.list = LAYOUT.map((x) => ({ ...x, name: t(x.name) }));
      redraw(view);
    };
    $('[data-save]', view).onclick = () => save(view);
    $('[data-apply]', view).onclick = () => applyNow(view);
    if (R.locked) for (const b of $$('[data-add],[data-layout],[data-save],[data-up],[data-down],[data-del],[data-ins],[data-locale],[data-auto]', view)) b.disabled = true;
  }

  function redraw(view) {
    const y = scrollY;
    draw(view);
    scrollTo({ top: y });
    preview(view);
  }

  // ---------------------------------------------------------------------------
  // preview
  // ---------------------------------------------------------------------------
  async function preview(view) {
    const seq = ++R.seq;
    const rs = R.rules;
    if (!rs.list.length) { R.preview = null; drawPreview(view); return; }
    for (const el of $$('.ru-prev', view)) el.classList.add('busy');
    const r = await post('/api/admin/rules/preview', rs).catch(() => null);
    if (seq !== R.seq || SK.route().view !== 'ablage') return;
    R.preview = r;
    drawPreview(view);
  }

  function pathHTML(p) {
    const i = p.lastIndexOf('/');
    return i < 0 ? `<span class="ru-root">${esc(t('oberster Ordner'))}</span> / <span class="ru-file">${esc(p)}</span>`
      : `<span class="ru-dir">${esc(p.slice(0, i))}</span> / <span class="ru-file">${esc(p.slice(i + 1))}</span>`;
  }

  function drawPreview(view) {
    const p = R.preview;
    $$('.ru-rule', view).forEach((card, i) => {
      const el = $('.ru-prev', card);
      el.classList.remove('busy');
      const st = p?.rules?.[i];
      if (!st) { el.innerHTML = ''; return; }
      if (st.error) { el.innerHTML = `<div class="ru-err">${icon('info')}<span>${esc(t(st.error))}</span></div>`; return; }
      if (!st.matches) { el.innerHTML = `<div class="ru-stat faint">${esc(t('Keine Datei – frühere Regeln greifen zuerst, oder die Bedingung passt nicht.'))}</div>`; return; }
      el.innerHTML = `<div class="ru-stat"><b>${esc(pl(st.matches, '{n} Datei', '{n} Dateien'))}</b> · ${esc(pl(st.folders, '{n} Ordner', '{n} Ordner'))} · ${st.moves ? `<span class="ru-mv">${esc(pl(st.moves, '{n} wird verschoben', '{n} werden verschoben'))}</span>` : esc(t('alle liegen schon richtig'))}</div>
        ${st.blocked ? `<div class="ru-stat ru-blocked">${icon('info')}<span>${esc(pl(st.blocked, '{n} bleibt liegen: Dokumente bleiben in Dokument-Ordnern, Fotos und Videos außerhalb', '{n} bleiben liegen: Dokumente bleiben in Dokument-Ordnern, Fotos und Videos außerhalb'))}</span></div>` : ''}
        <ul class="ru-ex">${st.examples.map((x) => `<li>${pathHTML(x.to)}${x.from !== x.to ? `<small>${esc(t('bisher'))}: ${esc(x.from)}</small>` : ''}</li>`).join('')}</ul>`;
    });
    updateBar(view);
  }

  function updateBar(view) {
    const p = R.preview;
    const sum = $('.ru-sum', view);
    if (!sum) return;
    const d = dirty();
    if (!R.rules.list.length) sum.textContent = d ? t('Ohne Regeln bleibt alles, wie es ist.') : t('Keine Regeln: es wird nichts verschoben.');
    else if (!p) sum.textContent = t('Vorschau wird berechnet …');
    else sum.innerHTML = `<b>${esc(p.moves ? pl(p.moves, '{n} Datei würde verschoben', '{n} Dateien würden verschoben') : t('Alles liegt schon richtig'))}</b><span>${esc(t('{t} Dateien · {u} ohne passende Regel bleiben, wo sie sind', { t: num(p.total), u: num(p.unmatched) }))}</span>`;
    $('[data-save]', view).disabled = R.locked || !d;
    $('[data-save]', view).classList.toggle('primary', d);
    const ap = $('[data-apply]', view);
    ap.disabled = R.locked || !R.rules.list.length || !p || !p.moves || (p.rules || []).some((x) => x.error) || R.job?.running;
    ap.classList.toggle('primary', !d);
  }

  // ---------------------------------------------------------------------------
  // save and apply
  // ---------------------------------------------------------------------------
  async function save(view) {
    const r = await ui.call('/api/admin/rules', R.rules);
    if (!r) return false;
    R.saved = JSON.stringify(R.rules);
    toast(t('Regeln gespeichert'));
    updateBar(view);
    return true;
  }

  async function applyNow(view) {
    const n = R.preview?.moves || 0;
    const ok = await ui.confirmDlg(pl(n, '{n} Datei einsortieren?', '{n} Dateien einsortieren?'),
      t('Die Dateien werden im Hintergrund in die Ordner der Regeln verschoben; Ordner, die dabei leer werden, verschwinden. Ort, Datum und Kategorie, die bisher nur im Ordnernamen standen, bleiben erhalten.'),
      t('Einsortieren'));
    if (!ok) return;
    if (dirty() && !(await save(view))) return;
    const r = await ui.call('/api/admin/rules/apply', {});
    if (!r) return;
    toast(r.planned ? t('{n} Dateien werden einsortiert', { n: num(r.planned) }) : t('Alles liegt schon richtig'));
    R.job = { running: !!r.planned, done: 0, todo: r.planned };
    drawJob(view);
    updateBar(view);
    poll(view);
  }

  function drawJob(view) {
    const el = $('.ru-job', view);
    if (!el) return;
    const j = R.job;
    el.hidden = !j?.running;
    if (j?.running) el.innerHTML = `<span>${esc(t('Wird einsortiert'))} · ${num(j.done)} / ${num(j.todo)}</span><span class="bar"><i style="width:${j.todo ? (j.done / j.todo) * 100 : 5}%"></i></span>`;
  }

  function poll(view) {
    clearTimeout(pollT);
    if (!R.job?.running) return;
    pollT = setTimeout(async () => {
      if (SK.route().view !== 'ablage') return;
      const r = await api('/api/admin/rules').catch(() => null);
      if (!r) return poll(view);
      const was = R.job.running;
      R.job = { running: r.running, done: r.done, todo: r.todo };
      drawJob(view);
      if (was && !r.running) {
        toast(t('Einsortieren fertig'));
        await SK.loadItems();
        preview(view);
      }
      updateBar(view);
      poll(view);
    }, 1500);
  }

  // ---------------------------------------------------------------------------
  // sidebar
  // ---------------------------------------------------------------------------
  const anchor = $('#nav a[data-v="duplikate"]') || $('#nav a[data-v="favoriten"]');
  anchor?.insertAdjacentHTML('afterend',
    `<a href="#/ablage" data-v="ablage" data-need="admin">${icon('rules')}<span>${esc(t('Ablage'))}</span></a>`);
})();
