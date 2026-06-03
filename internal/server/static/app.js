'use strict';

// ─── API client ───────────────────────────────────────────────────────────────
const api = (() => {
  function token() { return localStorage.getItem('igloo_token'); }

  async function req(method, path, body) {
    const headers = { 'Content-Type': 'application/json' };
    if (token()) headers['Authorization'] = 'Bearer ' + token();
    const res = await fetch(path, { method, headers, body: body ? JSON.stringify(body) : undefined });
    if (res.status === 204) return null;
    const data = await res.json();
    if (!res.ok) throw new Error(data.error || `HTTP ${res.status}`);
    return data;
  }

  return {
    auth: {
      register: (name, email, password) => req('POST', '/api/auth/register', { name, email, password }),
      login:    (email, password)        => req('POST', '/api/auth/login',    { email, password }),
      me:       ()                       => req('GET',  '/api/auth/me'),
      saveSSHKey: (key)                  => req('PUT',  '/api/auth/me/ssh-key', { ssh_public_key: key }),
    },
    workspaces: {
      list:     ()             => req('GET',    '/api/workspaces'),
      create:   (name, tplId) => req('POST',   '/api/workspaces', { name, template_id: tplId }),
      get:      (id)           => req('GET',    `/api/workspaces/${id}`),
      start:    (id)           => req('POST',   `/api/workspaces/${id}/start`),
      stop:     (id)           => req('POST',   `/api/workspaces/${id}/stop`),
      del:      (id)           => req('DELETE', `/api/workspaces/${id}`),
      terminal: (id)           => req('GET',    `/api/workspaces/${id}/terminal`),
      vsCodeToken: (id)        => req('GET',    `/api/workspaces/${id}/vscode-token`),
    },
    templates: {
      list:   ()         => req('GET',  '/api/templates'),
      files:  (id)       => req('GET',  `/api/templates/${id}/files`),
      create: (body)     => req('POST', '/api/templates', body),
      update: (id, body) => req('PUT',  `/api/templates/${id}`, body),
    },
    admin: {
      listUsers:  ()          => req('GET',    '/api/admin/users'),
      updateUser: (id, body)  => req('PUT',    `/api/admin/users/${id}`, body),
      deleteUser: (id)        => req('DELETE', `/api/admin/users/${id}`),
    },
    workspaceLogs: (id) => fetch('/api/workspaces/' + id + '/logs', {
      headers: { 'Authorization': 'Bearer ' + localStorage.getItem('igloo_token') }
    }).then(r => r.text()),
    agentLogs: (id) => fetch('/api/workspaces/' + id + '/agent-logs', {
      headers: { 'Authorization': 'Bearer ' + localStorage.getItem('igloo_token') }
    }).then(r => r.ok ? r.text() : r.json().then(d => Promise.reject(new Error(d.error)))),
  };
})();

// ─── State ────────────────────────────────────────────────────────────────────
const state = { user: null, workspaces: [], templates: [], view: 'igloos', poll: null };

// ─── Terminal ─────────────────────────────────────────────────────────────────
const term = (() => {
  let xterm, fit, ws;

  function init() {
    xterm = new Terminal({
      cursorBlink: true,
      scrollback: 10000,
      fontFamily: "'JetBrains Mono', 'Cascadia Code', monospace",
      fontSize: 13,
      theme: {
        background: '#020c18', foreground: '#c8e8ff', cursor: '#00d4ff',
        black: '#071828', red: '#ff3d6b', green: '#00ffb3', yellow: '#ffc400',
        blue: '#00d4ff', magenta: '#9d5cff', cyan: '#00e5cc', white: '#c8e8ff',
        brightBlack: '#1c3550',
      },
    });
    fit = new FitAddon.FitAddon();
    xterm.loadAddon(fit);
    xterm.open(document.getElementById('xterm-container'));
    fit.fit();
    window.addEventListener('resize', () => fit.fit());
    xterm.onData(data => {
      if (ws && ws.readyState === WebSocket.OPEN)
        ws.send(JSON.stringify({ type: 'pty_data', data: btoa(String.fromCharCode(...new TextEncoder().encode(data))) }));
    });
    xterm.onResize(({ cols, rows }) => {
      if (ws && ws.readyState === WebSocket.OPEN)
        ws.send(JSON.stringify({ type: 'pty_resize', cols, rows }));
    });
  }

  function connect(workspaceId, workspaceName, wsUrl, wsToken) {
    document.getElementById('terminal-workspace-name').textContent = workspaceName;
    if (ws) ws.close();
    xterm.clear();
    xterm.writeln('\x1b[2m[igloo] connecting…\x1b[0m');
    const url = wsUrl.replace(/^http/, 'ws') + `?token=${encodeURIComponent(wsToken)}`;
    ws = new WebSocket(url);
    ws.onopen = () => ws.send(JSON.stringify({ type: 'pty_spawn', cols: xterm.cols, rows: xterm.rows }));
    ws.onmessage = evt => {
      let msg; try { msg = JSON.parse(evt.data); } catch { return; }
      if (msg.type === 'pty_output' && msg.data) {
        try { xterm.write(Uint8Array.from(atob(msg.data), c => c.charCodeAt(0))); }
        catch { xterm.write(msg.data); }
      } else if (msg.type === 'pty_ready') { xterm.clear(); xterm.focus(); }
      else if (msg.type === 'pty_exit')  { xterm.writeln('\r\n\x1b[2m[session ended]\x1b[0m'); }
      else if (msg.type === 'pty_error') { xterm.writeln(`\r\n\x1b[31m[error: ${msg.data}]\x1b[0m`); }
    };
    ws.onclose = () => xterm.writeln('\r\n\x1b[2m[disconnected]\x1b[0m');
  }

  function open(id, name) {
    api.workspaces.terminal(id)
      .then(({ token: t, ws_url }) => { openPanel(); connect(id, name, ws_url, t); })
      .catch(e => toast(e.message, 'error'));
  }
  function openPanel() {
    document.getElementById('terminal-panel').classList.add('open');
    setTimeout(() => { if (fit) fit.fit(); }, 270);
  }
  function closePanel() {
    document.getElementById('terminal-panel').classList.remove('open');
    if (ws) { ws.close(); ws = null; }
  }
  return { init, open, close: closePanel };
})();

// ─── Toast ────────────────────────────────────────────────────────────────────
function toast(msg, type = 'info') {
  const el = document.createElement('div');
  el.className = `toast ${type}`;
  el.textContent = msg;
  document.getElementById('toast-stack').appendChild(el);
  setTimeout(() => el.remove(), 3500);
}

// ─── Icons ────────────────────────────────────────────────────────────────────
const icon = {
  igloo: `<svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M3 12 Q3 4 12 4 Q21 4 21 12"/><line x1="1" y1="12" x2="23" y2="12"/><path d="M9 12 Q9 8 12 8 Q15 8 15 12"/></svg>`,
  templates: `<svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><rect x="3" y="3" width="7" height="7" rx="1"/><rect x="14" y="3" width="7" height="7" rx="1"/><rect x="3" y="14" width="7" height="7" rx="1"/><rect x="14" y="14" width="7" height="7" rx="1"/></svg>`,
  settings: `<svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><circle cx="12" cy="12" r="3"/><path d="M19.4 15a1.65 1.65 0 0 0 .33 1.82l.06.06a2 2 0 0 1-2.83 2.83l-.06-.06a1.65 1.65 0 0 0-1.82-.33 1.65 1.65 0 0 0-1 1.51V21a2 2 0 0 1-4 0v-.09A1.65 1.65 0 0 0 9 19.4a1.65 1.65 0 0 0-1.82.33l-.06.06a2 2 0 0 1-2.83-2.83l.06-.06A1.65 1.65 0 0 0 4.68 15a1.65 1.65 0 0 0-1.51-1H3a2 2 0 0 1 0-4h.09A1.65 1.65 0 0 0 4.6 9a1.65 1.65 0 0 0-.33-1.82l-.06-.06a2 2 0 0 1 2.83-2.83l.06.06A1.65 1.65 0 0 0 9 4.68a1.65 1.65 0 0 0 1-1.51V3a2 2 0 0 1 4 0v.09a1.65 1.65 0 0 0 1 1.51 1.65 1.65 0 0 0 1.82-.33l.06-.06a2 2 0 0 1 2.83 2.83l-.06.06A1.65 1.65 0 0 0 19.4 9a1.65 1.65 0 0 0 1.51 1H21a2 2 0 0 1 0 4h-.09a1.65 1.65 0 0 0-1.51 1z"/></svg>`,
  users:    `<svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><path d="M17 21v-2a4 4 0 0 0-4-4H5a4 4 0 0 0-4 4v2"/><circle cx="9" cy="7" r="4"/><path d="M23 21v-2a4 4 0 0 0-3-3.87"/><path d="M16 3.13a4 4 0 0 1 0 7.75"/></svg>`,
  plus:     `<svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.5"><line x1="12" y1="5" x2="12" y2="19"/><line x1="5" y1="12" x2="19" y2="12"/></svg>`,
  logo:     `<svg width="20" height="20" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M3 12 Q3 4 12 4 Q21 4 21 12"/><line x1="1" y1="12" x2="23" y2="12"/><path d="M9 12 Q9 8 12 8 Q15 8 15 12"/></svg>`,
};

// ─── Eskimo building igloo animation ──────────────────────────────────────────
function buildingAnimation() {
  // Eskimo on left, igloo growing on right. Pure SVG + CSS.
  return `<div class="igloo-anim-wrap">
  <svg class="igloo-build-svg" viewBox="0 0 200 90" xmlns="http://www.w3.org/2000/svg">
    <!-- Ground shadow -->
    <ellipse cx="100" cy="84" rx="85" ry="5" fill="#041220" opacity="0.6"/>

    <!-- Igloo structure — blocks appear one by one -->
    <!-- Row 1: base (5 blocks) -->
    <rect class="blk blk-1"  x="82"  y="63" width="16" height="12" rx="2.5" fill="none" stroke="#00d4ff" stroke-width="1.2" opacity="0.7"/>
    <rect class="blk blk-2"  x="100" y="63" width="16" height="12" rx="2.5" fill="none" stroke="#00d4ff" stroke-width="1.2" opacity="0.7"/>
    <rect class="blk blk-3"  x="118" y="63" width="16" height="12" rx="2.5" fill="none" stroke="#00d4ff" stroke-width="1.2" opacity="0.7"/>
    <rect class="blk blk-4"  x="136" y="65" width="14" height="10" rx="2.5" fill="none" stroke="#00d4ff" stroke-width="1.2" opacity="0.6"/>
    <rect class="blk blk-1"  x="79"  y="66" width="11" height="9"  rx="2"   fill="none" stroke="#00d4ff" stroke-width="1"   opacity="0.5"/>
    <!-- Row 2 -->
    <rect class="blk blk-5"  x="86"  y="51" width="15" height="11" rx="2.5" fill="none" stroke="#00d4ff" stroke-width="1.2" opacity="0.7"/>
    <rect class="blk blk-6"  x="103" y="51" width="15" height="11" rx="2.5" fill="none" stroke="#00d4ff" stroke-width="1.2" opacity="0.7"/>
    <rect class="blk blk-7"  x="120" y="52" width="14" height="10" rx="2.5" fill="none" stroke="#00d4ff" stroke-width="1.2" opacity="0.6"/>
    <rect class="blk blk-8"  x="134" y="55" width="11" height="9"  rx="2"   fill="none" stroke="#00d4ff" stroke-width="1"   opacity="0.5"/>
    <!-- Row 3 -->
    <rect class="blk blk-9"  x="91"  y="40" width="14" height="10" rx="2.5" fill="none" stroke="#00d4ff" stroke-width="1.2" opacity="0.7"/>
    <rect class="blk blk-10" x="107" y="40" width="14" height="10" rx="2.5" fill="none" stroke="#00d4ff" stroke-width="1.2" opacity="0.7"/>
    <rect class="blk blk-11" x="123" y="42" width="12" height="9"  rx="2"   fill="none" stroke="#00d4ff" stroke-width="1"   opacity="0.6"/>
    <!-- Cap -->
    <ellipse class="blk blk-12" cx="110" cy="36" rx="11" ry="6" fill="none" stroke="#00d4ff" stroke-width="1.2" opacity="0.7"/>

    <!-- Door -->
    <path class="blk blk-1" d="M96 75 Q96 68 101 68 Q106 68 106 75" fill="#020c18" stroke="#00d4ff" stroke-width="1" opacity="0.6"/>

    <!-- Igloo glow fill (subtle) -->
    <path class="blk blk-12" d="M82 75 Q82 34 110 34 Q138 34 138 75 Z" fill="rgba(0,212,255,0.03)" stroke="none"/>

    <!-- Eskimo body -->
    <g class="eskimo">
      <!-- Coat body -->
      <ellipse cx="36" cy="68" rx="16" ry="17" fill="#0d4a8a"/>
      <!-- Fur trim -->
      <ellipse cx="36" cy="82" rx="14" ry="4" fill="#1a6dc0" opacity="0.7"/>
      <!-- Hood -->
      <circle cx="36" cy="46" r="13" fill="#0d4a8a"/>
      <!-- Face -->
      <circle cx="36" cy="46" r="9"  fill="#e8c09a"/>
      <!-- Hood rim (fur) -->
      <path d="M26 44 Q36 36 46 44" fill="#1a6dc0" stroke="none" opacity="0.8"/>
      <!-- Eyes (happy) -->
      <circle cx="32.5" cy="44" r="1.8" fill="#1a0800"/>
      <circle cx="39.5" cy="44" r="1.8" fill="#1a0800"/>
      <!-- Eye shine -->
      <circle cx="33.3" cy="43.3" r="0.6" fill="white"/>
      <circle cx="40.3" cy="43.3" r="0.6" fill="white"/>
      <!-- Smile -->
      <path d="M32 49 Q36 53 40 49" stroke="#8b4513" stroke-width="1.2" fill="none" stroke-linecap="round"/>
      <!-- Rosy cheeks -->
      <circle cx="29" cy="48" r="3" fill="#ff9999" opacity="0.25"/>
      <circle cx="43" cy="48" r="3" fill="#ff9999" opacity="0.25"/>
      <!-- Arm (animated, swinging to place block) -->
      <g class="eskimo-arm">
        <rect x="50" y="54" width="22" height="8" rx="4" fill="#0d4a8a"/>
        <!-- Block in hand -->
        <rect x="70" y="48" width="13" height="9" rx="2" fill="#00d4ff" opacity="0.9" stroke="#00d4ff" stroke-width="0.8"/>
        <!-- Hand holding block -->
        <circle cx="68" cy="58" r="4" fill="#e8c09a"/>
      </g>
      <!-- Other arm (static) -->
      <rect x="16" y="62" width="14" height="7" rx="3.5" fill="#0d4a8a" transform="rotate(20 20 65)"/>
    </g>

    <!-- Snow sparkles -->
    <circle cx="65" cy="30" r="1.5" fill="#00d4ff" opacity="0.4">
      <animate attributeName="opacity" values="0.4;0;0.4" dur="2.3s" repeatCount="indefinite"/>
    </circle>
    <circle cx="155" cy="40" r="1" fill="#00d4ff" opacity="0.3">
      <animate attributeName="opacity" values="0.3;0;0.3" dur="1.8s" begin="0.5s" repeatCount="indefinite"/>
    </circle>
    <circle cx="145" cy="25" r="1.5" fill="#00ffb3" opacity="0.3">
      <animate attributeName="opacity" values="0.3;0;0.3" dur="2.8s" begin="1s" repeatCount="indefinite"/>
    </circle>
  </svg>
  <div style="font-size:11px;color:var(--amber);font-family:var(--mono);letter-spacing:.05em;margin-top:-4px;text-align:center">building your igloo…</div>
  </div>`;
}

// ─── Sad eskimo error animation ────────────────────────────────────────────────
function errorAnimation() {
  return `<div class="igloo-anim-wrap">
  <svg class="igloo-error-svg" viewBox="0 0 200 90" xmlns="http://www.w3.org/2000/svg">
    <!-- Ground -->
    <ellipse cx="100" cy="84" rx="80" ry="5" fill="#041220" opacity="0.6"/>

    <!-- Broken igloo pieces -->
    <g class="broken-piece">
      <rect x="120" y="62" width="16" height="11" rx="2.5" fill="none" stroke="#ff3d6b" stroke-width="1.2" opacity="0.5" transform="rotate(18 128 67)"/>
    </g>
    <g class="broken-piece">
      <rect x="138" y="58" width="13" height="9" rx="2.5" fill="none" stroke="#ff3d6b" stroke-width="1.2" opacity="0.4" transform="rotate(-25 144 62)"/>
    </g>
    <g class="broken-piece">
      <ellipse cx="133" cy="50" rx="10" ry="5" fill="none" stroke="#ff3d6b" stroke-width="1.2" opacity="0.35" transform="rotate(10 133 50)"/>
    </g>
    <!-- Crack line -->
    <path d="M118 75 L123 65 L117 60 L122 48" stroke="#ff3d6b" stroke-width="1.2" fill="none" opacity="0.4" stroke-linecap="round" stroke-linejoin="round"/>
    <!-- Base stub (what's left) -->
    <rect x="90" y="67" width="16" height="11" rx="2.5" fill="none" stroke="#ff3d6b" stroke-width="1.2" opacity="0.4"/>
    <rect x="108" y="67" width="14" height="11" rx="2.5" fill="none" stroke="#ff3d6b" stroke-width="1.2" opacity="0.35"/>

    <!-- Sad eskimo -->
    <g class="eskimo-sad-body">
      <!-- Coat body (hunched) -->
      <ellipse cx="44" cy="70" rx="16" ry="15" fill="#0d4a8a"/>
      <!-- Hood -->
      <circle cx="44" cy="50" r="13" fill="#0d4a8a"/>
      <!-- Face -->
      <circle cx="44" cy="50" r="9"  fill="#e8c09a"/>
      <!-- Hood rim -->
      <path d="M34 48 Q44 40 54 48" fill="#1a6dc0" opacity="0.8"/>
      <!-- Sad eyes (droopy) -->
      <path d="M40 47 Q41.5 45 43 47" stroke="#1a0800" stroke-width="1.2" fill="none" stroke-linecap="round"/>
      <path d="M45 47 Q46.5 45 48 47" stroke="#1a0800" stroke-width="1.2" fill="none" stroke-linecap="round"/>
      <!-- Frown -->
      <path d="M40 54 Q44 50 48 54" stroke="#8b4513" stroke-width="1.2" fill="none" stroke-linecap="round"/>
      <!-- Tear -->
      <ellipse class="tear" cx="41" cy="55" rx="1.5" ry="2.5" fill="#00d4ff" opacity="0.7"/>
      <!-- Arms hanging down -->
      <rect x="26" y="66" width="14" height="6" rx="3" fill="#0d4a8a" transform="rotate(40 28 66)"/>
      <rect x="56" y="66" width="14" height="6" rx="3" fill="#0d4a8a" transform="rotate(-40 68 66)"/>
    </g>

    <!-- Red X marks -->
    <text x="155" y="52" font-size="22" fill="#ff3d6b" opacity="0.6" font-family="monospace">✕</text>
  </svg>
  <div style="font-size:11px;color:var(--red);font-family:var(--mono);letter-spacing:.05em;margin-top:-4px;text-align:center">build failed — check logs</div>
  </div>`;
}

// ─── Helpers ──────────────────────────────────────────────────────────────────
function esc(s) {
  return String(s || '').replace(/&/g,'&amp;').replace(/</g,'&lt;').replace(/>/g,'&gt;').replace(/"/g,'&quot;');
}

function statusBadge(status) {
  const labels = {
    running:  'Active',
    sleeping: 'Frozen',
    creating: 'Building',
    error:    'Failed',
    deleting: 'Destroying',
  };
  return `<span class="status-badge status-${status}">
    <span class="status-dot"></span>${labels[status] || status}
  </span>`;
}

function costLabel(ws) {
  if (ws.status === 'running' && ws.cost_per_hr > 0)
    return `<span class="cost-badge" style="color:var(--teal)">$${ws.cost_per_hr.toFixed(4)}/hr</span>`;
  if (ws.status === 'sleeping')
    return `<span class="cost-badge" style="color:var(--muted)">$0.00/hr ❄️</span>`;
  return '';
}

function timeAgo(iso) {
  const diff = Date.now() - new Date(iso).getTime();
  const m = Math.floor(diff / 60000);
  if (m < 1) return 'just now';
  if (m < 60) return `${m}m ago`;
  const h = Math.floor(m / 60);
  if (h < 24) return `${h}h ago`;
  return `${Math.floor(h/24)}d ago`;
}

// ─── Igloo card ───────────────────────────────────────────────────────────────
function renderIglooCard(ws) {
  const isDeleting = ws.status === 'deleting';
  const canConnect = ws.status === 'running';
  const canStart   = ws.status === 'sleeping' || ws.status === 'error';
  const canStop    = ws.status === 'running'  || ws.status === 'creating';
  const isBuilding = ws.status === 'creating';
  const isFailed   = ws.status === 'error';

  const isTerraform = ['docker-terraform','aws-fargate','fly','gcp-cloudrun'].includes(ws.provider);

  return `
  <div class="igloo-card" data-id="${ws.id}">
    <div class="igloo-card-header">
      <div>
        <div class="igloo-name">${esc(ws.name)}</div>
        <div class="igloo-provider">${esc(ws.template_name)}</div>
      </div>
      ${statusBadge(ws.status)}
    </div>

    ${isBuilding ? buildingAnimation() : ''}
    ${isFailed   ? errorAnimation()   : ''}

    <div class="igloo-meta">
      <span class="text-muted text-sm">${timeAgo(ws.last_active_at)}</span>
      ${costLabel(ws)}
    </div>
    <div class="igloo-actions">
      ${canConnect ? `<button class="btn btn-primary" onclick="app.openTerminal('${ws.id}','${esc(ws.name)}')">Terminal</button>` : ''}
      ${canConnect ? `<button class="btn btn-ghost btn-sm" onclick="app.openVSCode('${ws.id}')">VS Code</button>` : ''}
      ${canStart   ? `<button class="btn btn-ghost" onclick="app.startWorkspace('${ws.id}')">Wake</button>` : ''}
      ${canStop    ? `<button class="btn btn-ghost" onclick="app.stopWorkspace('${ws.id}')">Sleep</button>` : ''}
      <button class="btn btn-ghost" onclick="app.showLogs('${ws.id}','${esc(ws.name)}','${isTerraform?'terraform':'agent'}')">Logs</button>
      ${isDeleting ? '' : `<button class="btn btn-icon btn-danger" title="Destroy" onclick="app.deleteWorkspace('${ws.id}','${esc(ws.name)}')">🗑</button>`}
    </div>
  </div>`;
}

function renderTemplateCard(t, selected) {
  return `
  <div class="template-card${selected ? ' selected' : ''}" onclick="app.selectTemplate('${t.id}')">
    ${t.builtin ? '<span class="builtin-tag">built-in</span>' : ''}
    <div class="template-icon">${t.icon || '📦'}</div>
    <div class="template-name">${esc(t.name)}</div>
    <div class="template-desc">${esc(t.description)}</div>
  </div>`;
}

// ─── App shell ────────────────────────────────────────────────────────────────
function renderShell() {
  const u = state.user;
  const initials = u ? (u.name || u.email).slice(0,2).toUpperCase() : '??';

  document.getElementById('root').innerHTML = `
  <div class="app">
    <aside class="sidebar">
      <div class="sidebar-logo">
        <div class="sidebar-logo-icon">${icon.logo}</div>
        igloo
      </div>
      <nav class="sidebar-nav">
        <span class="nav-section-label">Workspace</span>
        <button class="nav-item ${state.view==='igloos'?'active':''}" onclick="app.setView('igloos')">
          ${icon.igloo} My Igloos
        </button>
        <button class="nav-item ${state.view==='templates'?'active':''}" onclick="app.setView('templates')">
          ${icon.templates} Templates
        </button>
        ${u?.is_admin ? `
        <span class="nav-section-label">Admin</span>
        <button class="nav-item ${state.view==='users'?'active':''}" onclick="app.setView('users')">
          ${icon.users} Users
        </button>` : ''}
        <span class="nav-section-label">Account</span>
        <button class="nav-item ${state.view==='settings'?'active':''}" onclick="app.setView('settings')">
          ${icon.settings} Settings
        </button>
      </nav>
      <div class="sidebar-footer">
        <div class="user-pill">
          <div class="user-avatar">${initials}</div>
          <div style="overflow:hidden;flex:1">
            <div class="user-name" style="display:flex;align-items:center;gap:6px">
              ${esc(u?.name || '')}
              ${u?.is_admin ? '<span class="admin-badge">Admin</span>' : ''}
            </div>
            <div class="user-email">${esc(u?.email || '')}</div>
          </div>
        </div>
      </div>
    </aside>
    <div class="main">
      <div class="topbar">
        <h1 id="view-title">My Igloos</h1>
        <div id="topbar-actions"></div>
      </div>
      <div class="content" id="content"></div>
    </div>
  </div>`;
}

// ─── Views ────────────────────────────────────────────────────────────────────
function renderIgloosView() {
  document.getElementById('view-title').textContent = 'My Igloos';
  document.getElementById('topbar-actions').innerHTML =
    `<button class="btn btn-primary" onclick="app.showNewIgloo()">${icon.plus} Build new igloo</button>`;

  const el = document.getElementById('content');
  if (!state.workspaces.length) {
    el.innerHTML = `
    <div class="empty-state">
      <svg class="empty-igloo" viewBox="0 0 100 60" fill="none" xmlns="http://www.w3.org/2000/svg">
        <path d="M10 42 Q10 8 50 8 Q90 8 90 42" stroke="white" stroke-width="3" stroke-linecap="round"/>
        <line x1="4" y1="42" x2="96" y2="42" stroke="white" stroke-width="3" stroke-linecap="round"/>
        <path d="M37 42 Q37 30 50 30 Q63 30 63 42" stroke="white" stroke-width="2.5" stroke-linecap="round"/>
        <path d="M18 35 Q50 26 82 35" stroke="white" stroke-width="1.5" stroke-dasharray="3 3" opacity="0.5"/>
      </svg>
      <h2>No igloos yet</h2>
      <p>Build your first igloo from a template and connect instantly. It'll sleep to $0 when idle.</p>
      <button class="btn btn-primary" style="margin-top:4px;width:auto" onclick="app.showNewIgloo()">${icon.plus} Build new igloo</button>
    </div>`;
    return;
  }
  el.innerHTML = `<div class="igloo-grid">${state.workspaces.map(renderIglooCard).join('')}</div>`;
}

function renderTemplatesView(selectedId) {
  const isAdmin = state.user?.is_admin;
  document.getElementById('view-title').textContent = 'Templates';
  document.getElementById('topbar-actions').innerHTML = isAdmin
    ? `<button class="btn btn-primary" onclick="app.showNewTemplate()">${icon.plus} New template</button>` : '';

  const sel = selectedId || state.templates[0]?.id;
  const el = document.getElementById('content');
  el.innerHTML = `
  <div class="templates-layout">
    <div class="templates-list">
      ${state.templates.map(t => `
      <div class="tpl-list-item${t.id === sel ? ' selected' : ''}" onclick="app.selectTemplateView('${t.id}')">
        <span class="template-icon" style="font-size:1.2em">${t.icon || '📦'}</span>
        <div style="flex:1;min-width:0">
          <div class="template-name">${esc(t.name)}</div>
          <div class="template-desc">${esc(t.description)}</div>
        </div>
        ${isAdmin ? `<button class="btn btn-ghost btn-sm" style="flex-shrink:0" onclick="event.stopPropagation();app.editTemplate('${t.id}')">Edit</button>` : ''}
      </div>`).join('')}
    </div>
    <div class="templates-code" id="tpl-code">
      <div class="tpl-code-loading">Select a template to view its Terraform source</div>
    </div>
  </div>`;
  if (sel) app.selectTemplateView(sel);
}

function renderSettingsView() {
  document.getElementById('view-title').textContent = 'Settings';
  document.getElementById('topbar-actions').innerHTML = '';
  document.getElementById('content').innerHTML = `
  <div style="max-width:540px;display:flex;flex-direction:column;gap:14px">
    <div class="settings-card">
      <div class="settings-card-title">Account</div>
      <div class="field"><label>Name</label>
        <input type="text" value="${esc(state.user?.name)}" readonly style="opacity:0.6" />
      </div>
      <div class="field" style="margin:0"><label>Email</label>
        <input type="text" value="${esc(state.user?.email)}" readonly style="opacity:0.6" />
      </div>
    </div>
    <div class="settings-card">
      <div class="settings-card-title">SSH Public Key</div>
      <p style="font-size:13px;color:var(--muted);margin-bottom:14px;line-height:1.6">
        Automatically injected into every igloo you build. Add it once — no manual setup needed.
      </p>
      <div class="field">
        <label>Public key (paste your ~/.ssh/id_ed25519.pub)</label>
        <textarea id="ssh-key-input" rows="3" placeholder="ssh-ed25519 AAAA…"
          style="width:100%;font-family:var(--mono);font-size:12px;resize:vertical;background:rgba(2,12,24,0.8);color:var(--text);border:1px solid var(--border);border-radius:var(--radius-sm);padding:10px 12px;outline:none"
        >${esc(state.user?.ssh_public_key || '')}</textarea>
      </div>
      <button class="btn btn-primary" style="width:auto" onclick="app.saveSSHKey()">Save key</button>
    </div>
    <div>
      <button class="btn btn-ghost" onclick="app.logout()">Sign out</button>
    </div>
  </div>`;
}

async function renderUsersView() {
  document.getElementById('view-title').textContent = 'Users';
  document.getElementById('topbar-actions').innerHTML = '';
  const el = document.getElementById('content');
  el.innerHTML = `<div class="text-muted" style="padding:24px">Loading…</div>`;
  try {
    const users = await api.admin.listUsers();
    el.innerHTML = `
    <div style="max-width:780px">
      <table class="users-table">
        <thead><tr><th>Name</th><th>Email</th><th>Role</th><th></th></tr></thead>
        <tbody>
          ${users.map(u => `
          <tr>
            <td>${esc(u.name || '—')}</td>
            <td style="font-family:var(--mono);font-size:13px">${esc(u.email)}</td>
            <td>${u.is_admin ? '<span class="admin-badge">Admin</span>' : '<span class="text-muted">Member</span>'}</td>
            <td style="text-align:right;white-space:nowrap">
              ${u.id !== state.user.id ? `
                ${u.is_admin
                  ? `<button class="btn btn-ghost btn-sm" onclick="app.demoteUser('${u.id}')">Remove admin</button>`
                  : `<button class="btn btn-ghost btn-sm" onclick="app.promoteUser('${u.id}')">Make admin</button>`}
                <button class="btn btn-danger btn-sm" style="margin-left:6px" onclick="app.deleteUserAdmin('${u.id}','${esc(u.name||u.email)}')">Delete</button>
              ` : '<span class="text-muted text-sm">you</span>'}
            </td>
          </tr>`).join('')}
        </tbody>
      </table>
    </div>`;
  } catch(e) {
    el.innerHTML = `<div class="text-muted" style="padding:24px">Failed: ${esc(e.message)}</div>`;
  }
}

// ─── Modals ───────────────────────────────────────────────────────────────────
let selectedTemplateId = null;

function showNewIglooModal() {
  selectedTemplateId = state.templates[0]?.id || null;
  document.body.insertAdjacentHTML('beforeend', `
  <div class="modal-overlay" id="new-ws-modal">
    <div class="modal">
      <div class="modal-header">
        <h2>Build a new igloo</h2>
        <button class="btn-icon" onclick="document.getElementById('new-ws-modal').remove()">✕</button>
      </div>
      <div class="field">
        <label>Name</label>
        <input id="ws-name" type="text" placeholder="my-project" autofocus />
      </div>
      <div class="field">
        <label>Template</label>
        <div class="template-grid" id="modal-templates" style="margin-top:8px">
          ${state.templates.map(t => renderTemplateCard(t, t.id === selectedTemplateId)).join('')}
        </div>
      </div>
      <div class="modal-footer">
        <button class="btn btn-ghost" onclick="document.getElementById('new-ws-modal').remove()">Cancel</button>
        <button class="btn btn-primary" style="width:auto;min-width:130px" onclick="app.createWorkspace()">Start building</button>
      </div>
    </div>
  </div>`);
}

function templateFilesHTML(files) {
  const entries = Object.entries(files || {}).sort(([a],[b]) => a.localeCompare(b));
  if (!entries.length) entries.push(['main.tf', '']);
  return entries.map(([name, content], i) => `
  <div class="tf-editor-block" data-idx="${i}">
    <div style="display:flex;align-items:center;gap:8px;margin-bottom:6px">
      <input class="tf-filename" type="text" value="${esc(name)}" placeholder="main.tf" style="flex:1" />
      <button class="btn btn-ghost btn-sm" onclick="this.closest('.tf-editor-block').remove()">✕</button>
    </div>
    <textarea class="tf-editor" rows="12" spellcheck="false">${esc(content)}</textarea>
  </div>`).join('');
}

function collectTemplateFiles(modal) {
  const files = {};
  modal.querySelectorAll('.tf-editor-block').forEach(block => {
    const name = block.querySelector('.tf-filename').value.trim();
    const content = block.querySelector('.tf-editor').value;
    if (name) files[name] = content;
  });
  return files;
}

async function showTemplateModal(template) {
  const isNew = !template;
  let files = {};
  if (!isNew) { try { files = await api.templates.files(template.id); } catch {} }

  document.getElementById('tpl-modal')?.remove();
  document.body.insertAdjacentHTML('beforeend', `
  <div class="modal-overlay" id="tpl-modal">
    <div class="modal modal-wide" style="max-width:760px">
      <div class="modal-header">
        <h2>${isNew ? 'New template' : 'Edit template'}</h2>
        <button class="btn-icon" onclick="document.getElementById('tpl-modal').remove()">✕</button>
      </div>
      <div style="display:grid;grid-template-columns:1fr 1fr 80px;gap:10px;margin-bottom:16px">
        <div class="field" style="margin:0"><label>Name</label>
          <input id="tpl-name" type="text" value="${esc(template?.name||'')}" placeholder="My Template" /></div>
        <div class="field" style="margin:0"><label>Provider</label>
          <input id="tpl-provider" type="text" value="${esc(template?.provider||'')}" placeholder="aws-fargate" ${!isNew?'readonly style="opacity:.5"':''} /></div>
        <div class="field" style="margin:0"><label>Icon</label>
          <input id="tpl-icon" type="text" value="${esc(template?.icon||'')}" placeholder="🚀" style="font-size:18px;text-align:center" /></div>
      </div>
      <div class="field">
        <label>Description</label>
        <input id="tpl-desc" type="text" value="${esc(template?.description||'')}" placeholder="Short description" />
      </div>
      <div style="display:flex;align-items:center;justify-content:space-between;margin:16px 0 8px">
        <label style="font-weight:600;font-size:12px;letter-spacing:.05em;text-transform:uppercase;color:var(--muted)">Terraform files</label>
        <button class="btn btn-ghost btn-sm" onclick="app._addTfFile()">+ Add file</button>
      </div>
      <div id="tpl-files">${templateFilesHTML(files)}</div>
      <div class="modal-footer">
        <button class="btn btn-ghost" onclick="document.getElementById('tpl-modal').remove()">Cancel</button>
        <button class="btn btn-primary" style="width:auto" onclick="app.saveTemplate('${template?.id||''}')">
          ${isNew ? 'Create template' : 'Save changes'}
        </button>
      </div>
    </div>
  </div>`);
}

// ─── Main app object ──────────────────────────────────────────────────────────
window.app = {
  setView(v) {
    state.view = v;
    renderShell();
    if      (v === 'igloos')    renderIgloosView();
    else if (v === 'templates') renderTemplatesView();
    else if (v === 'settings')  renderSettingsView();
    else if (v === 'users')     renderUsersView();
  },

  showNewIgloo() { showNewIglooModal(); },

  async createWorkspace() {
    const name = document.getElementById('ws-name')?.value?.trim();
    if (!name) { toast('Enter an igloo name', 'error'); return; }
    if (!selectedTemplateId) { toast('Pick a template', 'error'); return; }
    try {
      const ws = await api.workspaces.create(name, selectedTemplateId);
      document.getElementById('new-ws-modal')?.remove();
      state.workspaces.unshift(ws);
      renderIgloosView();
      toast(`Building "${ws.name}"… your igloo is on its way`, 'success');
    } catch(e) { toast(e.message, 'error'); }
  },

  async startWorkspace(id) {
    const ws = state.workspaces.find(w => w.id === id);
    if (ws) { ws.status = 'creating'; renderIgloosView(); }
    try {
      await api.workspaces.start(id);
      await refreshWorkspaces();
      toast('Igloo waking up…', 'success');
    } catch(e) { toast(e.message, 'error'); await refreshWorkspaces(); }
  },

  async stopWorkspace(id) {
    try {
      await api.workspaces.stop(id);
      const ws = state.workspaces.find(w => w.id === id);
      if (ws) ws.status = 'sleeping';
      renderIgloosView();
      toast('Igloo frozen — $0.00/hr ❄️');
    } catch(e) { toast(e.message, 'error'); }
  },

  async deleteWorkspace(id, name) {
    if (!confirm(`Melt "${name}"? This cannot be undone.`)) return;
    try {
      await api.workspaces.del(id);
      state.workspaces = state.workspaces.filter(w => w.id !== id);
      renderIgloosView();
      toast('Igloo melted');
    } catch(e) { toast(e.message, 'error'); }
  },

  selectTemplate(id) {
    selectedTemplateId = id;
    const grid = document.getElementById('modal-templates');
    if (grid) grid.innerHTML = state.templates.map(t => renderTemplateCard(t, t.id === id)).join('');
  },

  async selectTemplateView(id) {
    document.querySelectorAll('.tpl-list-item').forEach(el => {
      el.classList.toggle('selected', el.getAttribute('onclick')?.includes(`'${id}'`));
    });
    const codeEl = document.getElementById('tpl-code');
    if (!codeEl) return;
    codeEl.innerHTML = `<div class="tpl-code-loading">Loading…</div>`;
    try {
      const files = await api.templates.files(id);
      const entries = Object.entries(files).sort(([a],[b]) => a.localeCompare(b));
      if (!entries.length) { codeEl.innerHTML = `<div class="tpl-code-loading">No Terraform files.</div>`; return; }
      codeEl.innerHTML = entries.map(([name, content]) => `
        <div class="tf-file">
          <div class="tf-file-name">${esc(name)}</div>
          <pre class="tf-code">${esc(content)}</pre>
        </div>`).join('');
    } catch { codeEl.innerHTML = `<div class="tpl-code-loading">No Terraform files for this template.</div>`; }
  },

  async showLogs(id, name, initialTab) {
    document.getElementById('logs-modal')?.remove();
    const ws = state.workspaces.find(w => w.id === id);
    const hasTerraform = ws && ['docker-terraform','aws-fargate','fly','gcp-cloudrun'].includes(ws.provider);
    const activeTab = initialTab || (hasTerraform ? 'terraform' : 'agent');

    document.body.insertAdjacentHTML('beforeend', `
    <div class="modal-overlay" id="logs-modal">
      <div class="modal modal-wide">
        <div class="modal-header">
          <h2>Logs — ${esc(name)}</h2>
          <button class="btn-icon" onclick="app.closeLogs()">✕</button>
        </div>
        <div class="logs-tabs">
          ${hasTerraform ? `<button class="logs-tab${activeTab==='terraform'?' active':''}" onclick="app.switchLogsTab('${id}','${esc(name)}','terraform')">Terraform</button>` : ''}
          <button class="logs-tab${activeTab==='agent'?' active':''}" onclick="app.switchLogsTab('${id}','${esc(name)}','agent')">Agent</button>
        </div>
        <pre class="logs-output" id="logs-content">Loading…</pre>
      </div>
    </div>`);

    await this._loadLogs(id, activeTab);
    const iv = setInterval(async () => {
      if (!document.getElementById('logs-modal')) { clearInterval(iv); return; }
      const tab = document.querySelector('.logs-tab.active')?.textContent?.toLowerCase();
      await this._loadLogs(id, tab === 'terraform' ? 'terraform' : 'agent');
      const cur = state.workspaces.find(w => w.id === id);
      if (cur && cur.status !== 'creating') clearInterval(iv);
    }, 2000);
  },

  async _loadLogs(id, tab) {
    const el = document.getElementById('logs-content');
    if (!el) return;
    try {
      const text = tab === 'terraform' ? await api.workspaceLogs(id) : await api.agentLogs(id);
      el.textContent = text || '(no output yet)';
      el.scrollTop = el.scrollHeight;
    } catch(e) { el.textContent = e.message || '(no logs available)'; }
  },

  async switchLogsTab(id, name, tab) {
    document.querySelectorAll('.logs-tab').forEach(b => b.classList.toggle('active', b.textContent.toLowerCase() === tab));
    await this._loadLogs(id, tab);
  },

  closeLogs() { document.getElementById('logs-modal')?.remove(); },

  showNewTemplate() { showTemplateModal(null); },
  async editTemplate(id) { await showTemplateModal(state.templates.find(t => t.id === id)); },

  _addTfFile() {
    const container = document.getElementById('tpl-files');
    const idx = container.querySelectorAll('.tf-editor-block').length;
    const div = document.createElement('div');
    div.innerHTML = `
    <div class="tf-editor-block" data-idx="${idx}">
      <div style="display:flex;align-items:center;gap:8px;margin-bottom:6px">
        <input class="tf-filename" type="text" value="" placeholder="variables.tf" style="flex:1" />
        <button class="btn btn-ghost btn-sm" onclick="this.closest('.tf-editor-block').remove()">✕</button>
      </div>
      <textarea class="tf-editor" rows="12" spellcheck="false"></textarea>
    </div>`;
    container.appendChild(div.firstElementChild);
  },

  async saveTemplate(id) {
    const modal = document.getElementById('tpl-modal');
    const name     = document.getElementById('tpl-name').value.trim();
    const provider = document.getElementById('tpl-provider').value.trim();
    const tplIcon  = document.getElementById('tpl-icon').value.trim();
    const desc     = document.getElementById('tpl-desc').value.trim();
    const files    = collectTemplateFiles(modal);
    if (!name) { toast('Name is required', 'error'); return; }
    try {
      if (id) {
        const updated = await api.templates.update(id, { name, description: desc, icon: tplIcon, files });
        const idx = state.templates.findIndex(t => t.id === id);
        if (idx !== -1) state.templates[idx] = updated;
        toast('Template saved', 'success');
      } else {
        if (!provider) { toast('Provider is required', 'error'); return; }
        const created = await api.templates.create({ name, provider, description: desc, icon: tplIcon, files });
        state.templates.push(created);
        toast('Template created', 'success');
      }
      modal.remove();
      renderTemplatesView(id || state.templates[state.templates.length - 1]?.id);
    } catch(e) { toast(e.message, 'error'); }
  },

  async promoteUser(id) {
    try { await api.admin.updateUser(id, { is_admin: true }); toast('User promoted', 'success'); renderUsersView(); }
    catch(e) { toast(e.message, 'error'); }
  },
  async demoteUser(id) {
    try { await api.admin.updateUser(id, { is_admin: false }); toast('Admin removed', 'success'); renderUsersView(); }
    catch(e) { toast(e.message, 'error'); }
  },
  async deleteUserAdmin(id, name) {
    if (!confirm(`Delete user "${name}"? This cannot be undone.`)) return;
    try { await api.admin.deleteUser(id); toast('User deleted'); renderUsersView(); }
    catch(e) { toast(e.message, 'error'); }
  },

  openTerminal(id, name) { term.open(id, name); },
  openVSCode(id) {
    api.workspaces.vsCodeToken(id)
      .then(data => window.open(data.url, '_blank'))
      .catch(err => toast('VS Code unavailable: ' + (err.message || err), 'error'));
  },
  closeTerminal()   { term.close(); },
  maximiseTerminal() {
    const p = document.getElementById('terminal-panel');
    p.style.height = p.style.height === '100vh' ? '420px' : '100vh';
    setTimeout(() => window.dispatchEvent(new Event('resize')), 10);
  },

  async saveSSHKey() {
    const key = document.getElementById('ssh-key-input').value.trim();
    try {
      const user = await api.auth.saveSSHKey(key);
      state.user = user;
      toast('SSH key saved — next igloo will have it automatically', 'success');
    } catch(e) { toast('Failed: ' + e.message, 'error'); }
  },

  logout() {
    localStorage.removeItem('igloo_token');
    state.user = null;
    clearInterval(state.poll);
    renderLogin();
  },
};

// ─── Auth pages ───────────────────────────────────────────────────────────────
function renderLogin(errorMsg) {
  document.getElementById('root').innerHTML = `
  <div class="auth-page">
    <div class="auth-logo">
      <div class="auth-logo-icon">${icon.logo}</div>
      igloo
    </div>
    <div class="auth-card">
      <h1>Welcome back</h1>
      <p>Sign in to your account to continue.</p>
      ${errorMsg ? `<div class="auth-error visible">${esc(errorMsg)}</div>` : '<div class="auth-error" id="auth-err"></div>'}
      <div class="field">
        <label>Email</label>
        <input id="email" type="email" placeholder="you@example.com" autocomplete="email" />
      </div>
      <div class="field">
        <label>Password</label>
        <input id="password" type="password" placeholder="••••••••" autocomplete="current-password"
               onkeydown="if(event.key==='Enter')handleLogin()" />
      </div>
      <button class="btn btn-primary" onclick="handleLogin()" id="login-btn">Sign in</button>
      <div class="auth-footer">Don't have an account? <a href="#" onclick="renderRegister()">Create one →</a></div>
    </div>
  </div>`;
  document.getElementById('email')?.focus();
}

function renderRegister() {
  document.getElementById('root').innerHTML = `
  <div class="auth-page">
    <div class="auth-logo">
      <div class="auth-logo-icon">${icon.logo}</div>
      igloo
    </div>
    <div class="auth-card">
      <h1>Build your first igloo</h1>
      <p>Serverless dev workspaces that sleep to $0 when idle.</p>
      <div class="auth-error" id="auth-err"></div>
      <div class="field">
        <label>Full name</label>
        <input id="name" type="text" placeholder="Ada Lovelace" autocomplete="name" />
      </div>
      <div class="field">
        <label>Email</label>
        <input id="email" type="email" placeholder="you@example.com" autocomplete="email" />
      </div>
      <div class="field">
        <label>Password</label>
        <input id="password" type="password" placeholder="min. 8 characters"
               onkeydown="if(event.key==='Enter')handleRegister()" />
      </div>
      <button class="btn btn-primary" onclick="handleRegister()" id="reg-btn">Create account</button>
      <div class="auth-footer">Already have an account? <a href="#" onclick="renderLogin()">Sign in →</a></div>
    </div>
  </div>`;
  document.getElementById('name')?.focus();
}

async function handleLogin() {
  const btn = document.getElementById('login-btn');
  const email    = document.getElementById('email').value.trim();
  const password = document.getElementById('password').value;
  if (!email || !password) { showAuthErr('Email and password are required'); return; }
  btn.disabled = true;
  btn.innerHTML = '<span class="spinner"></span> Signing in…';
  try {
    const { token, user } = await api.auth.login(email, password);
    localStorage.setItem('igloo_token', token);
    state.user = user;
    await boot();
  } catch(e) {
    showAuthErr(e.message);
    btn.disabled = false;
    btn.textContent = 'Sign in';
  }
}

async function handleRegister() {
  const btn = document.getElementById('reg-btn');
  const name     = document.getElementById('name').value.trim();
  const email    = document.getElementById('email').value.trim();
  const password = document.getElementById('password').value;
  if (!name || !email || !password) { showAuthErr('All fields are required'); return; }
  btn.disabled = true;
  btn.innerHTML = '<span class="spinner"></span> Creating account…';
  try {
    const { token, user } = await api.auth.register(name, email, password);
    localStorage.setItem('igloo_token', token);
    state.user = user;
    await boot();
  } catch(e) {
    showAuthErr(e.message);
    btn.disabled = false;
    btn.textContent = 'Create account';
  }
}

function showAuthErr(msg) {
  const el = document.getElementById('auth-err');
  if (el) { el.textContent = msg; el.classList.add('visible'); }
}

// ─── Bootstrap ────────────────────────────────────────────────────────────────
async function refreshWorkspaces() {
  state.workspaces = await api.workspaces.list();
  if (state.view === 'igloos') renderIgloosView();
}

async function boot() {
  [state.templates, state.workspaces] = await Promise.all([
    api.templates.list().catch(() => []),
    api.workspaces.list().catch(() => []),
  ]);
  renderShell();
  renderIgloosView();
  term.init();
  clearInterval(state.poll);
  state.poll = setInterval(async () => {
    const updated = await api.workspaces.list().catch(() => null);
    if (!updated) return;
    state.workspaces = updated;
    if (state.view === 'igloos') renderIgloosView();
  }, 5000);
}

async function init() {
  const token = localStorage.getItem('igloo_token');
  if (!token) { renderLogin(); return; }
  try {
    state.user = await api.auth.me();
    await boot();
  } catch {
    localStorage.removeItem('igloo_token');
    renderLogin();
  }
}

init();
