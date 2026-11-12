'use strict';
// Herakles GUI — a dependency free single page application.

// ---------- DOM helpers ----------
function h(tag, attrs, ...children) {
  const el = document.createElement(tag);
  for (const [k, v] of Object.entries(attrs || {})) {
    if (v === null || v === undefined || v === false) continue;
    if (k.startsWith('on') && typeof v === 'function') el.addEventListener(k.slice(2), v);
    else if (k === 'class') el.className = v;
    else if (k === 'style') el.style.cssText = v;
    else if (k === 'value') el.value = v;
    else if (k === 'checked') el.checked = !!v;
    else el.setAttribute(k, v === true ? '' : v);
  }
  for (const c of children.flat(Infinity)) {
    if (c === null || c === undefined || c === false) continue;
    el.append(c instanceof Node ? c : document.createTextNode(String(c)));
  }
  return el;
}
const $ = (sel, root = document) => root.querySelector(sel);
function clear(el) { while (el.firstChild) el.removeChild(el.firstChild); return el; }

function fmtBytes(n) {
  if (n === undefined || n === null) return '-';
  const u = ['B', 'KiB', 'MiB', 'GiB', 'TiB', 'PiB'];
  let i = 0; let x = n;
  while (x >= 1024 && i < u.length - 1) { x /= 1024; i++; }
  return (i === 0 ? x : x.toFixed(2)) + ' ' + u[i];
}
function fmtTime(t) {
  if (!t || t.startsWith('0001')) return '-';
  return new Date(t).toLocaleString();
}
function ago(t) {
  if (!t || t.startsWith('0001')) return 'never';
  const s = (Date.now() - new Date(t).getTime()) / 1000;
  if (s < 0) return 'in ' + until(t);
  if (s < 60) return 'just now';
  if (s < 3600) return Math.floor(s / 60) + ' min ago';
  if (s < 172800) return Math.floor(s / 3600) + ' h ago';
  return Math.floor(s / 86400) + ' days ago';
}
function until(t) {
  const s = (new Date(t).getTime() - Date.now()) / 1000;
  if (s < 60) return '<1 min';
  if (s < 3600) return Math.floor(s / 60) + ' min';
  if (s < 172800) return (s / 3600).toFixed(1) + ' h';
  return Math.floor(s / 86400) + ' days';
}
function fmtDur(ns) {
  const s = Math.round(ns / 1e9);
  const hh = Math.floor(s / 3600), mm = Math.floor(s % 3600 / 60), ss = s % 60;
  return (hh ? hh + ':' + String(mm).padStart(2, '0') : mm) + ':' + String(ss).padStart(2, '0');
}

// ---------- toasts & modals ----------
function toast(msg, err) {
  const t = h('div', { class: 'toast' + (err ? ' err' : '') }, msg);
  $('#toasts').append(t);
  setTimeout(() => t.remove(), err ? 9000 : 4500);
}
function modal(title, body, buttons, cls) {
  return new Promise(resolve => {
    const root = $('#modal-root');
    let done;
    const close = v => { backdrop.remove(); document.removeEventListener('keydown', esc); resolve(v); };
    const esc = e => { if (e.key === 'Escape') close(undefined); };
    document.addEventListener('keydown', esc);
    const foot = h('footer', {}, buttons.map(b => h('button', {
      class: b.class || '',
      onclick: async () => {
        if (b.action) {
          try { const r = await b.action(); if (r === false) return; done = r; } catch (e) { toast(e.message, true); return; }
        }
        close(b.value !== undefined ? b.value : done);
      }
    }, b.label)));
    const backdrop = h('div', { class: 'modal-backdrop', onmousedown: e => { if (e.target === backdrop) close(undefined); } },
      h('div', { class: 'modal ' + (cls || '') }, h('header', {}, title), h('div', { class: 'body' }, body), foot));
    root.append(backdrop);
    const first = backdrop.querySelector('input, textarea, select');
    if (first) first.focus();
  });
}
function confirmDlg(title, text, danger) {
  return modal(title, h('p', {}, text), [
    { label: 'Cancel', value: false },
    { label: 'Confirm', value: true, class: danger ? 'danger solid' : 'primary' }]);
}

// ---------- API ----------
// HUB_LOCAL are the hub's own pages; everything else under /api/ goes to
// the server picked in the sidebar (state.target) when there is one.
const HUB_LOCAL = /^\/api\/(hub|users|account)(\/|$|\?)/;
function apiURL(path, local) {
  if (!state.target || local || !path.startsWith('/api/') || HUB_LOCAL.test(path)) return path;
  return '/api/hub/servers/' + encodeURIComponent(state.target.id) + path;
}
async function localApi(method, path, body) { return api(method, path, body, false, true); }
async function api(method, path, body, retried, local) {
  const opts = { method, headers: { 'X-Herakles': '1' } };
  if (body !== undefined) { opts.body = JSON.stringify(body); opts.headers['Content-Type'] = 'application/json'; }
  const res = await fetch(apiURL(path, local), opts);
  let data = null;
  const text = await res.text();
  try { data = text ? JSON.parse(text) : null; } catch (e) { data = { error: text }; }
  if (res.ok) return data;
  if (data && data.needs_password && !retried) {
    const ok = await askPassword(data.repo, data.error);
    if (ok) return api(method, path, body, true, local);
  }
  if (res.status === 401 && !(data && data.needs_password) && state.info && state.info.mode === 'web') {
    location.href = '/login.html?next=' + encodeURIComponent(location.pathname);
  }
  throw new Error((data && data.error) || res.statusText);
}
async function askPassword(repo, why) {
  const input = h('input', { type: 'password', autocomplete: 'current-password' });
  const res = await modal('Unlock repository ' + repo, [
    h('p', { class: 'muted' }, why && !why.includes('no password') ? why : 'This repository has no stored password source. Enter its password (kept in memory for this session only).'),
    h('label', {}, 'Password'), input], [
    { label: 'Cancel', value: false },
    { label: 'Unlock', class: 'primary', action: async () => { await api('POST', '/api/repos/' + encodeURIComponent(repo) + '/unlock', { password: input.value }, true); return true; } }], 'small');
  input.addEventListener('keydown', e => { if (e.key === 'Enter') e.target.closest('.modal').querySelector('footer .primary').click(); });
  return res === true;
}

// ---------- state & routing ----------
const state = { view: 'dashboard', config: null, info: null, timer: null, snapRepo: null, target: null, hubInfo: null };
const views = {};

function navigate(view) {
  state.view = view;
  document.querySelectorAll('#nav a').forEach(a => a.classList.toggle('active', a.dataset.view === view));
  if (state.timer) { clearInterval(state.timer); state.timer = null; }
  const main = clear($('#main'));
  if (state.target && !['servers', 'users', 'account'].includes(view)) {
    main.append(h('div', { class: 'remote-bar' + (state.target.online ? '' : ' offline') }, icon('server'),
      h('span', {}, 'Managing ', h('b', {}, state.target.name), state.target.online ? ' (attached server)' : ' (offline)'),
      h('span', { class: 'spacer' }),
      h('button', { class: 'small', title: 'Manage the hub itself again', onclick: () => switchTarget(null) }, 'Back to ' + (state.hubInfo ? state.hubInfo.hostname : 'the hub'))));
  }
  views[view](main).catch(e => main.append(h('div', { class: 'empty' }, 'Error: ' + e.message)));
}
document.querySelectorAll('#nav a').forEach(a => a.addEventListener('click', () => navigate(a.dataset.view)));

async function loadConfig() { state.config = await api('GET', '/api/config'); return state.config; }
async function saveConfig(cfg) { state.config = await api('PUT', '/api/config', cfg); toast('Configuration saved'); return state.config; }

function badge(status) { return h('span', { class: 'badge ' + (status || '') }, status || 'never run'); }

function progressBar(p) {
  const pct = p ? (p.scan_done && p.bytes_total > 0 ? Math.min(100, 100 * p.bytes_done / p.bytes_total) : -1) : -1;
  return h('div', { class: 'progress' + (pct < 0 ? ' indet' : '') }, h('div', { style: 'width:' + Math.max(pct, 0).toFixed(1) + '%' }));
}
function progressText(p) {
  if (!p) return '';
  const pct = p.scan_done && p.bytes_total > 0 ? (100 * p.bytes_done / p.bytes_total).toFixed(1) + '% · ' : '';
  return pct + p.files_done + ' files · ' + fmtBytes(p.bytes_done) + (p.scan_done ? ' / ' + fmtBytes(p.bytes_total) : '') +
    ' · ' + fmtBytes(p.bytes_new) + ' new · ' + fmtDur(p.elapsed) + (p.errors ? ' · ' + p.errors + ' errors' : '');
}

// ---------- dashboard ----------
views.dashboard = async main => {
  main.append(h('h1', {}, 'Dashboard'), h('div', { class: 'sub', id: 'dash-sub' }, ''));
  const jobsEl = h('div', { class: 'grid' });
  const activity = h('div');
  main.append(jobsEl, h('h2', {}, 'Activity'), activity);
  const render = async () => {
    const [jobs, runs, info] = await Promise.all([api('GET', '/api/jobs'), api('GET', '/api/runs'), api('GET', '/api/info')]);
    state.info = mergeInfo(info);
    renderDaemon(state.info);
    const active = {};
    runs.active.forEach(r => { active[r.kind + ':' + r.target] = r; });
    $('#dash-sub').textContent = jobs.length + ' jobs · configuration ' + info.config_path;
    clear(jobsEl);
    if (!jobs.length) {
      jobsEl.append(h('div', { class: 'empty' }, 'No backup jobs yet. ',
        h('button', { class: 'primary', onclick: () => navigate('wizard') }, 'Get started')));
    }
    for (const j of jobs) {
      const run = active['backup:' + j.name];
      const last = j.last;
      const sched = (j.schedule || []).map(s => h('span', { class: 'chip' }, s.cron + ' · ' + (s.type || 'auto')));
      const next = info.daemon ? (info.daemon.schedule || []).find(e => e.kind === 'backup' && e.target === j.name) : null;
      jobsEl.append(h('div', { class: 'card' },
        h('h3', {}, icon(jobIcon(j)), j.name, run ? badge('running') : badge(last && last.status), j.disabled ? h('span', { class: 'chip' }, 'disabled') : null),
        j.description ? h('div', { class: 'meta' }, j.description) : null,
        h('div', { class: 'meta' }, '→ ', j.repository, ' · ', jobSources(j)),
        h('div', { class: 'meta' }, 'Last run: ', last ? ago(last.start) + ' (' + last.type + ')' : 'never',
          last && last.summary ? ' · ' + fmtBytes(last.summary.total_bytes) + ', ' + fmtBytes(last.summary.bytes_stored) + ' stored' : ''),
        last && last.error ? h('div', { class: 'meta', style: 'color:var(--err)' }, last.error) : null,
        next ? h('div', { class: 'meta' }, 'Next (daemon): ', fmtTime(next.next), ' · ', next.type || 'auto') : null,
        h('div', {}, sched),
        run ? [progressBar(run.progress), h('div', { class: 'meta' }, run.state === 'queued' ? 'queued…' : progressText(run.progress)),
          run.progress && run.progress.current ? h('div', { class: 'meta mono' }, run.progress.current) : null] : null,
        h('div', { class: 'actions' },
          run ? h('button', { class: 'danger', title: 'Stop this backup', onclick: async () => { await api('POST', '/api/runs/' + run.id + '/cancel'); toast('Cancelling ' + j.name); } }, 'Cancel') : [
            h('button', { class: 'primary', title: 'Back up now: full when one is due (full_interval), otherwise incremental', onclick: () => runJob(j.name, 'auto') }, 'Back up now'),
            h('button', { title: 'Save everything again, independent of earlier backups', onclick: () => runJob(j.name, 'full') }, 'Full'),
            h('button', { title: 'Save only what changed since the last backup (fastest)', onclick: () => runJob(j.name, 'incremental') }, 'Incremental'),
            h('button', { title: 'Save what changed since the last full backup', onclick: () => runJob(j.name, 'differential') }, 'Differential')],
          h('span', { class: 'spacer' }),
          !j.docker && last && last.snapshot ? h('button', { class: 'small', title: 'Look through the latest backup; download or restore files', onclick: () => openExplorer({ repo: j.repository, snapshot: last.snapshot, job: j.name }) }, 'Browse') : null,
          h('button', { class: 'small', title: 'Change what is backed up, where to, when and how long it is kept', onclick: () => editJob(j.name) }, 'Edit'))));
    }
    clear(activity);
    const rows = [...runs.active, ...runs.recent];
    if (!rows.length) { activity.append(h('div', { class: 'muted' }, 'No activity in this session yet.')); return; }
    activity.append(h('table', {}, h('tr', {}, ['Started', 'Task', 'Target', 'Type', 'State', 'Result'].map(t => h('th', {}, t))),
      rows.map(r => h('tr', {},
        h('td', {}, fmtTime(r.started || r.queued)), h('td', {}, r.kind), h('td', {}, r.target), h('td', {}, r.type || (r.result && r.result.type) || '-'),
        h('td', {}, r.state === 'done' ? badge(r.error ? 'failure' : (r.result ? r.result.status : 'success')) : badge('running')),
        h('td', { class: 'wrap' }, r.error || (r.result && r.result.snapshot ? 'snapshot ' + r.result.snapshot.slice(0, 8) + (r.result.summary ? ' · ' + fmtBytes(r.result.summary.bytes_added) + ' new' : '') : (r.message || '')))))));
  };
  await loadConfig();
  await render();
  state.timer = setInterval(() => render().catch(() => {}), 1000);
};

async function runJob(name, type) {
  try { await api('POST', '/api/jobs/' + encodeURIComponent(name) + '/run', { type }); toast('Started ' + type + ' backup of ' + name); }
  catch (e) { toast(e.message, true); }
}

function renderDaemon(info) {
  const el = clear($('#daemon-state'));
  $('#version').textContent = info.version + ' · ' + info.hostname;
  if (info.daemon) {
    const n = (info.daemon.schedule || [])[0];
    el.append(h('div', {}, h('span', { class: 'badge success' }, 'daemon running')),
      n ? h('div', { style: 'margin-top:6px' }, 'next: ' + n.kind + ' ' + n.target, h('br'), fmtTime(n.next)) : '');
  } else {
    el.append(h('div', {}, h('span', { class: 'badge' }, 'daemon not running')),
      h('div', { style: 'margin-top:6px' }, 'Scheduled backups need ', h('span', { class: 'mono' }, 'herakles daemon install')));
  }
  if (info.agent && !info.remote) {
    el.append(h('div', { style: 'margin-top:8px', title: info.agent.error || '' },
      h('span', { class: 'badge ' + (info.agent.connected ? 'success' : 'warning') }, info.agent.connected ? 'hub connected' : 'hub not connected'),
      h('div', { class: 'muted', style: 'margin-top:4px;word-break:break-all' }, 'Managed by ', info.agent.hub)));
  }
  // Updates concern this Herakles, also while managing an attached server.
  const up = (state.hubInfo || info).update;
  if (up && up.state.available) {
    const st = up.state;
    const box = h('div', { class: 'update-box' }, h('div', {}, icon('sparkle'), ' Herakles ', h('b', {}, st.latest), ' is available'));
    if (st.installing) box.append(h('div', { class: 'hint' }, 'Updating… Herakles restarts and opens a new window.'));
    else if (up.can_install && isAdmin()) box.append(h('button', { class: 'small primary', title: 'Download ' + st.latest + ', check its signature and install it: ' + st.hint, onclick: installUpdate }, 'Install update'));
    else box.append(h('div', { class: 'hint' }, st.hint));
    if (st.error && !st.installing) box.append(h('div', { class: 'hint err' }, st.error));
    el.append(box);
  }
}

async function installUpdate() {
  const st = state.hubInfo.update.state;
  if (!await confirmDlg('Update Herakles', 'Install Herakles ' + st.latest + ' (this is ' + st.current + ')? Herakles ' + st.hint + ', then restarts: this window stops working and a new one opens. While a backup runs, the update is refused, so nothing is cut off.')) return;
  try {
    await localApi('POST', '/api/update/install');
    toast('Updating to ' + st.latest + '…');
    const poll = setInterval(async () => {
      try { await loadInfo(); renderDaemon(state.info); if (!state.hubInfo.update.state.installing) clearInterval(poll); } catch (e) { clearInterval(poll); }
    }, 1500);
  } catch (e) { toast(e.message, true); }
}

// ---------- systems (backed-up hosts) ----------
// systemIcon guesses what kind of machine a host is from its snapshots.
function systemIcon(snaps) {
  if (snaps.some(s => (s.paths || []).some(p => /^[A-Za-z]:\\|^\\\\/.test(p)))) return ['desktop', 'Windows'];
  if (snaps.some(s => s.docker)) return ['server', 'Server with Docker'];
  return ['server', 'Linux / Unix'];
}
function freshness(t) {
  const h = (Date.now() - new Date(t).getTime()) / 3600e3;
  return h < 26 ? ['success', 'up to date'] : h < 24 * 8 ? ['warning', 'older than a day'] : ['failure', 'older than a week'];
}

views.systems = async main => {
  main.append(h('h1', {}, 'Systems'), h('div', { class: 'sub' }, 'Every computer that has backups in one of the repositories. Click a system to see its backups.'));
  const cfg = await loadConfig();
  const repos = cfg.repositories || [];
  if (!repos.length) { main.append(h('div', { class: 'empty' }, 'No repositories configured.')); return; }
  const box = h('div', {}, h('div', { class: 'muted' }, 'Loading…'));
  main.append(box);
  const hosts = new Map();
  const problems = [];
  for (const r of repos) {
    let snaps;
    try { snaps = await api('GET', '/api/repos/' + encodeURIComponent(r.name) + '/snapshots'); }
    catch (e) { problems.push(r.name + ': ' + e.message); continue; }
    for (const s of snaps || []) {
      if (!hosts.has(s.hostname)) hosts.set(s.hostname, { name: s.hostname, snaps: [], repos: new Map() });
      const x = hosts.get(s.hostname);
      x.snaps.push(s);
      x.repos.set(r.name, (x.repos.get(r.name) || 0) + 1);
      s.repo = r.name;
    }
  }
  clear(box);
  if (problems.length) box.append(h('div', { class: 'notice warn' }, icon('alert'), 'Some repositories could not be read: ' + problems.join('; ')));
  if (!hosts.size) { box.append(h('div', { class: 'empty' }, 'No backups yet.')); return; }
  const open = (host, repo) => { state.snapRepo = repo; state.snapHost = host; state.snapSet = null; state.snapAll = false; navigate('snapshots'); };
  const grid = h('div', { class: 'sys-grid' });
  box.append(grid);
  for (const x of [...hosts.values()].sort((a, b) => a.name.localeCompare(b.name))) {
    const newest = x.snaps.reduce((a, s) => !a || s.time > a.time ? s : a, null);
    const [fcls, ftext] = freshness(newest.time);
    const [ic, kindText] = systemIcon(x.snaps);
    const jobs = [...new Set(x.snaps.map(s => s.job || 'no job'))].sort();
    const stored = x.snaps.reduce((n, s) => n + (s.summary ? s.summary.bytes_stored : 0), 0);
    const kinds = [...new Map(x.snaps.map(s => { const k = snapKind(s); return [k.key, k]; })).values()];
    const errs = x.snaps.filter(s => s.summary && s.summary.errors > 0).length;
    const repoNames = [...x.repos.keys()];
    grid.append(h('div', { class: 'sys-card' },
      h('button', { class: 'sys-main', title: 'Show the backups of ' + x.name, onclick: () => open(x.name, repoNames.reduce((a, b) => x.repos.get(b) > x.repos.get(a) ? b : a, newest.repo)) },
        h('span', { class: 'sys-ico' }, icon(ic), h('span', { class: 'sys-dot ' + fcls, title: ftext })),
        h('span', { class: 'sys-text' },
          h('b', {}, x.name),
          h('span', { class: 'muted small' }, kindText),
          h('span', { class: 'sys-last' }, 'Last backup ', h('strong', {}, ago(newest.time)), ' ', h('span', { class: 'badge ' + fcls }, ftext))),
        h('span', { class: 'sys-go' }, icon('next'))),
      h('div', { class: 'sys-facts' },
        h('span', {}, icon('snapshots'), x.snaps.length + ' snapshots'),
        h('span', {}, icon('jobs'), jobs.length + (jobs.length === 1 ? ' job' : ' jobs')),
        h('span', {}, icon('repos'), fmtBytes(stored) + ' stored'),
        errs ? h('span', { class: 'warn' }, icon('alert'), errs + ' with read errors') : null),
      h('div', { class: 'sys-kinds' }, kinds.map(k => h('span', { class: 'chip' }, icon(k.icon), k.label)),
        jobs.slice(0, 6).map(j => h('span', { class: 'chip mono' }, j)), jobs.length > 6 ? h('span', { class: 'chip' }, '+' + (jobs.length - 6)) : null),
      repoNames.length > 1 ? h('div', { class: 'sys-repos' }, h('span', { class: 'muted small' }, 'Repositories:'),
        repoNames.map(rn => h('button', { class: 'small', onclick: () => open(x.name, rn) }, icon('repos'), rn + ' (' + x.repos.get(rn) + ')'))) : null));
  }
};

// jobIcon and jobSources describe what a job backs up.
const dbLabel = d => d.name || d.type;
function jobIcon(j) {
  if (j.docker) return 'containers';
  if ((j.devices || []).length) return 'disk';
  if ((j.databases || []).length && !(j.paths || []).length) return 'database';
  return 'folder';
}
const jobSources = j => [...(j.docker ? ['Docker projects'] : []), ...(j.paths || []), ...(j.devices || []),
  ...(j.databases || []).map(d => 'database ' + dbLabel(d))].join(', ');

// ---------- snapshots ----------
const SNAP_TYPES = [['full', 'Full'], ['incremental', 'Incremental'], ['differential', 'Differential']];
const TYPE_RANK = { full: 0, differential: 1, incremental: 2 };
const isDevPath = p => /^\/dev\/|^\\\\\.\\/.test(p);
// snapKind tells what a snapshot holds: a Docker project, disk images or files.
function snapKind(s) {
  if (s.docker) return { key: 'docker', icon: s.docker.databases && s.docker.databases.length ? 'database' : 'containers', label: 'Docker project' };
  if ((s.paths || []).length && s.paths.every(isDevPath)) return { key: 'disk', icon: 'disk', label: 'Disk image' };
  if ((s.databases || []).length && !(s.paths || []).length) return { key: 'database', icon: 'database', label: 'Databases' };
  return { key: 'files', icon: 'folder', label: 'Files and folders' };
}
const setKey = s => s.hostname + '\n' + (s.job || 'paths:' + (s.paths || []).join(','));
// setName is what a backup set is called: the Docker project, else the job,
// else the backed-up path.
function setName(s) {
  if (s.docker) return s.docker.project;
  if (s.job) return s.job;
  const p = (s.paths || [])[0] || '?';
  return (s.paths || []).length > 1 ? p + ' +' + (s.paths.length - 1) : p;
}
function typeChip(t) { return h('span', { class: 'chip type-' + t }, icon(t), t); }
function iconBtn(name, title, onclick, cls) {
  return h('button', { class: 'small icon-only ' + (cls || ''), title, 'aria-label': title, onclick }, icon(name));
}
function loadPref(key, def) {
  try { return Object.assign({}, def, JSON.parse(localStorage.getItem(key) || '{}')); } catch (e) { return def; }
}
function savePref(key, v) { try { localStorage.setItem(key, JSON.stringify(v)); } catch (e) { /* storage blocked */ } }

views.snapshots = async main => {
  const cfg = await loadConfig();
  main.append(h('h1', {}, 'Snapshots & Restore'), h('div', { class: 'sub' }, 'Browse backups in the explorer, restore or download files, folders or whole snapshots, compare snapshots.'));
  const repos = cfg.repositories || [];
  if (!repos.length) { main.append(h('div', { class: 'empty' }, 'No repositories configured.')); return; }
  if (!state.snapRepo || !repos.find(r => r.name === state.snapRepo)) state.snapRepo = repos[0].name;
  const pref = loadPref('herakles.snapView', { sort: 'time', desc: true, group: 'none', type: '' });
  const keep = () => savePref('herakles.snapView', pref);
  const sel = h('select', { style: 'width:auto', onchange: e => { state.snapRepo = e.target.value; state.snapSet = null; load(); } },
    repos.map(r => h('option', { value: r.name, selected: r.name === state.snapRepo }, r.name)));
  const hostSel = h('select', { style: 'width:auto', 'aria-label': 'System', onchange: e => { state.snapHost = e.target.value; state.snapSet = null; renderTypes(); render(); } });
  const modeBox = h('div', { class: 'seg' });
  const filter = h('input', { type: 'search', class: 'snap-filter', placeholder: 'Filter: job, host, path, tag, ID…', value: state.snapFilter || '', oninput: () => render() });
  state.snapFilter = '';
  const typeBox = h('div', { class: 'seg' });
  const groupSel = h('select', { style: 'width:auto', onchange: e => { pref.group = e.target.value; keep(); render(); } },
    [['none', 'No grouping'], ['type', 'Backup type'], ['job', 'Job'], ['kind', 'Content'], ['day', 'Day'], ['host', 'System']]
      .map(([v, t]) => h('option', { value: v, selected: pref.group === v }, t)));
  const diffBtn = h('button', { title: 'Show what changed between two selected snapshots', disabled: true, onclick: () => diffSelected() }, 'Compare selected');
  const forgetBtn = h('button', { class: 'danger', title: 'Delete the selected snapshots (space is freed by Prune)', disabled: true, onclick: () => forgetSelected() }, 'Forget selected');
  const stats = h('div', { class: 'stat-strip' });
  const setHead = h('div');
  const listBar = h('div', { class: 'toolbar' }, typeBox, h('label', { class: 'inline' }, 'Group by'), groupSel, h('span', { class: 'spacer' }), diffBtn, forgetBtn);
  const tableBox = h('div');
  main.append(
    h('div', { class: 'toolbar' }, modeBox, h('label', { class: 'inline' }, icon('repos'), 'Repository'), sel,
      h('label', { class: 'inline' }, icon('systems'), 'System'), hostSel, filter, h('span', { class: 'spacer' }),
      h('button', { title: 'Read the snapshot list again', onclick: () => load() }, 'Reload')),
    stats, setHead, listBar, tableBox);
  let snaps = [];
  const picked = new Set();
  const open = new Set();
  const updateSel = () => {
    diffBtn.disabled = picked.size !== 2;
    forgetBtn.disabled = !picked.size;
    forgetBtn.lastChild.nodeValue = picked.size ? 'Forget ' + picked.size + ' selected' : 'Forget selected';
  };

  const COLS = [
    { key: 'id', label: 'ID' }, { key: 'time', label: 'Time' }, { key: 'job', label: 'Job / system' },
    { key: 'type', label: 'Type' }, { key: 'size', label: 'Size', num: true }, { key: 'stored', label: 'Stored', num: true }, { key: '', label: 'Contents' }];
  const val = (s, k) => ({
    id: s.id, time: s.time, job: (s.job || '').toLowerCase(), host: (s.hostname || '').toLowerCase(), type: TYPE_RANK[s.type] ?? 9,
    size: s.summary ? s.summary.total_bytes : -1, stored: s.summary ? s.summary.bytes_stored : -1,
  })[k];
  const GROUPS = {
    type: s => [TYPE_RANK[s.type] ?? 9, s.type, typeChip(s.type)],
    job: s => [s.job || '~', s.job || 'no job', h('span', {}, icon(snapKind(s).icon), s.job || 'no job')],
    kind: s => { const k = snapKind(s); return [k.key, k.label, h('span', {}, icon(k.icon), k.label)]; },
    day: s => { const d = new Date(s.time); const k = d.toDateString(); return [-d.setHours(0, 0, 0, 0), k, h('span', {}, icon('calendar'), d.toLocaleDateString(undefined, { weekday: 'long', year: 'numeric', month: 'long', day: 'numeric' }))]; },
    host: s => [s.hostname, s.hostname, h('span', {}, icon('server'), s.hostname)],
  };

  // A backup set is everything one job backed up from one system: a Docker
  // project, a disk, a folder job. The overview shows one card per set.
  const scoped = () => snaps.filter(s => (!state.snapHost || s.hostname === state.snapHost) && (!state.snapSet || setKey(s) === state.snapSet));
  const overview = () => !state.snapSet && !state.snapAll;
  const renderMode = () => {
    clear(modeBox).append(
      h('button', { title: 'One card per backed-up item', class: overview() || state.snapSet ? 'on' : '', onclick: () => { state.snapAll = false; state.snapSet = null; renderTypes(); render(); } }, icon('dashboard'), 'Backups'),
      h('button', { title: 'Every snapshot in one list', class: state.snapAll && !state.snapSet ? 'on' : '', onclick: () => { state.snapAll = true; state.snapSet = null; renderTypes(); render(); } }, icon('logs'), 'All snapshots'));
  };
  const openSet = key => { state.snapSet = key; picked.clear(); updateSel(); renderTypes(); render(); main.scrollTop = 0; };

  const renderOverview = q => {
    const groups = new Map();
    for (const s of scoped()) {
      const k = setKey(s);
      if (!groups.has(k)) groups.set(k, []);
      groups.get(k).push(s);
    }
    let sets = [...groups.entries()].map(([key, list]) => {
      const latest = list.reduce((a, s) => !a || s.time > a.time ? s : a, null);
      return { key, list, latest, kind: snapKind(latest), name: setName(latest) };
    });
    if (q) sets = sets.filter(x => [x.name, x.latest.job, x.latest.hostname, ...(x.latest.paths || [])].join(' ').toLowerCase().includes(q));
    if (!sets.length) {
      tableBox.append(h('div', { class: 'empty' }, snaps.length ? 'No backup matches the filter.' : state.snapHost ? 'No backups of ' + state.snapHost + ' in this repository.' : 'No backups in this repository yet.'));
      return;
    }
    const multiHost = new Set(sets.map(x => x.latest.hostname)).size > 1;
    const SECTIONS = [['docker', 'Docker projects', 'containers'], ['database', 'Databases', 'database'], ['disk', 'Disks and partitions', 'disk'], ['files', 'Files and folders', 'folder']];
    for (const [kind, title, ic] of SECTIONS) {
      const mine = sets.filter(x => x.kind.key === kind).sort((a, b) => a.name.localeCompare(b.name) || a.latest.hostname.localeCompare(b.latest.hostname));
      if (!mine.length) continue;
      const stored = mine.reduce((n, x) => n + x.list.reduce((m, s) => m + (s.summary ? s.summary.bytes_stored : 0), 0), 0);
      tableBox.append(h('h2', { class: 'set-section' }, icon(ic), title, h('span', { class: 'muted' }, mine.length + ' · ' + fmtBytes(stored) + ' stored')));
      const grid = h('div', { class: 'sys-grid set-grid' });
      tableBox.append(grid);
      for (const x of mine) grid.append(setCard(x, multiHost));
    }
  };

  const setCard = (x, multiHost) => {
    const l = x.latest;
    const [fcls, ftext] = freshness(l.time);
    const counts = {};
    x.list.forEach(s => { counts[s.type] = (counts[s.type] || 0) + 1; });
    const stored = x.list.reduce((n, s) => n + (s.summary ? s.summary.bytes_stored : 0), 0);
    const errs = x.list.filter(s => s.summary && s.summary.errors > 0).length;
    const sub = [x.kind.label, l.docker && l.job ? 'job ' + l.job.split('/')[0] : (l.docker ? '' : l.job ? '' : 'ad-hoc backup'), multiHost || state.snapHost ? l.hostname : ''].filter(Boolean).join(' · ');
    return h('div', { class: 'sys-card set-card' },
      h('button', { class: 'sys-main', title: 'Show the snapshots of ' + x.name, onclick: () => openSet(x.key) },
        h('span', { class: 'sys-ico kind-' + x.kind.key }, icon(x.kind.icon), h('span', { class: 'sys-dot ' + fcls, title: 'Last backup ' + ftext })),
        h('span', { class: 'sys-text' },
          h('b', {}, x.name),
          h('span', { class: 'muted small' }, sub),
          h('span', { class: 'sys-last' }, 'Last backup ', h('strong', { title: fmtTime(l.time) }, ago(l.time)), ' ', typeChip(l.type))),
        h('span', { class: 'sys-go' }, icon('next'))),
      h('div', { class: 'sys-facts' },
        h('span', {}, icon('snapshots'), x.list.length + (x.list.length === 1 ? ' snapshot' : ' snapshots')),
        h('span', {}, icon('archive'), l.summary ? fmtBytes(l.summary.total_bytes) : '-'),
        h('span', {}, icon('repos'), fmtBytes(stored) + ' stored'),
        errs ? h('span', { class: 'warn' }, icon('alert'), errs + ' with read errors') : null),
      h('div', { class: 'sys-kinds' }, SNAP_TYPES.filter(([t]) => counts[t]).map(([t, label]) => h('span', { class: 'chip type-' + t }, icon(t), counts[t] + ' ' + label.toLowerCase())),
        h('div', { class: 'set-paths' }, contents(l))),
      h('div', { class: 'sys-repos set-actions' },
        h('button', { class: 'small', title: 'Look through the latest backup', onclick: () => openExplorer({ repo: state.snapRepo, snapshot: l }) }, 'Browse'),
        h('button', { class: 'small', title: 'Put the latest backup back', onclick: () => restoreDlg(state.snapRepo, l, ['/']) }, 'Restore'),
        h('button', { class: 'small', title: 'Download the latest backup as an archive', onclick: () => downloadDlg(state.snapRepo, l, ['/']) }, 'Download'),
        h('span', { class: 'muted small' }, 'latest')));
  };

  const renderSetHead = () => {
    clear(setHead);
    if (!state.snapSet) return;
    const list = scoped();
    if (!list.length) return;
    const l = list.reduce((a, s) => !a || s.time > a.time ? s : a, null);
    const k = snapKind(l);
    setHead.append(h('div', { class: 'set-head' },
      h('button', { title: 'Back to the overview', onclick: () => { state.snapSet = null; renderTypes(); render(); } }, icon('back'), 'All backups'),
      h('span', { class: 'sys-ico small kind-' + k.key }, icon(k.icon)),
      h('div', {}, h('b', {}, setName(l)), h('div', { class: 'muted small' }, [k.label, l.job ? 'job ' + l.job : '', l.hostname].filter(Boolean).join(' · ')))));
  };
  const renderHosts = () => {
    const hosts = [...new Set(snaps.map(s => s.hostname))].sort();
    if (state.snapHost && !hosts.includes(state.snapHost)) hosts.push(state.snapHost);
    clear(hostSel).append(h('option', { value: '' }, 'All systems (' + hosts.length + ')'),
      ...hosts.map(x => h('option', { value: x }, x)));
    hostSel.value = state.snapHost || '';
  };
  const renderStats = () => {
    clear(stats);
    const snaps = scoped();
    if (!snaps.length) return;
    const jobs = new Set(snaps.map(s => s.job || ''));
    const newest = snaps.reduce((a, s) => !a || s.time > a.time ? s : a, null);
    const stored = snaps.reduce((n, s) => n + (s.summary ? s.summary.bytes_stored : 0), 0);
    const errs = snaps.filter(s => s.summary && s.summary.errors > 0).length;
    const stat = (ic, k, v, cls) => h('div', { class: 'stat ' + (cls || '') }, icon(ic), h('div', {}, h('div', { class: 'k' }, k), h('div', { class: 'v' }, v)));
    stats.append(
      stat('snapshots', 'Snapshots', String(snaps.length)),
      stat('jobs', 'Jobs', String(jobs.size)),
      stat('clock', 'Latest backup', ago(newest.time)),
      stat('repos', 'Stored', fmtBytes(stored)),
      errs ? stat('alert', 'With read errors', String(errs), 'warn') : stat('check', 'Read errors', 'none', 'ok'));
  };

  const renderTypes = () => {
    clear(typeBox);
    const snaps = scoped();
    const count = t => snaps.filter(s => !t || s.type === t).length;
    for (const [t, label] of [['', 'All types'], ...SNAP_TYPES]) {
      const n = count(t);
      if (t && !n && pref.type !== t) continue;
      typeBox.append(h('button', { title: t ? 'Show only ' + label.toLowerCase() + ' backups' : 'Show all backup types', class: (pref.type === t ? 'on ' : '') + (t ? 'type-' + t : ''), onclick: () => { pref.type = t; keep(); renderTypes(); render(); } },
        t ? icon(t) : icon('snapshots'), label, h('span', { class: 'n' }, String(n))));
    }
  };

  const contents = s => {
    const k = snapKind(s);
    const paths = s.paths || [];
    if (s.docker) {
      const d = s.docker;
      const extra = Object.keys(d.volumes || {}).length + (d.binds || []).length;
      return h('div', { class: 'snap-paths' }, h('span', { class: 'mono' }, d.home || d.working_dir || paths[0] || d.project),
        extra ? h('span', { class: 'more' }, '+ ' + extra + (extra === 1 ? ' volume' : ' volumes and bind mounts')) : null,
        d.databases && d.databases.length ? h('span', { class: 'chip' }, icon('database'), d.databases.join(', ')) : null);
    }
    const dbs = s.databases || [];
    return h('div', { class: 'snap-paths', title: paths.join('\n') }, paths.length || !dbs.length ? h('span', { class: 'mono' }, paths[0] || '-') : null,
      paths.length > 1 ? h('span', { class: 'more' }, '+ ' + (paths.length - 1) + ' more') : null,
      k.key === 'disk' ? h('span', { class: 'chip' }, icon('disk'), 'image') : null,
      dbs.length ? h('span', { class: 'chip', title: dbs.map(dumpName).join('\n') }, icon('database'), dbs.length === 1 ? dumpName(dbs[0]) : dbs.length + ' database dumps') : null);
  };

  const details = s => {
    const sm = s.summary;
    const kv = (k, v) => v === null || v === undefined || v === '' ? [] : [h('div', {}, k), h('div', {}, v)];
    const n = x => (x || 0).toLocaleString();
    const short = id => id ? id.slice(0, 8) : '';
    const parent = s.parent && snaps.find(x => x.id === s.parent);
    return h('div', { class: 'snap-details' },
      h('div', { class: 'kv' },
        kv('Snapshot', h('span', { class: 'mono' }, s.id)),
        kv('Created', fmtTime(s.time) + (sm && sm.end && !sm.end.startsWith('0001') ? ' · took ' + fmtDur((new Date(sm.end) - new Date(sm.start)) * 1e6) : '')),
        kv('Based on', s.parent ? h('span', {}, h('span', { class: 'mono' }, short(s.parent)), parent ? ' (' + parent.type + ', ' + fmtTime(parent.time) + ')' : ' (no longer in the repository)') : (s.type === 'full' ? 'nothing (self-contained full backup)' : null)),
        kv('Backup file', s.file ? h('span', {}, h('span', { class: 'mono' }, s.file), ' · ',
          (s.chain || []).length > 1
            ? h('span', { title: 'To restore it elsewhere, copy these files together:\n' + s.chain.join('\n') }, 'restoring needs ' + s.chain.length + ' files (' + fmtBytes(s.chain_bytes) + ')')
            : 'restorable on its own (' + fmtBytes(s.chain_bytes) + ')') : 'older repository format (shared data files)'),
        kv('Files', sm ? n(sm.total_files) + ' files · ' + n(sm.files_new) + ' new, ' + n(sm.files_changed) + ' changed, ' + n(sm.files_unmodified) + ' unchanged' : null),
        kv('Data', sm ? fmtBytes(sm.total_bytes) + ' in the snapshot · ' + fmtBytes(sm.bytes_added) + ' new data · ' + fmtBytes(sm.bytes_stored) + ' written after compression' : null),
        kv('Read errors', sm && sm.errors ? h('div', { class: 'err-text' }, sm.errors + ' file(s) could not be read', (sm.error_samples || []).map(e => h('div', { class: 'mono' }, e))) : null),
        kv('Docker project', s.docker ? s.docker.project + (s.docker.standalone ? ' (standalone container)' : '') : null),
        kv('Tags', (s.tags || []).length ? h('span', {}, s.tags.map(t => h('span', { class: 'chip' }, t))) : null),
        kv('User', s.username),
        kv('Program', s.program_version ? 'Herakles ' + s.program_version : null)),
      (s.databases || []).length ? [h('div', { class: 'k-head' }, 'Database dumps'), dumpList(state.snapRepo, s)] : null,
      (s.paths || []).length ? [h('div', { class: 'k-head' }, 'Backed-up paths'),
        h('ul', { class: 'pathlist mono' }, s.paths.map(p => h('li', {}, p)))] : null);
  };

  let oneHost = false;
  const row = s => {
    const k = snapKind(s);
    const errs = s.summary && s.summary.errors;
    const tr = h('tr', { class: 'snap-row' + (picked.has(s.id) ? ' selected' : '') + (open.has(s.id) ? ' open' : ''),
      onclick: e => { if (e.target.closest('button, input, a')) return; open.has(s.id) ? open.delete(s.id) : open.add(s.id); render(); } },
      h('td', { class: 'cb' }, h('input', { type: 'checkbox', checked: picked.has(s.id), 'aria-label': 'Select ' + s.id.slice(0, 8),
        onchange: e => { e.target.checked ? picked.add(s.id) : picked.delete(s.id); tr.classList.toggle('selected', e.target.checked); updateSel(); } })),
      h('td', {}, h('span', { class: 'snap-id' }, h('span', { class: 'twisty' }, '▸'), h('span', { class: 'mono' }, s.id.slice(0, 8)))),
      h('td', { title: fmtTime(s.time) }, h('div', {}, ago(s.time)), h('div', { class: 'muted small' }, new Date(s.time).toLocaleString(undefined, { dateStyle: 'short', timeStyle: 'short' }))),
      h('td', {}, h('div', { class: 'job-cell', title: k.label }, icon(k.icon, 'kind kind-' + k.key), s.job || h('span', { class: 'muted' }, 'no job')),
        oneHost ? null : h('div', { class: 'job-cell muted small host-line' }, icon('server'), s.hostname)),
      h('td', {}, typeChip(s.type), errs ? h('span', { class: 'chip warn', title: errs + ' file(s) could not be read' }, icon('alert'), errs) : null),
      h('td', { class: 'num' }, s.summary ? fmtBytes(s.summary.total_bytes) : '-'),
      h('td', { class: 'num' }, s.summary ? h('div', {}, fmtBytes(s.summary.bytes_stored),
        s.summary.total_bytes && s.summary.bytes_stored < s.summary.total_bytes ? h('div', { class: 'muted small' },
          s.summary.bytes_stored ? Math.max(0.1, s.summary.bytes_stored / s.summary.total_bytes * 100).toFixed(1) + ' % of size' : 'nothing new') : null) : '-'),
      h('td', { class: 'paths-cell' }, contents(s)),
      h('td', { class: 'nowrap actions-cell' },
        h('button', { class: 'small', title: 'Look through this snapshot; download or restore files', onclick: () => openExplorer({ repo: state.snapRepo, snapshot: s }) }, 'Browse'),
        iconBtn('download', 'Download the whole snapshot', () => downloadDlg(state.snapRepo, s, ['/'])),
        iconBtn('restore', 'Restore the whole snapshot', () => restoreDlg(state.snapRepo, s, ['/'])),
        iconBtn('trash', 'Forget this snapshot', () => forget([s]), 'danger')));
    const out = [tr];
    if (open.has(s.id)) out.push(h('tr', { class: 'snap-more' }, h('td', {}), h('td', { colspan: COLS.length + 1 }, details(s))));
    return out;
  };

  const render = () => {
    renderMode();
    renderStats();
    renderSetHead();
    clear(tableBox);
    const q = filter.value.toLowerCase().trim();
    listBar.hidden = overview();
    if (overview()) { renderOverview(q); return; }
    const snaps = scoped();
    oneHost = new Set(snaps.map(s => s.hostname)).size === 1;
    const cols = COLS.map(c => c.key === 'job' && oneHost ? { key: 'job', label: 'Job' } : c);
    let list = snaps.filter(s => (!pref.type || s.type === pref.type) &&
      (!q || [s.id, s.job, s.hostname, s.type, ...(s.tags || []), ...(s.paths || []), s.docker && s.docker.project].join(' ').toLowerCase().includes(q)));
    if (!list.length) {
      tableBox.append(h('div', { class: 'empty' }, snaps.length ? 'No snapshot matches the filter.' : state.snapHost ? 'No snapshots of ' + state.snapHost + ' in this repository.' : 'No snapshots in this repository yet.'));
      return;
    }
    const cmp = (a, b) => {
      const x = val(a, pref.sort), y = val(b, pref.sort);
      const c = x < y ? -1 : x > y ? 1 : 0;
      return (pref.desc ? -c : c) || (b.time < a.time ? -1 : b.time > a.time ? 1 : 0);
    };
    list = list.slice().sort(cmp);
    const head = h('tr', {}, h('th', { class: 'cb' }, h('input', { type: 'checkbox', 'aria-label': 'Select all shown',
      checked: list.every(s => picked.has(s.id)), onchange: e => { list.forEach(s => e.target.checked ? picked.add(s.id) : picked.delete(s.id)); updateSel(); render(); } })),
    cols.map(c => c.key ? h('th', { class: 'sortable' + (pref.sort === c.key ? ' on' : '') + (c.num ? ' num' : ''),
      onclick: () => { pref.desc = pref.sort === c.key ? !pref.desc : ['time', 'size', 'stored'].includes(c.key); pref.sort = c.key; keep(); render(); } },
    c.label, pref.sort === c.key ? (pref.desc ? ' ↓' : ' ↑') : '') : h('th', {}, c.label)), h('th', {}));
    const body = [];
    if (pref.group !== 'none' && GROUPS[pref.group]) {
      const groups = new Map();
      for (const s of list) {
        const [rank, key, label] = GROUPS[pref.group](s);
        if (!groups.has(key)) groups.set(key, { rank, label, items: [] });
        groups.get(key).items.push(s);
      }
      const sorted = [...groups.values()].sort((a, b) => a.rank < b.rank ? -1 : a.rank > b.rank ? 1 : 0);
      for (const g of sorted) {
        const stored = g.items.reduce((n, s) => n + (s.summary ? s.summary.bytes_stored : 0), 0);
        body.push(h('tr', { class: 'group-row' }, h('td', { colspan: cols.length + 2 },
          g.label, h('span', { class: 'muted' }, ' · ' + g.items.length + (g.items.length === 1 ? ' snapshot' : ' snapshots') + ' · ' + fmtBytes(stored) + ' stored'))));
        g.items.forEach(s => body.push(...row(s)));
      }
    } else list.forEach(s => body.push(...row(s)));
    tableBox.append(h('div', { class: 'table-scroll' }, h('table', { class: 'snap-table' }, head, body)),
      h('div', { class: 'hint' }, list.length === snaps.length ? list.length + ' snapshots' : list.length + ' of ' + snaps.length + ' snapshots shown',
        ' · click a row for details, click a column title to sort'));
  };

  const load = async () => {
    clear(tableBox).append(h('div', { class: 'muted' }, 'Loading…'));
    picked.clear(); open.clear(); updateSel();
    try { snaps = await api('GET', '/api/repos/' + encodeURIComponent(state.snapRepo) + '/snapshots'); }
    catch (e) { snaps = []; renderStats(); clear(tableBox).append(h('div', { class: 'empty' }, e.message)); return; }
    if (state.snapJob) {
      const s = snaps.filter(x => x.job === state.snapJob).sort((a, b) => a.time < b.time ? 1 : -1)[0];
      state.snapSet = s ? setKey(s) : null;
      state.snapJob = '';
    }
    if (state.snapSet && !snaps.some(s => setKey(s) === state.snapSet)) state.snapSet = null;
    renderHosts();
    renderTypes();
    render();
  };
  const forget = async list => {
    const what = list.length === 1 ? 'snapshot ' + list[0].id.slice(0, 8) + ' (' + list[0].job + ', ' + fmtTime(list[0].time) + ')' : list.length + ' snapshots';
    if (!await confirmDlg('Forget snapshot' + (list.length > 1 ? 's' : ''), 'Remove ' + what + '? Run prune afterwards to free space.', true)) return;
    let done = 0;
    for (const s of list) {
      try { await api('DELETE', '/api/repos/' + encodeURIComponent(state.snapRepo) + '/snapshots/' + s.id); done++; }
      catch (e) { toast(s.id.slice(0, 8) + ': ' + e.message, true); }
    }
    if (done) toast(done === 1 ? 'Snapshot forgotten' : done + ' snapshots forgotten');
    load();
  };
  const forgetSelected = () => forget(snaps.filter(s => picked.has(s.id)));
  const diffSelected = async () => {
    const [a, b] = snaps.filter(s => picked.has(s.id)).sort((x, y) => new Date(x.time) - new Date(y.time));
    const changes = await api('GET', '/api/repos/' + encodeURIComponent(state.snapRepo) + '/diff?a=' + a.id + '&b=' + b.id);
    const sym = { added: '+', removed: '−', modified: 'M', type: 'T' };
    await modal('Changes ' + a.id.slice(0, 8) + ' → ' + b.id.slice(0, 8), changes.length ?
      h('table', {}, h('tr', {}, h('th', {}, ''), h('th', {}, 'Path'), h('th', {}, 'Size')),
        changes.slice(0, 2000).map(c => h('tr', {}, h('td', {}, c.metadata_only ? 'U' : sym[c.kind]), h('td', { class: 'wrap mono' }, c.path),
          h('td', {}, c.kind === 'removed' ? fmtBytes(c.old_size) : fmtBytes(c.new_size))))) : h('p', {}, 'No differences.'), [{ label: 'Close' }]);
  };
  await load();
};

// ---------- jobs ----------
views.jobs = async main => {
  const cfg = await loadConfig();
  main.append(h('h1', {}, 'Backup Jobs'), h('div', { class: 'sub' }, 'What to back up, where to, when, and how long to keep it.'),
    h('div', { class: 'toolbar' }, h('button', { class: 'primary', onclick: () => navigate('wizard') }, 'Backup wizard'),
      h('button', { onclick: () => editJob(null) }, '+ New job')));
  const jobs = cfg.jobs || [];
  if (!jobs.length) { main.append(h('div', { class: 'empty' }, 'No jobs defined.')); return; }
  main.append(h('table', {}, h('tr', {}, ['Job', 'Repository', 'Sources', 'Schedule', 'Retention', ''].map(t => h('th', {}, t))),
    jobs.map(j => h('tr', {},
      h('td', {}, h('b', {}, j.name), j.disabled ? ' (disabled)' : ''), h('td', {}, j.repository),
      h('td', { class: 'wrap' }, jobSources(j)),
      h('td', { class: 'wrap' }, (j.schedule || []).map(s => h('span', { class: 'chip' }, s.cron + ' · ' + (s.type || 'auto')))),
      h('td', { class: 'normal' }, retentionText(j.retention)),
      h('td', {}, h('button', { class: 'small', title: 'Change this job', onclick: () => editJob(j.name) }, 'Edit'), ' ',
        h('button', { class: 'small danger', title: 'Remove this job; its backups are kept', onclick: async () => {
          if (!await confirmDlg('Delete job', 'Delete job ' + j.name + '? Existing snapshots are kept.', true)) return;
          const c = await loadConfig(); c.jobs = c.jobs.filter(x => x.name !== j.name); await saveConfig(c); navigate('jobs');
        } }, 'Delete'))))));
};

function retentionText(r) {
  if (!r) return 'keep everything';
  const parts = [];
  const names = { keep_last: 'last', keep_hourly: 'hourly', keep_daily: 'daily', keep_weekly: 'weekly', keep_monthly: 'monthly', keep_yearly: 'yearly', keep_full: 'full' };
  for (const [k, n] of Object.entries(names)) if (r[k]) parts.push(r[k] + ' ' + n);
  for (const k of ['keep_within', 'keep_within_hourly', 'keep_within_daily', 'keep_within_weekly', 'keep_within_monthly', 'keep_within_yearly'])
    if (r[k] && r[k] !== '0') parts.push(k.replace('keep_', '').replace(/_/g, ' ') + ' ' + r[k]);
  if (r.keep_tags && r.keep_tags.length) parts.push('tags ' + r.keep_tags.join(','));
  return parts.length ? parts.join(', ') : 'keep everything';
}

// Repositories of the hub that the attached server being managed does not
// have yet. Choosing one passes it on (with its password) when saving.
async function hubOffers(cfg) {
  if (!state.target || !isAdmin()) return [];
  try {
    const hc = await localApi('GET', '/api/config');
    const have = new Set((cfg.repositories || []).map(r => r.name));
    return (hc.repositories || []).filter(r => !r.hub && !have.has(r.name)).map(r => ({ name: r.name, location: r.location, offer: true }));
  } catch (e) { return []; }
}

// useHubRepo shares a hub repository with the attached servers and waits
// until the managed server has it; it returns the server's configuration.
async function useHubRepo(name) {
  const hub = await localApi('GET', '/api/hub');
  const shared = new Set(hub.shared || []);
  if (!shared.has(name)) {
    shared.add(name);
    await localApi('PUT', '/api/hub/shared', { repositories: [...shared] });
  }
  for (let i = 0; i < 40; i++) {
    const c = await loadConfig();
    if ((c.repositories || []).some(r => r.name === name)) return c;
    await new Promise(res => setTimeout(res, 500));
  }
  throw new Error('The server did not receive the repository “' + name + '” from the hub. Is it online?');
}
const offerNote = 'From the hub: passed to the attached servers with its password when you save (Servers page → Repositories for all servers).';

const lines = v => (v || []).join('\n');
const unlines = v => v.split('\n').map(x => x.trim()).filter(Boolean);
const num = el => parseInt(el.value, 10) || 0;

async function editJob(name) {
  const cfg = await loadConfig();
  const repos = (cfg.repositories || []).concat(await hubOffers(cfg));
  if (!repos.length) { toast('Add a repository first', true); navigate('repos'); return; }
  const orig = name ? cfg.jobs.find(j => j.name === name) : null;
  const j = orig ? JSON.parse(JSON.stringify(orig)) : { repository: repos[0].name, schedule: [{ cron: '0 2 * * *', type: 'auto' }], full_interval: '7d', retention: { keep_daily: 7, keep_weekly: 4, keep_monthly: 12 } };
  const r = j.retention || {};
  const dock = dockerPicker(j);
  const dbs = databaseEditor(j);
  const f = {
    name: h('input', { value: j.name || '' }), description: h('input', { value: j.description || '' }),
    repository: h('select', { title: repos.some(x => x.offer) ? offerNote : null }, repos.map(x => h('option', { value: x.name, selected: x.name === j.repository }, x.name + ' — ' + x.location + (x.offer ? ' (from the hub)' : '')))),
    paths: h('textarea', { rows: 3, placeholder: '/home\n/etc' }, lines(j.paths)),
    devices: h('textarea', { rows: 2, placeholder: '/dev/sda1   or   \\\\.\\C:' }, lines(j.devices)),
    exclude: h('textarea', { rows: 3, placeholder: '*.tmp\n**/node_modules\n/home/*/Downloads' }, lines(j.exclude)),
    excludeIfPresent: h('input', { value: (j.exclude_if_present || []).join(', '), placeholder: '.nobackup' }),
    excludeLarger: h('input', { value: j.exclude_larger_than || '', placeholder: 'e.g. 20G' }),
    excludeCaches: h('input', { type: 'checkbox', checked: j.exclude_caches }), oneFS: h('input', { type: 'checkbox', checked: j.one_file_system }),
    tags: h('input', { value: (j.tags || []).join(', ') }), fullInterval: h('input', { value: j.full_interval || '', placeholder: '7d' }),
    prune: h('input', { type: 'checkbox', checked: j.prune }), catchUp: h('input', { type: 'checkbox', checked: j.catch_up }),
    disabled: h('input', { type: 'checkbox', checked: j.disabled }),
    hooksBefore: h('textarea', { rows: 2 }, lines(j.hooks && j.hooks.before)), hooksAfter: h('textarea', { rows: 2 }, lines(j.hooks && j.hooks.after)),
    hooksFail: h('textarea', { rows: 2 }, lines(j.hooks && j.hooks.on_failure)),
    retryAttempts: h('input', { type: 'number', min: 0, value: (j.retry && j.retry.attempts) || 0 }), retryDelay: h('input', { value: (j.retry && j.retry.delay) || '', placeholder: '10m' }),
  };
  const ret = {};
  for (const k of ['keep_last', 'keep_hourly', 'keep_daily', 'keep_weekly', 'keep_monthly', 'keep_yearly', 'keep_full'])
    ret[k] = h('input', { type: 'number', min: -1, value: r[k] || '' });
  for (const k of ['keep_within', 'keep_within_daily', 'keep_within_weekly', 'keep_within_monthly'])
    ret[k] = h('input', { value: r[k] && r[k] !== '0' ? r[k] : '', placeholder: 'e.g. 14d, 6m, 1y' });
  ret.keep_tags = h('input', { value: (r.keep_tags || []).join(', ') });
  ret.group_by = h('input', { value: r.group_by || '', placeholder: 'job,host' });

  const schedBox = h('div');
  const addSched = s => {
    const cron = h('input', { value: s.cron || '', placeholder: 'min hour day month weekday — or @daily' });
    const type = h('select', {}, ['auto', 'full', 'incremental', 'differential'].map(t => h('option', { value: t, selected: (s.type || 'auto') === t }, t)));
    const row = h('div', { class: 'sched-row' }, cron, type, h('button', { onclick: () => row.remove() }, '×'));
    row._get = () => ({ cron: cron.value.trim(), type: type.value === 'auto' ? '' : type.value });
    schedBox.append(row);
  };
  (j.schedule || []).forEach(addSched);
  const presets = h('select', { style: 'width:auto', onchange: e => {
    const p = e.target.value; e.target.value = '';
    clear(schedBox);
    if (p === 'gfs') { addSched({ cron: '0 2 * * 0', type: 'full' }); addSched({ cron: '0 2 * * 1-6', type: 'incremental' }); }
    if (p === 'daily') addSched({ cron: '30 1 * * *', type: 'auto' });
    if (p === 'hourly') { addSched({ cron: '0 * * * *', type: 'incremental' }); addSched({ cron: '0 3 * * 0', type: 'full' }); }
    if (p === 'diff') { addSched({ cron: '0 1 1 * *', type: 'full' }); addSched({ cron: '0 1 2-31 * *', type: 'differential' }); }
  } }, h('option', { value: '' }, 'Presets…'), h('option', { value: 'gfs' }, 'Weekly full + daily incremental'),
    h('option', { value: 'daily' }, 'Daily (auto full every interval)'), h('option', { value: 'hourly' }, 'Hourly incremental + weekly full'),
    h('option', { value: 'diff' }, 'Monthly full + daily differential'));

  const body = [
    h('div', { class: 'row' }, h('div', {}, h('label', {}, 'Name'), f.name), h('div', {}, h('label', {}, 'Repository'), f.repository)),
    h('label', {}, 'Description'), f.description,
    h('fieldset', {}, h('legend', {}, 'Sources'),
      h('div', { class: 'row' }, h('div', {}, h('label', {}, 'Paths (one per line)'), f.paths), h('div', {}, h('label', {}, 'Disks / partitions as raw images (one per line)'), f.devices,
        h('button', { type: 'button', class: 'small', style: 'margin-top:6px', onclick: async () => { const v = await chooseDevices(unlines(f.devices.value)); if (v) f.devices.value = v.join('\n'); } }, 'Choose disks…')))),
    dock.el,
    dbs.el,
    h('fieldset', {}, h('legend', {}, 'Exclusions'),
      h('label', {}, 'Exclude patterns (one per line; *.tmp, **/cache, /abs/path)'), f.exclude,
      h('div', { class: 'row' }, h('div', {}, h('label', {}, 'Skip dirs containing'), f.excludeIfPresent), h('div', {}, h('label', {}, 'Skip files larger than'), f.excludeLarger)),
      h('label', { class: 'check' }, f.excludeCaches, 'Skip cache directories (CACHEDIR.TAG)'), h('label', { class: 'check' }, f.oneFS, 'Stay on one file system')),
    h('fieldset', {}, h('legend', {}, 'Schedule'),
      h('div', { class: 'toolbar' }, h('button', { onclick: () => addSched({ cron: '', type: 'auto' }) }, '+ Add schedule'), presets),
      schedBox,
      h('div', { class: 'row' }, h('div', {}, h('label', {}, 'Make "auto" backups full every'), f.fullInterval), h('div', {}, h('label', {}, 'Tags'), f.tags)),
      h('div', { class: 'hint' }, 'Every backup is one file. full: re-reads everything and stores it in a file that restores on its own · incremental: changes since the last backup · differential: changes since the last full · every snapshot restores completely.'),
      h('label', { class: 'check' }, f.catchUp, 'Run missed backups when the daemon starts'), h('label', { class: 'check' }, f.disabled, 'Disabled (not scheduled)')),
    h('fieldset', {}, h('legend', {}, 'Retention (a snapshot is kept if any rule matches; empty = keep all)'),
      h('div', { class: 'row4' }, ['keep_last', 'keep_hourly', 'keep_daily', 'keep_weekly', 'keep_monthly', 'keep_yearly', 'keep_full'].map(k =>
        h('div', {}, h('label', {}, k.replace('keep_', 'keep ')), ret[k]))),
      h('div', { class: 'row4' }, ['keep_within', 'keep_within_daily', 'keep_within_weekly', 'keep_within_monthly'].map(k =>
        h('div', {}, h('label', {}, k.replace(/_/g, ' ')), ret[k]))),
      h('div', { class: 'row' }, h('div', {}, h('label', {}, 'Always keep tags'), ret.keep_tags), h('div', {}, h('label', {}, 'Group by'), ret.group_by)),
      h('label', { class: 'check' }, f.prune, 'Prune right after applying retention (frees space immediately)')),
    h('fieldset', {}, h('legend', {}, 'Hooks & retries'),
      h('div', { class: 'row3' }, h('div', {}, h('label', {}, 'Before (one command per line)'), f.hooksBefore),
        h('div', {}, h('label', {}, 'After'), f.hooksAfter), h('div', {}, h('label', {}, 'On failure'), f.hooksFail)),
      h('div', { class: 'row' }, h('div', {}, h('label', {}, 'Retry attempts'), f.retryAttempts), h('div', {}, h('label', {}, 'Retry delay'), f.retryDelay))),
  ];
  await modal(orig ? 'Edit job ' + orig.name : 'New backup job', body, [
    { label: 'Cancel' },
    { label: 'Save', class: 'primary', action: async () => {
      const nj = Object.assign({}, j, {
        name: f.name.value.trim(), description: f.description.value, repository: f.repository.value,
        paths: unlines(f.paths.value), devices: unlines(f.devices.value), exclude: unlines(f.exclude.value),
        exclude_if_present: f.excludeIfPresent.value.split(',').map(x => x.trim()).filter(Boolean),
        exclude_larger_than: f.excludeLarger.value.trim(), exclude_caches: f.excludeCaches.checked, one_file_system: f.oneFS.checked,
        tags: f.tags.value.split(',').map(x => x.trim()).filter(Boolean), full_interval: f.fullInterval.value.trim(),
        schedule: [...schedBox.children].map(r => r._get()).filter(s => s.cron), prune: f.prune.checked, catch_up: f.catchUp.checked, disabled: f.disabled.checked,
        hooks: Object.assign({}, j.hooks || {}, { before: unlines(f.hooksBefore.value), after: unlines(f.hooksAfter.value), on_failure: unlines(f.hooksFail.value) }),
        retry: { attempts: num(f.retryAttempts), delay: f.retryDelay.value.trim() },
      });
      const nr = { group_by: ret.group_by.value.trim(), keep_tags: ret.keep_tags.value.split(',').map(x => x.trim()).filter(Boolean) };
      for (const k of ['keep_last', 'keep_hourly', 'keep_daily', 'keep_weekly', 'keep_monthly', 'keep_yearly', 'keep_full']) nr[k] = num(ret[k]);
      for (const k of ['keep_within', 'keep_within_daily', 'keep_within_weekly', 'keep_within_monthly']) nr[k] = ret[k].value.trim() || '0';
      for (const k of ['keep_within_hourly', 'keep_within_yearly']) nr[k] = r[k] || '0';
      nj.retention = nr;
      nj.docker = dock.value();
      nj.databases = dbs.value();
      if (!nj.docker && !nj.paths.length && !nj.devices.length && !nj.databases.length) { toast('Choose paths, disks, databases or Docker projects to back up', true); return false; }
      const offer = repos.find(x => x.offer && x.name === nj.repository);
      if (offer) await useHubRepo(offer.name);
      const c = await loadConfig();
      c.jobs = c.jobs || [];
      const i = orig ? c.jobs.findIndex(x => x.name === orig.name) : -1;
      if (i >= 0) c.jobs[i] = nj; else c.jobs.push(nj);
      await saveConfig(c);
      navigate(state.view);
    } }]);
}

// ---------- databases ----------
const DB_TYPES = [['postgres', 'PostgreSQL'], ['mysql', 'MySQL / MariaDB'], ['mongodb', 'MongoDB'], ['sqlite', 'SQLite']];
const DB_HINTS = {
  postgres: 'Leave host and user empty for the server on this computer: Herakles dumps it as the system user postgres. Each database becomes one .sql file, plus _globals.sql with the roles.',
  mysql: 'Leave host and user empty for the server on this computer (root over the socket). Each database becomes one .sql file.',
  mongodb: 'One archive of the whole server, or one per database listed below. A connection string (uri) can be set in the YAML.',
  sqlite: 'List the database files (patterns like /var/lib/app/*.db work). Needs the sqlite3 program.',
};
const csv = v => v.split(',').map(x => x.trim()).filter(Boolean);
const dumpName = d => d.path ? d.source + '/' + d.path.split('/').pop().replace(/\.[^.]*$/, '') : d.source;

// databaseEditor is the Databases part of the job editor: one row per
// server (or SQLite files), with a connection test.
function databaseEditor(j) {
  const box = h('div');
  const add = d => {
    const type = h('select', {}, DB_TYPES.map(([v, l]) => h('option', { value: v, selected: d.type === v }, l)));
    const name = h('input', { value: d.name || '', placeholder: 'default: the type' });
    const host = h('input', { value: d.host || '', placeholder: 'this computer' });
    const port = h('input', { type: 'number', min: 0, value: d.port || '', placeholder: 'default' });
    const user = h('input', { value: d.user || '', placeholder: 'default' });
    const pass = h('input', { type: 'password', value: d.password || '', placeholder: d.password_env || d.password_file || d.password_command ? 'set in the YAML' : 'none', autocomplete: 'new-password' });
    const list = h('input', { value: (d.databases || []).join(', ') });
    const excl = h('input', { value: (d.exclude || []).join(', '), placeholder: 'e.g. test_*' });
    const hint = h('div', { class: 'hint' });
    const result = h('div', { class: 'hint' });
    const net = h('div', { class: 'row4' }, h('div', {}, h('label', {}, 'Host or socket'), host), h('div', {}, h('label', {}, 'Port'), port),
      h('div', {}, h('label', {}, 'User'), user), h('div', {}, h('label', {}, 'Password'), pass));
    const listLabel = h('label', {});
    const sync = () => {
      const sq = type.value === 'sqlite';
      net.style.display = sq ? 'none' : '';
      listLabel.textContent = sq ? 'Database files (comma separated)' : 'Only these databases (comma separated; empty = all)';
      list.placeholder = sq ? '/var/lib/app/app.db' : 'all';
      hint.textContent = DB_HINTS[type.value];
    };
    type.onchange = sync;
    const row = h('div', { class: 'db-entry' },
      h('div', { class: 'row' }, h('div', {}, h('label', {}, 'Type'), type), h('div', {}, h('label', {}, 'Name in the backup'), name)),
      net,
      h('div', { class: 'row' }, h('div', {}, listLabel, list), h('div', {}, h('label', {}, 'Leave out (comma separated)'), excl)),
      hint,
      h('div', { class: 'toolbar' },
        h('button', { type: 'button', class: 'small', title: 'Connect with these settings and list what would be dumped', onclick: async () => {
          result.textContent = 'Connecting…';
          result.className = 'hint';
          try {
            const r = await api('POST', '/api/databases/test', row._get());
            result.textContent = 'Would dump: ' + r.dumps.map(x => (x.database || x.file) + (x.size ? ' (' + fmtBytes(x.size) + ')' : '')).join(', ');
          } catch (e) { result.textContent = e.message; result.className = 'hint err-text'; }
        } }, 'Test connection'),
        h('button', { type: 'button', class: 'small danger', title: 'Stop backing up this database', onclick: () => row.remove() }, 'Remove')),
      result);
    row._get = () => {
      const o = Object.assign({}, d, { type: type.value, name: name.value.trim(), host: host.value.trim(), port: parseInt(port.value, 10) || 0,
        user: user.value.trim(), password: pass.value, databases: csv(list.value), exclude: csv(excl.value) });
      for (const k of Object.keys(o)) if (o[k] === '' || o[k] === 0 || (Array.isArray(o[k]) && !o[k].length)) delete o[k];
      return o;
    };
    sync();
    box.append(row);
  };
  (j.databases || []).forEach(add);
  const el = h('fieldset', {}, h('legend', {}, 'Databases'),
    h('div', { class: 'hint' }, 'Dumped with the database\'s own tools (pg_dump, mysqldump, mongodump, sqlite3) while it keeps running, and stored in the backup under /herakles-databases. Databases in Docker containers are handled by the Docker section.'),
    box,
    h('button', { type: 'button', title: 'Back up a PostgreSQL, MySQL/MariaDB, MongoDB or SQLite database', onclick: () => add({ type: 'postgres' }) }, '+ Add database'));
  return { el, value: () => [...box.children].map(r => r._get()) };
}

// dumpList shows the database dumps of a snapshot with download and restore.
function dumpList(repo, s) {
  return h('table', { class: 'dump-list' }, (s.databases || []).map(d => h('tr', {},
    h('td', { class: 'nowrap' }, icon('database'), ' ', h('span', { class: 'mono' }, dumpName(d))),
    h('td', { class: 'muted' }, (DB_TYPES.find(t => t[0] === d.kind) || [0, d.kind])[1]),
    d.error ? h('td', { class: 'err-text', colspan: 2 }, 'not dumped: ' + d.error.split('\n')[0]) : [
      h('td', { class: 'num' }, fmtBytes(d.size)),
      h('td', { class: 'nowrap' },
        iconBtn('download', 'Download the dump file', () => startDownload(downloadURL(repo, s.id, [d.path]))),
        h('button', { class: 'small', title: 'Load this dump back into the database server', onclick: () => dbRestoreDlg(repo, s, d) }, 'Restore…'))])));
}

// dbRestoreDlg loads a dump into its database server.
async function dbRestoreDlg(repo, s, d) {
  const sq = d.kind === 'sqlite';
  const globals = !d.database && d.kind === 'postgres';
  const into = h('input', { value: d.database || '', placeholder: d.kind === 'mongodb' ? 'all databases in the dump' : '' });
  const replace = h('input', { type: 'checkbox' });
  const what = {
    postgres: globals ? 'Creates the roles and tablespaces; ones that exist are left as they are.' : 'The database is created if it does not exist. The dump replaces its tables, all or nothing. On a new server, restore ' + d.source + '/_globals first: it creates the roles that own the tables.',
    mysql: 'The database is created if it does not exist; the dump replaces its tables.',
    mongodb: 'Collections in the dump replace the existing ones.',
    sqlite: 'Writes the database file again from the dump.',
  }[d.kind];
  await modal('Restore ' + dumpName(d), [
    h('p', {}, 'Load the dump from the backup of ', fmtTime(s.time), ' into the database server. ', what),
    globals ? null : [h('label', {}, sq ? 'Database file' : 'Into database'), into,
      h('div', { class: 'hint' }, sq ? 'Restore into a new file to compare first.' : 'Enter another name to restore next to the current database.')],
    sq ? h('label', { class: 'check' }, replace, 'Replace the file if it exists') : null], [
    { label: 'Cancel' },
    { label: 'Restore', class: 'primary', action: async () => {
      toast('Restoring ' + dumpName(d) + '…');
      await api('POST', snapBase(repo, s.id) + '/database-restore', { path: d.path, into: globals ? '' : into.value.trim(), replace: replace.checked });
      toast('Restored ' + dumpName(d));
    } }], 'small');
}

// Docker part of the job editor: choose projects by ticking them, or by
// folders that include every project inside them (also future ones).
const globRe = g => new RegExp('^' + g.replace(/[.+^${}()|\\]/g, '\\$&').replace(/\*/g, '.*').replace(/\?/g, '.') + '$');
const isGlob = p => /[*?[]/.test(p);
const globMatch = (pats, n) => (pats || []).some(p => globRe(p).test(n));
function inFolders(folders, dirs) {
  for (let f of folders) {
    f = f.replace(/\/+$/, '') || '/';
    if ((dirs || []).some(d => d === f || d.startsWith(f === '/' ? '/' : f + '/'))) return f;
  }
  return '';
}

function dockerPicker(j) {
  const d = j.docker || {};
  const csv = v => v.split(',').map(x => x.trim()).filter(Boolean);
  const enabled = h('input', { type: 'checkbox', checked: !!j.docker });
  const modeAll = h('input', { type: 'radio', name: 'dmode', checked: !(d.projects && d.projects.length) && !(d.folders && d.folders.length) });
  const modePick = h('input', { type: 'radio', name: 'dmode', checked: !modeAll.checked });
  const folders = h('input', { value: (d.folders || []).join(', '), placeholder: 'e.g. /srv' });
  const scan = h('input', { value: (d.scan_dirs || []).join(', '), placeholder: 'e.g. /srv/docker' });
  const skipDumps = h('input', { type: 'checkbox', checked: d.skip_dumps });
  const skipStandalone = h('input', { type: 'checkbox', checked: d.skip_standalone });
  const list = h('div', { class: 'dock-list' });
  const body = h('div');
  // Per project: included / stopped, remembered across list reloads.
  const chosen = {}, stopped = {};
  let projects = [];
  const included = p => {
    if (p.name in chosen) return chosen[p.name];
    if (globMatch(d.exclude_projects, p.name)) return false;
    if (modeAll.checked) return true;
    return globMatch(d.projects, p.name) || !!inFolders(csv(folders.value), p.dirs);
  };
  const render = () => {
    clear(list);
    if (!projects.length) { list.append(h('div', { class: 'muted' }, 'No containers or project directories found.')); return; }
    list.append(h('table', {}, h('tr', {}, h('th', {}, 'Back up'), h('th', {}, 'Project'), h('th', {}, 'Containers'), h('th', {}, 'Stop during backup')),
      projects.map(p => {
        const via = !modeAll.checked ? inFolders(csv(folders.value), p.dirs) : '';
        const cb = h('input', { type: 'checkbox', style: 'width:auto', checked: included(p), onchange: e => { chosen[p.name] = e.target.checked; } });
        const st = h('input', { type: 'checkbox', style: 'width:auto', checked: p.name in stopped ? stopped[p.name] : globMatch(d.stop, p.name),
          onchange: e => { stopped[p.name] = e.target.checked; } });
        return h('tr', {}, h('td', {}, cb),
          h('td', {}, h('b', {}, p.name), p.standalone ? h('span', { class: 'chip' }, 'standalone') : null, via ? h('span', { class: 'chip' }, 'in ' + via) : null,
            h('div', { class: 'meta mono' }, p.working_dir || (p.dirs || []).join(', '))),
          h('td', {}, (p.containers || []).length ? p.running + '/' + p.containers.length + ' running' : 'directory only',
            p.databases && p.databases.length ? h('div', { class: 'meta' }, p.databases.join(', ')) : null),
          h('td', {}, st));
      })));
  };
  let timer;
  const load = async () => {
    clear(list).append(h('div', { class: 'muted' }, 'Looking for containers…'));
    try {
      const q = new URLSearchParams({ job: j.name || '', folders: folders.value, scan: scan.value });
      projects = await api('GET', '/api/docker/host?' + q);
      render();
    } catch (e) { clear(list).append(h('div', { class: 'muted' }, 'Could not list the containers on this host: ' + e.message)); }
  };
  const sync = () => {
    body.hidden = !enabled.checked;
    folders.closest('div').hidden = modeAll.checked;
    Object.keys(chosen).forEach(k => delete chosen[k]); // a mode change re-applies the rules
    render();
  };
  enabled.addEventListener('change', () => { sync(); if (enabled.checked && !projects.length) load(); });
  modeAll.addEventListener('change', sync); modePick.addEventListener('change', sync);
  folders.addEventListener('input', () => { clearTimeout(timer); timer = setTimeout(load, 500); });
  scan.addEventListener('change', load);
  body.append(
    h('label', { class: 'check' }, modeAll, 'All projects on this host (new ones are included automatically); untick projects to leave them out'),
    h('label', { class: 'check' }, modePick, 'Only the projects I choose'),
    h('div', {}, h('label', {}, 'Also include every project in these folders automatically, including ones created later (comma separated)'), folders),
    list,
    h('details', {}, h('summary', {}, 'More options'),
      h('label', {}, 'Also look for project directories without containers in'), scan,
      h('label', { class: 'check' }, skipDumps, 'Do not dump databases (Postgres, MySQL/MariaDB, MongoDB)'),
      h('label', { class: 'check' }, skipStandalone, 'Ignore containers that are not part of a Compose project'),
      (d.projects || []).concat(d.exclude_projects || []).some(isGlob)
        ? h('div', { class: 'meta' }, 'Patterns from the configuration are kept: ', [...(d.projects || []), ...(d.exclude_projects || [])].filter(isGlob).join(', ')) : null));
  const el = h('fieldset', {}, h('legend', {}, 'Docker'),
    h('label', { class: 'check' }, enabled, 'Back up Docker containers: each Compose project becomes its own snapshot (directory, bind mounts, volumes, database dumps)'),
    body);
  sync();
  if (enabled.checked) load();
  return {
    el,
    value() {
      if (!enabled.checked) return null;
      const keepGlobs = a => (a || []).filter(isGlob);
      const fs = modeAll.checked ? [] : csv(folders.value);
      const names = projects.map(p => p.name);
      const out = Object.assign({}, d, { folders: fs, scan_dirs: csv(scan.value), skip_dumps: skipDumps.checked, skip_standalone: skipStandalone.checked,
        projects: keepGlobs(d.projects), exclude_projects: keepGlobs(d.exclude_projects).concat((d.exclude_projects || []).filter(x => !isGlob(x) && !names.includes(x))),
        stop: keepGlobs(d.stop).concat((d.stop || []).filter(x => !isGlob(x) && !names.includes(x))) });
      if (!modeAll.checked) out.projects = out.projects.concat((d.projects || []).filter(x => !isGlob(x) && !names.includes(x)));
      for (const p of projects) {
        const on = included(p);
        const byRule = modeAll.checked ? !globMatch(out.exclude_projects, p.name) : !!inFolders(fs, p.dirs) || globMatch(out.projects, p.name);
        if (on && !byRule && !modeAll.checked) out.projects.push(p.name);
        if (!on && byRule) out.exclude_projects.push(p.name);
        const stop = p.name in stopped ? stopped[p.name] : globMatch(d.stop, p.name);
        if (stop && !globMatch(out.stop, p.name)) out.stop.push(p.name);
      }
      if (modePick.checked && !out.projects.length && !out.folders.length) throw new Error('Choose at least one project or folder, or select "All projects"');
      return out;
    },
  };
}

// ---------- repositories ----------
views.repos = async main => {
  const cfg = await loadConfig();
  main.append(h('h1', {}, 'Repositories'), h('div', { class: 'sub' }, 'Encrypted, deduplicated storage locations: local disks, network shares, SFTP or a Herakles server.'),
    h('div', { class: 'toolbar' }, h('button', { class: 'primary', onclick: () => editRepo(null) }, '+ Add repository')));
  const repos = cfg.repositories || [];
  const offers = await hubOffers(cfg);
  if (offers.length) {
    main.append(h('div', { class: 'notice info' }, icon('server'), h('span', {}, 'The hub\'s repositories ', h('b', {}, offers.map(r => r.name).join(', ')),
      ' can be chosen in the backup wizard and the job editor of this server. The hub passes a repository on, with its password, when a job of this server uses it.')));
  }
  if (!repos.length) { main.append(h('div', { class: 'empty' }, 'No repositories yet. Add one to start backing up' + (offers.length ? ', or use one of the hub\'s.' : '.'))); return; }
  const grid = h('div', { class: 'grid' });
  main.append(grid);
  for (const r of repos) {
    const statsBox = h('div', { class: 'meta' });
    const usageBox = h('div', { class: 'usage' }, h('div', { class: 'usage-bar loading' }), h('div', { class: 'meta' }, 'Measuring storage use…'));
    loadUsage(r.name, usageBox);
    const unlockWith = [r.password ? 'inline password' : r.password_file ? 'password file' : r.password_env ? 'env ' + r.password_env : r.password_command ? 'password command' : null, r.key_file ? 'key file' : null].filter(Boolean).join(' + ') || 'asks for password';
    const m = r.maintenance || {};
    grid.append(h('div', { class: 'card' },
      h('h3', {}, r.name,
        r.hub ? h('span', { class: 'chip', title: 'Passed on by the hub with its password; it changes it on every connection' }, 'from the hub') : null,
        (cfg.hub && cfg.hub.repositories || []).includes(r.name) ? h('span', { class: 'chip', title: 'Passed to every attached server (Servers page)' }, 'shared with servers') : null),
      h('div', { class: 'meta mono' }, r.location),
      h('div', { class: 'meta' }, 'Unlock: ', unlockWith, ' · compression: ', r.compression || 'auto'),
      m.prune_schedule || m.check_schedule ? h('div', { class: 'meta' }, 'Maintenance: ', [m.prune_schedule ? 'prune ' + m.prune_schedule : '', m.check_schedule ? 'check ' + m.check_schedule : ''].filter(Boolean).join(' · ')) : null,
      usageBox,
      statsBox,
      h('div', { class: 'actions' },
        h('button', { onclick: async () => {
          try {
            const s = await api('GET', '/api/repos/' + encodeURIComponent(r.name) + '/stats');
            const st = s.stats;
            clear(statsBox).append(st.snapshots + ' snapshots · ' + fmtBytes(st.raw_bytes) + ' unique data · ' + fmtBytes(st.stored_bytes) + ' stored (' + (st.stored_bytes ? (st.raw_bytes / st.stored_bytes).toFixed(2) : '-') + 'x compression)');
          } catch (e) { toast(e.message, true); }
        }, title: 'Show how much data the repository holds' }, 'Statistics'),
        h('button', { title: 'Set up a new, empty repository at this location', onclick: () => initRepo(r) }, 'Initialize'),
        h('button', { title: 'Verify that the stored backups are complete and readable', onclick: () => task(r.name, 'check') }, 'Check'),
        h('button', { title: 'Delete snapshots the retention rules no longer keep and free their space', onclick: async () => { if (await confirmDlg('Prune', 'Apply all retention policies of jobs using ' + r.name + ' and remove unreferenced data?')) task(r.name, 'prune'); } }, 'Prune'),
        h('span', { class: 'spacer' }),
        r.hub ? null : h('button', { class: 'small', title: 'Change location or password source', onclick: () => editRepo(r.name) }, 'Edit'),
        r.hub ? null : h('button', { class: 'small danger', title: 'Remove from the configuration; stored backups are kept', onclick: async () => {
          if (!await confirmDlg('Remove repository', 'Remove ' + r.name + ' from the configuration? Stored backups are NOT deleted.', true)) return;
          const c = await loadConfig(); c.repositories = c.repositories.filter(x => x.name !== r.name);
          try { await saveConfig(c); navigate('repos'); } catch (e) { toast(e.message, true); }
        } }, 'Remove'))));
  }
};

// loadUsage fills a repository card's storage bar: this repository, other
// data on the same storage, and free space (or the server's quota).
async function loadUsage(name, box, refresh) {
  let u;
  try { u = await api('GET', '/api/repos/' + encodeURIComponent(name) + '/usage' + (refresh ? '?refresh=1' : '')); }
  catch (e) { clear(box).append(h('div', { class: 'meta' }, 'Storage use unknown: ' + e.message)); return; }
  const again = h('a', { class: 'usage-refresh', title: 'Measure again', onclick: () => loadUsage(name, box, true) }, icon('refresh'));
  if (u.error && !u.files) { clear(box).append(h('div', { class: 'meta' }, 'Storage use: ', u.error, ' ', again)); return; }
  const sp = u.space;
  if (!sp || !sp.total) {
    clear(box).append(h('div', { class: 'meta' }, fmtBytes(u.bytes) + ' in ' + u.files + ' files · the storage does not report its capacity ', again));
    return;
  }
  const total = sp.total, free = Math.min(sp.free, total);
  const used = total - free;
  const mine = Math.min(u.bytes, used);
  const other = sp.quota ? 0 : used - mine;
  const pct = x => (100 * x / total).toFixed(2) + '%';
  const full = used / total;
  const seg = (cls, x, title) => x > 0 ? h('span', { class: cls, style: 'width:' + pct(x), title }) : null;
  const bar = h('div', { class: 'usage-bar' + (full > 0.9 ? ' crit' : full > 0.8 ? ' warn' : '') },
    seg('mine', mine, 'This repository: ' + fmtBytes(u.bytes)),
    seg('other', other, 'Other data on this storage: ' + fmtBytes(other)),
    seg('free', free, 'Free: ' + fmtBytes(free)));
  const parts = sp.quota
    ? [h('b', {}, fmtBytes(u.bytes)), ' of the ' + fmtBytes(total) + ' quota on the server · ' + fmtBytes(free) + ' left']
    : [h('span', { class: 'dot mine' }), 'this repository ', h('b', {}, fmtBytes(u.bytes)), ' · ', h('span', { class: 'dot other' }), 'other ' + fmtBytes(other),
      ' · ', h('span', { class: 'dot free' }), 'free ' + fmtBytes(free) + ' of ' + fmtBytes(total)];
  clear(box).append(bar, h('div', { class: 'meta' }, ...parts, ' (' + Math.round(full * 100) + ' % full) ', again));
}

async function task(repo, kind) {
  try { await api('POST', '/api/repos/' + encodeURIComponent(repo) + '/task/' + kind); toast(kind + ' of ' + repo + ' started — see Dashboard'); }
  catch (e) { toast(e.message, true); }
}

async function initRepo(r) {
  const hasSource = r.password || r.password_file || r.password_env || r.password_command || r.key_file;
  const pw = h('input', { type: 'password' }), pw2 = h('input', { type: 'password' });
  await modal('Initialize repository ' + r.name, [
    h('p', {}, 'Creates a new, empty, encrypted repository at ', h('span', { class: 'mono' }, r.location), '.'),
    hasSource ? h('p', { class: 'muted' }, 'The configured password source / key file will be used.') : [h('label', {}, 'New password'), pw, h('label', {}, 'Repeat password'), pw2],
    h('p', { style: 'color:var(--warn)' }, 'Without the password (and key file, if used) your backups cannot be recovered. Store it safely.')], [
    { label: 'Cancel' },
    { label: 'Create', class: 'primary', action: async () => {
      if (!hasSource && (!pw.value || pw.value !== pw2.value)) throw new Error('Passwords are empty or do not match');
      await api('POST', '/api/repos/' + encodeURIComponent(r.name) + '/init', { password: pw.value }, true);
      toast('Repository ' + r.name + ' initialized');
    } }], 'small');
}

async function editRepo(name) {
  const cfg = await loadConfig();
  const orig = name ? cfg.repositories.find(r => r.name === name) : null;
  const r = orig ? JSON.parse(JSON.stringify(orig)) : { compression: 'auto' };
  r.remote = r.remote || {}; r.maintenance = r.maintenance || {};
  const kind = h('select', { onchange: () => updHint() },
    ['local', 'smb', 'sftp', 'server'].map(k => h('option', { value: k }, { local: 'Local disk / mounted or UNC network share', smb: 'SMB/CIFS share (no mount needed)', sftp: 'SFTP server', server: 'Herakles repository server' }[k])));
  const loc = r.location || '';
  kind.value = loc.startsWith('smb://') ? 'smb' : loc.startsWith('sftp://') ? 'sftp' : (loc.startsWith('herakles') || loc.startsWith('rest:')) ? 'server' : 'local';
  const f = {
    name: h('input', { value: r.name || '' }), location: h('input', { value: loc }),
    pwMode: h('select', {}, h('option', { value: 'prompt' }, 'Ask when needed'), h('option', { value: 'file' }, 'Password file'), h('option', { value: 'env' }, 'Environment variable'), h('option', { value: 'command' }, 'Command output'), h('option', { value: 'inline' }, 'Stored in config (not recommended)')),
    pwValue: h('input', {}), keyFile: h('input', { value: r.key_file || '', placeholder: 'optional; generate with: herakles key generate PATH' }),
    compression: h('select', {}, ['auto', 'none', 'zstd:1', 'zstd:2', 'zstd:3', 'zstd:4', 'auto,zstd:3'].map(c => h('option', { value: c, selected: (r.compression || 'auto') === c }, c))),
    user: h('input', { value: r.remote.username || '' }), rpass: h('input', { type: 'password', value: r.remote.password || '' }),
    rpassEnv: h('input', { value: r.remote.password_env || '' }), domain: h('input', { value: r.remote.domain || '' }),
    identity: h('input', { value: r.remote.identity_file || '' }), caCert: h('input', { value: r.remote.ca_cert || '' }),
    insecure: h('input', { type: 'checkbox', checked: r.remote.insecure_skip_verify }),
    up: h('input', { type: 'number', min: 0, value: r.upload_limit_kib || '' }), down: h('input', { type: 'number', min: 0, value: r.download_limit_kib || '' }),
    pruneSched: h('input', { value: r.maintenance.prune_schedule || '', placeholder: 'e.g. 30 4 * * 0' }),
    checkSched: h('input', { value: r.maintenance.check_schedule || '', placeholder: 'e.g. 0 5 1 * *' }),
    checkRead: h('input', { value: r.maintenance.check_read_data || '', placeholder: 'e.g. 10%' }),
    repack: h('input', { type: 'number', step: '0.05', min: 0, max: 1, value: r.maintenance.repack_threshold || '' }),
  };
  if (r.password_file) { f.pwMode.value = 'file'; f.pwValue.value = r.password_file; }
  else if (r.password_env) { f.pwMode.value = 'env'; f.pwValue.value = r.password_env; }
  else if (r.password_command) { f.pwMode.value = 'command'; f.pwValue.value = r.password_command; }
  else if (r.password) { f.pwMode.value = 'inline'; f.pwValue.value = r.password; f.pwValue.type = 'password'; }
  f.pwMode.addEventListener('change', () => { f.pwValue.type = f.pwMode.value === 'inline' ? 'password' : 'text'; f.pwValue.disabled = f.pwMode.value === 'prompt'; });
  f.pwValue.disabled = f.pwMode.value === 'prompt';
  const hint = h('div', { class: 'hint' });
  const remoteBox = h('fieldset', {}, h('legend', {}, 'Connection'),
    h('div', { class: 'row' }, h('div', {}, h('label', {}, 'User name'), f.user), h('div', {}, h('label', {}, 'Domain (SMB)'), f.domain)),
    h('div', { class: 'row' }, h('div', {}, h('label', {}, 'Password'), f.rpass), h('div', {}, h('label', {}, '…or password from env var'), f.rpassEnv)),
    h('div', { class: 'row' }, h('div', {}, h('label', {}, 'SSH identity file (SFTP)'), f.identity), h('div', {}, h('label', {}, 'CA certificate (server)'), f.caCert)),
    h('label', { class: 'check' }, f.insecure, 'Skip TLS/host key verification (testing only)'),
    h('div', { class: 'row' }, h('div', {}, h('label', {}, 'Upload limit KiB/s'), f.up), h('div', {}, h('label', {}, 'Download limit KiB/s'), f.down)));
  const updHint = () => {
    hint.textContent = {
      local: 'Examples: /mnt/backup/herakles · D:\\Backups\\herakles · \\\\nas\\backup\\herakles · /mnt/nfs/herakles',
      smb: 'Example: smb://WORKGROUP;user@nas.local/backups/herakles',
      sftp: 'Example: sftp://backup@host.example.com/srv/herakles',
      server: 'Example: herakles://backup.example.com:8443/alice-laptop (herakles+http:// for plain HTTP)',
    }[kind.value];
    remoteBox.style.display = kind.value === 'local' ? 'none' : '';
  };
  updHint();
  await modal(orig ? 'Edit repository ' + orig.name : 'Add repository', [
    h('div', { class: 'row' }, h('div', {}, h('label', {}, 'Name'), f.name), h('div', {}, h('label', {}, 'Type'), kind)),
    h('label', {}, 'Location'), f.location, hint,
    h('fieldset', {}, h('legend', {}, 'Encryption (AES-256)'),
      h('div', { class: 'row' }, h('div', {}, h('label', {}, 'Password source'), f.pwMode), h('div', {}, h('label', {}, 'Path / variable / command / password'), f.pwValue)),
      h('label', {}, 'Key file (used in addition to, or instead of, the password)'), f.keyFile,
      h('label', {}, 'Compression'), f.compression),
    remoteBox,
    h('fieldset', {}, h('legend', {}, 'Maintenance (run by the daemon)'),
      h('div', { class: 'row' }, h('div', {}, h('label', {}, 'Prune schedule (cron)'), f.pruneSched), h('div', {}, h('label', {}, 'Repack threshold (0-1)'), f.repack)),
      h('div', { class: 'row' }, h('div', {}, h('label', {}, 'Check schedule (cron)'), f.checkSched), h('div', {}, h('label', {}, 'Check reads data'), f.checkRead)))], [
    { label: 'Cancel' },
    { label: 'Save', class: 'primary', action: async () => {
      const nr = Object.assign({}, r, { name: f.name.value.trim(), location: f.location.value.trim(), key_file: f.keyFile.value.trim(), compression: f.compression.value === 'auto' ? '' : f.compression.value,
        password: '', password_file: '', password_env: '', password_command: '',
        upload_limit_kib: num(f.up), download_limit_kib: num(f.down) });
      const pv = f.pwValue.value.trim();
      if (f.pwMode.value === 'file') nr.password_file = pv;
      if (f.pwMode.value === 'env') nr.password_env = pv;
      if (f.pwMode.value === 'command') nr.password_command = pv;
      if (f.pwMode.value === 'inline') nr.password = f.pwValue.value;
      nr.remote = { username: f.user.value.trim(), password: f.rpass.value, password_env: f.rpassEnv.value.trim(), domain: f.domain.value.trim(),
        identity_file: f.identity.value.trim(), ca_cert: f.caCert.value.trim(), insecure_skip_verify: f.insecure.checked };
      nr.maintenance = { prune_schedule: f.pruneSched.value.trim(), check_schedule: f.checkSched.value.trim(), check_read_data: f.checkRead.value.trim(), repack_threshold: parseFloat(f.repack.value) || 0 };
      const c = await loadConfig();
      c.repositories = c.repositories || [];
      const i = orig ? c.repositories.findIndex(x => x.name === orig.name) : -1;
      if (i >= 0) {
        c.repositories[i] = nr;
        (c.jobs || []).forEach(j => { if (j.repository === orig.name) j.repository = nr.name; });
      } else c.repositories.push(nr);
      await saveConfig(c);
      navigate('repos');
      if (!orig && await confirmDlg('Initialize?', 'Create a new repository at ' + nr.location + ' now? Choose Cancel if it already exists.')) initRepo(nr);
    } }]);
}

// ---------- settings (YAML) ----------
// hubCard attaches this computer to a hub (enrollment code) or detaches it.
function hubCard() {
  const info = state.info || {};
  if (state.target) return '';
  const card = h('div', { class: 'card', style: 'margin-bottom:16px' }, h('h3', {}, 'Central management'));
  if (!info.can_attach) {
    card.append(h('div', {}, 'A Herakles hub can manage this computer: its backups, repositories and restores, from one web interface.'),
      h('div', { class: 'meta' }, 'Only the Herakles service connects to a hub. Let the service run the backups (',
        h('span', { class: 'mono' }, info.os === 'windows' ? 'herakles daemon desktop on' : 'sudo herakles daemon desktop on'),
        info.os === 'windows' ? ' as administrator, or the setup\'s “Whole computer” option' : '', '), then attach it here.'));
    return card;
  }
  const a = info.agent;
  if (a) {
    card.append(h('div', {}, h('span', { class: 'badge ' + (a.connected ? 'success' : 'warning') }, a.connected ? 'connected' : 'not connected'),
      ' Managed by ', h('b', {}, a.hub), a.name ? ' as ' : '', a.name ? h('b', {}, a.name) : null),
      a.error ? h('div', { class: 'meta', style: 'color:var(--warn)' }, a.error) : '',
      h('div', { class: 'meta' }, 'The hub\'s administrators can see and change this computer\'s backups and configuration, and restore files to it.'),
      isAdmin() ? h('div', { class: 'actions' }, h('button', { class: 'danger', onclick: async () => {
        if (!await confirmDlg('Leave the hub', 'Detach this computer from ' + a.hub + '? Its backups keep running here. Repositories the hub passed on stay configured where jobs use them. Remove the computer on the hub\'s Servers page too.', true)) return;
        try {
          const r = await api('POST', '/api/hub/leave');
          toast(r.kept && r.kept.length ? 'Detached; kept the hub\'s repositories ' + r.kept.join(', ') : 'Detached from the hub');
          await loadInfo(); navigate('settings');
        } catch (e) { toast(e.message, true); }
      } }, 'Leave the hub')) : '');
    return card;
  }
  const code = h('input', { placeholder: 'HKE1-…', spellcheck: 'false', autocomplete: 'off', class: 'mono' });
  const name = h('input', { placeholder: info.hostname || '' });
  const btn = h('button', { class: 'primary', onclick: async () => {
    btn.disabled = true;
    try {
      const r = await api('POST', '/api/hub/join', { code: code.value.trim(), name: name.value.trim() });
      toast('Attached to ' + r.hub + ' as ' + r.name);
      for (let i = 0; i < 20; i++) { await new Promise(res => setTimeout(res, 500)); await loadInfo(); if (state.info.agent && state.info.agent.connected) break; }
      navigate('settings');
    } catch (e) { toast(e.message, true); btn.disabled = false; }
  } }, 'Attach to the hub');
  card.append(h('div', {}, 'Let a Herakles hub manage this computer: backups, repositories and restores from one web interface. The hub passes on its repositories, so this computer needs no repository passwords of its own.'),
    h('div', { class: 'row' }, h('div', {}, h('label', {}, 'Enrollment code (hub: Servers → Enroll a server)'), code),
      h('div', {}, h('label', {}, 'Name on the hub (optional)'), name)),
    h('div', { class: 'meta' }, 'The code is valid once. This computer connects out to the hub, so it needs no open port. The hub\'s administrators get full control over its backups.'),
    isAdmin() ? h('div', { class: 'actions' }, btn) : '');
  return card;
}

views.settings = async main => {
  const data = await api('GET', '/api/config/yaml');
  const cfg = await loadConfig();
  const ta = h('textarea', { rows: 32, spellcheck: 'false' }, data.yaml);
  const autostart = h('input', { type: 'checkbox', checked: !!(cfg.tray && cfg.tray.autostart), onchange: async e => {
    try {
      const c = await loadConfig();
      c.tray = Object.assign({}, c.tray, { autostart: e.target.checked });
      await saveConfig(c);
      ta.value = (await api('GET', '/api/config/yaml')).yaml;
    } catch (err) { e.target.checked = !e.target.checked; toast(err.message, true); }
  } });
  main.append(h('h1', {}, 'Configuration'),
    state.info && state.info.mode === 'web' ? '' : state.info && state.info.service
      ? h('div', { class: 'card', style: 'margin-bottom:16px' }, h('h3', {}, 'Desktop'),
        h('div', {}, 'This window belongs to the Herakles system service: it runs as ' + (state.info.os === 'windows' ? 'LocalSystem' : 'root') + ', so it can back up whole disks and every file, also when nobody is logged in.'),
        h('div', { class: 'meta' }, 'Shared with: ' + (state.info.service.users.join(', ') || 'nobody') + ' (gui.users). Change with ', h('span', { class: 'mono' }, state.info.os === 'windows' ? 'herakles daemon desktop on|off --user NAME' : 'sudo herakles daemon desktop on|off --user NAME'), state.info.os === 'windows' ? ' as administrator.' : '.'))
      : h('div', { class: 'card', style: 'margin-bottom:16px' }, h('h3', {}, 'Desktop'),
        h('label', { class: 'check' }, autostart, 'Start Herakles in the system tray when I log in'),
        h('div', { class: 'meta' }, 'Runs scheduled backups and this interface in the background (tray.autostart). Use either this or the daemon service. To back up whole disks or system files, let the system service run them: ', h('span', { class: 'mono' }, 'sudo herakles daemon desktop on'), '.')),
    hubCard(),
    h('div', { class: 'sub' }, 'Full YAML configuration (', h('span', { class: 'mono' }, data.path), '). Comments are preserved when saving here. Everything — notifications, daemon and server settings — can be edited.'),
    h('div', { class: 'toolbar' },
      h('button', { class: 'primary', onclick: async () => { try { await api('PUT', '/api/config/yaml', { yaml: ta.value }); toast('Saved and applied'); } catch (e) { toast(e.message, true); } } }, 'Validate & save'),
      h('button', { onclick: async () => { const d = await api('GET', '/api/config/yaml'); ta.value = d.yaml; toast('Reverted'); } }, 'Revert'),
      h('span', { class: 'spacer' }),
      h('button', { onclick: async () => { const e = await api('GET', '/api/config/example'); await modal('Example configuration', h('textarea', { rows: 30, readonly: true }, e.yaml), [{ label: 'Close' }]); } }, 'Show annotated example')),
    ta);
  ta.addEventListener('keydown', e => {
    if (e.key === 'Tab') { e.preventDefault(); const s = ta.selectionStart; ta.setRangeText('  ', s, ta.selectionEnd, 'end'); }
    if ((e.ctrlKey || e.metaKey) && e.key === 's') { e.preventDefault(); main.querySelector('.toolbar .primary').click(); }
  });
};

// ---------- logs ----------
views.logs = async main => {
  main.append(h('h1', {}, 'Activity Log'), h('div', { class: 'sub' }, 'Backup history and messages of this session.'));
  const histBox = h('div'), logBox = h('div');
  main.append(h('h2', {}, 'Backup history'), histBox, h('h2', {}, 'Session log'), logBox);
  const render = async () => {
    const [hist, logs] = await Promise.all([api('GET', '/api/history'), api('GET', '/api/logs')]);
    clear(histBox).append(hist.length ? h('table', {}, h('tr', {}, ['Started', 'Job', 'Type', 'Trigger', 'Status', 'Duration', 'Files', 'New data', 'Snapshot / error'].map(t => h('th', {}, t))),
      hist.slice(0, 200).map(r => h('tr', {}, h('td', {}, fmtTime(r.start)), h('td', {}, r.job), h('td', {}, r.type), h('td', {}, r.trigger || '-'),
        h('td', {}, badge(r.status)), h('td', {}, fmtDur((new Date(r.end) - new Date(r.start)) * 1e6)),
        h('td', {}, r.summary ? r.summary.total_files : '-'), h('td', {}, r.summary ? fmtBytes(r.summary.bytes_added) : '-'),
        h('td', { class: 'wrap' }, r.error || (r.snapshot ? r.snapshot.slice(0, 8) : '')))))
      : h('div', { class: 'muted' }, 'No backups recorded yet.'));
    clear(logBox).append(logs.length ? h('table', {}, logs.slice(-300).reverse().map(l => h('tr', {},
      h('td', {}, fmtTime(l.time)), h('td', { class: 'log-' + l.level }, l.level), h('td', {}, l.message), h('td', { class: 'wrap mono muted' }, l.attrs))))
      : h('div', { class: 'muted' }, 'No messages yet.'));
  };
  await render();
  state.timer = setInterval(() => render().catch(() => {}), 3000);
};

// ---------- backup wizard ----------
const WIZ_STEPS = [
  { id: 'what', title: 'What', icon: 'sparkle' }, { id: 'sources', title: 'Sources', icon: 'folder' },
  { id: 'where', title: 'Where', icon: 'repos' }, { id: 'when', title: 'When', icon: 'clock' },
  { id: 'keep', title: 'How long', icon: 'retention' }, { id: 'finish', title: 'Finish', icon: 'done' }];

function tile(iconName, title, desc, selected, onclick) {
  return h('button', { type: 'button', class: 'tile' + (selected ? ' sel' : ''), 'data-ico': '1', onclick }, icon(iconName), h('b', {}, title), desc ? h('span', { class: 'd' }, desc) : null);
}
function genPassword() {
  const abc = 'ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz23456789';
  const r = crypto.getRandomValues(new Uint32Array(24));
  return Array.from(r, x => abc[x % abc.length]).join('').replace(/(.{6})(?!$)/g, '$1-');
}

views.wizard = async main => {
  const [info, cfg] = await Promise.all([api('GET', '/api/wizard/info'), loadConfig()]);
  const repos = (cfg.repositories || []).concat(await hubOffers(cfg));
  const jobs = cfg.jobs || [];
  const win = info.os === 'windows';
  const wz = {
    step: 0, kind: '', paths: '', excl: { caches: true, build: false, downloads: false }, oneFS: false, customExcl: '', device: '',
    repoMode: repos.length ? 'existing' : 'new', repo: repos.length ? repos[0].name : '', type: 'local',
    loc: { path: '', host: '', share: '', dir: 'herakles', user: '', pass: '', identity: '', url: '', srvRepo: info.hostname || 'backup', ca: '' },
    repoName: '', pwMode: 'generate', password: genPassword(), pw1: '', pw2: '', saved: false, maintenance: true,
    schedule: 'nightly', time: '02:00', keep: 'standard', name: '', description: '', runNow: true, autostart: false, dock: null,
  };
  main.append(h('h1', {}, 'New backup'), h('div', { class: 'sub' }, 'This wizard sets up a complete backup job step by step: what to back up, where to, when, and for how long.'));
  const stepsEl = h('div', { class: 'wiz-steps' });
  const card = h('div', { class: 'wiz-card' });
  main.append(stepsEl, card);
  const uniqueName = base => { let n = base, i = 2; while (jobs.some(j => j.name === n) || repos.some(r => r.name === n && base !== n)) n = base + '-' + i++; return n; };
  const uniqueRepo = base => { let n = base, i = 2; while (repos.some(r => r.name === n)) n = base + '-' + i++; return n; };

  const input = (key, attrs, obj) => { obj = obj || wz; const el = h('input', Object.assign({ value: obj[key] || '' }, attrs || {})); el.addEventListener('input', () => { obj[key] = el.type === 'checkbox' ? el.checked : el.value; }); return el; };
  const check = (key, label, obj) => { obj = obj || wz; const el = h('input', { type: 'checkbox', checked: !!obj[key], onchange: e => { obj[key] = e.target.checked; } }); return h('label', { class: 'check' }, el, label); };

  const steps = {
    what: () => [h('h2', {}, icon('sparkle'), 'What do you want to back up?'), h('div', { class: 'sub' }, 'You can add more jobs later; every job has its own schedule.'),
      h('div', { class: 'tiles' },
        tile('folder', 'Files & folders', 'Documents, home directories, server data. Permissions and timestamps are kept.', wz.kind === 'files', () => pick('files')),
        tile('containers', 'Docker containers', info.docker ? info.docker_projects + ' projects found on this host. Each one is backed up and restored on its own, databases included.' : 'Docker Compose projects with volumes and database dumps. (Docker was not found on this computer.)', wz.kind === 'docker', () => pick('docker')),
        info.container ? tile('server', 'This whole container', 'Every file of this container (' + info.container + '), to restore it completely. This is the full backup of a container.', wz.kind === 'system', () => pick('system')) : null,
        tile('disk', 'A whole disk or partition', info.container ? 'Only for disks the host passes into this container: a container cannot read the host\'s disks.' : 'An image for disaster recovery that can be written back to a disk.', wz.kind === 'disk', () => pick('disk')))],

    sources: () => {
      if (wz.kind === 'docker') {
        if (!wz.dock) wz.dock = dockerPicker({ name: '', docker: {} });
        return [h('h2', {}, icon('containers'), 'Which containers?'),
          h('div', { class: 'sub' }, 'Choose all projects, pick them one by one, or name folders (such as /srv) whose projects — including future ones — are backed up automatically. Files in those folders that belong to no project are backed up too.'),
          wz.dock.el];
      }
      if (wz.kind === 'system') {
        return [h('h2', {}, icon('server'), 'The whole container'),
          h('div', { class: 'sub' }, 'Backs up / with everything in it, so the container can be restored completely: into a new container or onto its own volume.'),
          h('ul', { class: 'plain-list' },
            h('li', {}, 'Left out by themselves: /proc, /sys, /dev, /run and other kernel and memory file systems (their mount points are kept).'),
            h('li', {}, 'Also left out: the contents of /tmp and /var/tmp, package caches and folders marked with CACHEDIR.TAG.'),
            h('li', {}, 'Databases are best added as dumps afterwards (Backup Jobs → Edit → Databases): a running database\'s files may be inconsistent.')),
          check('oneFS', 'Only the container\'s own file system (leave out volumes mounted into it)')];
      }
      if (wz.kind === 'disk') {
        return [h('h2', {}, icon('disk'), 'Which disk or partition?'),
          h('div', { class: 'sub' }, 'The device is read block by block; unused space deduplicates to almost nothing. For a consistent image, back up a filesystem that is not being written to (or a snapshot of it).'),
          devicePicker({ value: [wz.device], onChange: v => { wz.device = v[0] || ''; } }),
          h('p', { class: 'muted' }, win ? 'Reading a device needs an administrator account.' : 'Pick a whole disk for a complete image (partition table, boot loader, all partitions) or a single partition.')];
      }
      const ta = h('textarea', { rows: 4, placeholder: win ? 'C:\\Users\\me\\Documents' : '/home/me\n/etc' }, wz.paths);
      ta.addEventListener('input', () => { wz.paths = ta.value; });
      const add = p => { const cur = ta.value.split('\n').map(x => x.trim()).filter(Boolean); if (!cur.includes(p)) cur.push(p); ta.value = wz.paths = cur.join('\n'); };
      const ex = h('textarea', { rows: 2, placeholder: '*.iso\n**/big-files' }, wz.customExcl);
      ex.addEventListener('input', () => { wz.customExcl = ex.value; });
      return [h('h2', {}, icon('folder'), 'Which files and folders?'), h('div', { class: 'sub' }, 'One folder or file per line. Everything below a folder is included.'),
        h('div', { class: 'chips-pick' }, (info.suggested_paths || []).map(p => h('button', { type: 'button', onclick: () => add(p) }, '+ ' + p))), ta,
        h('label', {}, 'Leave out'),
        check('caches', 'Caches and temporary files (.cache, *.tmp, folders marked with CACHEDIR.TAG)', wz.excl),
        check('build', 'Build and dependency folders (node_modules, target, .venv, __pycache__)', wz.excl),
        check('downloads', 'Downloads and trash', wz.excl),
        check('oneFS', 'Stay on one file system (skip mounted disks and network shares below the folders)'),
        h('label', {}, 'More patterns to leave out (optional, one per line)'), ex];
    },

    where: () => {
      const typeFields = () => {
        const L = wz.loc;
        switch (wz.type) {
          case 'local': return [h('label', {}, 'Folder for the backups'), input('path', { placeholder: win ? 'D:\\Backups\\herakles' : '/mnt/usb-disk/herakles' }, L),
            h('p', { class: 'muted' }, 'A folder on another disk: an external USB disk, a second internal disk, or a mounted network share (NFS, SMB). The folder is created if needed.')];
          case 'smb': return [h('div', { class: 'row' }, h('div', {}, h('label', {}, 'NAS / server'), input('host', { placeholder: 'nas.local' }, L)), h('div', {}, h('label', {}, 'Share'), input('share', { placeholder: 'backups' }, L))),
            h('div', { class: 'row' }, h('div', {}, h('label', {}, 'Folder in the share'), input('dir', {}, L)), h('div', {}, h('label', {}, 'User name'), input('user', { placeholder: 'backup' }, L))),
            h('label', {}, 'Password of the share account'), input('pass', { type: 'password', autocomplete: 'new-password' }, L),
            h('p', { class: 'muted' }, 'Herakles talks SMB itself — no need to mount the share.')];
          case 'sftp': return [h('div', { class: 'row' }, h('div', {}, h('label', {}, 'Server'), input('host', { placeholder: 'backup.example.com' }, L)), h('div', {}, h('label', {}, 'User name'), input('user', { placeholder: 'backup' }, L))),
            h('label', {}, 'Folder on the server'), input('dir', { placeholder: '/srv/herakles' }, L),
            h('label', {}, 'SSH key (optional; ssh-agent and ~/.ssh keys are used otherwise)'), input('identity', { placeholder: '~/.ssh/id_ed25519' }, L)];
          case 'server': return [h('div', { class: 'row' }, h('div', {}, h('label', {}, 'Herakles server'), input('url', { placeholder: 'backup.example.com:8443' }, L)), h('div', {}, h('label', {}, 'Repository name on the server'), input('srvRepo', {}, L))),
            h('div', { class: 'row' }, h('div', {}, h('label', {}, 'Account'), input('user', {}, L)), h('div', {}, h('label', {}, 'Password of the account'), input('pass', { type: 'password', autocomplete: 'new-password' }, L))),
            h('label', {}, 'Server certificate (for self-signed certificates, optional)'), input('ca', { placeholder: '/etc/herakles/server.crt' }, L)];
        }
      };
      const pwPart = () => {
        const modes = h('div', { class: 'tiles' },
          tile('sparkle', 'Generate a strong password', 'Stored on this computer so scheduled backups run unattended. You keep a copy.', wz.pwMode === 'generate', () => { wz.pwMode = 'generate'; render(); }),
          tile('key', 'Choose a password', 'Also stored on this computer for unattended backups.', wz.pwMode === 'enter', () => { wz.pwMode = 'enter'; render(); }),
          tile('lock', 'Ask for it every time', 'Not stored. Scheduled backups only run while you have unlocked the repository in the open app.', wz.pwMode === 'ask', () => { wz.pwMode = 'ask'; render(); }));
        const out = [h('h3', {}, 'Encryption password'), h('div', { class: 'muted' }, 'Everything is encrypted with AES-256. Without this password nobody — including you — can read the backups.'), modes];
        if (wz.pwMode === 'generate') {
          out.push(h('div', { class: 'pw-box' }, wz.password),
            h('div', { class: 'actions', style: 'margin-top:8px' }, h('button', { type: 'button', class: 'small', onclick: async () => { try { await navigator.clipboard.writeText(wz.password); toast('Copied'); } catch (e) { toast('Select the password and copy it', true); } } }, 'Copy'),
              h('button', { type: 'button', class: 'small', onclick: () => { wz.password = genPassword(); wz.saved = false; render(); } }, 'New password')),
            check('saved', 'I have saved this password in a safe place (password manager, paper)'));
        } else {
          out.push(h('div', { class: 'row' }, h('div', {}, h('label', {}, 'Password (at least 10 characters)'), input('pw1', { type: 'password', autocomplete: 'new-password' })),
            h('div', {}, h('label', {}, 'Repeat'), input('pw2', { type: 'password', autocomplete: 'new-password' }))));
        }
        out.push(check('maintenance', 'Keep the repository tidy: free unused space every Sunday and check its integrity every month'));
        return out;
      };
      const out = [h('h2', {}, icon('repos'), 'Where should the backups go?'), h('div', { class: 'sub' }, 'Backups are stored in a repository: deduplicated, compressed and encrypted.')];
      out.push(h('div', { class: 'tiles' },
        repos.map(r => tile(r.offer ? 'server' : r.location.startsWith('smb://') ? 'share' : r.location.startsWith('sftp://') ? 'terminal' : r.location.startsWith('herakles') ? 'server' : 'usb',
          r.name, r.offer ? r.location + ' — from the hub' : r.location, wz.repoMode === 'existing' && wz.repo === r.name, () => { wz.repoMode = 'existing'; wz.repo = r.name; render(); })),
        tile('plus', 'New location', 'A disk, a NAS, an SFTP server or a Herakles backup server.', wz.repoMode === 'new', () => { wz.repoMode = 'new'; render(); })));
      const offer = wz.repoMode === 'existing' && repos.find(r => r.offer && r.name === wz.repo);
      if (offer) out.push(h('div', { class: 'notice info' }, icon('server'), 'This repository is on the hub. When you create the job, the hub passes it to its attached servers, with its password: they need no credentials of their own. Pruning and checks run on the hub.'));
      if (wz.repoMode === 'new') {
        out.push(h('h3', {}, 'Kind of storage'), h('div', { class: 'tiles' },
          tile('usb', 'Local or USB disk', 'Also mounted network shares.', wz.type === 'local', () => { wz.type = 'local'; render(); }),
          tile('share', 'Network share (SMB)', 'A NAS or Windows share, without mounting.', wz.type === 'smb', () => { wz.type = 'smb'; render(); }),
          tile('terminal', 'SFTP server', 'Any server you can reach with SSH.', wz.type === 'sftp', () => { wz.type = 'sftp'; render(); }),
          tile('server', 'Herakles server', 'A repository server, optionally append-only.', wz.type === 'server', () => { wz.type = 'server'; render(); })),
          typeFields(), pwPart());
      }
      return out;
    },

    when: () => {
      const t = input('time', { type: 'time', style: 'width:140px' });
      return [h('h2', {}, icon('clock'), 'When should it run?'), h('div', { class: 'sub' }, 'Every backup is complete and restorable on its own; only changed data is stored again.'),
        h('div', { class: 'tiles' },
          tile('calendar', 'Every night', 'Quick incremental backups, a full one every 7 days.', wz.schedule === 'nightly', () => { wz.schedule = 'nightly'; render(); }),
          tile('full', 'Weekly full, daily incremental', 'Full backup on Sundays, incremental Monday to Saturday.', wz.schedule === 'weekly', () => { wz.schedule = 'weekly'; render(); }),
          tile('refresh', 'Every 4 hours', 'For data that changes during the day.', wz.schedule === 'hourly', () => { wz.schedule = 'hourly'; render(); }),
          tile('play', 'Only when I start it', 'No schedule.', wz.schedule === 'manual', () => { wz.schedule = 'manual'; render(); })),
        wz.schedule === 'nightly' || wz.schedule === 'weekly' ? [h('label', {}, 'Time'), t] : null,
        wz.schedule !== 'manual' ? h('p', { class: 'muted' }, 'Missed backups (computer switched off) run when Herakles starts again. Scheduled backups need Herakles running in the background: the tray icon or the system service.') : null];
    },

    keep: () => [h('h2', {}, icon('retention'), 'How long should backups be kept?'), h('div', { class: 'sub' }, 'Older backups are thinned out automatically after every run. The newest backup is never removed.'),
      h('div', { class: 'tiles' },
        tile('clock', 'One week', 'The last 7 days.', wz.keep === 'week', () => { wz.keep = 'week'; render(); }),
        tile('calendar', 'Standard', '7 daily, 4 weekly and 12 monthly backups — a year back.', wz.keep === 'standard', () => { wz.keep = 'standard'; render(); }),
        tile('archive', 'Long', '14 daily, 8 weekly, 24 monthly and 5 yearly backups.', wz.keep === 'long', () => { wz.keep = 'long'; render(); }),
        tile('shield', 'Keep everything', 'Nothing is ever removed automatically.', wz.keep === 'all', () => { wz.keep = 'all'; render(); }))],

    finish: () => {
      if (!wz.name) wz.name = uniqueName({ files: 'files', docker: 'containers', disk: 'disk', system: 'container' }[wz.kind] || 'backup');
      const job = buildJob();
      const repoLabel = wz.repoMode === 'existing' ? wz.repo + ' (' + (repos.find(r => r.name === wz.repo) || {}).location + ')' : repoDraft().name + ' — ' + repoDraft().location + ' (new)';
      const row = (ic, k, v) => h('div', { class: 'summary-row' }, icon(ic), h('div', {}, h('div', { class: 'k' }, k), h('div', {}, v)));
      return [h('h2', {}, icon('done'), 'Check and create'),
        h('div', { class: 'row' }, h('div', {}, h('label', {}, 'Name of the job'), input('name')), h('div', {}, h('label', {}, 'Description (optional)'), input('description'))),
        h('div', { style: 'margin-top:14px' },
          row(wz.kind === 'docker' ? 'containers' : wz.kind === 'disk' ? 'disk' : wz.kind === 'system' ? 'server' : 'folder', 'Backs up', describeSources(job)),
          row('repos', 'Stores in', repoLabel),
          wz.repoMode === 'new' ? row('lock', 'Encryption', wz.pwMode === 'ask' ? 'Password asked for each session' : 'Password stored in a file next to the configuration') : null,
          row('clock', 'Runs', { nightly: 'every night at ' + wz.time + ', full every 7 days', weekly: 'Sundays full, Monday to Saturday incremental, at ' + wz.time, hourly: 'every 4 hours, full every 7 days', manual: 'only when started' }[wz.schedule]),
          row('retention', 'Keeps', { week: '7 daily backups', standard: '7 daily, 4 weekly, 12 monthly', long: '14 daily, 8 weekly, 24 monthly, 5 yearly', all: 'everything' }[wz.keep])),
        check('runNow', 'Start the first backup right away'),
        state.info && state.info.mode !== 'web' && !state.info.service && wz.schedule !== 'manual' && !(cfg.tray && cfg.tray.autostart) ? check('autostart', 'Run scheduled backups in the background: start Herakles in the system tray when I log in') : null];
    },
  };

  function describeSources(job) {
    if (job.docker) {
      const d = job.docker;
      const parts = [];
      if (!(d.projects || []).length && !(d.folders || []).length) parts.push('all Docker projects on this host');
      if ((d.folders || []).length) parts.push('every project in ' + d.folders.join(', '));
      if ((d.projects || []).length) parts.push('projects ' + d.projects.join(', '));
      if ((d.exclude_projects || []).length) parts.push('except ' + d.exclude_projects.join(', '));
      if ((d.stop || []).length) parts.push(d.stop.join(', ') + ' stopped during backup');
      return parts.join('; ');
    }
    if (job.devices && job.devices.length) return job.devices.join(', ');
    if (job.paths.length === 1 && job.paths[0] === '/') return 'the whole container (/)' + (job.one_file_system ? ', without mounted volumes' : '');
    return job.paths.join(', ') + (job.exclude && job.exclude.length ? ' (leaving out ' + job.exclude.length + ' patterns)' : '');
  }

  // "backup/", "/backup//herakles", "\\nas\backup" → clean segments without doubled slashes.
  const cleanPath = p => (p || '').trim().split(/[\\/]+/).filter(x => x && x !== '.').join('/');
  const hostOf = x => (x || '').trim().replace(/^[a-z]+:\/\//i, '').replace(/^[\\/]+|[\\/]+$/g, '');

  function repoDraft() {
    const L = wz.loc;
    const base = { local: 'local', smb: 'nas', sftp: 'sftp', server: 'backup-server' }[wz.type];
    const name = wz.repoName || uniqueRepo(base);
    const r = { name, compression: '' };
    if (wz.type === 'local') r.location = L.path.trim();
    if (wz.type === 'smb') { r.location = 'smb://' + (L.user ? encodeURIComponent(L.user) + '@' : '') + hostOf(L.host) + '/' + cleanPath(L.share) + (cleanPath(L.dir) ? '/' + cleanPath(L.dir) : ''); r.remote = { password: L.pass }; }
    if (wz.type === 'sftp') { r.location = 'sftp://' + (L.user ? L.user + '@' : '') + hostOf(L.host) + '/' + cleanPath(L.dir); if (L.identity) r.remote = { identity_file: L.identity }; }
    if (wz.type === 'server') { r.location = 'herakles://' + L.url.trim().replace(/^https?:\/\//, '').replace(/\/+$/, '') + '/' + L.srvRepo.trim(); r.remote = { username: L.user, password: L.pass, ca_cert: L.ca }; }
    if (wz.maintenance) r.maintenance = { prune_schedule: '30 4 * * 0', repack_threshold: 0.2, check_schedule: '0 5 1 * *', check_read_data: '5%' };
    return r;
  }

  const SYSTEM_EXCLUDES = ['/tmp/*', '/var/tmp/*', '/var/cache/apt/archives/*.deb', '/var/cache/dnf', '/var/cache/pacman/pkg', '/swapfile', '/swap.img'];

  function buildJob() {
    const [hh, mm] = (wz.time || '02:00').split(':').map(Number);
    const at = (mm || 0) + ' ' + (hh || 0);
    const job = { name: wz.name.trim(), description: wz.description, repository: wz.repoMode === 'existing' ? wz.repo : repoDraft().name, catch_up: true };
    if (wz.kind === 'files') {
      job.paths = wz.paths.split('\n').map(x => x.trim()).filter(Boolean);
      const ex = [];
      if (wz.excl.caches) { ex.push('**/.cache', '*.tmp', '*.swp'); job.exclude_caches = true; }
      if (wz.excl.build) ex.push('**/node_modules', '**/target', '**/.venv', '**/__pycache__');
      if (wz.excl.downloads) ex.push('**/Downloads', '**/.local/share/Trash', '**/$RECYCLE.BIN');
      ex.push(...wz.customExcl.split('\n').map(x => x.trim()).filter(Boolean));
      job.exclude = ex;
      job.one_file_system = !!wz.oneFS;
    } else if (wz.kind === 'system') {
      job.paths = ['/'];
      job.exclude = [...SYSTEM_EXCLUDES];
      job.exclude_caches = true;
      job.one_file_system = !!wz.oneFS;
    } else if (wz.kind === 'disk') {
      job.devices = [wz.device.trim()];
    } else {
      job.docker = wz.dock ? wz.dock.value() : {};
    }
    job.schedule = { nightly: [{ cron: at + ' * * *', type: 'auto' }], weekly: [{ cron: at + ' * * 0', type: 'full' }, { cron: at + ' * * 1-6', type: 'incremental' }],
      hourly: [{ cron: '0 */4 * * *', type: 'auto' }], manual: [] }[wz.schedule];
    if (wz.schedule !== 'weekly') job.full_interval = '7d';
    job.retention = { week: { keep_daily: 7 }, standard: { keep_daily: 7, keep_weekly: 4, keep_monthly: 12 },
      long: { keep_daily: 14, keep_weekly: 8, keep_monthly: 24, keep_yearly: 5 }, all: {} }[wz.keep];
    return job;
  }

  function validate() {
    const step = WIZ_STEPS[wz.step].id;
    if (step === 'what' && !wz.kind) return 'Choose what to back up';
    if (step === 'sources') {
      if (wz.kind === 'files' && !wz.paths.trim()) return 'Enter at least one folder';
      if (wz.kind === 'disk' && !wz.device.trim()) return 'Choose a disk or partition';
      if (wz.kind === 'docker') { try { if (!wz.dock.value()) return 'Tick "Back up Docker containers"'; } catch (e) { return e.message; } }
    }
    if (step === 'where' && wz.repoMode === 'new') {
      const L = wz.loc;
      if (wz.type === 'local' && !L.path.trim()) return 'Enter the folder for the backups';
      if (wz.type === 'smb' && (!L.host.trim() || !L.share.trim())) return 'Enter the NAS and the share';
      if (wz.type === 'sftp' && (!L.host.trim() || !L.dir.trim())) return 'Enter the server and the folder';
      if (wz.type === 'server' && (!L.url.trim() || !L.srvRepo.trim())) return 'Enter the server and the repository name';
      if (wz.pwMode === 'generate' && !wz.saved) return 'Save the password first and tick the box';
      if (wz.pwMode !== 'generate') {
        if (wz.pw1.length < 10) return 'The password needs at least 10 characters';
        if (wz.pw1 !== wz.pw2) return 'The passwords do not match';
      }
    }
    if (step === 'finish') {
      if (!/^[A-Za-z0-9][A-Za-z0-9._-]*$/.test(wz.name.trim())) return 'Use letters, digits, dot, dash or underscore for the name';
      if (jobs.some(j => j.name === wz.name.trim())) return 'A job with this name exists already';
    }
    return '';
  }

  async function create(btn) {
    btn.disabled = true;
    const progress = h('div', { class: 'muted', style: 'margin-top:10px' });
    btn.after(progress);
    const say = m => { progress.textContent = m; };
    let step = '';
    try {
      const job = buildJob();
      let c = await loadConfig();
      if (wz.repoMode === 'new') {
        // A retry after an error skips what the previous attempt finished.
        const done = wz.done = wz.done || {};
        const r = repoDraft();
        const pw = wz.pwMode === 'generate' ? wz.password : wz.pw1;
        if (wz.pwMode !== 'ask') {
          step = 'Storing the password';
          say(step + '…');
          const res = await api('POST', '/api/repos/' + encodeURIComponent(r.name) + '/password-file', { password: pw });
          r.password_file = res.path;
        }
        step = 'Saving the repository';
        say(step + '…');
        const others = (c.repositories || []).filter(x => !(done.repo && x.name === done.repo));
        if (others.some(x => x.name === r.name)) throw new Error('A repository named “' + r.name + '” exists already; go back and choose another name');
        c.repositories = others.concat([r]);
        c = await api('PUT', '/api/config', c);
        done.repo = r.name;
        if (!done.init) {
          step = 'Creating the encrypted repository';
          say(step + '…');
          await api('POST', '/api/repos/' + encodeURIComponent(r.name) + '/init', { password: pw }, true);
          done.init = true;
        }
      }
      const offer = wz.repoMode === 'existing' && repos.find(r => r.offer && r.name === wz.repo);
      if (offer) {
        step = 'Passing the hub repository to the server';
        say(step + '…');
        await useHubRepo(offer.name);
      }
      step = 'Saving the job';
      say(step + '…');
      c = await loadConfig();
      c.jobs = (c.jobs || []).concat([job]);
      if (wz.autostart) c.tray = Object.assign({}, c.tray, { autostart: true });
      await saveConfig(c);
      if (wz.runNow) { say('Starting the first backup…'); await api('POST', '/api/jobs/' + encodeURIComponent(job.name) + '/run', { type: 'auto' }); }
      clear(card).append(h('div', { class: 'wiz-done' }, icon('done'), h('h2', {}, 'Backup job “' + job.name + '” is ready'),
        h('p', { class: 'muted' }, wz.runNow ? 'The first backup is running. Follow it on the dashboard.' : 'It runs on its schedule; start it any time from the dashboard.'),
        wz.repoMode === 'new' && wz.pwMode === 'generate' ? h('p', {}, 'Remember: the repository password you saved is the only way to read the backups.') : null,
        h('div', { class: 'wiz-nav', style: 'justify-content:center' },
          h('button', { class: 'primary', onclick: () => navigate(wz.kind === 'docker' ? 'containers' : 'dashboard') }, wz.kind === 'docker' ? 'Go to Containers' : 'Go to the Dashboard'),
          h('button', { onclick: () => navigate('wizard') }, 'Set up another backup'))));
      clear(stepsEl);
    } catch (e) {
      toast((step ? step + ' failed: ' : '') + e.message, true);
      say(step ? step + ' failed: ' + e.message + ' — fix it and click “Create backup” again; finished steps are not repeated.' : '');
      btn.disabled = false;
    }
  }

  function pick(kind) {
    if (wz.kind !== kind) { wz.kind = kind; wz.name = ''; }
    render();
  }

  function render() {
    clear(stepsEl).append(...WIZ_STEPS.map((st, i) => h('div', { class: 'wiz-step' + (i === wz.step ? ' active' : i < wz.step ? ' done' : '') }, icon(i < wz.step ? 'done' : st.icon), (i + 1) + '. ' + st.title)));
    const id = WIZ_STEPS[wz.step].id;
    const last = wz.step === WIZ_STEPS.length - 1;
    // Keep the Docker picker element alive between steps.
    if (wz.dock && wz.dock.el.parentNode) wz.dock.el.remove();
    clear(card).append(...steps[id]().flat().filter(Boolean));
    const nav = h('div', { class: 'wiz-nav' },
      wz.step > 0 ? h('button', { onclick: () => { wz.step--; render(); } }, 'Back') : null,
      h('span', { class: 'spacer' }),
      last ? h('button', { class: 'primary', onclick: e => { const err = validate(); if (err) return toast(err, true); create(e.currentTarget); } }, 'Create backup')
        : h('button', { class: 'primary', onclick: () => { const err = validate(); if (err) return toast(err, true); wz.step++; render(); } }, 'Next'));
    card.append(nav);
  }
  render();
};

// ---------- containers (Docker projects) ----------
views.containers = async main => {
  main.append(h('h1', {}, 'Containers'), h('div', { class: 'sub' },
    'Docker Compose projects and containers. Each one is backed up as its own snapshot (directory, bind mounts, volumes and database dumps) and can be restored on its own.'));
  const box = h('div');
  main.append(box);
  const render = async () => {
    const [jobs, runs] = await Promise.all([api('GET', '/api/docker'), api('GET', '/api/runs')]);
    const active = {};
    runs.active.forEach(r => { active[r.kind + ':' + r.target] = r; });
    clear(box);
    if (!jobs.length) {
      box.append(h('div', { class: 'empty' }, 'No backup job has a Docker section yet. ',
        isAdmin() ? h('button', { class: 'primary', onclick: () => navigate('jobs') }, 'Create one under Backup Jobs') : 'Ask an administrator to set one up.'));
      return;
    }
    for (const j of jobs) {
      box.append(h('h2', {}, 'Job ', j.job, h('span', { class: 'muted', style: 'font-weight:400;font-size:14px' }, ' → ' + j.repository)));
      if (j.error) { box.append(h('div', { class: 'empty' }, j.error)); continue; }
      const grid = h('div', { class: 'grid' });
      box.append(grid);
      for (const p of j.projects) {
        const unit = j.job + '/' + p.name;
        const run = active['backup:' + unit] || active['restore:' + unit] || active['backup:' + j.job];
        const last = p.last;
        const res = p.result;
        grid.append(h('div', { class: 'card' },
          h('h3', {}, icon(p.databases && p.databases.length ? 'database' : 'containers'), p.name, p.standalone ? h('span', { class: 'chip' }, 'standalone') : null,
            run ? badge('running') : badge(res ? res.status : (last ? 'success' : null))),
          p.working_dir ? h('div', { class: 'meta mono' }, p.working_dir) : null,
          h('div', {}, (p.containers || []).map(c => h('div', { class: 'container-row' }, h('span', { class: 'dot' + (c.running ? ' on' : '') }), c.name, ' · ', c.image))),
          !(p.containers || []).length ? h('div', { class: 'meta' }, 'no containers (project directory only)') : null,
          h('div', { class: 'meta' }, (p.databases && p.databases.length ? 'Databases: ' + p.databases.join(', ') + ' · ' : '') +
            { dump: 'dumped while running', stop: 'stopped during backup', files: 'files only' }[p.mode]),
          h('div', { class: 'meta' }, 'Last backup: ', last ? ago(last.time) + ' (' + last.type + (last.summary ? ', ' + fmtBytes(last.summary.total_bytes) : '') + ') · ' + p.snapshots + ' snapshots' : 'never'),
          res && res.error ? h('div', { class: 'meta', style: 'color:var(--err)' }, res.error) : null,
          run ? [h('div', { class: 'meta' }, run.kind === 'restore' ? 'Restoring: ' + (run.message || 'queued…') : (run.state === 'queued' ? 'queued…' : progressText(run.progress))),
            run.kind === 'backup' ? progressBar(run.progress) : h('div', { class: 'progress indet' }, h('div'))] : null,
          h('div', { class: 'actions' },
            h('button', { class: 'primary', title: 'Back up this project now, databases included', disabled: !!run, onclick: () => dockerBackup(j.job, p.name) }, 'Back up now'),
            h('button', { title: 'Put this project back to the state of a backup', disabled: !!run || !p.snapshots, onclick: () => dockerRestore(j.job, p) }, 'Restore…'),
            h('span', { class: 'spacer' }),
            h('button', { class: 'small', title: 'List all backups of this project', disabled: !p.snapshots, onclick: () => { state.snapRepo = j.repository; state.snapHost = ''; state.snapJob = unit; navigate('snapshots'); } }, 'Snapshots'),
            h('button', { class: 'small', title: 'Look through the latest backup; download or restore files', disabled: !last, onclick: () => openExplorer({ repo: j.repository, snapshot: last.id, job: unit }) }, 'Browse'))));
      }
    }
  };
  await render();
  state.timer = setInterval(() => render().catch(() => {}), 3000);
};

async function dockerBackup(job, project) {
  try { await api('POST', '/api/docker/' + encodeURIComponent(job) + '/' + encodeURIComponent(project) + '/backup', { type: 'auto' }); toast('Backing up ' + project); }
  catch (e) { toast(e.message, true); }
}

async function dockerRestore(job, p) {
  const base = '/api/docker/' + encodeURIComponent(job) + '/' + encodeURIComponent(p.name);
  const snaps = await api('GET', base + '/snapshots');
  const sel = h('select', {}, snaps.map((s, i) => h('option', { value: s.id }, fmtTime(s.time) + ' · ' + s.type + (s.stopped ? ' · stopped' : '') +
    (s.dumps ? ' · ' + s.dumps.length + ' dumps' : '') + (i === 0 ? ' (latest)' : ''))));
  const db = h('select', {},
    h('option', { value: 'auto' }, 'Automatic: load dumps (or files for stopped snapshots)'),
    h('option', { value: 'dumps' }, 'Fresh databases, then load the dumps'),
    h('option', { value: 'files' }, 'Restore the raw database files'));
  const keep = h('input', { type: 'checkbox' });
  const confirm = h('input', { placeholder: p.name, autocomplete: 'off' });
  const info = h('div', { class: 'meta' });
  const showInfo = () => {
    const s = snaps.find(x => x.id === sel.value);
    clear(info).append(s && s.dumps ? 'Dumps: ' + s.dumps.join(', ') : 'No database dumps in this snapshot.');
    if (s && s.failed_dumps) info.append(h('div', { style: 'color:var(--err)' }, 'Failed dumps: ' + s.failed_dumps.join('; ')));
  };
  sel.addEventListener('change', showInfo);
  showInfo();
  const ok = await modal('Restore ' + p.name, [
    h('p', {}, 'This stops ', h('b', {}, p.name), ', replaces its data with the snapshot and starts it again:'),
    h('ul', { class: 'mono', style: 'font-size:12px' }, (p.paths || []).map(x => h('li', {}, x))),
    h('label', {}, 'Snapshot'), sel, info,
    h('label', {}, 'Databases'), db,
    h('label', { class: 'check' }, keep, 'Keep the replaced data next to it (PATH.herakles-old-TIME)'),
    h('p', { class: 'muted' }, 'If something fails before the project runs again, the previous data is put back automatically.'),
    h('label', {}, 'Type the project name to confirm'), confirm], [
    { label: 'Cancel', value: false },
    { label: 'Restore', class: 'danger solid', action: async () => {
      if (confirm.value.trim() !== p.name) { toast('Type ' + p.name + ' to confirm', true); return false; }
      await api('POST', base + '/restore', { snapshot: sel.value, databases: db.value, keep_old: keep.checked, confirm: confirm.value.trim() });
      return true;
    } }]);
  if (ok) toast('Restoring ' + p.name + ' — progress is shown on its card');
}

// ---------- account & users (web mode) ----------
const b64url = buf => btoa(String.fromCharCode(...new Uint8Array(buf))).replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '');
function fromB64url(s) {
  const b = atob((s + '==='.slice((s.length + 3) % 4)).replace(/-/g, '+').replace(/_/g, '/'));
  return Uint8Array.from(b, c => c.charCodeAt(0)).buffer;
}
function isAdmin() { return !state.info || state.info.mode !== 'web' || (state.info.user && state.info.user.role === 'admin'); }

async function addPasskey() {
  const nameInput = h('input', { value: guessDevice(), placeholder: 'e.g. MacBook, iPhone, YubiKey' });
  const name = await modal('Add a passkey', [h('p', { class: 'muted' }, 'Give it a name so you can tell your passkeys apart.'), h('label', {}, 'Name'), nameInput],
    [{ label: 'Cancel', value: null }, { label: 'Continue', class: 'primary', action: () => nameInput.value.trim() || 'Passkey' }], 'small');
  if (!name) return false;
  const opts = await api('POST', '/api/account/passkeys/begin');
  const pk = opts.publicKey;
  pk.challenge = fromB64url(pk.challenge);
  pk.user.id = fromB64url(pk.user.id);
  (pk.excludeCredentials || []).forEach(c => { c.id = fromB64url(c.id); });
  let cred;
  try { cred = await navigator.credentials.create({ publicKey: pk }); }
  catch (e) { if (e.name === 'InvalidStateError') toast('This authenticator already has a passkey for your account', true); else if (e.name !== 'NotAllowedError') toast(e.message, true); return false; }
  const r = cred.response;
  await api('POST', '/api/account/passkeys/finish?name=' + encodeURIComponent(name), {
    id: cred.id, rawId: b64url(cred.rawId), type: cred.type,
    response: { clientDataJSON: b64url(r.clientDataJSON), attestationObject: b64url(r.attestationObject), transports: r.getTransports ? r.getTransports() : [] },
  });
  toast('Passkey added');
  return true;
}
function guessDevice() {
  const ua = navigator.userAgent;
  return /iPhone/.test(ua) ? 'iPhone' : /iPad/.test(ua) ? 'iPad' : /Android/.test(ua) ? 'Android' : /Mac/.test(ua) ? 'Mac' : /Windows/.test(ua) ? 'Windows PC' : /Linux/.test(ua) ? 'Linux PC' : 'Passkey';
}

views.account = async main => {
  const me = await api('GET', '/api/account');
  main.append(h('h1', {}, 'Account'), h('div', { class: 'sub' }, 'Signed in as ', h('b', {}, me.name), ' (' + me.role + ')'));
  const cur = h('input', { type: 'password', autocomplete: 'current-password' });
  const pw1 = h('input', { type: 'password', autocomplete: 'new-password' });
  const pw2 = h('input', { type: 'password', autocomplete: 'new-password' });
  main.append(h('div', { class: 'grid' },
    h('div', { class: 'card' }, h('h3', {}, 'Passkeys'),
      h('div', { class: 'meta' }, 'Sign in with Face ID, Touch ID, Windows Hello, your phone, a password manager or a security key.'),
      me.passkeys.length ? h('table', { style: 'margin-top:10px' }, me.passkeys.map(p => h('tr', {},
        h('td', {}, h('b', {}, p.name), h('div', { class: 'meta' }, 'added ' + ago(p.created) + (p.last_used ? ' · used ' + ago(p.last_used) : ''))),
        h('td', { style: 'text-align:right' }, h('button', { class: 'small danger', onclick: async () => {
          if (!await confirmDlg('Remove passkey', 'Remove the passkey "' + p.name + '"?', true)) return;
          try { await api('DELETE', '/api/account/passkeys/' + encodeURIComponent(p.id)); navigate('account'); } catch (e) { toast(e.message, true); }
        } }, 'Remove')))))
        : h('p', { class: 'muted' }, 'No passkeys yet.'),
      h('div', { class: 'actions' }, window.PublicKeyCredential
        ? h('button', { class: 'primary', onclick: async () => { try { if (await addPasskey()) navigate('account'); } catch (e) { toast(e.message, true); } } }, 'Add a passkey')
        : h('span', { class: 'muted' }, 'This browser does not support passkeys.'))),
    h('div', { class: 'card' }, h('h3', {}, me.has_password ? 'Change password' : 'Set a password'),
      me.has_password ? [h('label', {}, 'Current password'), cur] : null,
      h('label', {}, 'New password (at least 10 characters)'), pw1, h('label', {}, 'Repeat'), pw2,
      h('div', { class: 'actions' }, h('button', { class: 'primary', onclick: async () => {
        if (pw1.value !== pw2.value) return toast('The passwords do not match', true);
        try { await api('POST', '/api/account/password', { current: cur.value, new: pw1.value }); toast('Password changed; other sessions were signed out'); navigate('account'); }
        catch (e) { toast(e.message, true); }
      } }, 'Save')))));
};

views.users = async main => {
  const users = await api('GET', '/api/users');
  main.append(h('h1', {}, 'Users'), h('div', { class: 'sub' }, 'Accounts of the web interface. There is no public sign-up; new users get an initial password and can add passkeys themselves. Users can run backups, browse and restore; administrators can also change the configuration, repositories and users.'),
    h('div', { class: 'toolbar' }, h('button', { class: 'primary', onclick: () => editUser() }, '+ New user')),
    h('table', {}, h('tr', {}, ['User', 'Role', 'Sign-in', 'Last login', 'State', ''].map(t => h('th', {}, t))),
      users.map(u => h('tr', {},
        h('td', {}, h('b', {}, u.name)), h('td', {}, u.role),
        h('td', {}, [u.has_password ? 'password' : null, u.passkeys.length ? u.passkeys.length + ' passkey' + (u.passkeys.length > 1 ? 's' : '') : null].filter(Boolean).join(' + ') || '-'),
        h('td', {}, u.last_login ? ago(u.last_login) : 'never'), h('td', {}, u.disabled ? badge('failure') : 'active'),
        h('td', { style: 'text-align:right' }, h('button', { class: 'small', onclick: () => editUser(u) }, 'Edit'), ' ',
          state.info.user.id !== u.id ? h('button', { class: 'small danger', onclick: async () => {
            if (!await confirmDlg('Delete user', 'Delete the account ' + u.name + '?', true)) return;
            try { await api('DELETE', '/api/users/' + u.id); navigate('users'); } catch (e) { toast(e.message, true); }
          } }, 'Delete') : null)))));
};

async function editUser(u) {
  const name = h('input', { value: u ? u.name : '', disabled: !!u, autocomplete: 'off' });
  const role = h('select', {}, h('option', { value: 'user', selected: u && u.role === 'user' }, 'User (backups, browse, restore)'),
    h('option', { value: 'admin', selected: u && u.role === 'admin' }, 'Administrator (everything)'));
  const pw = h('input', { type: 'password', autocomplete: 'new-password', placeholder: u ? 'leave empty to keep' : 'at least 10 characters' });
  const disabled = h('input', { type: 'checkbox', checked: u && u.disabled });
  const reset = h('input', { type: 'checkbox' });
  await modal(u ? 'Edit ' + u.name : 'New user', [
    h('label', {}, 'User name'), name, h('label', {}, 'Role'), role,
    h('label', {}, u ? 'New password' : 'Initial password'), pw,
    u ? [h('label', { class: 'check' }, disabled, 'Disabled (cannot sign in)'),
      u.passkeys.length ? h('label', { class: 'check' }, reset, 'Remove all ' + u.passkeys.length + ' passkeys') : null] : null], [
    { label: 'Cancel' },
    { label: 'Save', class: 'primary', action: async () => {
      if (!u) await api('POST', '/api/users', { name: name.value.trim(), role: role.value, password: pw.value });
      else {
        const body = { role: role.value, disabled: disabled.checked, reset_passkeys: reset.checked };
        if (pw.value) body.password = pw.value;
        await api('PATCH', '/api/users/' + u.id, body);
      }
      navigate('users');
    } }]);
}

function renderUser(info) {
  const box = clear($('#user-box'));
  if (info.mode !== 'web' || !info.user) { box.hidden = true; return; }
  box.hidden = false;
  box.append(h('div', {}, 'Signed in as ', h('span', { class: 'name' }, info.user.name)),
    h('div', {}, h('a', { onclick: () => navigate('account') }, 'Account'), ' · ',
      h('a', { onclick: async () => { await api('POST', '/auth/logout'); location.href = '/login.html'; } }, 'Sign out')));
}

// ---------- hub: attached servers ----------
// mergeInfo keeps the hub's login on a remote server's info.
function mergeInfo(i) {
  if (!state.target || !state.hubInfo) return i;
  return { ...i, mode: state.hubInfo.mode, user: state.hubInfo.user, remote: state.target };
}
async function loadInfo() {
  try { state.hubInfo = await localApi('GET', '/api/info'); } catch (e) { state.hubInfo = null; }
  state.info = state.hubInfo;
  if (state.target) {
    try { state.info = mergeInfo(await api('GET', '/api/info')); } catch (e) { state.info = { ...state.hubInfo, remote: state.target }; }
  }
}
async function switchTarget(t) {
  state.target = t;
  try { sessionStorage.setItem('herakles.target', t ? JSON.stringify(t) : ''); } catch (e) { /* storage blocked */ }
  state.config = null;
  await loadInfo();
  await renderTargets();
  if (state.info) renderDaemon(state.info);
  navigate(['servers', 'users', 'account'].includes(state.view) ? 'dashboard' : state.view);
}
// renderTargets fills the server picker of a hub.
async function renderTargets() {
  const box = clear($('#target-box'));
  if (!state.hubInfo || !state.hubInfo.hub) { box.hidden = true; return; }
  let servers = [];
  try { servers = (await localApi('GET', '/api/hub')).servers; } catch (e) { /* keep the hub only */ }
  box.hidden = !servers.length;
  if (state.target) {
    const cur = servers.find(x => x.id === state.target.id);
    if (cur) state.target.online = cur.online;
  }
  const sel = h('select', { title: 'The computer this interface manages', onchange: () => {
    const v = sel.value; const x = servers.find(y => y.id === v);
    switchTarget(x ? { id: x.id, name: x.name, online: x.online } : null);
  } },
  h('option', { value: '' }, (state.hubInfo.hostname || 'this computer') + ' (hub)'),
  servers.map(x => h('option', { value: x.id, selected: state.target && state.target.id === x.id }, (x.online ? '● ' : '○ ') + x.name + (x.online ? '' : ' (offline)') + (x.compat && x.compat.status === 'error' ? ' ⚠ incompatible' : ''))));
  box.append(h('label', {}, 'Managing'), sel);
}
// compatChip shows whether a server and this hub work together.
function compatChip(x) {
  const c = x.compat || {};
  const hubV = state.hubInfo && state.hubInfo.version;
  const other = hubV && x.hello && x.hello.version && x.hello.version !== hubV ? ' · hub has ' + hubV : '';
  switch (c.status) {
    case 'error': return h('span', { class: 'badge failure', title: 'Some things do not work between this server and the hub; see below' }, 'incompatible' + other);
    case 'warning': return h('span', { class: 'badge warning', title: 'Some functions are not available; see below' }, (c.issues || []).length + ' limitation' + ((c.issues || []).length === 1 ? '' : 's') + other);
    case 'ok': return h('span', { class: 'badge success', title: 'Server and hub have the same capabilities' + (c.derived ? ' (derived from its version)' : '') }, 'compatible' + other);
  }
  return h('span', { class: 'badge', title: 'The server has not connected yet' }, 'not connected yet');
}

function compatIssues(x) {
  const c = x.compat || {};
  if (!(c.issues || []).length) return null;
  const hubV = state.hubInfo && state.hubInfo.version;
  const fix = c.newer === 'hub' || c.issues.some(i => i.side === 'server')
    ? 'Update ' + x.name + (hubV ? ' to ' + hubV : '') + ' (it can update itself from version 0.11 on: tray menu, window or "herakles update").'
    : 'Update this hub' + (x.hello && x.hello.version ? ' to ' + x.hello.version : '') + ' (window: Install update, or "herakles update").';
  return h('div', { class: 'compat' },
    h('ul', {}, c.issues.map(i => h('li', { class: i.severity === 'error' ? 'bad-text' : 'warn-text' },
      h('b', {}, i.side === 'hub' ? 'This hub: ' : 'Server: '), i.message))),
    h('div', { class: 'muted' }, fix + (c.derived ? ' Its capabilities were derived from its version; versions before 0.13 do not announce them.' : '')));
}

function copyText(text) {
  if (navigator.clipboard && window.isSecureContext) { navigator.clipboard.writeText(text).then(() => toast('Copied')); return; }
  const t = h('textarea', {}, text); document.body.append(t); t.select();
  try { document.execCommand('copy'); toast('Copied'); } catch (e) { toast('Copy it by hand', true); }
  t.remove();
}

views.servers = async main => {
  const [data, cfg] = await Promise.all([localApi('GET', '/api/hub'), localApi('GET', '/api/config')]);
  main.append(h('h1', {}, 'Servers'),
    h('div', { class: 'sub' }, 'Servers attached to this hub. Pick one under "Managing" in the menu, or click Manage, to set up its backups with the wizard, see its disks, run, browse and restore, as if you were logged in there.'),
    h('div', { class: 'toolbar' },
      isAdmin() ? h('button', { class: 'primary', title: 'Create a one-time code that attaches another server to this hub', onclick: enrollDlg }, '+ Enroll a server') : null,
      h('button', { title: 'Reload the list', onclick: () => navigate('servers') }, 'Refresh')));
  const bad = data.servers.filter(x => x.compat && (x.compat.status === 'error' || x.compat.status === 'warning'));
  if (bad.length) {
    const errs = bad.filter(x => x.compat.status === 'error').length;
    main.append(h('div', { class: 'notice warn' }, icon('alert'),
      errs ? errs + (errs === 1 ? ' server is' : ' servers are') + ' not compatible with this hub' + (bad.length > errs ? ', ' + (bad.length - errs) + ' more with limitations' : '') + ': ' + bad.map(x => x.name).join(', ') + '. Details below.'
        : bad.length + (bad.length === 1 ? ' server has' : ' servers have') + ' limitations with this hub: ' + bad.map(x => x.name).join(', ') + '. Details below.'));
  }
  const grid = h('div', { class: 'grid' });
  if (!data.servers.length) {
    grid.append(h('div', { class: 'empty' }, 'No servers attached yet. Install Herakles on a server, then enroll it with a code from "Enroll a server".'));
  }
  for (const x of data.servers) {
    const jobsBox = h('div', { class: 'meta' });
    const hel = x.hello || {};
    const sync = x.sync || {};
    grid.append(h('div', { class: 'card' },
      h('h3', {}, icon('server'), x.name, h('span', { class: 'badge ' + (x.online ? 'success' : '') }, x.online ? 'online' : 'offline')),
      h('div', { class: 'meta' }, [hel.hostname, hel.os && hel.os + '/' + hel.arch, hel.container && 'container (' + hel.container + ')'].filter(Boolean).join(' · ')),
      h('div', { class: 'meta' }, 'Herakles ', h('b', {}, hel.version || '?'), ' ', compatChip(x)),
      compatIssues(x),
      h('div', { class: 'meta' }, x.online ? 'Connected since ' + fmtTime(x.since) : 'Last seen ' + ago(x.last_seen), x.address ? ' from ' + x.address : ''),
      x.sync_error ? h('div', { class: 'meta', style: 'color:var(--err)' }, 'Repositories not passed on: ' + x.sync_error) : null,
      (sync.conflicts || []).length ? h('div', { class: 'meta', style: 'color:var(--warn)' }, 'Not passed on, the server has its own repository with that name: ' + sync.conflicts.join(', ')) : null,
      (sync.kept || []).length ? h('div', { class: 'meta' }, 'No longer shared but still used: ' + sync.kept.join(', ')) : null,
      jobsBox,
      h('div', { class: 'actions' },
        h('button', { class: 'primary', disabled: !x.online, title: 'Manage this server: its backups, disks and snapshots', onclick: () => switchTarget({ id: x.id, name: x.name, online: x.online }) }, 'Manage'),
        h('span', { class: 'spacer' }),
        isAdmin() ? h('button', { class: 'small', title: 'Change the name shown on the hub', onclick: async () => {
          const input = h('input', { value: x.name });
          const ok = await modal('Rename ' + x.name, [h('label', {}, 'Name'), input], [{ label: 'Cancel', value: false },
            { label: 'Rename', class: 'primary', action: async () => { await localApi('PUT', '/api/hub/servers/' + x.id, { name: input.value }); return true; } }], 'small');
          if (ok) { await renderTargets(); navigate('servers'); }
        } }, 'Rename') : null,
        isAdmin() ? h('button', { class: 'small danger', title: 'Detach: the server can no longer connect; its backups keep running on their own', onclick: async () => {
          if (!await confirmDlg('Remove ' + x.name, 'Detach ' + x.name + ' from the hub? It can no longer connect. Its own backups keep running, with the repositories the hub passed on. To attach it again, enroll it with a new code.', true)) return;
          try { await localApi('DELETE', '/api/hub/servers/' + x.id); if (state.target && state.target.id === x.id) state.target = null; await renderTargets(); navigate('servers'); } catch (e) { toast(e.message, true); }
        } }, 'Remove') : null)));
    if (x.online) {
      localApi('GET', '/api/hub/servers/' + encodeURIComponent(x.id) + '/api/jobs').then(jobs => {
        clear(jobsBox).append(...(jobs.length ? jobs.map(j => h('span', { class: 'chip', title: j.last && j.last.error ? j.last.error : (j.last ? 'last run ' + ago(j.last.start) : 'never run') },
          icon(jobIcon(j)), j.name, ' ', h('span', { class: 'badge ' + (j.active ? 'running' : (j.last && j.last.status) || '') }, j.active ? 'running' : (j.last && j.last.status) || 'never run'))) : ['No backup jobs yet.']));
      }).catch(e => clear(jobsBox).append(e.message));
    }
  }
  main.append(grid);

  // Repositories passed to every server.
  const own = (cfg.repositories || []).filter(r => !r.hub);
  const shared = new Set(data.shared || []);
  const boxes = own.map(r => { const c = h('input', { type: 'checkbox', checked: shared.has(r.name), disabled: !isAdmin() }); c.dataset.name = r.name; return c; });
  main.append(h('h2', {}, 'Repositories for all servers'),
    h('div', { class: 'card' },
      h('div', { class: 'meta' }, 'These repositories are passed to every attached server, with their passwords: the servers need no credentials of their own. Each backup is one file in the repository; a server’s incremental backups build on its own full backups. Pruning and checks of shared repositories run here on the hub.'),
      own.length ? h('div', { class: 'checks' }, own.map((r, i) => h('label', { class: 'check' }, boxes[i], ' ', h('b', {}, r.name), ' ', h('span', { class: 'muted mono' }, r.location)))) : h('div', { class: 'muted' }, 'Add a repository first (Repositories).'),
      isAdmin() && own.length ? h('div', { class: 'actions' }, h('button', { class: 'primary', onclick: async () => {
        try {
          await localApi('PUT', '/api/hub/shared', { repositories: boxes.filter(b => b.checked).map(b => b.dataset.name) });
          toast('Saved; connected servers get the change now');
        } catch (e) { toast(e.message, true); }
      } }, 'Save')) : null),
    h('div', { class: 'hint' }, 'Only servers the hub\'s administrators may control should be attached: from here, their backups can be changed completely, including commands that run before and after them.'));
  renderTargets();
};

async function enrollDlg() {
  const name = h('input', { placeholder: 'e.g. web-1 (default: the server\'s host name)' });
  const out = h('div');
  const site = 'https://herakles.kloeck-it.de/install.sh';
  await modal('Enroll a server', [
    h('p', {}, 'Creates a one-time enrollment code. It holds everything the server needs: the hub\'s address, a secret that works once within 24 hours, and the hub\'s identity key, so the server only ever talks to this hub.'),
    h('label', {}, 'Name on the hub'), name, out], [
    { label: 'Close', value: true },
    { label: 'Create code', class: 'primary', action: async () => {
      const r = await localApi('POST', '/api/hub/enroll', { name: name.value.trim() });
      clear(out).append(
        h('label', {}, '1. Install Herakles on the server (choose "server"), if it is not yet:'),
        h('pre', { class: 'code-box' }, 'curl -fsSL ' + site + ' | sudo sh'),
        h('label', {}, '2. Run on the server:'),
        h('textarea', { class: 'mono code-box', readonly: true, rows: 4, onclick: e => e.target.select() }, r.command),
        h('div', { class: 'actions' },
          h('button', { onclick: () => copyText(r.command) }, 'Copy command'),
          h('button', { onclick: () => copyText(r.code) }, 'Copy code only')),
        h('div', { class: 'hint' }, 'Works once, until ', fmtTime(r.expires), '. Hub key ', h('span', { class: 'mono' }, r.hub_key), '. The server appears on this page as soon as it is attached.'));
      return false;
    } }]);
  navigate('servers');
}

async function boot() {
  try { const t = sessionStorage.getItem('herakles.target'); if (t) state.target = JSON.parse(t); } catch (e) { /* storage blocked */ }
  await loadInfo();
  if (state.target && !(state.hubInfo && state.hubInfo.hub)) { state.target = null; state.info = state.hubInfo; }
  const web = state.hubInfo && state.hubInfo.mode === 'web';
  document.querySelectorAll('#nav [data-web]').forEach(a => { a.hidden = !web; });
  document.querySelectorAll('#nav [data-hub]').forEach(a => { a.hidden = !(state.hubInfo && state.hubInfo.hub); });
  document.querySelectorAll('#nav [data-admin]').forEach(a => { a.hidden = a.hidden || !isAdmin(); });
  if (state.info) renderUser(state.info);
  await renderTargets();
  navigate('dashboard');
}
boot();

// ---------- snapshot explorer ----------
const snapBase = (repo, id) => '/api/repos/' + encodeURIComponent(repo) + '/snapshots/' + id;
const FORMATS = [['zip', 'ZIP (.zip)'], ['tar.gz', 'Compressed tar (.tar.gz)'], ['tar', 'tar (.tar)']];

function downloadURL(repo, id, paths, format) {
  const q = paths.map(p => 'path=' + encodeURIComponent(p));
  if (format) q.push('format=' + encodeURIComponent(format));
  return snapBase(repo, id) + '/download?' + q.join('&');
}
function startDownload(url) {
  const a = h('a', { href: apiURL(url), download: '' });
  document.body.append(a); a.click(); a.remove();
}
function formatSelect() {
  let f = 'zip';
  try { f = localStorage.getItem('herakles.format') || f; } catch (e) { /* storage blocked */ }
  return h('select', { style: 'width:auto', onchange: e => { try { localStorage.setItem('herakles.format', e.target.value); } catch (x) { /* ignore */ } } },
    FORMATS.map(([v, l]) => h('option', { value: v, selected: v === f }, l)));
}

// downloadDlg asks for the archive format and downloads paths.
async function downloadDlg(repo, s, paths) {
  const fmt = formatSelect();
  const what = paths.length === 1 ? (paths[0] === '/' ? 'the whole snapshot' : paths[0]) : paths.length + ' items';
  await modal('Download ' + (paths.length === 1 && paths[0] !== '/' ? paths[0].split('/').pop() : 'snapshot ' + s.id.slice(0, 8)), [
    h('p', {}, 'Download ', h('b', {}, what), ' from the snapshot of ', fmtTime(s.time), ' as an archive.'),
    h('label', {}, 'Format'), fmt,
    h('p', { class: 'muted' }, 'ZIP opens everywhere. tar keeps owners and permissions. Large snapshots stream directly from the repository; the size is not known in advance.')], [
    { label: 'Cancel' },
    { label: 'Download', class: 'primary', action: () => startDownload(downloadURL(repo, s.id, paths, fmt.value)) }], 'small');
}

// restoreDlg restores paths of snapshot s.
async function restoreDlg(repo, s, paths) {
  const target = h('input', { value: (state.info && state.info.os === 'windows' ? 'C:\\herakles-restore' : '~/herakles-restore') });
  const ow = h('select', {}, h('option', { value: 'always' }, 'Overwrite existing files'), h('option', { value: 'if-changed' }, 'Only if different'), h('option', { value: 'never' }, 'Never overwrite'));
  const inc = h('input', { placeholder: 'e.g. *.pdf; **/Documents' });
  const verify = h('input', { type: 'checkbox', checked: true });
  const what = paths.length > 1 ? paths.length + ' items' : paths[0] === '/' ? 'snapshot ' + s.id.slice(0, 8) : paths[0];
  await modal('Restore ' + what, [
    paths.length > 1 ? h('ul', { class: 'pathlist mono' }, paths.slice(0, 12).map(p => h('li', {}, p)), paths.length > 12 ? h('li', {}, '… and ' + (paths.length - 12) + ' more') : null) : null,
    h('label', {}, paths.length > 1 ? 'Target directory (the selected items are restored inside it)' : 'Target directory (the selected item is restored inside it)'), target,
    h('label', {}, 'Existing files'), ow,
    h('label', {}, 'Only restore matching paths (optional, ; separated)'), inc,
    h('label', { class: 'check' }, verify, 'Verify restored data')], [
    { label: 'Cancel' },
    { label: 'Restore', class: 'primary', action: async () => {
      toast('Restoring…');
      const r = await api('POST', snapBase(repo, s.id) + '/restore',
        { paths, target: target.value, overwrite: ow.value, verify: verify.checked, includes: inc.value.split(';').map(x => x.trim()).filter(Boolean) });
      const res = r.result;
      toast('Restored ' + res.files + ' files (' + fmtBytes(res.bytes) + ')' + (res.skipped ? ', skipped ' + res.skipped : '') + (res.errors ? ', ' + res.errors + ' errors' : ''), res.errors > 0);
      if (r.errors.length) await modal('Restore errors', h('pre', { class: 'mono' }, r.errors.join('\n')), [{ label: 'Close' }]);
    } }]);
}

// startPath is the deepest directory containing all backed-up paths.
function startPath(s) {
  const ps = (s.paths || []).map(p => p.replace(/\\/g, '/').replace(/^([A-Za-z]):/, '/$1')).filter(p => p.startsWith('/'));
  if (!ps.length) return '/';
  let common = ps[0].split('/').filter(Boolean);
  for (const p of ps.slice(1)) {
    const parts = p.split('/').filter(Boolean);
    let n = 0;
    while (n < common.length && n < parts.length && common[n] === parts[n]) n++;
    common = common.slice(0, n);
  }
  return '/' + common.join('/');
}

const PREVIEW_IMG = /\.(png|jpe?g|gif|webp|svg|ico|bmp)$/i;
const entryIcon = e => e.type === 'dir' ? 'folder' : e.type === 'symlink' ? 'share' : e.type === 'device' || e.type === 'volume' ? 'disk' : PREVIEW_IMG.test(e.name) ? 'eye' : 'file';
// Folders, disk images and their partitions open like folders.
const isFolder = e => e.type === 'dir' || e.type === 'device' || e.type === 'volume';

// openExplorer shows a snapshot in a file explorer dialog: navigate,
// search, preview, select, download and restore. opts: {repo, snapshot
// (object or id), path, job}.
async function openExplorer(opts) {
  const repo = opts.repo;
  let all;
  try { all = await api('GET', '/api/repos/' + encodeURIComponent(repo) + '/snapshots'); }
  catch (e) { toast(e.message, true); return; }
  const want = typeof opts.snapshot === 'string' ? opts.snapshot : opts.snapshot && opts.snapshot.id;
  let snap = all.find(s => s.id === want || (want && s.id.startsWith(want))) || all.find(s => opts.job && s.job === opts.job);
  if (!snap) { toast('No snapshot found' + (opts.job ? ' for ' + opts.job : ''), true); return; }
  const family = all.filter(s => s.job === snap.job && s.hostname === snap.hostname);
  let path = opts.path || startPath(snap);
  let entries = [];
  let mode = 'list'; // or 'search'
  let sort = { key: 'name', dir: 1 };
  const selected = new Map(); // path → entry
  let current = null; // previewed entry

  const snapSel = h('select', { class: 'snap-pick', title: 'Version', onchange: e => { snap = family.find(s => s.id === e.target.value); selected.clear(); go(path, true); } },
    family.map(s => h('option', { value: s.id, selected: s.id === snap.id }, fmtTime(s.time) + ' · ' + s.type + ' · ' + s.id.slice(0, 8))));
  const crumbs = h('div', { class: 'crumbs' });
  const upBtn = h('button', { class: 'small', title: 'Up one folder (Backspace)', onclick: () => go(parent(path)) }, icon('back'), 'Up');
  const filter = h('input', { type: 'search', placeholder: 'Filter this folder…', oninput: () => render() });
  const query = h('input', { type: 'search', placeholder: 'Search names below this folder (*.pdf, invoice…)' });
  query.addEventListener('keydown', e => { if (e.key === 'Enter') search(); });
  const listBox = h('div', { class: 'ex-list' });
  const preview = h('div', { class: 'ex-preview' });
  const selInfo = h('span', { class: 'muted' });
  const fmt = formatSelect();
  const dlSel = h('button', { class: 'primary', onclick: () => downloadSelection() }, 'Download');
  const rsSel = h('button', { onclick: () => restoreDlg(repo, snap, selPaths()) }, 'Restore…');
  const title = h('span');

  const root = $('#modal-root');
  const close = () => { backdrop.remove(); document.removeEventListener('keydown', keys); };
  const keys = e => {
    if (root.lastElementChild !== backdrop) return; // a dialog on top has the keys
    if (e.key === 'Escape') close();
    else if (e.key === 'Backspace' && !/INPUT|SELECT|TEXTAREA/.test(document.activeElement.tagName) && path !== '/') { e.preventDefault(); go(parent(path)); }
  };
  document.addEventListener('keydown', keys);
  const backdrop = h('div', { class: 'modal-backdrop', onmousedown: e => { if (e.target === backdrop) close(); } },
    h('div', { class: 'modal explorer' },
      h('header', {}, icon('browse'), title, h('span', { class: 'spacer' }), snapSel,
        h('button', { class: 'small ghost', title: 'Close (Esc)', onclick: close }, icon('cancel'))),
      h('div', { class: 'ex-bar' }, upBtn, crumbs),
      h('div', { class: 'ex-bar' }, filter, query, h('button', { class: 'small', onclick: () => search() }, icon('eye'), 'Search')),
      h('div', { class: 'ex-main' }, listBox, preview),
      h('footer', {}, selInfo, h('span', { class: 'spacer' }), fmt, dlSel, rsSel, h('button', { onclick: close }, 'Close'))));
  root.append(backdrop);

  const relName = e => {
    const dir = e.path.slice(0, e.path.length - e.name.length).slice(path === '/' ? 1 : path.length + 1);
    return h('span', { class: 'ex-rel' }, h('span', {}, e.name), dir ? h('small', { class: 'muted' }, dir.replace(/\/$/, '') || '/') : null);
  };
  const parent = p => '/' + p.split('/').filter(Boolean).slice(0, -1).join('/');
  const selPaths = () => selected.size ? [...selected.keys()] : [path];
  const downloadSelection = () => {
    const ps = selPaths();
    const single = ps.length === 1 && selected.size === 1 && [...selected.values()][0].type === 'file';
    startDownload(downloadURL(repo, snap.id, ps, single ? '' : fmt.value));
  };
  const updateFooter = () => {
    let bytes = 0, dirs = 0;
    selected.forEach(e => { if (isFolder(e)) dirs++; else bytes += e.size || 0; });
    selInfo.textContent = selected.size ? selected.size + ' selected' + (bytes ? ' · ' + fmtBytes(bytes) : '') + (dirs ? ' · ' + dirs + ' folder' + (dirs > 1 ? 's' : '') : '')
      : 'Nothing selected: the buttons act on the current folder';
    dlSel.lastChild.textContent = selected.size ? 'Download selection' : 'Download folder';
    rsSel.lastChild.textContent = selected.size ? 'Restore selection…' : 'Restore folder…';
    const single = selected.size === 1 && [...selected.values()][0].type === 'file';
    fmt.disabled = single;
    fmt.title = single ? 'A single file is downloaded as it is' : '';
  };
  const go = async (p, keep) => {
    mode = 'list';
    listBox.classList.add('loading');
    try {
      entries = await api('GET', snapBase(repo, snap.id) + '/tree?path=' + encodeURIComponent(p));
      path = p;
    } catch (e) {
      if (keep && p !== '/') { toast(p + ' is not in this version; showing the top folder'); return go('/'); }
      toast(e.message, true);
      listBox.classList.remove('loading');
      return;
    }
    listBox.classList.remove('loading');
    filter.value = '';
    title.textContent = (snap.job || 'Snapshot') + ' · ' + fmtTime(snap.time);
    if (entries.some(e => e.image)) title.append(h('span', { class: 'chip', title: 'Read directly from the disk image in the backup. Files can be previewed, downloaded and restored.' }, 'inside a disk image'));
    const parts = path.split('/').filter(Boolean);
    clear(crumbs).append(h('a', { onclick: () => go('/') }, icon('archive'), snap.id.slice(0, 8)),
      ...parts.flatMap((x, i) => [h('span', { class: 'sep' }, '/'), h('a', { onclick: () => go('/' + parts.slice(0, i + 1).join('/')) }, x)]));
    upBtn.disabled = path === '/';
    render();
  };
  const search = async () => {
    const q = query.value.trim();
    if (!q) { go(path); return; }
    listBox.classList.add('loading');
    try {
      const r = await api('GET', snapBase(repo, snap.id) + '/find?q=' + encodeURIComponent(q) + '&path=' + encodeURIComponent(path));
      entries = r.results;
      mode = 'search';
      clear(crumbs).append(h('a', { onclick: () => { query.value = ''; go(path); } }, icon('back'), path),
        h('span', { class: 'sep' }, '·'), h('span', {}, r.results.length + (r.truncated ? '+' : '') + ' results for “' + q + '”'));
    } catch (e) { toast(e.message, true); }
    listBox.classList.remove('loading');
    render();
  };
  const sorted = () => {
    const f = filter.value.toLowerCase();
    const list = entries.filter(e => !f || e.name.toLowerCase().includes(f));
    const k = sort.key;
    return list.sort((a, b) => {
      if (isFolder(a) !== isFolder(b)) return isFolder(a) ? -1 : 1;
      let x = k === 'size' ? (a.size || 0) - (b.size || 0) : k === 'mtime' ? new Date(a.mtime) - new Date(b.mtime) : (k === 'path' ? a.path : a.name).localeCompare(k === 'path' ? b.path : b.name, undefined, { numeric: true, sensitivity: 'base' });
      return x * sort.dir;
    });
  };
  const render = () => {
    const list = sorted();
    const allSel = list.length && list.every(e => selected.has(e.path));
    const head = (label, key) => h('th', { class: 'sortable' + (sort.key === key ? ' on' : ''), onclick: () => { sort = { key, dir: sort.key === key ? -sort.dir : 1 }; render(); } },
      label, sort.key === key ? (sort.dir > 0 ? ' ▲' : ' ▼') : '');
    const toggle = (e, on) => { on ? selected.set(e.path, e) : selected.delete(e.path); updateFooter(); };
    clear(listBox).append(h('table', {},
      h('tr', {}, h('th', { class: 'cb' }, h('input', { type: 'checkbox', title: 'Select all', checked: allSel, onchange: ev => { list.forEach(e => toggle(e, ev.target.checked)); render(); } })),
        head('Name', mode === 'search' ? 'path' : 'name'), head('Size', 'size'), head('Modified', 'mtime'), h('th', {}, 'Mode')),
      !list.length ? h('tr', {}, h('td', { colspan: 5, class: 'muted' }, mode === 'search' ? 'Nothing found.' : entries.length ? 'No entry matches the filter.' : 'This folder is empty.')) : null,
      list.map(e => h('tr', { class: (selected.has(e.path) ? 'sel' : '') + (current && current.path === e.path ? ' cur' : ''), ondblclick: () => open(e) },
        h('td', { class: 'cb' }, h('input', { type: 'checkbox', checked: selected.has(e.path), onchange: ev => { toggle(e, ev.target.checked); ev.target.closest('tr').classList.toggle('sel', ev.target.checked); } })),
        h('td', { class: 'wrap' }, h('span', { class: 'entry-name' + (isFolder(e) ? ' dir' : ''), onclick: () => open(e) }, icon(entryIcon(e)),
          mode === 'search' ? relName(e) : e.type === 'volume' ? e.label : e.name, e.link ? h('span', { class: 'muted' }, ' → ' + e.link) : null),
          e.type === 'device' ? h('div', { class: 'muted small' }, 'Disk image: open it to browse its partitions and files') : null,
          e.type === 'volume' && e.note ? h('div', { class: 'muted small' + (e.browsable ? '' : ' warn') }, e.note) : null),
        h('td', { class: 'num' }, e.type === 'dir' ? '' : fmtBytes(e.size)), h('td', {}, e.type === 'volume' ? '' : fmtTime(e.mtime)), h('td', { class: 'mono' }, e.mode || '')))));
    updateFooter();
  };
  const open = e => {
    if (e.type === 'volume' && !e.browsable) { toast(e.label + ': ' + (e.note || 'cannot be browsed'), true); return; }
    if (isFolder(e)) { query.value = ''; go(e.path); return; }
    showPreview(e);
    render();
  };
  const showPreview = async e => {
    current = e;
    const url = apiURL(snapBase(repo, snap.id) + '/preview?path=' + encodeURIComponent(e.path));
    const body = h('div', { class: 'ex-pv-body' }, h('div', { class: 'muted' }, 'Loading…'));
    clear(preview).append(
      h('div', { class: 'ex-pv-head' }, icon(entryIcon(e)), h('b', { class: 'wrap' }, e.name), h('span', { class: 'spacer' }),
        h('button', { class: 'small ghost', title: 'Close preview', onclick: () => { current = null; clear(preview); preview.classList.remove('on'); render(); } }, icon('cancel'))),
      h('div', { class: 'meta' }, fmtBytes(e.size), ' · ', fmtTime(e.mtime), ' · ', h('span', { class: 'mono' }, e.mode)),
      h('div', { class: 'meta mono wrap' }, e.path),
      h('div', { class: 'actions' },
        h('button', { class: 'small primary', onclick: () => startDownload(downloadURL(repo, snap.id, [e.path], '')) }, 'Download'),
        h('button', { class: 'small', onclick: () => restoreDlg(repo, snap, [e.path]) }, 'Restore…')),
      body);
    preview.classList.add('on');
    if (e.type !== 'file') { clear(body).append(h('div', { class: 'muted' }, e.link ? 'Symbolic link to ' + e.link : 'No preview for this type.')); return; }
    if (PREVIEW_IMG.test(e.name)) { clear(body).append(h('img', { src: url, alt: e.name, onerror: () => clear(body).append(h('div', { class: 'muted' }, 'The image cannot be shown.')) })); return; }
    try {
      const res = await fetch(url, { headers: { 'X-Herakles': '1' } });
      const text = await res.text();
      if (current !== e) return;
      if (!res.ok) { let m = text; try { m = JSON.parse(text).error; } catch (x) { /* plain */ } clear(body).append(h('div', { class: 'muted' }, m)); return; }
      clear(body).append(h('pre', { class: 'ex-text' }, text));
      if (res.headers.get('X-Herakles-Truncated')) body.append(h('div', { class: 'muted' }, 'Only the first 256 KiB are shown.'));
    } catch (x) { clear(body).append(h('div', { class: 'muted' }, x.message)); }
  };
  await go(path, true);
}

// ---------- disks & partitions ----------
const DEV_KIND = { disk: 'Disk', part: 'Partition', lvm: 'LVM volume', crypt: 'Encrypted volume', raid: 'RAID', dm: 'Mapped volume', volume: 'Volume' };

// devicePicker lists the disks and partitions of this computer for image
// backups, with a field for any other device. opts: {multi, value (paths),
// onChange(paths)}. Jobs store the stable path (/dev/disk/by-id/…).
function devicePicker(opts) {
  const win = state.info && state.info.os === 'windows';
  const chosen = new Set((opts.value || []).filter(Boolean));
  const list = h('div', { class: 'dev-pick' }, h('div', { class: 'muted' }, 'Looking for disks and partitions…'));
  const manual = h(opts.multi ? 'textarea' : 'input', { rows: opts.multi ? 2 : null, placeholder: win ? '\\\\.\\C:   or   \\\\.\\PhysicalDrive0' : '/dev/sdb1   or   /dev/disk/by-id/…' });
  const el = h('div', {}, list, h('label', {}, opts.multi ? 'Other devices (one per line)' : 'Other device'), manual);
  let devs = [];
  const known = p => devs.some(d => d.path === p || d.stable === p);
  const emit = () => {
    const extra = manual.value.split('\n').map(x => x.trim()).filter(Boolean);
    opts.onChange(opts.multi ? [...chosen, ...extra.filter(x => !chosen.has(x))] : extra.length ? [extra[0]] : [...chosen].slice(0, 1));
  };
  manual.addEventListener('input', () => {
    if (!opts.multi && manual.value.trim()) { chosen.clear(); list.querySelectorAll('input').forEach(i => { i.checked = false; }); list.querySelectorAll('.dev-row').forEach(r => r.classList.remove('on')); }
    emit();
  });
  const render = () => {
    clear(list);
    if (!devs.length) return;
    const depth = {};
    for (const d of devs) {
      depth[d.name] = d.parent && depth[d.parent] !== undefined ? depth[d.parent] + 1 : 0;
      const key = d.stable || d.path;
      const on = chosen.has(d.path) || chosen.has(key);
      if (on && chosen.has(d.path) && key !== d.path) { chosen.delete(d.path); chosen.add(key); }
      const box = h('input', { type: opts.multi ? 'checkbox' : 'radio', name: 'dev-pick', checked: on, disabled: !!d.unavailable && !on });
      const row = h('label', { class: 'dev-row' + (on ? ' on' : '') + (d.system ? ' system' : '') + (d.unavailable ? ' unavailable' : ''), title: d.unavailable ? 'Cannot be read here: ' + d.unavailable : null, style: 'margin-left:' + depth[d.name] * 22 + 'px' },
        box, icon(d.usb ? 'usb' : d.type === 'crypt' ? 'lock' : d.type === 'lvm' || d.type === 'raid' ? 'repos' : 'disk', 'dev-ico'),
        h('div', { class: 'dev-main' },
          h('div', {}, h('b', { class: 'mono' }, d.path), ' ', h('span', { class: 'muted' }, fmtBytes(d.size)), ' ',
            h('span', { class: 'chip' }, (d.usb ? 'USB ' : '') + (DEV_KIND[d.type] || d.type)),
            d.system ? h('span', { class: 'chip warn', title: 'This device holds the running system and changes while it is read. Prefer a file backup, or image it from a rescue system.' }, 'system') : null,
            d.read_only ? h('span', { class: 'chip' }, 'read-only') : null,
            d.unavailable ? h('span', { class: 'chip warn' }, d.unavailable) : null),
          h('div', { class: 'meta' }, [d.model, d.fstype === 'swap' && (d.mounts || []).includes('[SWAP]') ? '' : d.fstype ? d.fstype + (d.label ? ' “' + d.label + '”' : '') : d.label ? '“' + d.label + '”' : '',
            (d.mounts || []).filter(m => m !== '[SWAP]').length ? 'mounted at ' + d.mounts.filter(m => m !== '[SWAP]').join(', ') : (d.mounts || []).includes('[SWAP]') ? 'swap in use' : ''].filter(Boolean).join(' · ') || ' '),
          key !== d.path ? h('div', { class: 'meta mono dev-stable', title: 'Saved in the job under this name, which stays the same when disks are added or reordered' }, '↳ ' + key) : null));
      box.addEventListener('change', () => {
        if (!opts.multi) { chosen.clear(); list.querySelectorAll('.dev-row').forEach(r => r.classList.remove('on')); manual.value = ''; }
        box.checked ? chosen.add(key) : chosen.delete(key);
        row.classList.toggle('on', box.checked);
        emit();
      });
      list.append(row);
    }
  };
  api('GET', '/api/wizard/devices').then(r => {
    devs = r.devices || [];
    const others = [...chosen].filter(p => !known(p));
    others.forEach(p => chosen.delete(p));
    manual.value = others.join('\n');
    if (r.error || !devs.length) { clear(list).append(h('div', { class: 'muted' }, r.error || 'No disks found. Enter the device name below.')); return; }
    render();
    if (r.container && devs.some(d => d.unavailable)) list.prepend(h('div', { class: 'note' },
      h('p', {}, icon('info'), ' Herakles runs in a container (' + r.container + '). It sees the host\'s disks but cannot read them unless the host passes them in (Proxmox: ', h('code', {}, 'dev0: /dev/sdb'), ').'),
      h('p', {}, 'For a full backup of this container, back up its files instead: choose ', h('b', {}, 'This whole container'), ' in the wizard (a job with the path ', h('code', {}, '/'), '). To image the container\'s volume, run Herakles on the host.')));
    if (!r.root && !win) list.append(h('p', { class: 'muted' }, icon('info'), ' Reading a device needs root: run the daemon (or the backup) as root.'));
  }).catch(e => { clear(list).append(h('div', { class: 'muted' }, e.message)); manual.value = [...chosen].join('\n'); });
  return el;
}

async function chooseDevices(current) {
  let picked = current;
  const ok = await modal('Choose disks and partitions', [
    h('p', { class: 'muted' }, 'Each device is backed up as a raw image; unused space deduplicates to almost nothing.'),
    devicePicker({ multi: true, value: current, onChange: v => { picked = v; } })], [
    { label: 'Cancel', value: false }, { label: 'Use selection', class: 'primary', value: true }]);
  return ok ? picked : null;
}
