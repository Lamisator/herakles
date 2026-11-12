'use strict';
// Herakles pictograms: line icons on a 24×24 grid, drawn with currentColor.

const ICONS = {
  dashboard: '<rect x="3" y="3" width="7" height="9" rx="1.5"/><rect x="14" y="3" width="7" height="5" rx="1.5"/><rect x="14" y="12" width="7" height="9" rx="1.5"/><rect x="3" y="16" width="7" height="5" rx="1.5"/>',
  containers: '<path d="M21 8 12 3 3 8v8l9 5 9-5z"/><path d="m3 8 9 5 9-5M12 13v8"/>',
  snapshots: '<path d="M3 12a9 9 0 1 0 3-6.7L3 8"/><path d="M3 3v5h5"/><path d="M12 7v5l3.5 2"/>',
  jobs: '<rect x="3" y="4" width="18" height="17" rx="2"/><path d="M3 9h18M8 2v4M16 2v4"/><path d="m9 15 2 2 4-4"/>',
  repos: '<ellipse cx="12" cy="5.5" rx="8" ry="3"/><path d="M4 5.5v6c0 1.7 3.6 3 8 3s8-1.3 8-3v-6"/><path d="M4 11.5v6c0 1.7 3.6 3 8 3s8-1.3 8-3v-6"/>',
  settings: '<path d="M4 6h10M18 6h2M4 12h4M12 12h8M4 18h12M20 18h0"/><circle cx="16" cy="6" r="2"/><circle cx="10" cy="12" r="2"/><circle cx="18" cy="18" r="2"/>',
  logs: '<path d="M8 6h13M8 12h13M8 18h13"/><path d="M3.5 6h.01M3.5 12h.01M3.5 18h.01" stroke-width="3"/>',
  users: '<circle cx="9" cy="8" r="3.5"/><path d="M2.5 20c.5-3.5 3.2-5.5 6.5-5.5s6 2 6.5 5.5"/><circle cx="17" cy="9" r="2.5"/><path d="M16 14.6c2.8.2 4.9 2 5.4 5.4"/>',
  account: '<circle cx="12" cy="12" r="9"/><circle cx="12" cy="10" r="3"/><path d="M6.3 18.4C7.5 16.4 9.6 15 12 15s4.5 1.4 5.7 3.4"/>',
  wizard: '<path d="m4 20 11-11"/><path d="m13 7 2-2 4 4-2 2"/><path d="M6 3v3M4.5 4.5h3M18 15v4M16 17h4M11 2.5v2M10 3.5h2"/>',
  play: '<circle cx="12" cy="12" r="9"/><path d="m10 8.5 5.5 3.5-5.5 3.5z" fill="currentColor"/>',
  full: '<path d="m12 3 9 4.5-9 4.5-9-4.5z"/><path d="m3 12 9 4.5 9-4.5M3 16.5 12 21l9-4.5"/>',
  incremental: '<rect x="3" y="3" width="18" height="18" rx="2"/><path d="M12 8v8M8 12h8"/>',
  differential: '<circle cx="6" cy="6" r="2.5"/><circle cx="18" cy="18" r="2.5"/><path d="M6 8.5V15a3 3 0 0 0 3 3h6.5"/><path d="M18 15.5V9a3 3 0 0 0-3-3H8.5"/>',
  restore: '<path d="M3 12a9 9 0 1 0 9-9 9.7 9.7 0 0 0-6.7 2.8L3 8"/><path d="M3 3v5h5"/>',
  browse: '<path d="M3 7a2 2 0 0 1 2-2h4l2 2h8a2 2 0 0 1 2 2v1"/><path d="M3 7v11a2 2 0 0 0 2 2h12.5a2 2 0 0 0 1.9-1.4L22 11H7.5a2 2 0 0 0-1.9 1.4L3 20"/>',
  download: '<path d="M12 3v12M7 10l5 5 5-5"/><path d="M4 17v2a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2v-2"/>',
  edit: '<path d="M4 20h4L19 9l-4-4L4 16z"/><path d="m13.5 6.5 4 4"/>',
  trash: '<path d="M3 6h18M8 6V4h8v2M6 6l1 14h10l1-14"/><path d="M10 11v6M14 11v6"/>',
  plus: '<path d="M12 5v14M5 12h14"/>',
  refresh: '<path d="M21 12a9 9 0 1 1-2.6-6.4L21 8"/><path d="M21 3v5h-5"/>',
  check: '<path d="M12 3 4 6v6c0 5 3.4 8.4 8 9.5 4.6-1.1 8-4.5 8-9.5V6z"/><path d="m8.5 12 2.5 2.5 4.5-5"/>',
  prune: '<circle cx="6" cy="6" r="3"/><circle cx="6" cy="18" r="3"/><path d="M8.1 8.1 20 20M8.1 15.9 20 4"/>',
  stats: '<path d="M4 20V10M10 20V4M16 20v-7M22 20H2"/>',
  init: '<path d="M12 3v3M12 18v3M3 12h3M18 12h3M5.6 5.6l2.1 2.1M16.3 16.3l2.1 2.1M5.6 18.4l2.1-2.1M16.3 7.7l2.1-2.1"/><circle cx="12" cy="12" r="3"/>',
  key: '<circle cx="7.5" cy="15.5" r="4.5"/><path d="m11 12 9-9M16 7l3 3M14 9l2 2"/>',
  lock: '<rect x="4" y="11" width="16" height="10" rx="2"/><path d="M8 11V7a4 4 0 0 1 8 0v4"/>',
  unlock: '<rect x="4" y="11" width="16" height="10" rx="2"/><path d="M8 11V7a4 4 0 0 1 7.7-1.5"/>',
  cancel: '<circle cx="12" cy="12" r="9"/><path d="m9 9 6 6M15 9l-6 6"/>',
  save: '<path d="M5 3h11l3 3v13a2 2 0 0 1-2 2H7a2 2 0 0 1-2-2z"/><path d="M8 3v5h7V3M8 21v-7h8v7"/>',
  compare: '<rect x="3" y="4" width="7" height="16" rx="1.5"/><rect x="14" y="4" width="7" height="16" rx="1.5"/><path d="M5.5 9h2M16.5 9h2M5.5 13h2M16.5 13h2"/>',
  folder: '<path d="M3 7a2 2 0 0 1 2-2h4l2 2h8a2 2 0 0 1 2 2v9a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2z"/>',
  file: '<path d="M14 3H7a2 2 0 0 0-2 2v14a2 2 0 0 0 2 2h10a2 2 0 0 0 2-2V8z"/><path d="M14 3v5h5"/>',
  disk: '<rect x="3" y="13" width="18" height="7" rx="2"/><path d="M5.5 13 8 5h8l2.5 8"/><path d="M7 16.5h.01M10 16.5h.01" stroke-width="2.5"/>',
  usb: '<rect x="7" y="9" width="10" height="12" rx="2"/><path d="M9 9V3h6v6M11 5.5h.01M13 5.5h.01"/>',
  cloud: '<path d="M7 18a5 5 0 1 1 .9-9.9A6 6 0 0 1 19.5 10 4 4 0 0 1 18 18z"/>',
  server: '<rect x="3" y="4" width="18" height="7" rx="1.5"/><rect x="3" y="13" width="18" height="7" rx="1.5"/><path d="M7 7.5h.01M7 16.5h.01" stroke-width="2.5"/>',
  share: '<rect x="2" y="3" width="20" height="13" rx="2"/><path d="M8 21h8M12 16v5"/><path d="m9 10 3-3 3 3M12 7v6"/>',
  terminal: '<rect x="3" y="4" width="18" height="16" rx="2"/><path d="m7 9 3 3-3 3M12 15h5"/>',
  database: '<ellipse cx="12" cy="6" rx="7" ry="3"/><path d="M5 6v12c0 1.7 3.1 3 7 3s7-1.3 7-3V6"/><path d="M5 12c0 1.7 3.1 3 7 3s7-1.3 7-3"/>',
  clock: '<circle cx="12" cy="12" r="9"/><path d="M12 7v5l3 2"/>',
  calendar: '<rect x="3" y="4" width="18" height="17" rx="2"/><path d="M3 9h18M8 2v4M16 2v4"/>',
  archive: '<rect x="3" y="4" width="18" height="4" rx="1"/><path d="M5 8v11a2 2 0 0 0 2 2h10a2 2 0 0 0 2-2V8M10 12h4"/>',
  bell: '<path d="M6 16V11a6 6 0 0 1 12 0v5l2 2H4z"/><path d="M10 20a2 2 0 0 0 4 0"/>',
  logout: '<path d="M15 4h3a2 2 0 0 1 2 2v12a2 2 0 0 1-2 2h-3"/><path d="m10 16-4-4 4-4M6 12h10"/>',
  passkey: '<circle cx="9" cy="8" r="4"/><path d="M3 20c.4-3.5 2.8-6 6-6 1.3 0 2.5.4 3.4 1.1"/><circle cx="17.5" cy="14" r="2.5"/><path d="M17.5 16.5V21M17.5 19h2"/>',
  shield: '<path d="M12 3 4 6v6c0 5 3.4 8.4 8 9.5 4.6-1.1 8-4.5 8-9.5V6z"/>',
  alert: '<path d="M12 3 2 20h20z"/><path d="M12 9v5M12 17h.01"/>',
  info: '<circle cx="12" cy="12" r="9"/><path d="M12 11v6M12 7.5h.01"/>',
  next: '<path d="M5 12h14M13 6l6 6-6 6"/>',
  back: '<path d="M19 12H5M11 6l-6 6 6 6"/>',
  done: '<path d="m5 12.5 4.5 4.5L19 7.5"/>',
  stop: '<rect x="5" y="5" width="14" height="14" rx="2"/>',
  retention: '<path d="M12 3a9 9 0 1 0 9 9"/><path d="M12 7v5l3 2M16 3h5v5"/><path d="m21 3-5 5"/>',
  sparkle: '<path d="M12 3c.6 4.2 2.8 6.4 7 7-4.2.6-6.4 2.8-7 7-.6-4.2-2.8-6.4-7-7 4.2-.6 6.4-2.8 7-7z"/>',
  systems: '<rect x="2" y="4" width="13" height="10" rx="1.5"/><path d="M6 18h5M8.5 14v4"/><rect x="17" y="3" width="5" height="18" rx="1"/><path d="M19.5 7h.01M19.5 10h.01" stroke-width="2.5"/>',
  desktop: '<rect x="3" y="4" width="18" height="12" rx="1.5"/><path d="M8 20h8M12 16v4"/>',
  eye: '<path d="M2 12s3.6-7 10-7 10 7 10 7-3.6 7-10 7S2 12 2 12z"/><circle cx="12" cy="12" r="3"/>',
};

function icon(name, cls) {
  const el = document.createElement('span');
  el.className = 'ico' + (cls ? ' ' + cls : '');
  el.setAttribute('aria-hidden', 'true');
  el.innerHTML = '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round">' + (ICONS[name] || ICONS.info) + '</svg>';
  return el;
}

// Pictograms are added automatically: navigation by view, page titles and
// empty states by their page, buttons by their label.
const NAV_ICONS = { dashboard: 'dashboard', servers: 'server', systems: 'systems', containers: 'containers', snapshots: 'snapshots', jobs: 'jobs', repos: 'repos',
  settings: 'settings', logs: 'logs', users: 'users', account: 'account', wizard: 'wizard' };
const TITLE_ICONS = { 'Dashboard': 'dashboard', 'Systems': 'systems', 'Containers': 'containers', 'Snapshots & Restore': 'snapshots', 'Backup Jobs': 'jobs',
  'Repositories': 'repos', 'Configuration': 'settings', 'Activity Log': 'logs', 'Users': 'users', 'Account': 'account', 'New backup': 'wizard' };
const BUTTON_ICONS = [
  [/^Back up now$/, 'play'], [/^Full$/, 'full'], [/^Incremental$/, 'incremental'], [/^Differential$/, 'differential'],
  [/^Restore/, 'restore'], [/^Browse$/, 'browse'], [/^Download/, 'download'], [/^(Edit|Change)/, 'edit'],
  [/^New password$/, 'refresh'], [/^Copy$/, 'file'], [/^Sign out$/, 'logout'],
  [/^(Delete|Forget|Remove)/, 'trash'], [/^\+ |^New |^Add /, 'plus'], [/^Reload$|^Revert$/, 'refresh'], [/^Check$/, 'check'],
  [/^Prune$/, 'prune'], [/^Statistics$/, 'stats'], [/^Initialize$/, 'init'], [/^Unlock$/, 'unlock'], [/^Cancel$/, 'cancel'],
  [/^(Save|Validate & save)/, 'save'], [/^Compare/, 'compare'], [/^Show annotated/, 'eye'], [/^Sign in with a passkey|^Add a passkey/, 'passkey'],
  [/^Create backup|^Create job|^Finish/, 'done'], [/^Next$|^Continue$/, 'next'], [/^Back$/, 'back'], [/^Get started|^Start the wizard|^Backup wizard/, 'wizard'],
  [/^Confirm$|^Use selection$/, 'done'], [/^Choose disks/, 'disk'], [/^Search$/, 'eye'],
];

function decorate(root) {
  root.querySelectorAll('button:not([data-ico]), a.btn:not([data-ico])').forEach(b => {
    b.dataset.ico = '1';
    if (b.querySelector('.ico')) return;
    const label = b.textContent.trim();
    const hit = BUTTON_ICONS.find(([re]) => re.test(label));
    if (!hit) return;
    // "+ Add …" gets the plus pictogram instead of the character.
    const t = b.firstChild;
    if (hit[1] === 'plus' && t && t.nodeType === 3 && t.nodeValue.startsWith('+ ')) t.nodeValue = t.nodeValue.slice(2);
    b.prepend(icon(hit[1]));
  });
  root.querySelectorAll('h1:not([data-ico])').forEach(h => {
    h.dataset.ico = '1';
    const name = TITLE_ICONS[h.textContent.trim()];
    if (name) h.prepend(icon(name, 'title'));
  });
  root.querySelectorAll('.empty:not([data-ico])').forEach(e => {
    e.dataset.ico = '1';
    e.prepend(icon(NAV_ICONS[(typeof state !== 'undefined' && state.view) || ''] || 'info', 'big'));
  });
}

document.addEventListener('DOMContentLoaded', () => {
  document.querySelectorAll('#nav a[data-view]').forEach(a => a.prepend(icon(NAV_ICONS[a.dataset.view] || 'info')));
  decorate(document.body);
  new MutationObserver(muts => {
    for (const m of muts) m.addedNodes.forEach(n => { if (n.nodeType === 1) decorate(n.parentNode || n); });
  }).observe(document.body, { childList: true, subtree: true });
});
