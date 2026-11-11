'use strict';
// Herakles web sign-in: password, passkey and first-administrator setup.

const $ = s => document.querySelector(s);
const b64url = buf => btoa(String.fromCharCode(...new Uint8Array(buf))).replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '');
function fromB64url(s) {
  const b = atob((s + '==='.slice((s.length + 3) % 4)).replace(/-/g, '+').replace(/_/g, '/'));
  return Uint8Array.from(b, c => c.charCodeAt(0)).buffer;
}

async function post(path, body) {
  const res = await fetch(path, { method: 'POST', headers: { 'X-Herakles': '1', 'Content-Type': 'application/json' }, body: JSON.stringify(body || {}) });
  const data = await res.json().catch(() => ({}));
  if (!res.ok) throw new Error(data.error || res.statusText);
  return data;
}

function showError(msg) {
  const el = $('#error');
  el.textContent = msg;
  el.hidden = !msg;
}

function next() {
  const n = new URLSearchParams(location.search).get('next');
  location.replace(n && n.startsWith('/') && !n.startsWith('//') ? n : '/');
}

async function passkeyLogin() {
  showError('');
  try {
    const opts = await post('/auth/passkey/begin');
    const pk = opts.publicKey;
    pk.challenge = fromB64url(pk.challenge);
    (pk.allowCredentials || []).forEach(c => { c.id = fromB64url(c.id); });
    const cred = await navigator.credentials.get({ publicKey: pk });
    const r = cred.response;
    await post('/auth/passkey/finish', {
      id: cred.id, rawId: b64url(cred.rawId), type: cred.type,
      response: { clientDataJSON: b64url(r.clientDataJSON), authenticatorData: b64url(r.authenticatorData),
        signature: b64url(r.signature), userHandle: r.userHandle ? b64url(r.userHandle) : null },
    });
    next();
  } catch (e) {
    if (e.name !== 'NotAllowedError') showError(e.message);
  }
}

async function init() {
  $('#login-host').textContent = location.host;
  const st = await (await fetch('/auth/state')).json();
  if (st.user) return next();
  const token = new URLSearchParams(location.hash.slice(1)).get('setup');
  if (st.setup) {
    (token ? $('#setup') : $('#need-setup')).hidden = false;
  } else {
    $('#login').hidden = false;
    if (!window.PublicKeyCredential) { $('#passkey').hidden = true; $('#passkey-or').hidden = true; }
    $('#name').focus();
  }
  $('#login').addEventListener('submit', async e => {
    e.preventDefault();
    showError('');
    try { await post('/auth/login', { name: $('#name').value.trim(), password: $('#password').value }); next(); }
    catch (err) { showError(err.message); $('#password').select(); }
  });
  $('#passkey').addEventListener('click', passkeyLogin);
  $('#setup').addEventListener('submit', async e => {
    e.preventDefault();
    showError('');
    if ($('#s-password').value !== $('#s-password2').value) return showError('The passwords do not match.');
    try {
      await post('/auth/setup', { token, name: $('#s-name').value.trim(), password: $('#s-password').value });
      history.replaceState(null, '', '/login.html');
      next();
    } catch (err) { showError(err.message); }
  });
}
init().catch(e => showError(e.message));
