// The theme before the first paint (loaded in the head of every page).
(function () {
  var t = 'system';
  try { t = localStorage.getItem('schuhkarton-theme') || 'system'; } catch (e) {}
  if (t !== 'light' && t !== 'dark') t = matchMedia('(prefers-color-scheme: light)').matches ? 'light' : 'dark';
  document.documentElement.dataset.theme = t;
})();
