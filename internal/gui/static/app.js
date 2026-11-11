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
async function api(method, path, body, retried) {
  const opts = { method, headers: { 'X-Herakles': '1' } };
  if (body !== undefined) { opts.body = JSON.stringify(body); opts.headers['Content-Type'] = 'application/json'; }
  const res = await fetch(path, opts);
  let data = null;
  const text = await res.text();
  try { data = text ? JSON.parse(text) : null; } catch (e) { data = { error: text }; }
  if (res.ok) return data;
  if (data && data.needs_password && !retried) {
    const ok = await askPassword(data.repo, data.error);
    if (ok) return api(method, path, body, true);
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
const state = { view: 'dashboard', config: null, info: null, timer: null, snapRepo: null };
const views = {};

function navigate(view) {
  state.view = view;
  document.querySelectorAll('#nav a').forEach(a => a.classList.toggle('active', a.dataset.view === view));
  if (state.timer) { clearInterval(state.timer); state.timer = null; }
  const main = clear($('#main'));
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
    state.info = info;
    renderDaemon(info);
    const active = {};
    runs.active.forEach(r => { active[r.kind + ':' + r.target] = r; });
    $('#dash-sub').textContent = jobs.length + ' jobs · configuration ' + info.config_path;
    clear(jobsEl);
    if (!jobs.length) {
      jobsEl.append(h('div', { class: 'empty' }, 'No backup jobs yet. ',
        h('button', { class: 'primary', onclick: () => navigate(state.config && state.config.repositories && state.config.repositories.length ? 'jobs' : 'repos') }, 'Get started')));
    }
    for (const j of jobs) {
      const run = active['backup:' + j.name];
      const last = j.last;
      const sched = (j.schedule || []).map(s => h('span', { class: 'chip' }, s.cron + ' · ' + (s.type || 'auto')));
      const next = info.daemon ? (info.daemon.schedule || []).find(e => e.kind === 'backup' && e.target === j.name) : null;
      jobsEl.append(h('div', { class: 'card' },
        h('h3', {}, j.name, run ? badge('running') : badge(last && last.status), j.disabled ? h('span', { class: 'chip' }, 'disabled') : null),
        j.description ? h('div', { class: 'meta' }, j.description) : null,
        h('div', { class: 'meta' }, '→ ', j.repository, ' · ', [...(j.paths || []), ...(j.devices || [])].join(', ')),
        h('div', { class: 'meta' }, 'Last run: ', last ? ago(last.start) + ' (' + last.type + ')' : 'never',
          last && last.summary ? ' · ' + fmtBytes(last.summary.total_bytes) + ', ' + fmtBytes(last.summary.bytes_stored) + ' stored' : ''),
        last && last.error ? h('div', { class: 'meta', style: 'color:var(--err)' }, last.error) : null,
        next ? h('div', { class: 'meta' }, 'Next (daemon): ', fmtTime(next.next), ' · ', next.type || 'auto') : null,
        h('div', {}, sched),
        run ? [progressBar(run.progress), h('div', { class: 'meta' }, run.state === 'queued' ? 'queued…' : progressText(run.progress)),
          run.progress && run.progress.current ? h('div', { class: 'meta mono' }, run.progress.current) : null] : null,
        h('div', { class: 'actions' },
          run ? h('button', { class: 'danger', onclick: async () => { await api('POST', '/api/runs/' + run.id + '/cancel'); toast('Cancelling ' + j.name); } }, 'Cancel') : [
            h('button', { class: 'primary', onclick: () => runJob(j.name, 'auto') }, 'Back up now'),
            h('button', { onclick: () => runJob(j.name, 'full') }, 'Full'),
            h('button', { onclick: () => runJob(j.name, 'incremental') }, 'Incremental'),
            h('button', { onclick: () => runJob(j.name, 'differential') }, 'Differential')],
          h('span', { class: 'spacer' }),
          h('button', { class: 'small', onclick: () => editJob(j.name) }, 'Edit'))));
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
      n ? h('div', { style: 'margin-top:6px' }, 'next: ' + n.kind + ' ' + n.target, h('br'), fmtTime(n.next)) : null);
  } else {
    el.append(h('div', {}, h('span', { class: 'badge' }, 'daemon not running')),
      h('div', { style: 'margin-top:6px' }, 'Scheduled backups need ', h('span', { class: 'mono' }, 'herakles daemon install')));
  }
}

// ---------- snapshots ----------
views.snapshots = async main => {
  const cfg = await loadConfig();
  main.append(h('h1', {}, 'Snapshots & Restore'), h('div', { class: 'sub' }, 'Browse backups, restore files or whole snapshots, download files, compare snapshots.'));
  const repos = cfg.repositories || [];
  if (!repos.length) { main.append(h('div', { class: 'empty' }, 'No repositories configured.')); return; }
  if (!state.snapRepo || !repos.find(r => r.name === state.snapRepo)) state.snapRepo = repos[0].name;
  const sel = h('select', { style: 'width:auto', onchange: e => { state.snapRepo = e.target.value; load(); } },
    repos.map(r => h('option', { value: r.name, selected: r.name === state.snapRepo }, r.name)));
  const jobFilter = h('input', { placeholder: 'filter (job, host, tag, id)…', style: 'width:240px', oninput: () => renderTable() });
  const diffBtn = h('button', { disabled: true, onclick: () => diffSelected() }, 'Compare selected');
  const tableBox = h('div');
  const browser = h('div');
  main.append(h('div', { class: 'toolbar' }, 'Repository', sel, jobFilter, h('span', { class: 'spacer' }), diffBtn,
    h('button', { onclick: () => load() }, 'Reload')), tableBox, browser);
  let snaps = [];
  const picked = new Set();
  const renderTable = () => {
    clear(tableBox);
    const q = jobFilter.value.toLowerCase();
    const list = snaps.filter(s => !q || [s.id, s.job, s.hostname, s.type, ...(s.tags || [])].join(' ').toLowerCase().includes(q));
    if (!list.length) { tableBox.append(h('div', { class: 'empty' }, snaps.length ? 'No snapshot matches the filter.' : 'No snapshots in this repository yet.')); return; }
    tableBox.append(h('table', {},
      h('tr', {}, ['', 'ID', 'Time', 'Job', 'Host', 'Type', 'Size', 'Stored', 'Paths', ''].map(t => h('th', {}, t))),
      list.map(s => h('tr', {},
        h('td', {}, h('input', { type: 'checkbox', style: 'width:auto', checked: picked.has(s.id), onchange: e => { e.target.checked ? picked.add(s.id) : picked.delete(s.id); diffBtn.disabled = picked.size !== 2; } })),
        h('td', { class: 'mono' }, s.id.slice(0, 8)), h('td', {}, fmtTime(s.time)), h('td', {}, s.job || '-'), h('td', {}, s.hostname),
        h('td', {}, h('span', { class: 'chip' }, s.type)), h('td', {}, s.summary ? fmtBytes(s.summary.total_bytes) : '-'),
        h('td', {}, s.summary ? fmtBytes(s.summary.bytes_stored) : '-'), h('td', { class: 'wrap' }, (s.paths || []).join(', ')),
        h('td', {}, h('button', { class: 'small', onclick: () => browse(s, '/') }, 'Browse'), ' ',
          h('button', { class: 'small', onclick: () => restoreDlg(s, '/') }, 'Restore'), ' ',
          h('button', { class: 'small danger', onclick: () => forget(s) }, 'Forget'))))));
  };
  const load = async () => {
    clear(tableBox).append(h('div', { class: 'muted' }, 'Loading…'));
    clear(browser);
    picked.clear(); diffBtn.disabled = true;
    try { snaps = await api('GET', '/api/repos/' + encodeURIComponent(state.snapRepo) + '/snapshots'); }
    catch (e) { clear(tableBox).append(h('div', { class: 'empty' }, e.message)); return; }
    renderTable();
  };
  const forget = async s => {
    if (!await confirmDlg('Forget snapshot', 'Remove snapshot ' + s.id.slice(0, 8) + ' (' + fmtTime(s.time) + ')? Run prune afterwards to free space.', true)) return;
    try { await api('DELETE', '/api/repos/' + encodeURIComponent(state.snapRepo) + '/snapshots/' + s.id); toast('Snapshot forgotten'); load(); }
    catch (e) { toast(e.message, true); }
  };
  const browse = async (s, path) => {
    clear(browser);
    const base = '/api/repos/' + encodeURIComponent(state.snapRepo) + '/snapshots/' + s.id;
    let entries;
    try { entries = await api('GET', base + '/tree?path=' + encodeURIComponent(path)); }
    catch (e) { toast(e.message, true); return; }
    const parts = path.split('/').filter(Boolean);
    const crumbs = h('div', { class: 'crumbs' }, h('a', { onclick: () => browse(s, '/') }, s.id.slice(0, 8)),
      parts.map((p, i) => [' / ', h('a', { onclick: () => browse(s, '/' + parts.slice(0, i + 1).join('/')) }, p)]));
    browser.append(h('h2', {}, 'Browse snapshot from ' + fmtTime(s.time)),
      h('div', { class: 'toolbar' }, crumbs, h('span', { class: 'spacer' }),
        h('button', { onclick: () => restoreDlg(s, path) }, 'Restore this folder…'),
        h('a', { class: 'btn', href: base + '/download?path=' + encodeURIComponent(path) }, 'Download as .tar')),
      h('table', {}, h('tr', {}, ['Name', 'Size', 'Modified', 'Mode', ''].map(t => h('th', {}, t))),
        path !== '/' ? h('tr', {}, h('td', { colspan: 5 }, h('span', { class: 'entry-name dir', onclick: () => browse(s, '/' + parts.slice(0, -1).join('/')) }, '⬑ ..'))) : null,
        entries.map(e => h('tr', {},
          h('td', { class: 'wrap' }, h('span', { class: 'entry-name' + (e.type === 'dir' ? ' dir' : ''), onclick: () => e.type === 'dir' && browse(s, e.path) },
            (e.type === 'dir' ? '📁 ' : e.type === 'device' ? '💽 ' : e.type === 'symlink' ? '🔗 ' : '📄 ') + e.name + (e.link ? ' → ' + e.link : ''))),
          h('td', {}, e.type === 'dir' ? '' : fmtBytes(e.size)), h('td', {}, fmtTime(e.mtime)), h('td', { class: 'mono' }, e.mode),
          h('td', {}, h('button', { class: 'small', onclick: () => restoreDlg(s, e.path) }, 'Restore…'), ' ',
            h('a', { class: 'btn small', href: base + '/download?path=' + encodeURIComponent(e.path) }, 'Download'))))));
    browser.scrollIntoView({ behavior: 'smooth' });
  };
  const restoreDlg = async (s, path) => {
    const target = h('input', { value: (state.info && state.info.os === 'windows' ? 'C:\\herakles-restore' : '~/herakles-restore') });
    const ow = h('select', {}, h('option', { value: 'always' }, 'Overwrite existing files'), h('option', { value: 'if-changed' }, 'Only if different'), h('option', { value: 'never' }, 'Never overwrite'));
    const inc = h('input', { placeholder: 'e.g. *.pdf; **/Documents' });
    const verify = h('input', { type: 'checkbox', checked: true });
    await modal('Restore ' + (path === '/' ? 'snapshot ' + s.id.slice(0, 8) : path), [
      h('label', {}, 'Target directory (the selected item is restored inside it)'), target,
      h('label', {}, 'Existing files'), ow,
      h('label', {}, 'Only restore matching paths (optional, ; separated)'), inc,
      h('label', { class: 'check' }, verify, 'Verify restored data')], [
      { label: 'Cancel' },
      { label: 'Restore', class: 'primary', action: async () => {
        toast('Restoring…');
        const r = await api('POST', '/api/repos/' + encodeURIComponent(state.snapRepo) + '/snapshots/' + s.id + '/restore',
          { path, target: target.value, overwrite: ow.value, verify: verify.checked, includes: inc.value.split(';').map(x => x.trim()).filter(Boolean) });
        const res = r.result;
        toast('Restored ' + res.files + ' files (' + fmtBytes(res.bytes) + ')' + (res.skipped ? ', skipped ' + res.skipped : '') + (res.errors ? ', ' + res.errors + ' errors' : ''), res.errors > 0);
        if (r.errors.length) await modal('Restore errors', h('pre', { class: 'mono' }, r.errors.join('\n')), [{ label: 'Close' }]);
      } }]);
  };
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
    h('div', { class: 'toolbar' }, h('button', { class: 'primary', onclick: () => editJob(null) }, '+ New job')));
  const jobs = cfg.jobs || [];
  if (!jobs.length) { main.append(h('div', { class: 'empty' }, 'No jobs defined.')); return; }
  main.append(h('table', {}, h('tr', {}, ['Job', 'Repository', 'Sources', 'Schedule', 'Retention', ''].map(t => h('th', {}, t))),
    jobs.map(j => h('tr', {},
      h('td', {}, h('b', {}, j.name), j.disabled ? ' (disabled)' : ''), h('td', {}, j.repository),
      h('td', { class: 'wrap' }, [...(j.paths || []), ...(j.devices || [])].join(', ')),
      h('td', { class: 'wrap' }, (j.schedule || []).map(s => h('span', { class: 'chip' }, s.cron + ' · ' + (s.type || 'auto')))),
      h('td', { class: 'wrap' }, retentionText(j.retention)),
      h('td', {}, h('button', { class: 'small', onclick: () => editJob(j.name) }, 'Edit'), ' ',
        h('button', { class: 'small danger', onclick: async () => {
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

const lines = v => (v || []).join('\n');
const unlines = v => v.split('\n').map(x => x.trim()).filter(Boolean);
const num = el => parseInt(el.value, 10) || 0;

async function editJob(name) {
  const cfg = await loadConfig();
  const repos = cfg.repositories || [];
  if (!repos.length) { toast('Add a repository first', true); navigate('repos'); return; }
  const orig = name ? cfg.jobs.find(j => j.name === name) : null;
  const j = orig ? JSON.parse(JSON.stringify(orig)) : { repository: repos[0].name, schedule: [{ cron: '0 2 * * *', type: 'auto' }], full_interval: '7d', retention: { keep_daily: 7, keep_weekly: 4, keep_monthly: 12 } };
  const r = j.retention || {};
  const f = {
    name: h('input', { value: j.name || '' }), description: h('input', { value: j.description || '' }),
    repository: h('select', {}, repos.map(x => h('option', { value: x.name, selected: x.name === j.repository }, x.name + ' — ' + x.location))),
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
      h('div', { class: 'row' }, h('div', {}, h('label', {}, 'Paths (one per line)'), f.paths), h('div', {}, h('label', {}, 'Disks / partitions as raw images (one per line)'), f.devices))),
    h('fieldset', {}, h('legend', {}, 'Exclusions'),
      h('label', {}, 'Exclude patterns (one per line; *.tmp, **/cache, /abs/path)'), f.exclude,
      h('div', { class: 'row' }, h('div', {}, h('label', {}, 'Skip dirs containing'), f.excludeIfPresent), h('div', {}, h('label', {}, 'Skip files larger than'), f.excludeLarger)),
      h('label', { class: 'check' }, f.excludeCaches, 'Skip cache directories (CACHEDIR.TAG)'), h('label', { class: 'check' }, f.oneFS, 'Stay on one file system')),
    h('fieldset', {}, h('legend', {}, 'Schedule'),
      h('div', { class: 'toolbar' }, h('button', { onclick: () => addSched({ cron: '', type: 'auto' }) }, '+ Add schedule'), presets),
      schedBox,
      h('div', { class: 'row' }, h('div', {}, h('label', {}, 'Make "auto" backups full every'), f.fullInterval), h('div', {}, h('label', {}, 'Tags'), f.tags)),
      h('div', { class: 'hint' }, 'full: re-reads everything · incremental: changes since last snapshot · differential: changes since last full · all snapshots are complete and deduplicated.'),
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
      const c = await loadConfig();
      c.jobs = c.jobs || [];
      const i = orig ? c.jobs.findIndex(x => x.name === orig.name) : -1;
      if (i >= 0) c.jobs[i] = nj; else c.jobs.push(nj);
      await saveConfig(c);
      navigate(state.view);
    } }]);
}

// ---------- repositories ----------
views.repos = async main => {
  const cfg = await loadConfig();
  main.append(h('h1', {}, 'Repositories'), h('div', { class: 'sub' }, 'Encrypted, deduplicated storage locations: local disks, network shares, SFTP or a Herakles server.'),
    h('div', { class: 'toolbar' }, h('button', { class: 'primary', onclick: () => editRepo(null) }, '+ Add repository')));
  const repos = cfg.repositories || [];
  if (!repos.length) { main.append(h('div', { class: 'empty' }, 'No repositories yet. Add one to start backing up.')); return; }
  const grid = h('div', { class: 'grid' });
  main.append(grid);
  for (const r of repos) {
    const statsBox = h('div', { class: 'meta' });
    const unlockWith = [r.password ? 'inline password' : r.password_file ? 'password file' : r.password_env ? 'env ' + r.password_env : r.password_command ? 'password command' : null, r.key_file ? 'key file' : null].filter(Boolean).join(' + ') || 'asks for password';
    const m = r.maintenance || {};
    grid.append(h('div', { class: 'card' },
      h('h3', {}, r.name), h('div', { class: 'meta mono' }, r.location),
      h('div', { class: 'meta' }, 'Unlock: ', unlockWith, ' · compression: ', r.compression || 'auto'),
      m.prune_schedule || m.check_schedule ? h('div', { class: 'meta' }, 'Maintenance: ', [m.prune_schedule ? 'prune ' + m.prune_schedule : '', m.check_schedule ? 'check ' + m.check_schedule : ''].filter(Boolean).join(' · ')) : null,
      statsBox,
      h('div', { class: 'actions' },
        h('button', { onclick: async () => {
          try {
            const s = await api('GET', '/api/repos/' + encodeURIComponent(r.name) + '/stats');
            const st = s.stats;
            clear(statsBox).append(st.snapshots + ' snapshots · ' + fmtBytes(st.raw_bytes) + ' unique data · ' + fmtBytes(st.stored_bytes) + ' stored (' + (st.stored_bytes ? (st.raw_bytes / st.stored_bytes).toFixed(2) : '-') + 'x compression)');
          } catch (e) { toast(e.message, true); }
        } }, 'Statistics'),
        h('button', { onclick: () => initRepo(r) }, 'Initialize'),
        h('button', { onclick: () => task(r.name, 'check') }, 'Check'),
        h('button', { onclick: async () => { if (await confirmDlg('Prune', 'Apply all retention policies of jobs using ' + r.name + ' and remove unreferenced data?')) task(r.name, 'prune'); } }, 'Prune'),
        h('span', { class: 'spacer' }),
        h('button', { class: 'small', onclick: () => editRepo(r.name) }, 'Edit'),
        h('button', { class: 'small danger', onclick: async () => {
          if (!await confirmDlg('Remove repository', 'Remove ' + r.name + ' from the configuration? Stored backups are NOT deleted.', true)) return;
          const c = await loadConfig(); c.repositories = c.repositories.filter(x => x.name !== r.name);
          try { await saveConfig(c); navigate('repos'); } catch (e) { toast(e.message, true); }
        } }, 'Remove'))));
  }
};

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
views.settings = async main => {
  const data = await api('GET', '/api/config/yaml');
  const ta = h('textarea', { rows: 32, spellcheck: 'false' }, data.yaml);
  main.append(h('h1', {}, 'Configuration'), h('div', { class: 'sub' }, 'Full YAML configuration (', h('span', { class: 'mono' }, data.path), '). Comments are preserved when saving here. Everything — notifications, daemon and server settings — can be edited.'),
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

navigate('dashboard');
