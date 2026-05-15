// VMaaS console — vanilla JS frontend.
//
// Talks to the backend at the same origin via /api/v1/*. nginx (or the
// embedded static fileserver) reverse-proxies that path to the Go server.
//
// State is intentionally simple: poll every 2s, re-render from scratch.
// Token lives in localStorage as 'vmaas_token'.

const API = '/api/v1';
const POLL_MS = 2000;

const STATUS_STYLES = {
  pending:    { color: 'border-slate-600 text-slate-300', dot: 'bg-slate-400' },
  allocating: { color: 'border-sky-700 text-sky-300',     dot: 'bg-sky-400' },
  cloning:    { color: 'border-amber-700 text-amber-300', dot: 'bg-amber-400' },
  injecting:  { color: 'border-amber-700 text-amber-300', dot: 'bg-amber-400' },
  starting:   { color: 'border-amber-700 text-amber-300', dot: 'bg-amber-400' },
  ready:      { color: 'border-emerald-700 text-emerald-300', dot: 'bg-emerald-400' },
  failed:     { color: 'border-rose-700 text-rose-300',    dot: 'bg-rose-500' },
  deleting:   { color: 'border-slate-600 text-slate-300', dot: 'bg-slate-400' },
};

function getToken() { return localStorage.getItem('vmaas_token') || ''; }
function setToken(t) { localStorage.setItem('vmaas_token', t || ''); }

async function api(path, opts = {}) {
  const headers = { 'Authorization': `Bearer ${getToken()}`, ...(opts.headers || {}) };
  if (opts.body && !headers['Content-Type']) headers['Content-Type'] = 'application/json';
  const r = await fetch(`${API}${path}`, { ...opts, headers });
  if (r.status === 204) return null;
  if (!r.ok) {
    let msg = `${r.status} ${r.statusText}`;
    try { const j = await r.json(); if (j.error) msg = j.error; } catch {}
    throw new Error(msg);
  }
  return r.json();
}

async function health() {
  try {
    const r = await fetch('/healthz');
    if (!r.ok) throw new Error('unhealthy');
    const j = await r.json();
    document.getElementById('health-dot').className = 'inline-block w-2.5 h-2.5 rounded-full bg-emerald-500';
    document.getElementById('health-label').textContent = `ESXi ${j.version} (build ${j.build})`;
  } catch (e) {
    document.getElementById('health-dot').className = 'inline-block w-2.5 h-2.5 rounded-full bg-rose-500';
    document.getElementById('health-label').textContent = `backend unreachable: ${e.message}`;
  }
}

function ageOf(iso) {
  const d = (Date.now() - new Date(iso).getTime()) / 1000;
  if (d < 60) return `${Math.floor(d)}s`;
  if (d < 3600) return `${Math.floor(d/60)}m ${Math.floor(d%60)}s`;
  return `${Math.floor(d/3600)}h ${Math.floor((d%3600)/60)}m`;
}

function statusPill(s) {
  const sty = STATUS_STYLES[s] || STATUS_STYLES.pending;
  return `<span class="status-pill ${sty.color}"><span class="status-dot ${sty.dot}"></span>${s}</span>`;
}

async function refreshList() {
  try {
    const vms = await api('/vms');
    const tb = document.getElementById('vm-tbody');
    if (!vms || vms.length === 0) {
      tb.innerHTML = `<tr><td colspan="6" class="px-5 py-8 text-center text-slate-500">No VMs yet. Click Provision above.</td></tr>`;
    } else {
      tb.innerHTML = vms.map(v => `
        <tr class="border-t border-slate-800 hover:bg-slate-900/50">
          <td class="px-5 py-3 font-mono text-slate-200">${escape(v.name)}</td>
          <td class="px-5 py-3">${statusPill(v.status)}</td>
          <td class="px-5 py-3 font-mono text-slate-300">${escape(v.ip || '—')}</td>
          <td class="px-5 py-3 text-slate-400">${ageOf(v.created_at)}</td>
          <td class="px-5 py-3 text-rose-400 text-xs max-w-xs truncate" title="${escape(v.error || '')}">${escape(v.error || '')}</td>
          <td class="px-5 py-3 text-right">
            <button data-id="${v.id}" class="info-btn text-xs text-slate-300 hover:text-emerald-400 mr-2">info</button>
            <button data-id="${v.id}" data-name="${escape(v.name)}" class="del-btn text-xs text-slate-300 hover:text-rose-400">delete</button>
          </td>
        </tr>`).join('');
    }
    document.getElementById('list-meta').textContent = `${vms ? vms.length : 0} total`;
    wireRowButtons();
  } catch (e) {
    document.getElementById('vm-tbody').innerHTML = `<tr><td colspan="6" class="px-5 py-8 text-center text-rose-400">${escape(e.message)}</td></tr>`;
  }
}

async function refreshPool() {
  try {
    const p = await api('/pool');
    const grid = document.getElementById('pool-grid');
    const usedKeys = Object.keys(p.used || {});
    const summary = `${usedKeys.length} / ${p.total} used`;
    document.getElementById('pool-summary').textContent = summary;
    const cells = [];
    const all = [...usedKeys, ...(p.free || [])].sort();
    for (const ip of all) {
      const inUse = ip in (p.used || {});
      cells.push(`<div class="pool-cell ${inUse ? 'pool-used' : 'pool-free'}" title="${escape(ip)}${inUse ? ' · '+escape(p.used[ip]) : ''}">${ip.split('.').pop()}</div>`);
    }
    grid.innerHTML = cells.join('');
  } catch (e) {
    document.getElementById('pool-summary').textContent = e.message;
  }
}

function wireRowButtons() {
  document.querySelectorAll('.info-btn').forEach(b => b.onclick = () => showInfo(b.dataset.id));
  document.querySelectorAll('.del-btn').forEach(b => b.onclick = () => doDelete(b.dataset.id, b.dataset.name));
}

async function showInfo(id) {
  try {
    const v = await api(`/vms/${id}`);
    document.getElementById('modal-title').textContent = v.name;
    const ssh = v.ip ? `ssh cuneyt@${v.ip}` : 'no IP yet';
    document.getElementById('modal-body').textContent =
      JSON.stringify(v, null, 2) + `\n\n# SSH (when status=ready):\n${ssh}`;
    document.getElementById('modal-actions').innerHTML =
      `<button id="modal-del" class="px-3 py-1.5 text-sm rounded bg-rose-700 hover:bg-rose-600">Delete</button>` +
      `<button id="modal-ok" class="px-3 py-1.5 text-sm rounded border border-slate-700">Close</button>`;
    document.getElementById('modal-del').onclick = () => { doDelete(v.id, v.name); closeModal('modal-bg'); };
    document.getElementById('modal-ok').onclick = () => closeModal('modal-bg');
    openModal('modal-bg');
  } catch (e) {
    alert(e.message);
  }
}

async function doDelete(id, name) {
  if (!confirm(`Delete VM "${name}"? This powers off, unregisters, deletes files, and frees the IP.`)) return;
  try {
    await api(`/vms/${id}`, { method: 'DELETE' });
    refreshAll();
  } catch (e) {
    alert(`delete failed: ${e.message}`);
  }
}

function openModal(id)  { document.getElementById(id).classList.add('modal-show'); }
function closeModal(id) { document.getElementById(id).classList.remove('modal-show'); }
function escape(s) { return String(s ?? '').replace(/[&<>"']/g, c => ({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;'}[c])); }

document.getElementById('new-form').addEventListener('submit', async (e) => {
  e.preventDefault();
  const name = document.getElementById('new-name').value.trim();
  const out = document.getElementById('new-result');
  out.textContent = 'submitting…';
  try {
    const r = await api('/vms', { method: 'POST', body: JSON.stringify({ name }) });
    out.textContent = `created ${r.id}`;
    document.getElementById('new-name').value = '';
    refreshAll();
  } catch (e2) {
    out.textContent = `error: ${e2.message}`;
  }
});

document.getElementById('token-btn').onclick = () => {
  document.getElementById('token-input').value = getToken();
  openModal('token-bg');
};
document.getElementById('token-cancel').onclick = () => closeModal('token-bg');
document.getElementById('token-save').onclick = () => {
  setToken(document.getElementById('token-input').value.trim());
  closeModal('token-bg');
  refreshAll();
};
document.getElementById('modal-close').onclick = () => closeModal('modal-bg');

function refreshAll() { health(); refreshList(); refreshPool(); }
refreshAll();
setInterval(refreshAll, POLL_MS);
