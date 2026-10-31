/* Schuhkarton — sign in and register */
'use strict';
(() => {
  const $ = (s) => document.querySelector(s);
  const LANG = window.skLang || 'de';
  const t = window.skT || ((s) => s);
  window.skTranslate?.();

  const next = (() => {
    const n = new URLSearchParams(location.search).get('next') || '/';
    return n.startsWith('/') && !n.startsWith('//') && !n.startsWith('/login') ? n : '/';
  })();
  let tab = 'login', me = {};

  const post = async (path, body) => {
    const r = await fetch(path, { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body) });
    if (!r.ok) throw new Error((await r.text()) || r.statusText);
    return r.json();
  };
  const msg = (text, good) => { const m = $('#msg'); m.textContent = text || ''; m.className = `msg ${good ? 'good' : 'bad'}`; };

  function draw() {
    for (const b of document.querySelectorAll('[data-tab]')) b.classList.toggle('on', b.dataset.tab === tab);
    const reg = tab === 'register';
    $('#pw2row').hidden = !reg;
    $('#pw').autocomplete = reg ? 'new-password' : 'current-password';
    $('#submit').textContent = reg ? t('Konto anlegen') : t('Anmelden');
    $('#note').textContent = reg ? t('Mindestens 8 Zeichen. Ein Admin gibt das Konto frei, dann kannst du dich anmelden.') : '';
    $('#tabs').hidden = !me.register;
    msg('');
  }

  for (const b of document.querySelectorAll('[data-tab]')) b.onclick = () => { tab = b.dataset.tab; draw(); };

  $('#form').onsubmit = async (e) => {
    e.preventDefault();
    const name = $('#name').value.trim(), password = $('#pw').value;
    if (tab === 'register' && password !== $('#pw2').value) { msg(t('Die Passwörter sind verschieden')); return; }
    $('#submit').disabled = true;
    try {
      if (tab === 'login') {
        const r = await post('/api/auth/login', { name, password });
        if (r.ok) { location.href = next; return; }
        msg(t(r.error || ''));
      } else {
        const r = await post('/api/auth/register', { name, password });
        if (r.ok) {
          tab = 'login';
          draw();
          $('#pw').value = '';
          msg(t('Konto angelegt. Sobald ein Admin es freigibt, kannst du dich anmelden.'), true);
        } else msg(t(r.error || ''));
      }
    } catch (err) { msg(t(err.message)); }
    $('#submit').disabled = false;
  };

  $('#lang').textContent = LANG === 'de' ? 'English' : 'Deutsch';
  $('#lang').onclick = () => window.skSetLang(LANG === 'de' ? 'en' : 'de');

  (async () => {
    me = await fetch('/api/me').then((r) => r.json()).catch(() => ({}));
    if (me.title) { $('#title').textContent = me.title; document.title = `${me.title} – ${t('Anmelden')}`; }
    if (me.role && me.role !== 'anon') { location.href = next; return; }
    const alt = $('#alt');
    if (me.anonymous && me.keyOK) alt.innerHTML = `<a href="${next.replace(/"/g, '')}">${t('Ohne Konto weiter ansehen')}</a>`;
    else if (me.anonymous) alt.textContent = t('Ohne Konto ansehen: den Link mit Zugangsschlüssel öffnen.');
    draw();
    $('#name').focus();
  })();
})();
