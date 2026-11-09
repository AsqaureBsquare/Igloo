'use strict';

// ─── API client ───────────────────────────────────────────────────────────────
const api = (() => {
  function token() { return localStorage.getItem('igloo_token'); }

  async function req(method, path, body) {
    const headers = { 'Content-Type': 'application/json' };
    if (token()) headers['Authorization'] = 'Bearer ' + token();
    if (state.activeOrgID) headers['X-Org-ID'] = state.activeOrgID;
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
      saveTailscaleKey: (key)  => req('PUT', '/api/auth/me/tailscale-key', { tailscale_auth_key: key }),
      changePassword:   (current_password, new_password) => req('POST', '/api/auth/password', { current_password, new_password }),
    },
    guardBaseline: {
      get:  ()     => req('GET', '/api/guard-baseline'),
      save: (body) => req('PUT', '/api/admin/guard-baseline', body),
    },
    platformConfig: {
      get:  ()     => req('GET', '/api/admin/platform-config'),
      save: (body) => req('PUT', '/api/admin/platform-config', body),
    },
    workspaces: {
      list:     ()             => req('GET',    '/api/workspaces'),
      create:   (name, tplId, vars, guard) => req('POST',   '/api/workspaces', { name, template_id: tplId, vars: vars || {}, ...(guard ? { guard } : {}) }),
      get:      (id)           => req('GET',    `/api/workspaces/${id}`),
      start:    (id)           => req('POST',   `/api/workspaces/${id}/start`),
      stop:     (id)           => req('POST',   `/api/workspaces/${id}/stop`),
      del:      (id)           => req('DELETE', `/api/workspaces/${id}`),
      terminal: (id)           => req('GET',    `/api/workspaces/${id}/terminal`),
      logsTicket: (id)         => req('GET',    `/api/workspaces/${id}/logs-ticket`),
      activity: (id)           => req('GET',    `/api/workspaces/${id}/activity?limit=100`),
      guardPolicy: (id)        => req('GET',    `/api/workspaces/${id}/guard-policy`),
      vsCodeToken: (id)        => req('GET',    `/api/workspaces/${id}/vscode-token`),
    },
    templates: {
      list:   ()         => req('GET',  '/api/templates'),
      files:  (id)       => req('GET',  `/api/templates/${id}/files`),
      create: (body)     => req('POST', '/api/templates', body),
      update: (id, body) => req('PUT',  `/api/templates/${id}`, body),
    },
    admin: {
      listUsers:       ()          => req('GET',    '/api/admin/users'),
      updateUser:      (id, body)  => req('PUT',    `/api/admin/users/${id}`, body),
      deleteUser:      (id)        => req('DELETE', `/api/admin/users/${id}`),
      createUser:      (body)      => req('POST',   '/api/admin/users', body),
      resetPassword:   (id, new_password) => req('PUT', `/api/admin/users/${id}/password`, { new_password }),
      listWorkspaces:  (q)         => req('GET',    `/api/admin/workspaces${q ? '?q='+encodeURIComponent(q) : ''}`),
    },
    orgs: {
      list:          ()              => req('GET',    '/api/orgs'),
      create:        (body)          => req('POST',   '/api/orgs', body),
      get:           (id)            => req('GET',    `/api/orgs/${id}`),
      update:        (id, body)      => req('PUT',    `/api/orgs/${id}`, body),
      del:           (id)            => req('DELETE', `/api/orgs/${id}`),
      listMembers:   (id)            => req('GET',    `/api/orgs/${id}/members`),
      addMember:     (id, body)      => req('POST',   `/api/orgs/${id}/members`, body),
      updateMember:  (id, uid, body) => req('PUT',    `/api/orgs/${id}/members/${uid}`, body),
      removeMember:  (id, uid)       => req('DELETE', `/api/orgs/${id}/members/${uid}`),
      listTeams:     (id)            => req('GET',    `/api/orgs/${id}/teams`),
      createTeam:    (id, body)      => req('POST',   `/api/orgs/${id}/teams`, body),
      deleteTeam:    (id, tid)       => req('DELETE', `/api/orgs/${id}/teams/${tid}`),
      listTeamMembers: (id, tid)     => req('GET',    `/api/orgs/${id}/teams/${tid}/members`),
      addTeamMember:   (id, tid, body) => req('POST', `/api/orgs/${id}/teams/${tid}/members`, body),
      removeTeamMember:(id, tid, uid)  => req('DELETE',`/api/orgs/${id}/teams/${tid}/members/${uid}`),
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
const state = { user: null, workspaces: [], templates: [], orgs: [], view: 'igloos', poll: null, currentOrg: null, activeOrgID: null, selectedWsId: null, wsFilter: { status: null, templateId: null }, latency: null, userSettingsTab: 'account' };

// ─── Latency polling ──────────────────────────────────────────────────────────
function startLatencyPolling() {
  function measure() {
    const t = Date.now();
    fetch('/health', { cache: 'no-store' })
      .then(() => {
        state.latency = Date.now() - t;
        const el = document.getElementById('latency-chip');
        if (el) {
          const color = state.latency < 100 ? 'var(--green)' : state.latency < 300 ? 'var(--amber)' : 'var(--red)';
          el.style.color = color;
          el.innerHTML = `<span style="display:inline-block;width:7px;height:7px;border-radius:50%;background:${color};box-shadow:0 0 5px ${color};flex-shrink:0"></span>${state.latency}ms`;
        }
      })
      .catch(() => {
        state.latency = null;
        const el = document.getElementById('latency-chip');
        if (el) el.innerHTML = `<span style="display:inline-block;width:7px;height:7px;border-radius:50%;background:var(--red);flex-shrink:0"></span>--`;
      });
  }
  measure();
  setInterval(measure, 10000);
}

// ─── Provision log streaming ──────────────────────────────────────────────────
const provLog = {
  lines:   {},   // workspaceID → string[]
  sources: {},   // workspaceID → EventSource
  pending: {},   // workspaceID → true while a ticket is being fetched

  async connect(wsID) {
    if (this.sources[wsID] || this.pending[wsID]) return;
    if (!this.lines[wsID]) this.lines[wsID] = [];
    this.pending[wsID] = true;
    let ticket;
    try { ({ ticket } = await api.workspaces.logsTicket(wsID)); }
    catch { return; }
    finally { delete this.pending[wsID]; }
    const es = new EventSource(`/api/workspaces/${wsID}/provision-logs?ticket=${encodeURIComponent(ticket)}`);
    this.sources[wsID] = es;
    es.onmessage = (e) => {
      if (!this.lines[wsID]) this.lines[wsID] = [];
      this.lines[wsID].push(e.data);
      if (this.lines[wsID].length > 500) this.lines[wsID].shift();
      const panel = document.getElementById(`log-panel-${wsID}`);
      if (panel) {
        const el = document.createElement('div');
        el.className = 'log-line ' + logLineClass(e.data);
        el.textContent = e.data;
        panel.appendChild(el);
        panel.scrollTop = panel.scrollHeight;
      }
    };
    es.onerror = () => { es.close(); delete this.sources[wsID]; };
  },

  disconnect(wsID) {
    if (this.sources[wsID]) { this.sources[wsID].close(); delete this.sources[wsID]; }
  },

  clear(wsID) {
    this.disconnect(wsID);
    delete this.lines[wsID];
  },

  sync(workspaces) {
    const active = new Set(
      workspaces.filter(w => w.status === 'creating' || w.status === 'deleting').map(w => w.id)
    );
    active.forEach(id => this.connect(id));
    Object.keys(this.sources).forEach(id => { if (!active.has(id)) this.disconnect(id); });
  },
};

function logLineClass(line) {
  if (/^✓|Apply complete|destroyed\.|creation complete/i.test(line)) return 'log-ok';
  if (/^✗|Error:|error:/i.test(line)) return 'log-err';
  if (/Warning:/i.test(line)) return 'log-warn';
  if (/^#|\s+#/.test(line)) return 'log-dim';
  return '';
}

function renderLogPanel(wsID) {
  const lines = provLog.lines[wsID] || [];
  const rows = lines.map(l =>
    `<div class="log-line ${logLineClass(l)}">${esc(l)}</div>`
  ).join('');
  return `
    <div class="log-panel-wrap">
      <div class="log-panel-header">
        <span>Provisioner output</span>
        <span id="log-status-${wsID}"></span>
      </div>
      <div class="log-panel" id="log-panel-${wsID}" style="scroll-behavior:auto">${rows}</div>
    </div>`;
}

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
        background: '#0e1b28', foreground: '#c9dde8', cursor: '#8ad1f0',
        black: '#16293a', red: '#ef4444', green: '#22c55e', yellow: '#ffc400',
        blue: '#8ad1f0', magenta: '#a78bfa', cyan: '#8ad1f0', white: '#c9dde8',
        brightBlack: '#22394d',
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
    <ellipse cx="100" cy="84" rx="85" ry="5" fill="#0e1b28" opacity="0.6"/>

    <!-- Igloo structure — blocks appear one by one -->
    <!-- Row 1: base (5 blocks) -->
    <rect class="blk blk-1"  x="82"  y="63" width="16" height="12" rx="2.5" fill="none" stroke="#8ad1f0" stroke-width="1.2" opacity="0.7"/>
    <rect class="blk blk-2"  x="100" y="63" width="16" height="12" rx="2.5" fill="none" stroke="#8ad1f0" stroke-width="1.2" opacity="0.7"/>
    <rect class="blk blk-3"  x="118" y="63" width="16" height="12" rx="2.5" fill="none" stroke="#8ad1f0" stroke-width="1.2" opacity="0.7"/>
    <rect class="blk blk-4"  x="136" y="65" width="14" height="10" rx="2.5" fill="none" stroke="#8ad1f0" stroke-width="1.2" opacity="0.6"/>
    <rect class="blk blk-1"  x="79"  y="66" width="11" height="9"  rx="2"   fill="none" stroke="#8ad1f0" stroke-width="1"   opacity="0.5"/>
    <!-- Row 2 -->
    <rect class="blk blk-5"  x="86"  y="51" width="15" height="11" rx="2.5" fill="none" stroke="#8ad1f0" stroke-width="1.2" opacity="0.7"/>
    <rect class="blk blk-6"  x="103" y="51" width="15" height="11" rx="2.5" fill="none" stroke="#8ad1f0" stroke-width="1.2" opacity="0.7"/>
    <rect class="blk blk-7"  x="120" y="52" width="14" height="10" rx="2.5" fill="none" stroke="#8ad1f0" stroke-width="1.2" opacity="0.6"/>
    <rect class="blk blk-8"  x="134" y="55" width="11" height="9"  rx="2"   fill="none" stroke="#8ad1f0" stroke-width="1"   opacity="0.5"/>
    <!-- Row 3 -->
    <rect class="blk blk-9"  x="91"  y="40" width="14" height="10" rx="2.5" fill="none" stroke="#8ad1f0" stroke-width="1.2" opacity="0.7"/>
    <rect class="blk blk-10" x="107" y="40" width="14" height="10" rx="2.5" fill="none" stroke="#8ad1f0" stroke-width="1.2" opacity="0.7"/>
    <rect class="blk blk-11" x="123" y="42" width="12" height="9"  rx="2"   fill="none" stroke="#8ad1f0" stroke-width="1"   opacity="0.6"/>
    <!-- Cap -->
    <ellipse class="blk blk-12" cx="110" cy="36" rx="11" ry="6" fill="none" stroke="#8ad1f0" stroke-width="1.2" opacity="0.7"/>

    <!-- Door -->
    <path class="blk blk-1" d="M96 75 Q96 68 101 68 Q106 68 106 75" fill="#0e1b28" stroke="#8ad1f0" stroke-width="1" opacity="0.6"/>

    <!-- Igloo glow fill (subtle) -->
    <path class="blk blk-12" d="M82 75 Q82 34 110 34 Q138 34 138 75 Z" fill="rgba(138,209,240,0.03)" stroke="none"/>

    <!-- Eskimo body -->
    <g class="eskimo">
      <!-- Coat body -->
      <ellipse cx="36" cy="68" rx="16" ry="17" fill="#1f5f80"/>
      <!-- Fur trim -->
      <ellipse cx="36" cy="82" rx="14" ry="4" fill="#5bb6dd" opacity="0.7"/>
      <!-- Hood -->
      <circle cx="36" cy="46" r="13" fill="#1f5f80"/>
      <!-- Face -->
      <circle cx="36" cy="46" r="9"  fill="#e8c09a"/>
      <!-- Hood rim (fur) -->
      <path d="M26 44 Q36 36 46 44" fill="#5bb6dd" stroke="none" opacity="0.8"/>
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
        <rect x="50" y="54" width="22" height="8" rx="4" fill="#1f5f80"/>
        <!-- Block in hand -->
        <rect x="70" y="48" width="13" height="9" rx="2" fill="#8ad1f0" opacity="0.9" stroke="#8ad1f0" stroke-width="0.8"/>
        <!-- Hand holding block -->
        <circle cx="68" cy="58" r="4" fill="#e8c09a"/>
      </g>
      <!-- Other arm (static) -->
      <rect x="16" y="62" width="14" height="7" rx="3.5" fill="#1f5f80" transform="rotate(20 20 65)"/>
    </g>

    <!-- Snow sparkles -->
    <circle cx="65" cy="30" r="1.5" fill="#8ad1f0" opacity="0.4">
      <animate attributeName="opacity" values="0.4;0;0.4" dur="2.3s" repeatCount="indefinite"/>
    </circle>
    <circle cx="155" cy="40" r="1" fill="#8ad1f0" opacity="0.3">
      <animate attributeName="opacity" values="0.3;0;0.3" dur="1.8s" begin="0.5s" repeatCount="indefinite"/>
    </circle>
    <circle cx="145" cy="25" r="1.5" fill="#22c55e" opacity="0.3">
      <animate attributeName="opacity" values="0.3;0;0.3" dur="2.8s" begin="1s" repeatCount="indefinite"/>
    </circle>
  </svg>
  <div style="font-size:11px;color:var(--amber);font-family:var(--mono);letter-spacing:.05em;margin-top:-4px;text-align:center">building your igloo…</div>
  </div>`;
}

// ─── Melting igloo destroy animation ─────────────────────────────────────────
function destroyingAnimation() {
  return `<div class="igloo-anim-wrap">
  <svg class="igloo-destroy-svg" viewBox="0 0 200 90" xmlns="http://www.w3.org/2000/svg">
    <ellipse cx="100" cy="84" rx="85" ry="5" fill="#0e1b28" opacity="0.5"/>
    <!-- Melting igloo blocks (dripping) -->
    <rect class="melt-blk melt-1" x="82" y="63" width="16" height="12" rx="2.5" fill="none" stroke="#f59e0b" stroke-width="1.2" opacity="0.7"/>
    <rect class="melt-blk melt-2" x="100" y="63" width="16" height="12" rx="2.5" fill="none" stroke="#f59e0b" stroke-width="1.2" opacity="0.7"/>
    <rect class="melt-blk melt-3" x="118" y="63" width="16" height="12" rx="2.5" fill="none" stroke="#f59e0b" stroke-width="1.2" opacity="0.6"/>
    <rect class="melt-blk melt-1" x="86"  y="51" width="15" height="11" rx="2.5" fill="none" stroke="#f59e0b" stroke-width="1.2" opacity="0.6"/>
    <rect class="melt-blk melt-2" x="103" y="51" width="15" height="11" rx="2.5" fill="none" stroke="#f59e0b" stroke-width="1.2" opacity="0.5"/>
    <rect class="melt-blk melt-3" x="91"  y="40" width="14" height="10" rx="2.5" fill="none" stroke="#f59e0b" stroke-width="1.2" opacity="0.5"/>
    <!-- Drips -->
    <ellipse class="drip drip-1" cx="90"  cy="78" rx="2.5" ry="3.5" fill="#f59e0b" opacity="0.5"/>
    <ellipse class="drip drip-2" cx="108" cy="76" rx="2"   ry="3"   fill="#f59e0b" opacity="0.4"/>
    <ellipse class="drip drip-3" cx="126" cy="77" rx="2"   ry="3"   fill="#f59e0b" opacity="0.45"/>
    <!-- Sad eskimo watching -->
    <g>
      <ellipse cx="36" cy="68" rx="16" ry="17" fill="#1f5f80"/>
      <circle cx="36" cy="46" r="13" fill="#1f5f80"/>
      <circle cx="36" cy="46" r="9"  fill="#e8c09a"/>
      <path d="M26 44 Q36 36 46 44" fill="#5bb6dd" opacity="0.8"/>
      <path d="M32 44 Q33.5 42 35 44" stroke="#1a0800" stroke-width="1.2" fill="none" stroke-linecap="round"/>
      <path d="M37 44 Q38.5 42 40 44" stroke="#1a0800" stroke-width="1.2" fill="none" stroke-linecap="round"/>
      <path d="M32 51 Q36 47 40 51" stroke="#8b4513" stroke-width="1.2" fill="none" stroke-linecap="round"/>
      <ellipse class="tear" cx="31" cy="53" rx="1.5" ry="2.5" fill="#8ad1f0" opacity="0.7"/>
      <rect x="16" y="62" width="14" height="7" rx="3.5" fill="#1f5f80" transform="rotate(40 18 66)"/>
      <rect x="48" y="62" width="14" height="7" rx="3.5" fill="#1f5f80" transform="rotate(-40 60 66)"/>
    </g>
    <!-- Amber particles rising -->
    <circle cx="95"  cy="50" r="1.2" fill="#f59e0b" opacity="0.5"><animate attributeName="cy" values="50;30;50" dur="2s" repeatCount="indefinite"/><animate attributeName="opacity" values="0.5;0;0.5" dur="2s" repeatCount="indefinite"/></circle>
    <circle cx="115" cy="45" r="1"   fill="#f59e0b" opacity="0.4"><animate attributeName="cy" values="45;25;45" dur="2.4s" begin="0.6s" repeatCount="indefinite"/><animate attributeName="opacity" values="0.4;0;0.4" dur="2.4s" begin="0.6s" repeatCount="indefinite"/></circle>
    <circle cx="105" cy="55" r="0.8" fill="#ef4444" opacity="0.3"><animate attributeName="cy" values="55;35;55" dur="1.8s" begin="1s" repeatCount="indefinite"/><animate attributeName="opacity" values="0.3;0;0.3" dur="1.8s" begin="1s" repeatCount="indefinite"/></circle>
  </svg>
  <div style="font-size:11px;color:var(--amber);font-family:var(--mono);letter-spacing:.05em;margin-top:-4px;text-align:center">melting your igloo…</div>
  </div>`;
}

// ─── Sad eskimo error animation ────────────────────────────────────────────────
function errorAnimation() {
  return `<div class="igloo-anim-wrap">
  <svg class="igloo-error-svg" viewBox="0 0 200 90" xmlns="http://www.w3.org/2000/svg">
    <!-- Ground -->
    <ellipse cx="100" cy="84" rx="80" ry="5" fill="#0e1b28" opacity="0.6"/>

    <!-- Broken igloo pieces -->
    <g class="broken-piece">
      <rect x="120" y="62" width="16" height="11" rx="2.5" fill="none" stroke="#ef4444" stroke-width="1.2" opacity="0.5" transform="rotate(18 128 67)"/>
    </g>
    <g class="broken-piece">
      <rect x="138" y="58" width="13" height="9" rx="2.5" fill="none" stroke="#ef4444" stroke-width="1.2" opacity="0.4" transform="rotate(-25 144 62)"/>
    </g>
    <g class="broken-piece">
      <ellipse cx="133" cy="50" rx="10" ry="5" fill="none" stroke="#ef4444" stroke-width="1.2" opacity="0.35" transform="rotate(10 133 50)"/>
    </g>
    <!-- Crack line -->
    <path d="M118 75 L123 65 L117 60 L122 48" stroke="#ef4444" stroke-width="1.2" fill="none" opacity="0.4" stroke-linecap="round" stroke-linejoin="round"/>
    <!-- Base stub (what's left) -->
    <rect x="90" y="67" width="16" height="11" rx="2.5" fill="none" stroke="#ef4444" stroke-width="1.2" opacity="0.4"/>
    <rect x="108" y="67" width="14" height="11" rx="2.5" fill="none" stroke="#ef4444" stroke-width="1.2" opacity="0.35"/>

    <!-- Sad eskimo -->
    <g class="eskimo-sad-body">
      <!-- Coat body (hunched) -->
      <ellipse cx="44" cy="70" rx="16" ry="15" fill="#1f5f80"/>
      <!-- Hood -->
      <circle cx="44" cy="50" r="13" fill="#1f5f80"/>
      <!-- Face -->
      <circle cx="44" cy="50" r="9"  fill="#e8c09a"/>
      <!-- Hood rim -->
      <path d="M34 48 Q44 40 54 48" fill="#5bb6dd" opacity="0.8"/>
      <!-- Sad eyes (droopy) -->
      <path d="M40 47 Q41.5 45 43 47" stroke="#1a0800" stroke-width="1.2" fill="none" stroke-linecap="round"/>
      <path d="M45 47 Q46.5 45 48 47" stroke="#1a0800" stroke-width="1.2" fill="none" stroke-linecap="round"/>
      <!-- Frown -->
      <path d="M40 54 Q44 50 48 54" stroke="#8b4513" stroke-width="1.2" fill="none" stroke-linecap="round"/>
      <!-- Tear -->
      <ellipse class="tear" cx="41" cy="55" rx="1.5" ry="2.5" fill="#8ad1f0" opacity="0.7"/>
      <!-- Arms hanging down -->
      <rect x="26" y="66" width="14" height="6" rx="3" fill="#1f5f80" transform="rotate(40 28 66)"/>
      <rect x="56" y="66" width="14" height="6" rx="3" fill="#1f5f80" transform="rotate(-40 68 66)"/>
    </g>

    <!-- Red X marks -->
    <text x="155" y="52" font-size="22" fill="#ef4444" opacity="0.6" font-family="monospace">✕</text>
  </svg>
  <div style="font-size:11px;color:var(--red);font-family:var(--mono);letter-spacing:.05em;margin-top:-4px;text-align:center">build failed — check logs</div>
  </div>`;
}

// ─── Helpers ──────────────────────────────────────────────────────────────────
function esc(s) {
  return String(s || '').replace(/&/g,'&amp;').replace(/</g,'&lt;').replace(/>/g,'&gt;').replace(/"/g,'&quot;');
}

// ─── Avatar / badge helpers ────────────────────────────────────────────────────
function _avatarPalette(str) {
  const palettes = [
    { bg: 'rgba(138,209,240,0.15)',   border: 'rgba(138,209,240,0.3)',   color: '#8ad1f0' },
    { bg: 'rgba(157,92,255,0.15)',  border: 'rgba(157,92,255,0.3)',  color: '#a78bfa' },
    { bg: 'rgba(52,211,153,0.15)',  border: 'rgba(52,211,153,0.3)',  color: '#22c55e' },
    { bg: 'rgba(251,191,36,0.15)',  border: 'rgba(251,191,36,0.3)',  color: '#fbbf24' },
    { bg: 'rgba(236,72,153,0.15)', border: 'rgba(236,72,153,0.3)',  color: '#ec4899' },
    { bg: 'rgba(255,107,53,0.15)',  border: 'rgba(255,107,53,0.3)',  color: '#ff6b35' },
  ];
  let h = 0;
  for (let i = 0; i < str.length; i++) h = Math.imul(h * 31 + str.charCodeAt(i), 1) | 0;
  return palettes[Math.abs(h) % palettes.length];
}

function userAvatar(name, size = 30) {
  const p = _avatarPalette(name || '?');
  const letter = (name || '?')[0].toUpperCase();
  const fs = Math.round(size * 0.42);
  return `<div style="width:${size}px;height:${size}px;border-radius:50%;background:${p.bg};border:1px solid ${p.border};display:flex;align-items:center;justify-content:center;font-size:${fs}px;font-weight:700;color:${p.color};flex-shrink:0;letter-spacing:-0.5px;user-select:none">${letter}</div>`;
}

function orgAvatar(name, size = 36, radius = '10px') {
  const p = _avatarPalette(name || '?');
  const letter = (name || '?')[0].toUpperCase();
  const fs = Math.round(size * 0.44);
  return `<div style="width:${size}px;height:${size}px;border-radius:${radius};background:${p.bg};border:1px solid ${p.border};display:flex;align-items:center;justify-content:center;font-size:${fs}px;font-weight:800;color:${p.color};flex-shrink:0;user-select:none">${letter}</div>`;
}

function roleBadge(role) {
  const styles = {
    admin:  'background:rgba(138,209,240,0.1);color:#8ad1f0;border:1px solid rgba(138,209,240,0.22)',
    member: 'background:rgba(255,255,255,0.05);color:var(--muted);border:1px solid var(--border)',
    viewer: 'background:rgba(255,255,255,0.03);color:var(--muted);border:1px solid var(--border)',
  };
  const s = styles[role] || styles.member;
  return `<span style="display:inline-flex;align-items:center;font-size:10px;font-weight:700;letter-spacing:.06em;text-transform:uppercase;border-radius:5px;padding:2px 8px;${s}">${esc(role)}</span>`;
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
    return `<span class="cost-badge" style="color:var(--muted);display:inline-flex;align-items:center;gap:3px">$0.00/hr <svg width="10" height="10" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" opacity="0.6"><line x1="12" y1="2" x2="12" y2="22"/><line x1="2" y1="12" x2="22" y2="12"/><path d="M17 7l-5 5-5-5M7 17l5-5 5 5"/></svg></span>`;
  return '';
}

function timeAgo(iso) {
  if (!iso) return '';
  const d = new Date(iso);
  if (isNaN(d.getTime()) || d.getFullYear() < 2000) return '';
  const diff = Date.now() - d.getTime();
  const m = Math.floor(diff / 60000);
  if (m < 1) return 'just now';
  if (m < 60) return `${m}m ago`;
  const h = Math.floor(m / 60);
  if (h < 24) return `${h}h ago`;
  const days = Math.floor(h / 24);
  if (days < 30) return `${days}d ago`;
  const mo = Math.floor(days / 30);
  return `${mo}mo ago`;
}

// ─── Workspace table row (click to open detail panel) ────────────────────────

// ─── Igloo mark: the workspace's state, drawn ────────────────────────────────
// running = lit door, frozen = frosted, building = dome draws itself, error = cracked.
function iglooMark(status) {
  return `<svg class="mark mark--${status}" viewBox="0 0 48 36" aria-hidden="true">
    <path class="mark-fill" d="M4 30 A20 20 0 0 1 44 30 Z"/>
    <path class="mark-seam" d="M9 22 H39 M14 14.5 H34"/>
    <path class="mark-dome" d="M4 30 A20 20 0 0 1 44 30"/>
    <path class="mark-door" d="M19 30 V25 A5 5 0 0 1 29 25 V30 Z"/>
    <path class="mark-ground" d="M1.5 30 H46.5"/>
    <g class="mark-frost"><circle cx="13" cy="19" r="1"/><circle cx="35" cy="17" r="1.2"/><circle cx="24" cy="12" r="0.9"/><circle cx="31" cy="25" r="0.8"/></g>
  </svg>`;
}

function renderIglooTile(ws) {
  const isSelected = state.selectedWsId === ws.id;
  const providerLabel = {
    'kubernetes': 'k8s', 'docker': 'docker', 'docker-terraform': 'docker',
    'aws-fargate': 'fargate', 'fly': 'fly.io', 'gcp-cloudrun': 'cloud run',
  }[ws.provider] || ws.provider || 'cloud';
  const label = { running: 'Running', sleeping: 'Frozen', creating: 'Building', error: 'Failed', deleting: 'Destroying' }[ws.status] || ws.status;
  const when = timeAgo(ws.last_active_at);
  const note = ws.status === 'running' ? (when ? `Active ${when}` : 'Active now')
             : ws.status === 'sleeping' ? (when ? `Last active ${when}` : 'Sleeping at $0')
             : ws.status === 'creating' ? 'Setting up'
             : ws.status === 'error' ? 'Setup did not finish'
             : '';
  const act = (cls, fn, text) => `<button class="tile-btn ${cls}" onclick="${fn}">${text}</button>`;
  return `
  <article class="tile tile--${ws.status}${isSelected ? ' tile--selected' : ''}" onclick="app.openWorkspace('${ws.id}')" data-id="${ws.id}">
    <div class="tile-head">
      ${iglooMark(ws.status)}
      <div class="tile-title">
        <h3 class="tile-name">${esc(ws.name)}</h3>
        <div class="tile-tags"><span class="tile-template">${esc(ws.template_name || 'custom')}</span><span class="ws-provider-tag">${esc(providerLabel)}</span></div>
      </div>
    </div>
    <div class="tile-state">
      <span class="tile-status tile-status--${ws.status}">${label}</span>
      <span class="tile-note">${esc(note)}</span>
    </div>
    <div class="tile-actions" onclick="event.stopPropagation()">
      ${ws.status === 'running' ? act('tile-btn--main', `app.openTerminal('${ws.id}','${esc(ws.name)}')`, 'Terminal') + act('', `app.openVSCode('${ws.id}')`, 'VS Code') + act('tile-btn--quiet', `app.stopWorkspace('${ws.id}')`, 'Sleep') : ''}
      ${ws.status === 'sleeping' ? act('tile-btn--main', `app.startWorkspace('${ws.id}')`, 'Wake up') : ''}
      ${ws.status === 'creating' ? act('', `app.openWorkspace('${ws.id}')`, 'View progress') : ''}
      ${ws.status === 'error' ? act('', `app.openWorkspace('${ws.id}')`, 'View log') : ''}
      <button class="tile-btn tile-btn--icon" title="Delete ${esc(ws.name)}" aria-label="Delete ${esc(ws.name)}" onclick="app.deleteWorkspace('${ws.id}','${esc(ws.name)}')">
        <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round"><polyline points="3 6 5 6 21 6"/><path d="M19 6l-1 14H6L5 6"/><path d="M9 6V4h6v2"/></svg>
      </button>
    </div>
  </article>`;
}

function renderIglooCard(ws) {
  const isSelected = state.selectedWsId === ws.id;
  const providerLabel = {
    'kubernetes': 'k8s', 'docker': 'docker', 'docker-terraform': 'docker',
    'aws-fargate': 'fargate', 'fly': 'fly.io', 'gcp-cloudrun': 'cloud run',
  }[ws.provider] || ws.provider || 'cloud';

  const statusLabels = { running: 'Active', sleeping: 'Frozen', creating: 'Building', error: 'Failed', deleting: 'Destroying' };

  return `
  <tr class="ws-table-row ws-table-row--${ws.status}${isSelected ? ' ws-table-row--selected' : ''}"
      onclick="app.openWorkspace('${ws.id}')" data-id="${ws.id}">
    <td class="ws-table-td ws-table-td--name">
      <div class="ws-table-name-cell">
        <span class="ws-dot ws-dot--${ws.status}"></span>
        <span class="ws-table-name">${esc(ws.name)}</span>
      </div>
    </td>
    <td class="ws-table-td ws-table-td--template">
      <span class="ws-table-template">${esc(ws.template_name || 'custom')}</span>
      <span class="ws-provider-tag" style="margin-left:6px">${esc(providerLabel)}</span>
    </td>
    <td class="ws-table-td ws-table-td--status">
      <span class="ws-pill ws-pill--${ws.status}">${statusLabels[ws.status] || ws.status}</span>
    </td>
    <td class="ws-table-td ws-table-td--time">
      <span class="ws-table-time">${timeAgo(ws.last_active_at) || '—'}</span>
    </td>
    <td class="ws-table-td ws-table-td--actions" onclick="event.stopPropagation()">
      <div class="ws-table-actions">
        ${ws.status === 'running' ? `<button class="btn-table-action" title="Terminal" onclick="app.openTerminal('${ws.id}','${esc(ws.name)}')">
          <svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round"><polyline points="4 17 10 11 4 5"/><line x1="12" y1="19" x2="20" y2="19"/></svg>
        </button>` : ''}
        ${ws.status === 'sleeping' ? `<button class="btn-table-action btn-table-action--wake" title="Wake" onclick="app.startWorkspace('${ws.id}')">
          <svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round"><polygon points="5 3 19 12 5 21 5 3"/></svg>
        </button>` : ''}
        ${ws.status === 'running' ? `<button class="btn-table-action" title="Sleep" onclick="app.stopWorkspace('${ws.id}')">
          <svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round"><rect x="6" y="4" width="4" height="16"/><rect x="14" y="4" width="4" height="16"/></svg>
        </button>` : ''}
        <button class="btn-table-action btn-table-action--danger" title="Destroy" onclick="app.deleteWorkspace('${ws.id}','${esc(ws.name)}')">
          <svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round"><polyline points="3 6 5 6 21 6"/><path d="M19 6l-1 14H6L5 6"/><path d="M10 11v6"/><path d="M14 11v6"/><path d="M9 6V4h6v2"/></svg>
        </button>
      </div>
    </td>
  </tr>`;
}

// ─── Workspace detail panel ───────────────────────────────────────────────────
function renderWorkspaceDetail(ws) {
  const canConnect  = ws.status === 'running';
  const canStart    = ws.status === 'sleeping';
  const canStop     = ws.status === 'running';
  const isBuilding  = ws.status === 'creating';
  const isDeleting  = ws.status === 'deleting';
  const isFailed    = ws.status === 'error';
  const isTerraform = ['docker-terraform','aws-fargate','fly','gcp-cloudrun'].includes(ws.provider);

  const providerLabel = {
    'kubernetes': 'Kubernetes', 'docker': 'Docker', 'docker-terraform': 'Docker',
    'aws-fargate': 'AWS Fargate', 'fly': 'Fly.io', 'gcp-cloudrun': 'Cloud Run',
  }[ws.provider] || ws.provider || 'Cloud';

  const statusLabel = { running: 'Running', sleeping: 'Frozen', creating: 'Building', deleting: 'Destroying', error: 'Failed' }[ws.status] || ws.status;

  // Default tab: build log if mid-build/failed, else overview
  const activeTab = state.wsDetailTab || (isBuilding || isFailed || isDeleting ? 'build' : 'overview');
  state.wsDetailTab = activeTab;

  return `
  <div class="ws-detail">
    <div class="ws-detail-header">
      <div class="ws-detail-header-left">
        ${iglooMark(ws.status)}
        <span class="ws-detail-name">${esc(ws.name)}</span>
        <span class="ws-pill ws-pill--${ws.status}">${statusLabel}</span>
      </div>
      <button class="btn-icon" onclick="app.closeWorkspace()" title="Close">
        <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.5" stroke-linecap="round"><line x1="18" y1="6" x2="6" y2="18"/><line x1="6" y1="6" x2="18" y2="18"/></svg>
      </button>
    </div>

    <div class="ws-detail-tabs">
      <button class="ws-detail-tab${activeTab==='overview'?' active':''}" onclick="app.setWsDetailTab('overview')">Overview</button>
      <button class="ws-detail-tab${activeTab==='build'?' active':''}" onclick="app.setWsDetailTab('build')">Build log</button>
      ${isGuarded(ws) ? `<button class="ws-detail-tab${activeTab==='activity'?' active':''}" onclick="app.setWsDetailTab('activity')">Network</button>` : ''}
      <button class="ws-detail-tab${activeTab==='settings'?' active':''}" onclick="app.setWsDetailTab('settings')">Settings</button>
    </div>

    ${activeTab === 'overview' ? `
      <div class="ws-detail-meta">
        <div class="ws-detail-row"><span class="ws-detail-label">Template</span><span>${esc(ws.template_name || 'custom')}</span></div>
        <div class="ws-detail-row"><span class="ws-detail-label">Provider</span><span>${esc(providerLabel)}</span></div>
        <div class="ws-detail-row"><span class="ws-detail-label">Last active</span><span>${timeAgo(ws.last_active_at)}</span></div>
        ${ws.created_at ? `<div class="ws-detail-row"><span class="ws-detail-label">Created</span><span>${timeAgo(ws.created_at)}</span></div>` : ''}
      </div>

      ${!isDeleting ? `
      <div class="ws-detail-actions">
        ${canConnect ? `<button class="btn btn-primary" style="width:auto;padding:8px 20px" onclick="app.openTerminal('${ws.id}','${esc(ws.name)}')">Terminal</button>` : ''}
        ${canConnect ? `<button class="btn btn-ghost" style="width:auto;padding:8px 16px" onclick="app.openVSCode('${ws.id}')">VS Code</button>` : ''}
        ${canConnect ? `<button class="btn btn-ghost" style="width:auto;padding:8px 16px" onclick="app.openNetMap('${ws.id}','${esc(ws.name)}')">Network</button>` : ''}
        ${canStart   ? `<button class="btn btn-wake" style="width:auto;padding:8px 16px" onclick="app.startWorkspace('${ws.id}')">Wake</button>` : ''}
        ${canStop    ? `<button class="btn btn-ghost" style="width:auto;padding:8px 16px" onclick="app.stopWorkspace('${ws.id}')">Sleep</button>` : ''}
      </div>` : ''}

      ${isBuilding ? `<div class="ws-detail-section">${buildingAnimation()}</div>` : ''}
      ${isDeleting ? `<div class="ws-detail-section">${destroyingAnimation()}</div>` : ''}
      ${isFailed   ? `<div class="ws-detail-section">${errorAnimation()}</div>` : ''}
    ` : ''}

    ${activeTab === 'build' ? `
      <div class="ws-detail-section ws-detail-build">
        ${(isBuilding || isDeleting) ? renderLogPanel(ws.id) : renderInlineBuildLog(ws, isTerraform)}
      </div>
    ` : ''}

    ${activeTab === 'activity' && isGuarded(ws) ? renderActivityPanel(ws) : ''}

    ${activeTab === 'settings' ? `
      <div class="ws-detail-meta">
        <div class="ws-detail-row"><span class="ws-detail-label">Workspace ID</span><span style="font-family:var(--mono);font-size:11px;color:var(--muted)">${esc(ws.id)}</span></div>
        <div class="ws-detail-row"><span class="ws-detail-label">Provider</span><span>${esc(providerLabel)}</span></div>
      </div>
      <div class="ws-detail-section">
        <div class="danger-zone-title">Danger zone</div>
        <p class="danger-zone-desc">Destroying this workspace permanently deletes the container, persistent disk, and all data inside it.</p>
        <button class="btn btn-danger" style="width:auto;padding:8px 16px;margin-top:8px" onclick="app.deleteWorkspace('${ws.id}','${esc(ws.name)}')">Destroy workspace</button>
      </div>
    ` : ''}
  </div>`;
}

// ─── Network activity (workspaces behind the egress gateway) ────────────────
const isGuarded = (ws) => !!(ws.meta && ws.meta.guard_policy);

const ACTIVITY_REASONS = {
  'not-in-allow-list': 'Not in this workspace’s allow-list',
  'private-address':   'Internal address — blocked',
  'unknown-route':     'No such credential route',
};

function renderActivityPanel(ws) {
  setTimeout(() => app._loadActivity(ws.id), 0);
  const sandbox = ws.meta && ws.meta.sandbox === 'true';
  const expires = sandbox && ws.meta.sandbox_expires ? new Date(ws.meta.sandbox_expires) : null;
  return `
    <div class="ws-detail-section activity" id="activity-panel">
      ${expires ? `<div class="activity-note">Sandbox — deleted automatically ${expires > new Date() ? 'at ' + expires.toLocaleTimeString() : 'now'}.</div>` : ''}
      <div class="activity-policy" id="activity-policy">loading policy…</div>
      <div class="activity-head"><span>Network activity</span><span class="log-panel-meta" id="activity-meta"></span></div>
      <div class="activity-list" id="activity-body"></div>
    </div>`;
}

function renderActivityRows(events) {
  if (!events.length) {
    return `<div class="activity-empty">
      <div class="activity-empty-title">No network activity yet</div>
      <div class="activity-empty-hint">When this workspace reaches for anything outside itself, the gateway’s decision shows up here.</div>
    </div>`;
  }
  return `<table class="activity-table"><thead><tr><th>When</th><th></th><th>Destination</th><th>Why</th></tr></thead><tbody>${
    events.map(e => {
      const t = new Date(e.time).toLocaleTimeString();
      const dest = e.kind === 'route' ? `route ${esc(e.rule.replace(/^route:/, ''))} → ${esc(e.host)}` : esc(e.host);
      const what = e.method ? `<span class="activity-method">${esc(e.method)}</span> ` : '';
      const why = e.decision === 'deny' ? (ACTIVITY_REASONS[e.rule] || e.rule || '') : (e.kind === 'route' ? '' : e.rule ? 'Allowed by ' + e.rule : '');
      const status = e.status ? ` · ${e.status}` : '';
      return `<tr class="activity-row activity-row--${e.decision}">
        <td class="activity-time">${t}</td>
        <td><span class="activity-pill activity-pill--${e.decision}">${e.decision === 'deny' ? 'Blocked' : 'Allowed'}</span></td>
        <td class="activity-dest">${what}${dest}${esc(e.path && e.kind !== 'connect' ? e.path : '')}<span class="activity-status">${status}</span></td>
        <td class="activity-why">${esc(why)}</td>
      </tr>`;
    }).join('')}</tbody></table>`;
}

function renderInlineBuildLog(ws, isTerraform) {
  // Trigger an async load on the next tick — content appears when fetched
  setTimeout(() => app._loadLogsInline(ws.id, isTerraform ? 'terraform' : 'agent'), 0);
  return `
    <div class="log-panel-wrap">
      <div class="log-panel-header">
        <span>${isTerraform ? 'Terraform' : 'Agent'} log</span>
        <span class="log-panel-meta" id="inline-log-meta-${ws.id}">loading…</span>
      </div>
      <pre class="log-panel log-panel--inline" id="inline-log-${ws.id}"></pre>
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
  const latencyColor = state.latency == null ? 'var(--muted)' : state.latency < 100 ? 'var(--green)' : state.latency < 300 ? 'var(--amber)' : 'var(--red)';
  const latencyDot = state.latency == null ? 'var(--muted)' : latencyColor;
  const latencyLabel = state.latency == null ? '--' : `${state.latency}ms`;

  document.getElementById('root').innerHTML = `
  <div class="app">
    <aside class="sidebar">
      <div class="sidebar-logo">
        <div class="sidebar-logo-icon">${icon.logo}</div>
        <span class="sidebar-logo-text">igloo</span>
      </div>
      <nav class="sidebar-nav">
        <span class="nav-section-label">Workspace</span>
        <button class="nav-item ${state.view==='igloos'?'active':''}" onclick="app.setView('igloos')">
          ${icon.igloo} My Igloos
        </button>
        <button class="nav-item ${state.view==='templates'?'active':''}" onclick="app.setView('templates')">
          ${icon.templates} Templates
        </button>
        <span class="nav-section-label">Organization</span>
        <button class="nav-item ${state.view==='orgs'?'active':''}" onclick="app.setView('orgs')">
          ${icon.users} Organizations
        </button>
        ${state.currentOrg ? `
        <button class="nav-item ${state.view==='org-detail'?'active':''}" onclick="app.setView('org-detail')" style="padding-left:28px;font-size:12px">
          ↳ ${esc(state.currentOrg.name)}
        </button>` : ''}
        ${state.activeOrgID ? `
        <div class="active-org-chip" title="Workspaces and templates are scoped to this org">
          <span class="active-org-dot"></span>
          ${esc(state.orgs.find(o=>o.id===state.activeOrgID)?.name || state.activeOrgID)}
          <button class="active-org-clear" onclick="app.clearActiveOrg()" title="Clear org context">✕</button>
        </div>` : ''}
        ${u?.is_admin ? `
        <span class="nav-section-label">Admin</span>
        <button class="nav-item ${state.view==='users'?'active':''}" onclick="app.setView('users')">
          ${icon.users} Users
        </button>
        <button class="nav-item ${state.view==='admin-workspaces'?'active':''}" onclick="app.setView('admin-workspaces')">
          ${icon.igloo} All Workspaces
        </button>
        <button class="nav-item ${state.view==='admin-network'?'active':''}" onclick="app.setView('admin-network')">
          ${icon.settings} Network baseline
        </button>` : ''}
        <span class="nav-section-label">Account</span>
        <button class="nav-item ${state.view==='userSettings'?'active':''}" onclick="app.setView('userSettings')">
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
        <div style="display:flex;align-items:center;gap:0;flex-shrink:0">
          <h1 id="view-title" style="font-family:var(--font);font-size:17px;font-weight:700;letter-spacing:-0.3px;color:var(--text)">My Igloos</h1>
        </div>
        <div style="flex:1"></div>
        <div id="topbar-actions" style="display:flex;align-items:center;gap:10px"></div>
        <div class="topbar-right-cluster">
          <div id="latency-chip" class="latency-chip" style="color:${latencyColor}">
            <span style="display:inline-block;width:7px;height:7px;border-radius:50%;background:${latencyDot};box-shadow:0 0 5px ${latencyDot};flex-shrink:0"></span>${latencyLabel}
          </div>
          <div class="user-menu-wrap" id="user-menu-wrap">
            <button class="user-menu-btn" onclick="app.toggleUserMenu()" id="user-menu-btn">
              <div class="user-menu-avatar">${initials}</div>
              <svg width="10" height="10" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.5" stroke-linecap="round"><polyline points="6 9 12 15 18 9"/></svg>
            </button>
            <div class="user-dropdown" id="user-dropdown">
              <div class="user-dropdown-header">
                <div class="user-dropdown-name">${esc(u?.name || u?.email || '')}</div>
                <div class="user-dropdown-email">${esc(u?.email || '')}</div>
              </div>
              <div class="user-dropdown-divider"></div>
              <button class="user-dropdown-item" onclick="app.setView('userSettings')">
                <svg width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round"><circle cx="12" cy="12" r="3"/><path d="M19.4 15a1.65 1.65 0 0 0 .33 1.82l.06.06a2 2 0 0 1-2.83 2.83l-.06-.06a1.65 1.65 0 0 0-1.82-.33 1.65 1.65 0 0 0-1 1.51V21a2 2 0 0 1-4 0v-.09A1.65 1.65 0 0 0 9 19.4a1.65 1.65 0 0 0-1.82.33l-.06.06a2 2 0 0 1-2.83-2.83l.06-.06A1.65 1.65 0 0 0 4.68 15a1.65 1.65 0 0 0-1.51-1H3a2 2 0 0 1 0-4h.09A1.65 1.65 0 0 0 4.6 9a1.65 1.65 0 0 0-.33-1.82l-.06-.06a2 2 0 0 1 2.83-2.83l.06.06A1.65 1.65 0 0 0 9 4.68a1.65 1.65 0 0 0 1-1.51V3a2 2 0 0 1 4 0v.09a1.65 1.65 0 0 0 1 1.51 1.65 1.65 0 0 0 1.82-.33l.06-.06a2 2 0 0 1 2.83 2.83l-.06.06A1.65 1.65 0 0 0 19.4 9a1.65 1.65 0 0 0 1.51 1H21a2 2 0 0 1 0 4h-.09a1.65 1.65 0 0 0-1.51 1z"/></svg>
                Account settings
              </button>
              <div class="user-dropdown-divider"></div>
              <button class="user-dropdown-item user-dropdown-item--danger" onclick="app.logout()">
                <svg width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round"><path d="M9 21H5a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2h4"/><polyline points="16 17 21 12 16 7"/><line x1="21" y1="12" x2="9" y2="12"/></svg>
                Sign out
              </button>
            </div>
          </div>
        </div>
      </div>
      <div class="content" id="content"></div>
    </div>
  </div>`;
}

// ─── Views ────────────────────────────────────────────────────────────────────
function renderSkeletonForView(v) {
  const el = document.getElementById('content');
  if (!el) return;
  const titleEl = document.getElementById('view-title');
  if (titleEl) {
    if (v === 'templates')    titleEl.textContent = 'Templates';
    else                       titleEl.textContent = 'My Igloos';
  }
  const cardSkel = `
    <div class="skeleton-card">
      <div class="skeleton skeleton-line skeleton-line--title"></div>
      <div class="skeleton skeleton-line skeleton-line--meta"></div>
      <div style="flex:1"></div>
      <div class="skeleton skeleton-line skeleton-line--body"></div>
      <div class="skeleton skeleton-line skeleton-line--short"></div>
    </div>`;
  el.innerHTML = `<div class="skeleton-grid">${cardSkel.repeat(6)}</div>`;
}

function renderIgloosViewInner(q, opts = {}) {
  const runningCount = state.workspaces.filter(w => w.status === 'running').length;
  const titleEl = document.getElementById('view-title');
  titleEl.textContent = state.user?.is_admin ? 'All Igloos' : 'My Igloos';
  // Append running badge after the title
  const existingBadge = document.getElementById('running-count-badge');
  if (existingBadge) existingBadge.remove();
  if (runningCount > 0) {
    const badge = document.createElement('span');
    badge.id = 'running-count-badge';
    badge.className = 'running-badge';
    badge.innerHTML = `<span class="running-badge-dot"></span>${runningCount} live`;
    badge.style.marginLeft = '12px';
    titleEl.parentNode.insertBefore(badge, titleEl.nextSibling);
  }

  const f = state.wsFilter;
  const statuses = [
    { val: null,        label: 'All' },
    { val: 'running',   label: 'Running' },
    { val: 'sleeping',  label: 'Frozen' },
    { val: 'creating',  label: 'Building' },
    { val: 'error',     label: 'Error' },
  ];
  const countOf = v => v ? state.workspaces.filter(w => w.status === v).length : state.workspaces.length;
  const statusChips = statuses.map(s =>
    `<button class="ws-filter-chip${f.status === s.val ? ' ws-filter-chip--active' : ''}${s.val ? ' ws-filter-chip--'+s.val : ''}"
       onclick="app.setWsFilter('status',${s.val === null ? 'null' : `'${s.val}'`})">${s.label}<span class="chip-n">${countOf(s.val)}</span></button>`
  ).join('');
  const tplOptions = state.templates.map(t =>
    `<option value="${t.id}" ${f.templateId === t.id ? 'selected' : ''}>${esc(t.name)}</option>`
  ).join('');
  const activeCount = (f.status ? 1 : 0) + (f.templateId ? 1 : 0);

  document.getElementById('topbar-actions').innerHTML =
    `<button class="btn btn-primary" style="width:auto;padding:8px 18px;flex-shrink:0" onclick="app.showNewIgloo()">${icon.plus} New workspace</button>`;

  const el = document.getElementById('content');
  const visible = state.workspaces.filter(w => {
    if (q && !wsMatchesQuery(w, q)) return false;
    if (state.wsFilter.status && w.status !== state.wsFilter.status) return false;
    if (state.wsFilter.templateId && w.template_id !== state.wsFilter.templateId) return false;
    return true;
  });

  if (!state.workspaces.length && !state.selectedWsId) {
    el.innerHTML = `<div class="empty-state">
           <svg class="empty-igloo" viewBox="0 0 100 60" fill="none" xmlns="http://www.w3.org/2000/svg">
             <path d="M10 42 Q10 8 50 8 Q90 8 90 42" stroke="white" stroke-width="3" stroke-linecap="round"/>
             <line x1="4" y1="42" x2="96" y2="42" stroke="white" stroke-width="3" stroke-linecap="round"/>
             <path d="M37 42 Q37 30 50 30 Q63 30 63 42" stroke="white" stroke-width="2.5" stroke-linecap="round"/>
             <path d="M18 35 Q50 26 82 35" stroke="white" stroke-width="1.5" stroke-dasharray="3 3" opacity="0.5"/>
           </svg>
           <h2>No workspaces yet</h2>
           <p>Create one from a template and connect in seconds. It sleeps at $0 when you're not using it.</p>
           <button class="btn btn-primary" style="margin-top:4px;width:auto" onclick="app.showNewIgloo()">${icon.plus} Create workspace</button>
         </div>`;
    return;
  }

  const selectedWs = state.selectedWsId
    ? state.workspaces.find(w => w.id === state.selectedWsId)
    : null;

  const tableHtml = visible.length
    ? `<div class="tiles">${visible.map(renderIglooTile).join('')}</div>`
    : `<div class="no-match">
         <p class="no-match-title">No workspaces match${q ? ` “${esc(q)}”` : ' these filters'}</p>
         <p class="no-match-hint">Search by name, template or status, or narrow with <code>status:frozen</code> or <code>template:kubernetes</code>.</p>
         <button class="btn btn-ghost btn-sm" style="width:auto" onclick="app.resetWsSearch()">Clear search and filters</button>
       </div>`;

  const filterBar = `
  <div class="ws-filter-bar">
    <div class="ws-filter-bar-left">
      <div class="search-bar-wrap">
        <svg style="position:absolute;left:10px;top:50%;transform:translateY(-50%);color:var(--muted);pointer-events:none" width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round"><circle cx="11" cy="11" r="8"/><line x1="21" y1="21" x2="16.65" y2="16.65"/></svg>
        <input class="search-bar search-bar--with-icon" id="ws-search" placeholder="Search by name, template or status" value="${esc(q||'')}"
          oninput="app.searchWorkspaces(this.value)" aria-label="Search workspaces" style="padding-left:32px">
      </div>
      <div class="ws-filter-chips-inline">
        ${statusChips}
        ${state.templates.length > 1 ? `
        <select class="ws-filter-select" onchange="app.setWsFilter('templateId',this.value||null)">
          <option value="">Template</option>
          ${tplOptions}
        </select>` : ''}
        ${activeCount > 0 ? `<button class="ws-filter-clear" onclick="app.clearWsFilter()">✕ Clear</button>` : ''}
      </div>
    </div>
  </div>`;

  const nOf = st => state.workspaces.filter(w => w.status === st).length;
  const bits = [[nOf('running'), 'running'], [nOf('sleeping'), 'frozen'], [nOf('creating'), 'building'], [nOf('error'), 'failed']]
    .filter(([n]) => n).map(([n, l]) => `${n} ${l}`);
  const statsStrip = selectedWs || !bits.length ? '' : `<p class="ws-summary">${bits.join(', ')}</p>`;

  el.innerHTML = `
  <div class="ws-layout${selectedWs ? ' ws-layout--split' : ''}">
    <div class="ws-cards-pane">
      ${statsStrip}
      ${filterBar}
      ${tableHtml}
    </div>
    ${selectedWs ? `<div class="ws-detail-pane${opts.noAnim ? ' ws-detail-pane--no-anim' : ''}" id="ws-detail-pane">${renderWorkspaceDetail(selectedWs)}</div>` : ''}
  </div>`;

  provLog.sync(state.workspaces);
  state.workspaces.forEach(ws => {
    const panel = document.getElementById(`log-panel-${ws.id}`);
    if (panel) panel.scrollTop = panel.scrollHeight;
  });
}

// Client-side workspace filter matching the same key:value syntax as the API.

// Re-rendering replaces the whole panel, including the search box; put the cursor back.
function renderIgloosView(q, opts = {}) {
  const prev = document.activeElement;
  const hadFocus = prev && prev.id === 'ws-search';
  const start = hadFocus ? prev.selectionStart : 0, end = hadFocus ? prev.selectionEnd : 0;
  renderIgloosViewInner(q, opts);
  if (hadFocus) {
    const input = document.getElementById('ws-search');
    if (input) { input.focus(); try { input.setSelectionRange(start, end); } catch (_) {} }
  }
}

// Words people use for each status, so "frozen", "sleeping" and "status:frozen" all work.
const WS_STATUS_WORDS = {
  running:  ['running', 'live', 'active'],
  sleeping: ['frozen', 'sleeping', 'asleep'],
  creating: ['building', 'creating'],
  error:    ['failed', 'error'],
  deleting: ['destroying', 'deleting'],
};

function wsStatusMatches(ws, word) {
  const w = word.toLowerCase();
  return (WS_STATUS_WORDS[ws.status] || [ws.status]).some(s => s.startsWith(w));
}

// Plain words match name, template, provider or status. key:value narrows one field.
function wsMatchesQuery(ws, q) {
  if (!q || !q.trim()) return true;
  const has = (v, t) => String(v || '').toLowerCase().includes(t);
  return q.trim().split(/\s+/).every(term => {
    const i = term.indexOf(':');
    const key = i > 0 ? term.slice(0, i).toLowerCase() : '';
    const val = (i > 0 ? term.slice(i + 1) : term).toLowerCase();
    if (i > 0 && !val) return true;              // "owner:" still being typed
    switch (key) {
      case 'owner':    return val === 'me' ? ws.user_id === state.user?.id : has(ws.user_id, val);
      case 'template': return has(ws.template_name, val);
      case 'name':     return has(ws.name, val);
      case 'status':   return wsStatusMatches(ws, val);
      case 'org':      return has(ws.org_id, val);
      case 'provider': return has(ws.provider, val);
      default: {
        const t = term.toLowerCase();             // plain word (or unknown "x:y")
        return has(ws.name, t) || has(ws.template_name, t) || has(ws.provider, t) || wsStatusMatches(ws, t);
      }
    }
  });
}

function renderTemplatesView(selectedId, q) {
  const isAdmin = state.user?.is_admin;
  document.getElementById('view-title').textContent = 'Templates';
  document.getElementById('topbar-actions').innerHTML =
    isAdmin ? `<button class="btn btn-primary" style="width:auto;padding:8px 18px;flex-shrink:0" onclick="app.showNewTemplate()">${icon.plus} New template</button>` : '';

  const visible = q
    ? state.templates.filter(t => t.name.toLowerCase().includes(q.toLowerCase()) || (t.description||'').toLowerCase().includes(q.toLowerCase()))
    : state.templates;

  const sel = selectedId || visible[0]?.id;
  const el = document.getElementById('content');

  const filterBar = `
  <div class="ws-filter-bar">
    <div class="ws-filter-bar-left">
      <div class="search-bar-wrap" style="position:relative">
        <svg style="position:absolute;left:10px;top:50%;transform:translateY(-50%);color:var(--muted);pointer-events:none" width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round"><circle cx="11" cy="11" r="8"/><line x1="21" y1="21" x2="16.65" y2="16.65"/></svg>
        <input class="search-bar search-bar--with-icon" id="tpl-search" placeholder="Search templates…" value="${esc(q||'')}"
          oninput="app.searchTemplates(this.value)" style="padding-left:32px">
      </div>
    </div>
  </div>`;

  if (!visible.length) {
    el.innerHTML = filterBar + `<div class="empty-state"><div class="empty-state-icon"><svg width="36" height="36" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.4" stroke-linecap="round" opacity="0.5"><path d="M21 16V8a2 2 0 0 0-1-1.73l-7-4a2 2 0 0 0-2 0l-7 4A2 2 0 0 0 3 8v8a2 2 0 0 0 1 1.73l7 4a2 2 0 0 0 2 0l7-4A2 2 0 0 0 21 16z"/><polyline points="3.27 6.96 12 12.01 20.73 6.96"/><line x1="12" y1="22.08" x2="12" y2="12"/></svg></div><h2>No templates${q ? ' match' : ''}</h2><p>${q ? 'Try a different search.' : 'No templates have been created yet.'}</p>${isAdmin ? `<button class="btn btn-primary" style="width:auto;margin-top:8px" onclick="app.showNewTemplate()">${icon.plus} New template</button>` : ''}</div>`;
    return;
  }

  el.innerHTML = filterBar + `
  <div class="tpl-page-layout">
    <div class="tpl-table-wrap">
      <table class="tpl-table">
        <thead>
          <tr>
            <th class="tpl-table-th">Template</th>
            <th class="tpl-table-th">Description</th>
            <th class="tpl-table-th">Version</th>
            <th class="tpl-table-th">Updated</th>
            ${isAdmin ? `<th class="tpl-table-th tpl-table-th--actions"></th>` : ''}
          </tr>
        </thead>
        <tbody>
          ${visible.map(t => `
          <tr class="tpl-table-row${t.id === sel ? ' tpl-table-row--selected' : ''}" onclick="app.selectTemplateView('${t.id}')">
            <td class="tpl-table-td tpl-table-td--name">
              <div class="tpl-table-name-cell">
                <span class="tpl-table-icon">${t.icon || '📦'}</span>
                <div>
                  <div class="tpl-table-name">${esc(t.name)}</div>
                  ${t.builtin ? '<span class="builtin-tag" style="position:static;display:inline;margin-top:3px;font-size:9px">built-in</span>' : ''}
                </div>
              </div>
            </td>
            <td class="tpl-table-td tpl-table-td--desc">${esc(t.description || '—')}</td>
            <td class="tpl-table-td tpl-table-td--version"><span class="tpl-version-chip">v${t.version || 1}</span></td>
            <td class="tpl-table-td tpl-table-td--updated" style="font-family:var(--mono);font-size:11px;color:var(--muted)">${timeAgo(t.updated_at) || '—'}</td>
            ${isAdmin ? `<td class="tpl-table-td tpl-table-td--actions" onclick="event.stopPropagation()">
              <div class="tpl-table-actions">
                <button class="btn btn-ghost btn-sm" onclick="app.editTemplate('${t.id}')">Edit</button>
              </div>
            </td>` : ''}
          </tr>`).join('')}
        </tbody>
      </table>
    </div>
    <div class="templates-code" id="tpl-code">
      <div class="tpl-code-loading">Select a template to view its source</div>
    </div>
  </div>`;
  if (sel) app.selectTemplateView(sel);
}

function renderSettingsView() {
  // Legacy redirect to new user settings
  renderUserSettings('account');
}

function renderUserSettings(tab) {
  tab = tab || state.userSettingsTab || 'account';
  state.userSettingsTab = tab;
  document.getElementById('view-title').textContent = 'Settings';
  document.getElementById('topbar-actions').innerHTML = '';

  const tabs = [
    { id: 'account',       label: 'Account',       icon: `<svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round"><path d="M20 21v-2a4 4 0 0 0-4-4H8a4 4 0 0 0-4 4v2"/><circle cx="12" cy="7" r="4"/></svg>` },
    { id: 'security',      label: 'Security',      icon: `<svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round"><path d="M12 22s8-4 8-10V5l-8-3-8 3v7c0 6 8 10 8 10z"/></svg>` },
    { id: 'ssh-keys',      label: 'SSH Keys',      icon: `<svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round"><circle cx="8" cy="15" r="5"/><path d="M13 15h8"/><path d="M17 12v6"/><path d="M21 12v6"/></svg>` },
    { id: 'tokens',        label: 'Tokens',        icon: `<svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round"><rect x="3" y="11" width="18" height="11" rx="2"/><path d="M7 11V7a5 5 0 0 1 10 0v4"/></svg>` },
    { id: 'notifications', label: 'Notifications', icon: `<svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round"><path d="M18 8A6 6 0 0 0 6 8c0 7-3 9-3 9h18s-3-2-3-9"/><path d="M13.73 21a2 2 0 0 1-3.46 0"/></svg>` },
  ];

  function sidebarItems() {
    return tabs.map(t => `
    <button class="usettings-nav-item${t.id === tab ? ' active' : ''}" onclick="app.setUserSettingsTab('${t.id}')">
      ${t.icon} ${t.label}
    </button>`).join('');
  }

  function tabContent() {
    if (tab === 'account') return `
    <div class="usettings-section">
      <div class="usettings-section-title">Account</div>
      <div class="usettings-section-desc">Manage your personal information and display preferences.</div>
      <div class="usettings-form">
        <div class="field">
          <label>Display name</label>
          <input type="text" value="${esc(state.user?.name || '')}" readonly style="opacity:0.6" />
          <div class="field-hint">Contact your admin to change your display name.</div>
        </div>
        <div class="field">
          <label>Email address</label>
          <input type="email" value="${esc(state.user?.email || '')}" readonly style="opacity:0.6" />
        </div>
      </div>
      <div class="usettings-section-divider"></div>
      <div class="usettings-section-title" style="margin-top:8px">Your groups</div>
      <div class="usettings-section-desc">Groups you belong to across organizations.</div>
      <div class="usettings-coming-soon">
        <svg width="20" height="20" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" style="color:var(--muted)"><path d="M17 21v-2a4 4 0 0 0-4-4H5a4 4 0 0 0-4 4v2"/><circle cx="9" cy="7" r="4"/><path d="M23 21v-2a4 4 0 0 0-3-3.87"/><path d="M16 3.13a4 4 0 0 1 0 7.75"/></svg>
        <span>Group membership will appear here once configured via OIDC or admin assignment.</span>
      </div>
    </div>`;

    if (tab === 'security') return `
    <div class="usettings-section">
      <div class="usettings-section-title">Password</div>
      ${['oidc', 'none'].includes(state.user?.login_type) ? `
      <div class="usettings-coming-soon usettings-coming-soon--info" style="margin-bottom:16px">
        <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" style="color:var(--accent);flex-shrink:0"><circle cx="12" cy="12" r="10"/><line x1="12" y1="8" x2="12" y2="12"/><line x1="12" y1="16" x2="12.01" y2="16"/></svg>
        <span>This account signs in another way, so it has no password to change here.</span>
      </div>` : `
      <div class="usettings-section-desc">Changing it signs you out everywhere else.</div>
      <div class="usettings-form">
        <div class="field">
          <label>Current password</label>
          <input id="pw-current" type="password" autocomplete="current-password" />
        </div>
        <div class="field">
          <label>New password</label>
          <input id="pw-new" type="password" autocomplete="new-password" />
          <div class="field-hint">8 to 72 characters.</div>
        </div>
        <div class="field">
          <label>New password again</label>
          <input id="pw-again" type="password" autocomplete="new-password" />
        </div>
        <button class="btn btn-primary" style="width:auto" onclick="app.changePassword()">Update password</button>
      </div>`}
      <div class="usettings-section-divider"></div>
      <div class="usettings-section-title" style="margin-top:8px">Single Sign-On</div>
      <div class="usettings-coming-soon usettings-coming-soon--success">
        <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" style="color:var(--green);flex-shrink:0"><path d="M22 11.08V12a10 10 0 1 1-5.93-9.14"/><polyline points="22 4 12 14.01 9 11.01"/></svg>
        <span>SSO is enabled for your organization. You can sign in with your identity provider.</span>
      </div>
    </div>`;

    if (tab === 'ssh-keys') {
      // Generate a placeholder public key display
      const fakePubKey = 'ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIG4fP... igloo@workspace';
      return `
    <div class="usettings-section">
      <div class="usettings-section-title">SSH Keys</div>
      <div class="usettings-section-desc">Your workspace SSH public key. Add this to services that need SSH access from your workspaces.</div>
      <div class="usettings-ssh-key-block">
        <div class="usettings-ssh-key-label">Public key</div>
        <div class="usettings-ssh-key-value" id="ssh-pub-key">${esc(fakePubKey)}</div>
        <button class="btn btn-ghost btn-sm" onclick="app.copySSHKey()">
          <svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round"><rect x="9" y="9" width="13" height="13" rx="2"/><path d="M5 15H4a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h9a2 2 0 0 1 2 2v1"/></svg>
          Copy
        </button>
      </div>
      <div class="usettings-coming-soon" style="margin-top:16px">
        <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" style="color:var(--muted);flex-shrink:0"><circle cx="12" cy="12" r="10"/><line x1="12" y1="8" x2="12" y2="12"/><line x1="12" y1="16" x2="12.01" y2="16"/></svg>
        <span>Per-workspace key management and authorized_keys injection coming soon.</span>
      </div>
    </div>`;
    }

    if (tab === 'tokens') return `
    <div class="usettings-section">
      <div class="usettings-section-title-row">
        <div>
          <div class="usettings-section-title">API Tokens</div>
          <div class="usettings-section-desc">Personal access tokens for CLI and API access.</div>
        </div>
        <button class="btn btn-ghost btn-sm" disabled title="Coming soon" style="opacity:0.5;cursor:not-allowed">
          ${icon.plus} Add token
        </button>
      </div>
      <div class="usettings-coming-soon" style="margin-top:20px">
        <svg width="20" height="20" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" style="color:var(--muted)"><rect x="3" y="11" width="18" height="11" rx="2"/><path d="M7 11V7a5 5 0 0 1 10 0v4"/></svg>
        <span>Token management is coming soon. For now, use the session token from your browser's local storage.</span>
      </div>
      <div style="margin-top:12px;padding:14px;background:rgba(0,0,0,0.2);border:1px solid var(--border);border-radius:var(--radius-sm)">
        <div style="font-size:10px;font-weight:700;letter-spacing:.08em;text-transform:uppercase;color:var(--muted);margin-bottom:6px;font-family:var(--font)">Current session token</div>
        <div style="font-family:var(--mono);font-size:11px;color:var(--muted);word-break:break-all;line-height:1.6">${esc((localStorage.getItem('igloo_token')||'').slice(0,48)+'…')}</div>
        <button class="btn btn-ghost btn-sm" style="margin-top:8px" onclick="app.copySessionToken()">
          <svg width="11" height="11" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round"><rect x="9" y="9" width="13" height="13" rx="2"/><path d="M5 15H4a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h9a2 2 0 0 1 2 2v1"/></svg>
          Copy full token
        </button>
      </div>
    </div>`;

    if (tab === 'notifications') return `
    <div class="usettings-section">
      <div class="usettings-section-title">Notifications</div>
      <div class="usettings-section-desc">Control which workspace events trigger notifications.</div>
      <div class="usettings-notif-group">
        <div class="usettings-notif-group-label">Workspace events</div>
        ${[
          ['workspace-started', 'Workspace started', 'When a workspace finishes building or waking'],
          ['workspace-stopped', 'Workspace stopped', 'When a workspace goes to sleep'],
          ['workspace-failed',  'Build failed',      'When a workspace fails to start'],
          ['workspace-deleted', 'Workspace destroyed', 'When a workspace is permanently deleted'],
        ].map(([id, label, hint]) => `
        <div class="usettings-notif-row">
          <div>
            <div class="usettings-notif-label">${label}</div>
            <div class="usettings-notif-hint">${hint}</div>
          </div>
          <label class="toggle-label" style="opacity:0.5;pointer-events:none">
            <input type="checkbox" disabled />
            <span class="toggle-track"><span class="toggle-thumb"></span></span>
          </label>
        </div>`).join('')}
      </div>
      <div class="usettings-coming-soon" style="margin-top:16px">
        <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" style="color:var(--muted);flex-shrink:0"><circle cx="12" cy="12" r="10"/><line x1="12" y1="8" x2="12" y2="12"/><line x1="12" y1="16" x2="12.01" y2="16"/></svg>
        <span>Notification delivery (email, Slack, webhooks) is coming in a future release.</span>
      </div>
    </div>`;

    return '';
  }

  document.getElementById('content').innerHTML = `
  <div class="usettings-layout">
    <aside class="usettings-sidebar">
      <div class="usettings-sidebar-label">User settings</div>
      ${sidebarItems()}
    </aside>
    <div class="usettings-content">
      ${tabContent()}
    </div>
  </div>`;
}

async function renderNetworkBaselineView() {
  document.getElementById('view-title').textContent = 'Network baseline';
  document.getElementById('topbar-actions').innerHTML = '';
  const el = document.getElementById('content');
  el.innerHTML = `<div class="text-muted" style="padding:24px">Loading…</div>`;
  let bl = {};
  try { bl = await api.guardBaseline.get(); } catch {}
  let outside = [];
  try {
    outside = (await api.admin.listWorkspaces()).filter(w => !(w.meta && w.meta.guard_policy) && !['deleting', 'deleted'].includes(w.status));
  } catch {}
  const lines = (a) => esc((a || []).join('\n'));
  el.innerHTML = `
  <div style="max-width:580px;display:flex;flex-direction:column;gap:14px">
    <div class="settings-card">
      <p style="font-size:13px;color:var(--muted);margin-bottom:16px;line-height:1.6">
        Rules every workspace and sandbox must follow, whatever its owner sets. They apply to
        existing workspaces within seconds. One host per line; <span style="font-family:var(--mono);font-size:12px">*.example.com</span> matches subdomains.
      </p>
      <div class="field">
        <label>Default network access</label>
        <select id="bl-mode" onchange="document.getElementById('bl-ceiling-field').style.display = this.value === 'open' ? 'none' : ''">
          <option value="restricted" ${bl.mode === 'open' ? '' : 'selected'}>Restricted: only hosts a workspace's own policy allows</option>
          <option value="open" ${bl.mode === 'open' ? 'selected' : ''}>Open: everything except the blocked list below</option>
        </select>
      </div>
      <div class="field">
        <label>Always blocked (wins over any allow)</label>
        <textarea id="bl-deny" rows="3" style="font-family:var(--mono);font-size:12px">${lines(bl.deny)}</textarea>
      </div>
      <div class="field" id="bl-ceiling-field" style="${bl.mode === 'open' ? 'display:none' : ''}">
        <label>Allowed at most (leave empty for no ceiling)</label>
        <textarea id="bl-ceiling" rows="3" style="font-family:var(--mono);font-size:12px">${lines(bl.ceiling)}</textarea>
      </div>
      <div class="field">
        <label>Longest sandbox lifetime, in seconds (0 for no limit)</label>
        <input id="bl-ttl" type="number" min="0" value="${bl.max_sandbox_ttl_seconds || 0}" style="font-family:var(--mono);font-size:12px" />
      </div>
      <label style="display:flex;gap:8px;align-items:center;font-size:13px;margin-bottom:8px">
        <input id="bl-private" type="checkbox" ${bl.forbid_private ? 'checked' : ''} /> Forbid access to private and internal addresses
      </label>
      <label style="display:flex;gap:8px;align-items:center;font-size:13px;margin-bottom:16px">
        <input id="bl-require" type="checkbox" ${bl.require_guard ? 'checked' : ''} /> Require every workspace to run behind the egress gateway (unguarded ones cannot be created or started)
      </label>
      <button class="btn btn-primary" style="width:auto" onclick="app.saveGuardBaseline()">Save baseline</button>
    </div>
    <div class="settings-card">
      <div class="settings-card-title">Not covered by the baseline (${outside.length})</div>
      <p style="font-size:13px;color:var(--muted);margin-bottom:${outside.length ? 12 : 0}px;line-height:1.6">
        ${outside.length
          ? 'These igloos were created without the egress gateway, so they reach the network directly and none of the rules above apply. A workspace cannot be moved behind the gateway afterwards: create a new one with the gateway on. With "Require every workspace…" ticked, they cannot be started again once stopped.'
          : 'Every workspace runs behind the egress gateway.'}
      </p>
      ${outside.map(w => `<div style="display:flex;justify-content:space-between;gap:12px;font-size:13px;padding:6px 0;border-top:1px solid var(--border)">
        <span>${esc(w.name)}</span><span class="text-muted">${esc(w.status)}</span></div>`).join('')}
    </div>
  </div>`;
}

async function renderIntegrationsView() {
  document.getElementById('view-title').textContent = 'Integrations';
  document.getElementById('topbar-actions').innerHTML = '';
  const el = document.getElementById('content');
  el.innerHTML = `<div class="text-muted" style="padding:24px">Loading…</div>`;

  let cfg = { tailscale_configured: false, tailscale_tailnet: '' };
  try { cfg = await api.platformConfig.get(); } catch {}

  el.innerHTML = `
  <div style="max-width:580px;display:flex;flex-direction:column;gap:14px">
    <div class="settings-card">
      <div class="settings-card-title" style="display:flex;align-items:center;gap:8px">
        Tailscale
        ${cfg.tailscale_configured
          ? '<span style="font-size:11px;background:rgba(34,197,94,.12);color:var(--teal);padding:2px 8px;border-radius:20px;font-weight:500">Connected</span>'
          : '<span style="font-size:11px;background:rgba(255,61,107,.08);color:var(--muted);padding:2px 8px;border-radius:20px">Not configured</span>'}
      </div>
      <p style="font-size:13px;color:var(--muted);margin-bottom:16px;line-height:1.6">
        Set a Tailscale API key once here and every workspace will automatically join your Tailnet on boot — no per-user setup required.
        Generate an API key at <span style="font-family:var(--mono);font-size:12px">tailscale.com/admin/settings/keys</span>
        with <strong>Write</strong> scope.
      </p>
      <div class="field">
        <label>Tailscale API key ${cfg.tailscale_configured ? '(leave blank to keep existing)' : ''}</label>
        <input id="ts-api-key" type="password" placeholder="tskey-api-kXXXXXX-XXXXXXXX"
          style="font-family:var(--mono);font-size:12px" />
      </div>
      <div class="field" style="margin-bottom:16px">
        <label>Tailnet (leave blank for default)</label>
        <input id="ts-tailnet" type="text" placeholder="example.com or - for personal"
          style="font-family:var(--mono);font-size:12px"
          value="${esc(cfg.tailscale_tailnet || '')}" />
      </div>
      <div style="display:flex;gap:8px;align-items:center">
        <button class="btn btn-primary" style="width:auto" onclick="app.savePlatformTailscale()">
          ${cfg.tailscale_configured ? 'Update key' : 'Save & enable'}
        </button>
        ${cfg.tailscale_configured ? `<button class="btn btn-ghost btn-sm" onclick="app.clearPlatformTailscale()">Disconnect</button>` : ''}
      </div>
      ${cfg.tailscale_configured ? `
      <div style="margin-top:14px;padding:12px 14px;background:rgba(138,209,240,.05);border:1px solid rgba(138,209,240,.15);border-radius:var(--radius-sm);font-size:12px;color:var(--muted);line-height:1.7">
        <strong style="color:var(--text)">How it works:</strong> On each workspace wake, Igloo issues a fresh ephemeral auth key via the Tailscale API and injects it into the container. The workspace joins your Tailnet automatically. SSH via <span style="font-family:var(--mono)">ssh root@igloo-&lt;name&gt;</span>.
      </div>` : ''}
    </div>
  </div>`;
}

async function renderUsersView() {
  document.getElementById('view-title').textContent = 'Users';
  document.getElementById('topbar-actions').innerHTML = `<button class="btn btn-primary btn-sm" style="width:auto" onclick="app.addUser()">Add user</button>`;
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
                ${['oidc', 'none'].includes(u.login_type) ? '' : `<button class="btn btn-ghost btn-sm" style="margin-left:6px" onclick="app.resetUserPassword('${u.id}','${esc(u.name||u.email)}')">Reset password</button>`}
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

// ─── Admin workspaces view ────────────────────────────────────────────────────

async function renderAdminWorkspacesView(q) {
  document.getElementById('view-title').textContent = 'All Workspaces';
  document.getElementById('topbar-actions').innerHTML = '';
  const el = document.getElementById('content');
  el.innerHTML = `
  <div class="ws-filter-bar">
    <div class="ws-filter-bar-left">
      <div class="search-bar-wrap" style="position:relative">
        <svg style="position:absolute;left:10px;top:50%;transform:translateY(-50%);color:var(--muted);pointer-events:none" width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round"><circle cx="11" cy="11" r="8"/><line x1="21" y1="21" x2="16.65" y2="16.65"/></svg>
        <input class="search-bar search-bar--with-icon" id="admin-ws-search" placeholder="owner:alice status:running template:kubernetes org:acme" value="${esc(q||'')}"
          oninput="app.searchAdminWorkspaces(this.value)" style="padding-left:32px;width:380px">
      </div>
    </div>
  </div>
  <div id="admin-ws-results"><div class="text-muted" style="padding:24px">Loading…</div></div>`;
  _renderAdminWsResults(q);
}

async function _renderAdminWsResults(q) {
  const res = document.getElementById('admin-ws-results');
  if (!res) return;
  res.innerHTML = `<div class="text-muted" style="padding:24px">Loading…</div>`;
  try {
    const ws = await api.admin.listWorkspaces(q || '');
    if (!ws.length) {
      res.innerHTML = `<div class="empty-state"><h2>No workspaces${q ? ' match' : ''}</h2><p>${q ? 'Try a different query.' : 'No workspaces have been created yet.'}</p></div>`;
      return;
    }
    res.innerHTML = `
    <div style="padding:0 20px 20px">
      <div style="font-size:12px;color:var(--muted);margin-bottom:12px">${ws.length} workspace${ws.length!==1?'s':''} found</div>
      <table class="users-table">
        <thead><tr><th>Name</th><th>Owner</th><th>Template</th><th>Org</th><th>Status</th><th>Updated</th><th></th></tr></thead>
        <tbody>
          ${ws.map(w => `
          <tr>
            <td style="font-weight:600">${esc(w.name)}</td>
            <td style="font-family:var(--mono);font-size:11px;color:var(--muted)">${esc(w.user_id.slice(0,8))}…</td>
            <td>${esc(w.template_name||'—')}</td>
            <td style="font-size:11px;color:var(--muted)">${esc(w.org_id ? w.org_id.slice(0,8)+'…' : '—')}</td>
            <td>${wsStatusBadge(w.status)}</td>
            <td style="font-size:11px;color:var(--muted)">${timeAgo(w.updated_at)}</td>
            <td style="text-align:right;white-space:nowrap;display:flex;gap:6px;justify-content:flex-end">
              ${w.status === 'sleeping' || w.status === 'error'
                ? `<button class="btn btn-ghost btn-sm" onclick="app.adminStartWs('${w.id}')">Start</button>` : ''}
              ${w.status === 'running' || w.status === 'creating'
                ? `<button class="btn btn-ghost btn-sm" onclick="app.adminStopWs('${w.id}')">Stop</button>` : ''}
              <button class="btn btn-danger btn-sm" onclick="app.adminDeleteWs('${w.id}','${esc(w.name)}')">Delete</button>
            </td>
          </tr>`).join('')}
        </tbody>
      </table>
    </div>`;
  } catch(e) {
    res.innerHTML = `<div class="text-muted" style="padding:24px">Failed: ${esc(e.message)}</div>`;
  }
}

function wsStatusBadge(status) {
  const colors = {
    running:  'background:rgba(34,197,94,.1);color:#22c55e;border:1px solid rgba(34,197,94,.2)',
    creating: 'background:rgba(255,196,0,.1);color:#ffc400;border:1px solid rgba(255,196,0,.2)',
    sleeping: 'background:rgba(74,127,165,.1);color:#4a7fa5;border:1px solid rgba(74,127,165,.2)',
    deleting: 'background:rgba(255,61,107,.1);color:#ef4444;border:1px solid rgba(255,61,107,.2)',
    error:    'background:rgba(255,61,107,.1);color:#ef4444;border:1px solid rgba(255,61,107,.2)',
  };
  const s = colors[status] || '';
  return `<span style="font-size:10px;font-weight:700;padding:2px 7px;border-radius:4px;text-transform:uppercase;letter-spacing:.05em;${s}">${status}</span>`;
}

// ─── Organizations views ──────────────────────────────────────────────────────

async function renderOrgsView() {
  document.getElementById('view-title').textContent = 'Organizations';
  document.getElementById('topbar-actions').innerHTML =
    `<button class="btn btn-ghost btn-sm" onclick="app.showCreateOrg()">${icon.plus} New organization</button>`;
  const el = document.getElementById('content');
  el.innerHTML = `<div class="text-muted" style="padding:24px">Loading…</div>`;
  try {
    const orgs = await api.orgs.list();
    state.orgs = orgs || [];
    if (!orgs || !orgs.length) {
      el.innerHTML = `
      <div class="empty-state">
        <div class="empty-icon">🏢</div>
        <h2>No organizations yet</h2>
        <p>Create an organization to manage teams and members.</p>
        <button class="btn btn-ghost btn-sm" style="margin:0 auto" onclick="app.showCreateOrg()">${icon.plus} New organization</button>
      </div>`;
      return;
    }
    el.innerHTML = `
    <div style="max-width:680px;display:grid;gap:6px">
      ${orgs.map(o => `
      <div class="org-list-card" onclick="app.openOrg('${o.id}')">
        ${orgAvatar(o.name, 38, '10px')}
        <div style="flex:1;min-width:0">
          <div style="font-weight:600;font-size:14px;letter-spacing:-0.2px">${esc(o.name)}</div>
          <div style="font-size:11px;color:var(--muted);font-family:var(--mono);margin-top:2px">${esc(o.slug)}</div>
          ${o.description ? `<div style="font-size:12px;color:var(--muted);margin-top:3px">${esc(o.description)}</div>` : ''}
        </div>
        <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.5" stroke-linecap="round" stroke-linejoin="round" style="color:var(--muted);opacity:.5;flex-shrink:0"><polyline points="9 18 15 12 9 6"/></svg>
      </div>`).join('')}
    </div>`;
  } catch(e) {
    el.innerHTML = `<div class="text-muted" style="padding:24px">Failed: ${esc(e.message)}</div>`;
  }
}

async function renderOrgDetailView() {
  const org = state.currentOrg;
  if (!org) { app.setView('orgs'); return; }
  document.getElementById('view-title').textContent = org.name;
  const isActive = state.activeOrgID === org.id;
  document.getElementById('topbar-actions').innerHTML =
    `<button class="btn btn-ghost btn-sm" onclick="app.setView('orgs')">← All orgs</button>
     ${isActive
       ? `<button class="btn btn-ghost btn-sm" onclick="app.clearActiveOrg()" style="color:var(--primary)">✓ Active org — clear</button>`
       : `<button class="btn btn-ghost btn-sm" onclick="app.setActiveOrg('${org.id}')">Set as active org</button>`
     }`;
  const el = document.getElementById('content');
  el.innerHTML = `<div class="text-muted" style="padding:24px">Loading…</div>`;

  try {
    const [members, teams] = await Promise.all([
      api.orgs.listMembers(org.id),
      api.orgs.listTeams(org.id),
    ]);

    const mc = (members||[]).length;
    const tc = (teams||[]).length;

    el.innerHTML = `
    <div style="max-width:820px;display:grid;gap:20px">

      <!-- Org header -->
      <div style="display:flex;align-items:center;gap:14px;padding-bottom:4px">
        ${orgAvatar(org.name, 44, '12px')}
        <div style="flex:1;min-width:0">
          <div style="font-size:18px;font-weight:700;letter-spacing:-0.4px">${esc(org.name)}</div>
          <div style="font-size:12px;color:var(--muted);margin-top:3px;display:flex;align-items:center;gap:6px;flex-wrap:wrap">
            <span style="font-family:var(--mono)">${esc(org.slug)}</span>
            <span style="opacity:.35">·</span>
            <span>${mc} member${mc !== 1 ? 's' : ''}</span>
            <span style="opacity:.35">·</span>
            <span>${tc} team${tc !== 1 ? 's' : ''}</span>
            ${org.description ? `<span style="opacity:.35">·</span><span>${esc(org.description)}</span>` : ''}
          </div>
        </div>
      </div>

      <!-- Members panel -->
      <div class="panel-card">
        <div class="panel-header">
          <div>
            <div class="panel-title">Members</div>
            <div class="panel-subtitle">${mc} member${mc !== 1 ? 's' : ''}</div>
          </div>
          <button class="btn btn-ghost btn-sm" onclick="app.showAddOrgMember('${org.id}')">${icon.plus} Add member</button>
        </div>
        <table class="panel-table">
          <thead><tr><th>Member</th><th>Email</th><th>Role</th><th></th></tr></thead>
          <tbody>
            ${(members||[]).map(m => `
            <tr>
              <td>
                <div style="display:flex;align-items:center;gap:10px">
                  ${userAvatar(m.user_name || m.user_email || '?', 28)}
                  <span style="font-weight:500;font-size:13px">${esc(m.user_name || '—')}</span>
                </div>
              </td>
              <td style="font-family:var(--mono);font-size:12px;color:var(--muted)">${esc(m.user_email || m.user_id)}</td>
              <td>
                <div style="display:flex;align-items:center;gap:8px">
                  ${roleBadge(m.role)}
                  <select class="role-select" style="font-size:11px;padding:2px 4px;opacity:.6" onchange="app.updateOrgMemberRole('${org.id}','${m.user_id}',this.value)" title="Change role">
                    <option value="admin"  ${m.role==='admin'  ?'selected':''}>Admin</option>
                    <option value="member" ${m.role==='member' ?'selected':''}>Member</option>
                    <option value="viewer" ${m.role==='viewer' ?'selected':''}>Viewer</option>
                  </select>
                </div>
              </td>
              <td style="text-align:right;white-space:nowrap">
                <button class="btn btn-ghost btn-sm" style="color:var(--red);font-size:11px"
                  onclick="app.removeOrgMember('${org.id}','${m.user_id}','${esc(m.user_email||'')}')">Remove</button>
              </td>
            </tr>`).join('') || `
            <tr><td colspan="4" style="color:var(--muted);text-align:center;padding:32px;font-size:13px">
              No members yet — add one above.
            </td></tr>`}
          </tbody>
        </table>
      </div>

      <!-- Teams panel -->
      <div class="panel-card">
        <div class="panel-header">
          <div>
            <div class="panel-title">Teams</div>
            <div class="panel-subtitle">${tc ? `${tc} team${tc !== 1 ? 's' : ''}` : 'Synced automatically from OIDC groups'}</div>
          </div>
          <button class="btn btn-ghost btn-sm" onclick="app.showCreateTeam('${org.id}')">${icon.plus} New team</button>
        </div>
        ${tc ? `
        <div style="padding:10px;display:grid;gap:6px">
          ${teams.map(t => `
          <div style="display:flex;align-items:center;gap:12px;padding:10px 12px;background:rgba(255,255,255,0.02);border:1px solid var(--border);border-radius:9px;transition:border-color .15s" onmouseover="this.style.borderColor='rgba(138,209,240,.2)'" onmouseout="this.style.borderColor='var(--border)'">
            ${orgAvatar(t.name, 30, '8px')}
            <div style="flex:1;min-width:0">
              <div style="font-weight:600;font-size:13px">${esc(t.name)}</div>
              ${t.description ? `<div style="font-size:11px;color:var(--muted);margin-top:1px">${esc(t.description)}</div>` : ''}
            </div>
            <button class="btn btn-ghost btn-sm" style="font-size:11px" onclick="app.showTeamMembers('${org.id}','${t.id}','${esc(t.name)}')">Members</button>
            <button class="btn btn-ghost btn-sm" style="color:var(--red);font-size:11px" onclick="app.deleteTeam('${org.id}','${t.id}','${esc(t.name)}')">Delete</button>
          </div>`).join('')}
        </div>` : `
        <div style="padding:36px 20px;text-align:center;color:var(--muted);font-size:13px;line-height:1.7">
          No teams yet.<br>
          <span style="font-size:12px;opacity:.7">Create one manually or they'll sync automatically from your OIDC provider groups.</span>
        </div>`}
      </div>

    </div>`;
  } catch(e) {
    el.innerHTML = `<div class="text-muted" style="padding:24px">Failed: ${esc(e.message)}</div>`;
  }
}

// ─── Modals ───────────────────────────────────────────────────────────────────
let selectedTemplateId = null;

function renderTemplateVarForm(tpl) {
  if (!tpl || !tpl.variables || !tpl.variables.length) return '';
  const fields = tpl.variables.map(v => {
    const id = `ws-var-${v.name}`;
    const labelText = v.display_name || v.name;
    const immutableBadge = v.mutable === false ? `<span style="font-size:10px;padding:1px 5px;border-radius:3px;background:var(--border);color:var(--muted);font-weight:500">immutable</span>` : '';
    const label = `<label for="${id}" style="display:flex;align-items:center;gap:6px">${esc(labelText)}${v.required ? '<span style="color:var(--accent)">*</span>' : ''}${immutableBadge}</label>`;
    const hint = v.description ? `<div class="field-hint">${esc(v.description)}</div>` : '';
    let input;
    if (v.type === 'select' && v.options?.length) {
      input = `<select id="${id}" class="ws-var-field" data-var="${esc(v.name)}">
        ${v.options.map(o => `<option value="${esc(o)}"${o === v.default ? ' selected' : ''}>${esc(o)}</option>`).join('')}
      </select>`;
    } else if (v.type === 'bool') {
      const checked = v.default === 'true' ? 'checked' : '';
      input = `<label class="toggle-label" style="margin-top:6px">
        <input type="checkbox" id="${id}" class="ws-var-field" data-var="${esc(v.name)}" ${checked} />
        <span class="toggle-track"><span class="toggle-thumb"></span></span>
        <span style="margin-left:8px;color:var(--muted);font-size:13px">${v.default === 'true' ? 'Enabled' : 'Disabled'}</span>
      </label>`;
    } else if (v.type === 'number') {
      input = `<input id="${id}" class="ws-var-field" data-var="${esc(v.name)}" type="number" value="${esc(v.default||'')}" ${v.min ? `min="${esc(v.min)}"` : ''} ${v.max ? `max="${esc(v.max)}"` : ''} placeholder="${esc(v.default||v.name)}" />`;
    } else {
      input = `<input id="${id}" class="ws-var-field" data-var="${esc(v.name)}" type="text" value="${esc(v.default||'')}" placeholder="${esc(v.default||v.name)}" />`;
    }
    return `<div class="field" style="margin-bottom:12px">${label}${hint}${input}</div>`;
  }).join('');
  return `<div class="ws-vars-section">
    <div class="ws-vars-header">Parameters</div>
    ${fields}
  </div>`;
}

// A guarded igloo cannot install packages while it starts, so its image has to come with what it
// needs. While the gateway is ticked, offer the server's ready-made image; unticking restores the
// template's own default. Anything the user typed over it is left alone.
function syncGuardImage() {
  const el = document.querySelector('.ws-var-field[data-var="workspace_image"]');
  if (!el || !state.guardImage) return;
  if (!el.dataset.def) el.dataset.def = el.value;
  const guarded = document.getElementById('ws-guard')?.style.display !== 'none' && document.getElementById('ws-guarded')?.checked;
  if (guarded && el.value === el.dataset.def) el.value = state.guardImage;
  else if (!guarded && el.value === state.guardImage) el.value = el.dataset.def;
}

function collectWsVars() {
  const vars = {};
  document.querySelectorAll('.ws-var-field').forEach(el => {
    const name = el.getAttribute('data-var');
    if (!name) return;
    if (el.type === 'checkbox') vars[name] = el.checked ? 'true' : 'false';
    else vars[name] = el.value;
  });
  return vars;
}

async function showNewIglooModal() {
  selectedTemplateId = state.templates[0]?.id || null;
  const firstTpl = state.templates.find(t => t.id === selectedTemplateId);
  let bl = {};
  try { bl = await api.guardBaseline.get(); } catch {}
  state.guardImage = bl.sandbox_image || '';
  const guardSection = !bl.gateway_enabled ? '' : `
      <div class="field" id="ws-guard" style="${firstTpl?.provider === 'kubernetes' ? '' : 'display:none'}">
        <div style="display:flex;gap:8px;align-items:center;font-size:13px">
          <input id="ws-guarded" type="checkbox" style="width:auto;flex:none" checked ${bl.require_guard ? 'disabled' : ''}
            onchange="document.getElementById('ws-guard-hosts').style.display = this.checked ? '' : 'none'; syncGuardImage()" />
          <span>Run behind the egress gateway (recommended)</span>
        </div>
        <div id="ws-guard-hosts" style="margin-top:8px">
          <div style="font-size:12px;color:var(--muted);margin-bottom:6px;line-height:1.5">
            Everything this igloo sends out goes through the gateway: nothing is reachable except what you list, and real credentials never enter it.
            ${bl.mode === 'open' ? 'Your administrator lets igloos reach anything that is not on the blocked list.' : ''}
            ${bl.require_guard ? 'Your administrator requires this.' : 'It cannot be added later.'}
          </div>
          <textarea id="ws-guard-allow" rows="3" placeholder="${bl.mode === 'open' ? 'Not needed: everything except the administrator\'s blocked list is reachable.' : 'Hosts it may reach, one per line (e.g. github.com, *.pypi.org). Empty allows nothing.'}" style="font-family:var(--mono);font-size:12px"></textarea>
        </div>
      </div>`;
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
      <div id="ws-vars">${renderTemplateVarForm(firstTpl)}</div>
      ${guardSection}
      <div class="modal-footer">
        <button class="btn btn-ghost" onclick="document.getElementById('new-ws-modal').remove()">Cancel</button>
        <button class="btn btn-primary" style="width:auto;min-width:130px" onclick="app.createWorkspace()">Start building</button>
      </div>
    </div>
  </div>`);
  syncGuardImage();
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

// ─── Org/Team modals ─────────────────────────────────────────────────────────

function showCreateOrgModal() {
  document.body.insertAdjacentHTML('beforeend', `
  <div class="modal-overlay" id="create-org-modal">
    <div class="modal">
      <div class="modal-header">
        <h2>New organization</h2>
        <button class="btn-icon" onclick="document.getElementById('create-org-modal').remove()">✕</button>
      </div>
      <div class="field"><label>Name</label>
        <input id="org-name" type="text" placeholder="Acme Corp" autofocus /></div>
      <div class="field"><label>Description <span style="color:var(--muted)">(optional)</span></label>
        <input id="org-desc" type="text" placeholder="Our company" /></div>
      <div style="display:flex;gap:10px;justify-content:flex-end;margin-top:8px">
        <button class="btn btn-ghost" onclick="document.getElementById('create-org-modal').remove()">Cancel</button>
        <button class="btn btn-primary" onclick="app.createOrg()">Create</button>
      </div>
    </div>
  </div>`);
}

function showAddOrgMemberModal(orgId) {
  document.body.insertAdjacentHTML('beforeend', `
  <div class="modal-overlay" id="add-member-modal">
    <div class="modal">
      <div class="modal-header">
        <h2>Add member</h2>
        <button class="btn-icon" onclick="document.getElementById('add-member-modal').remove()">✕</button>
      </div>
      <div class="field"><label>Email</label>
        <input id="member-email" type="email" placeholder="avinash@company.com" autofocus /></div>
      <div class="field"><label>Role</label>
        <select id="member-role">
          <option value="member" selected>Member</option>
          <option value="admin">Admin</option>
          <option value="viewer">Viewer</option>
        </select>
      </div>
      <div style="display:flex;gap:10px;justify-content:flex-end;margin-top:8px">
        <button class="btn btn-ghost" onclick="document.getElementById('add-member-modal').remove()">Cancel</button>
        <button class="btn btn-primary" onclick="app.addOrgMember('${orgId}')">Add</button>
      </div>
    </div>
  </div>`);
}

function showCreateTeamModal(orgId) {
  document.body.insertAdjacentHTML('beforeend', `
  <div class="modal-overlay" id="create-team-modal">
    <div class="modal">
      <div class="modal-header">
        <h2>New team</h2>
        <button class="btn-icon" onclick="document.getElementById('create-team-modal').remove()">✕</button>
      </div>
      <div class="field"><label>Name</label>
        <input id="team-name" type="text" placeholder="Engineering" autofocus /></div>
      <div class="field"><label>Description <span style="color:var(--muted)">(optional)</span></label>
        <input id="team-desc" type="text" placeholder="Backend engineers" /></div>
      <div style="display:flex;gap:10px;justify-content:flex-end;margin-top:8px">
        <button class="btn btn-ghost" onclick="document.getElementById('create-team-modal').remove()">Cancel</button>
        <button class="btn btn-primary" onclick="app.createTeam('${orgId}')">Create</button>
      </div>
    </div>
  </div>`);
}

async function showTeamMembersModal(orgId, teamId, teamName) {
  document.getElementById('team-members-modal')?.remove();
  document.body.insertAdjacentHTML('beforeend', `
  <div class="modal-overlay" id="team-members-modal">
    <div class="modal">
      <div class="modal-header">
        <h2>👥 ${esc(teamName)}</h2>
        <button class="btn-icon" onclick="document.getElementById('team-members-modal').remove()">✕</button>
      </div>
      <div id="team-members-list"><div style="color:var(--muted);font-size:13px">Loading…</div></div>
      <div style="margin-top:16px;border-top:1px solid var(--border);padding-top:16px;display:flex;gap:8px">
        <input id="team-member-email" type="email" placeholder="email@company.com" style="flex:1" />
        <button class="btn btn-primary btn-sm" onclick="app.addTeamMember('${orgId}','${teamId}')">Add</button>
      </div>
    </div>
  </div>`);

  const reload = async () => {
    const members = await api.orgs.listTeamMembers(orgId, teamId);
    document.getElementById('team-members-list').innerHTML = members?.length
      ? `<table class="users-table"><thead><tr><th>Name</th><th>Email</th><th></th></tr></thead><tbody>
          ${members.map(m => `<tr>
            <td>${esc(m.user_name||'—')}</td>
            <td style="font-family:var(--mono);font-size:13px">${esc(m.user_email||m.user_id)}</td>
            <td style="text-align:right">
              <button class="btn btn-ghost btn-sm" style="color:var(--danger)"
                onclick="app.removeTeamMember('${orgId}','${teamId}','${m.user_id}')">Remove</button>
            </td>
          </tr>`).join('')}
        </tbody></table>`
      : `<div style="color:var(--muted);font-size:13px;text-align:center;padding:16px">No members yet</div>`;
    window._reloadTeamMembers = reload;
  };
  await reload();
}

// ─── URL ↔ view mapping ───────────────────────────────────────────────────────
const viewToPath = {
  'igloos':           '/',
  'templates':        '/templates',
  'settings':         '/settings',
  'userSettings':     '/settings',
  'users':            '/users',
  'admin-workspaces': '/admin/workspaces',
  'admin-network':    '/admin/network',
  'orgs':             '/orgs',
  'org-detail':       '/orgs',
};
const pathToView = {
  '/':                 'igloos',
  '/igloos':           'igloos',
  '/templates':        'templates',
  '/settings':         'userSettings',
  '/users':            'users',
  '/admin/workspaces': 'admin-workspaces',
  '/admin/network':    'admin-network',
  '/orgs':             'orgs',
};

function applyView(v) {
  state.view = v;
  renderShell();
  if      (v === 'igloos')            renderIgloosView();
  else if (v === 'templates')         renderTemplatesView();
  else if (v === 'settings')          renderUserSettings();
  else if (v === 'userSettings')      renderUserSettings();
  else if (v === 'users')             renderUsersView();
  else if (v === 'admin-workspaces')  renderAdminWorkspacesView();
  else if (v === 'admin-network')     renderNetworkBaselineView();
  else if (v === 'orgs')              renderOrgsView();
  else if (v === 'org-detail')        renderOrgDetailView();
}

window.addEventListener('popstate', e => {
  const v = e.state?.view || pathToView[window.location.pathname] || 'igloos';
  applyView(v);
});

// ─── Main app object ──────────────────────────────────────────────────────────
window.app = {
  setView(v) {
    const path = viewToPath[v] || '/';
    history.pushState({ view: v }, '', path);
    applyView(v);
  },

  searchWorkspaces(q) {
    clearTimeout(this._wsSearchTimer);
    this._wsSearchTimer = setTimeout(() => renderIgloosView(q), 200);
  },

  setWsFilter(key, val) {
    state.wsFilter[key] = val;
    renderIgloosView(document.getElementById('ws-search')?.value || '', { noAnim: true });
  },

  resetWsSearch() {
    state.wsFilter = { status: null, templateId: null };
    renderIgloosView('', { noAnim: true });
    document.getElementById('ws-search')?.focus();
  },

  clearWsFilter() {
    state.wsFilter = { status: null, templateId: null };
    renderIgloosView(document.getElementById('ws-search')?.value || '', { noAnim: true });
  },

  searchTemplates(q) {
    clearTimeout(this._tplSearchTimer);
    this._tplSearchTimer = setTimeout(() => {
      const existing = document.getElementById('tpl-search');
      renderTemplatesView(undefined, q);
      if (existing) { const el = document.getElementById('tpl-search'); if (el) { el.focus(); el.setSelectionRange(q.length,q.length); } }
    }, 200);
  },

  searchAdminWorkspaces(q) {
    clearTimeout(this._adminWsTimer);
    this._adminWsTimer = setTimeout(() => {
      const existing = document.getElementById('admin-ws-results');
      if (existing) {
        _renderAdminWsResults(q);
      } else {
        renderAdminWorkspacesView(q);
      }
    }, 300);
  },

  async adminStartWs(id) {
    try { await api.workspaces.start(id); toast('Workspace starting', 'success'); renderAdminWorkspacesView(); }
    catch(e) { toast(e.message, 'error'); }
  },

  async adminStopWs(id) {
    try { await api.workspaces.stop(id); toast('Workspace stopped', 'success'); renderAdminWorkspacesView(); }
    catch(e) { toast(e.message, 'error'); }
  },

  async adminDeleteWs(id, name) {
    if (!confirm(`Delete workspace "${name}"? This cannot be undone.`)) return;
    try { await api.workspaces.del(id); toast('Workspace deleted', 'success'); renderAdminWorkspacesView(); }
    catch(e) { toast(e.message, 'error'); }
  },

  openOrg(id) {
    state.currentOrg = state.orgs.find(o => o.id === id) || { id };
    this.setView('org-detail');
  },

  async setActiveOrg(id) {
    state.activeOrgID = id;
    // Reload workspace + template data scoped to the new org.
    const [templates, workspaces] = await Promise.all([
      api.templates.list().catch(() => []),
      api.workspaces.list().catch(() => []),
    ]);
    state.templates = templates;
    state.workspaces = workspaces;
    toast(`Now scoped to "${state.orgs.find(o=>o.id===id)?.name || id}"`, 'success');
    renderShell();
    renderOrgDetailView();
  },

  async clearActiveOrg() {
    state.activeOrgID = null;
    const [templates, workspaces] = await Promise.all([
      api.templates.list().catch(() => []),
      api.workspaces.list().catch(() => []),
    ]);
    state.templates = templates;
    state.workspaces = workspaces;
    toast('Org context cleared — showing all workspaces', 'info');
    renderShell();
    renderOrgDetailView();
  },

  showCreateOrg() { showCreateOrgModal(); },

  async createOrg() {
    const name = document.getElementById('org-name')?.value?.trim();
    if (!name) { toast('Enter an organization name', 'error'); return; }
    const desc = document.getElementById('org-desc')?.value?.trim();
    try {
      const org = await api.orgs.create({ name, description: desc });
      state.orgs.push(org);
      document.getElementById('create-org-modal')?.remove();
      state.currentOrg = org;
      this.setView('org-detail');
      toast(`Organization "${org.name}" created`, 'success');
    } catch(e) { toast(e.message, 'error'); }
  },

  showAddOrgMember(orgId) { showAddOrgMemberModal(orgId); },

  async addOrgMember(orgId) {
    const email = document.getElementById('member-email')?.value?.trim();
    const role  = document.getElementById('member-role')?.value;
    if (!email) { toast('Enter an email', 'error'); return; }
    try {
      await api.orgs.addMember(orgId, { email, role });
      document.getElementById('add-member-modal')?.remove();
      toast('Member added', 'success');
      renderOrgDetailView();
    } catch(e) { toast(e.message, 'error'); }
  },

  async updateOrgMemberRole(orgId, userId, role) {
    try {
      await api.orgs.updateMember(orgId, userId, { role });
      toast('Role updated', 'success');
    } catch(e) { toast(e.message, 'error'); renderOrgDetailView(); }
  },

  async removeOrgMember(orgId, userId, email) {
    if (!confirm(`Remove ${email || userId} from this organization?`)) return;
    try {
      await api.orgs.removeMember(orgId, userId);
      toast('Member removed');
      renderOrgDetailView();
    } catch(e) { toast(e.message, 'error'); }
  },

  showCreateTeam(orgId) { showCreateTeamModal(orgId); },

  async createTeam(orgId) {
    const name = document.getElementById('team-name')?.value?.trim();
    if (!name) { toast('Enter a team name', 'error'); return; }
    const desc = document.getElementById('team-desc')?.value?.trim();
    try {
      await api.orgs.createTeam(orgId, { name, description: desc });
      document.getElementById('create-team-modal')?.remove();
      toast(`Team "${name}" created`, 'success');
      renderOrgDetailView();
    } catch(e) { toast(e.message, 'error'); }
  },

  async deleteTeam(orgId, teamId, name) {
    if (!confirm(`Delete team "${name}"?`)) return;
    try {
      await api.orgs.deleteTeam(orgId, teamId);
      toast('Team deleted');
      renderOrgDetailView();
    } catch(e) { toast(e.message, 'error'); }
  },

  showTeamMembers(orgId, teamId, name) { showTeamMembersModal(orgId, teamId, name); },

  async addTeamMember(orgId, teamId) {
    const email = document.getElementById('team-member-email')?.value?.trim();
    if (!email) { toast('Enter an email', 'error'); return; }
    try {
      await api.orgs.addTeamMember(orgId, teamId, { email });
      document.getElementById('team-member-email').value = '';
      toast('Member added to team', 'success');
      if (window._reloadTeamMembers) await window._reloadTeamMembers();
    } catch(e) { toast(e.message, 'error'); }
  },

  async removeTeamMember(orgId, teamId, userId) {
    try {
      await api.orgs.removeTeamMember(orgId, teamId, userId);
      toast('Removed from team');
      if (window._reloadTeamMembers) await window._reloadTeamMembers();
    } catch(e) { toast(e.message, 'error'); }
  },

  showNewIgloo() { showNewIglooModal(); },

  async createWorkspace() {
    const name = document.getElementById('ws-name')?.value?.trim();
    if (!name) { toast('Enter an igloo name', 'error'); return; }
    if (!selectedTemplateId) { toast('Pick a template', 'error'); return; }
    const tpl = state.templates.find(t => t.id === selectedTemplateId);
    if (tpl?.variables?.length) {
      for (const v of tpl.variables) {
        if (v.required) {
          const el = document.querySelector(`.ws-var-field[data-var="${v.name}"]`);
          const val = el?.type === 'checkbox' ? (el.checked ? 'true' : 'false') : el?.value?.trim();
          if (!val) { toast(`"${v.name}" is required`, 'error'); el?.focus(); return; }
        }
      }
    }
    const vars = collectWsVars();
    let guard = null;
    if (document.getElementById('ws-guard')?.style.display !== 'none' && document.getElementById('ws-guarded')?.checked) {
      guard = { allow: document.getElementById('ws-guard-allow').value.split(/[\n,]/).map(x => x.trim()).filter(Boolean) };
    }
    try {
      const ws = await api.workspaces.create(name, selectedTemplateId, vars, guard);
      document.getElementById('new-ws-modal')?.remove();
      state.workspaces.unshift(ws);
      state.selectedWsId = ws.id;
      renderIgloosView();
      toast(`"${ws.name}" is being built`, 'success');
    } catch(e) { toast(e.message, 'error'); }
  },

  async startWorkspace(id) {
    const ws = state.workspaces.find(w => w.id === id);
    if (!ws || ws._pending) return;
    const prevStatus = ws.status;
    ws._pending = 'starting';
    ws.status = 'creating';
    renderIgloosView(document.getElementById('ws-search')?.value || '', { noAnim: true });
    try {
      await api.workspaces.start(id);
      toast('Igloo waking up…', 'success');
      await refreshWorkspaces();
    } catch(e) {
      ws.status = prevStatus;
      toast(e.message, 'error');
      renderIgloosView(document.getElementById('ws-search')?.value || '', { noAnim: true });
    } finally {
      delete ws._pending;
    }
  },

  async stopWorkspace(id) {
    const ws = state.workspaces.find(w => w.id === id);
    if (!ws || ws._pending) return;
    const prevStatus = ws.status;
    ws._pending = 'stopping';
    ws.status = 'sleeping';
    renderIgloosView(document.getElementById('ws-search')?.value || '', { noAnim: true });
    try {
      await api.workspaces.stop(id);
      toast('Igloo sleeping');
    } catch(e) {
      ws.status = prevStatus;
      toast(e.message, 'error');
      renderIgloosView(document.getElementById('ws-search')?.value || '', { noAnim: true });
    } finally {
      delete ws._pending;
    }
  },

  async deleteWorkspace(id, name) {
    if (!confirm(`Melt "${name}"? This cannot be undone.`)) return;
    const ws = state.workspaces.find(w => w.id === id);
    if (!ws || ws._pending) return;
    const prevStatus = ws.status;
    ws._pending = 'deleting';
    ws.status = 'deleting';
    renderIgloosView(document.getElementById('ws-search')?.value || '', { noAnim: true });
    try {
      await api.workspaces.del(id);
      toast('Igloo melting…');
    } catch(e) {
      ws.status = prevStatus;
      toast(e.message, 'error');
      renderIgloosView(document.getElementById('ws-search')?.value || '', { noAnim: true });
    } finally {
      delete ws._pending;
    }
  },

  selectTemplate(id) {
    selectedTemplateId = id;
    const grid = document.getElementById('modal-templates');
    if (grid) grid.innerHTML = state.templates.map(t => renderTemplateCard(t, t.id === id)).join('');
    const guardEl = document.getElementById('ws-guard'); // only the Kubernetes template runs behind the gateway
    if (guardEl) guardEl.style.display = state.templates.find(t => t.id === id)?.provider === 'kubernetes' ? '' : 'none';
    const varsEl = document.getElementById('ws-vars');
    if (varsEl) {
      const tpl = state.templates.find(t => t.id === id);
      varsEl.innerHTML = renderTemplateVarForm(tpl);
      syncGuardImage();
    }
  },

  async selectTemplateView(id) {
    // Support both old list items and new table rows
    document.querySelectorAll('.tpl-list-item, .tpl-table-row').forEach(el => {
      const match = el.getAttribute('onclick')?.includes(`'${id}'`);
      el.classList.toggle('selected', match);
      el.classList.toggle('tpl-table-row--selected', match && el.classList.contains('tpl-table-row'));
    });
    const codeEl = document.getElementById('tpl-code');
    if (!codeEl) return;
    codeEl.innerHTML = `<div class="tpl-code-loading">Loading…</div>`;
    try {
      const files = await api.templates.files(id);
      const entries = Object.entries(files).sort(([a],[b]) => a.localeCompare(b));
      if (!entries.length) { codeEl.innerHTML = `<div class="tpl-code-loading">No Terraform files.</div>`; return; }
      const tpl = state.templates.find(t => t.id === id);
      const versionBar = tpl ? `<div class="tpl-version-bar">v${tpl.version || 1}${tpl.updated_at ? ` · updated ${timeAgo(tpl.updated_at)}` : ''}</div>` : '';
      codeEl.innerHTML = versionBar + entries.map(([name, content]) => `
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
  async changePassword() {
    const cur = document.getElementById('pw-current').value;
    const next = document.getElementById('pw-new').value;
    if (next !== document.getElementById('pw-again').value) { toast('The new passwords do not match', 'error'); return; }
    try {
      const { token } = await api.auth.changePassword(cur, next);
      localStorage.setItem('igloo_token', token); // the old session just ended; carry on with the new one
      toast('Password updated. Other sessions were signed out.', 'success');
      renderUserSettings('security');
    } catch(e) { toast(e.message, 'error'); }
  },

  async addUser() {
    const name = prompt('Name of the new user'); if (!name) return;
    const email = prompt('Their email address'); if (!email) return;
    const password = prompt('A first password for them (8 to 72 characters). They can change it in Settings.'); if (!password) return;
    try { await api.admin.createUser({ name, email, password }); toast('User created', 'success'); renderUsersView(); }
    catch(e) { toast(e.message, 'error'); }
  },

  async resetUserPassword(id, name) {
    const next = prompt(`New password for ${name} (8 to 72 characters). Their open sessions will end.`);
    if (next === null) return;
    try { await api.admin.resetPassword(id, next); toast('Password reset', 'success'); }
    catch(e) { toast(e.message, 'error'); }
  },

  async deleteUserAdmin(id, name) {
    if (!confirm(`Delete user "${name}"? This cannot be undone.`)) return;
    try { await api.admin.deleteUser(id); toast('User deleted'); renderUsersView(); }
    catch(e) { toast(e.message, 'error'); }
  },

  openWorkspace(id) {
    if (state.selectedWsId !== id) state.wsDetailTab = null;
    state.selectedWsId = id;
    renderIgloosView(document.getElementById('ws-search')?.value || '');
  },

  closeWorkspace() {
    state.selectedWsId = null;
    state.wsDetailTab = null;
    renderIgloosView(document.getElementById('ws-search')?.value || '');
  },

  openTerminal(id, name) {
    const url = `/terminal.html?wsId=${encodeURIComponent(id)}&name=${encodeURIComponent(name)}`;
    window.open(url, `igloo-term-${id}`, 'width=900,height=600,menubar=no,toolbar=no,location=no,status=no');
  },
  openNetMap(id, name) {
    const url = `/netmap.html?wsId=${encodeURIComponent(id)}&name=${encodeURIComponent(name)}`;
    window.open(url, `igloo-netmap-${id}`, 'width=1100,height=720,menubar=no,toolbar=no,location=no,status=no,resizable=yes');
  },
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

  async saveTailscaleKey() {
    const key = document.getElementById('tailscale-key-input').value.trim();
    try {
      const user = await api.auth.saveTailscaleKey(key);
      state.user = user;
      renderSettingsView();
      toast('Tailscale key saved — next igloo wake will join your Tailnet', 'success');
    } catch(e) { toast('Failed: ' + e.message, 'error'); }
  },

  async clearTailscaleKey() {
    try {
      const user = await api.auth.saveTailscaleKey('');
      state.user = user;
      renderSettingsView();
      toast('Tailscale key cleared');
    } catch(e) { toast('Failed: ' + e.message, 'error'); }
  },

  async saveGuardBaseline() {
    const list = (id) => document.getElementById(id).value.split('\n').map(x => x.trim()).filter(Boolean);
    try {
      await api.guardBaseline.save({
        mode: document.getElementById('bl-mode').value,
        deny: list('bl-deny'), ceiling: document.getElementById('bl-mode').value === 'open' ? [] : list('bl-ceiling'),
        max_sandbox_ttl_seconds: parseInt(document.getElementById('bl-ttl').value, 10) || 0,
        forbid_private: document.getElementById('bl-private').checked,
        require_guard: document.getElementById('bl-require').checked,
      });
      toast('Network baseline saved', 'success');
      renderIntegrationsView();
    } catch(e) { toast('Failed: ' + e.message, 'error'); }
  },

  async savePlatformTailscale() {
    const apiKey  = document.getElementById('ts-api-key').value.trim();
    const tailnet = document.getElementById('ts-tailnet').value.trim();
    try {
      await api.platformConfig.save({ tailscale_api_key: apiKey, tailscale_tailnet: tailnet });
      toast('Tailscale integration saved — workspaces will auto-join your Tailnet on next wake', 'success');
      renderIntegrationsView();
    } catch(e) { toast('Failed: ' + e.message, 'error'); }
  },

  async clearPlatformTailscale() {
    if (!confirm('Disconnect Tailscale? Workspaces will no longer auto-join your Tailnet.')) return;
    try {
      await api.platformConfig.save({ tailscale_api_key: '', tailscale_tailnet: '' });
      toast('Tailscale disconnected');
      renderIntegrationsView();
    } catch(e) { toast('Failed: ' + e.message, 'error'); }
  },

  logout() {
    localStorage.removeItem('igloo_token');
    state.user = null;
    clearInterval(state.poll);
    location.reload();
  },

  toggleUserMenu() {
    const dropdown = document.getElementById('user-dropdown');
    if (!dropdown) return;
    const isOpen = dropdown.classList.contains('open');
    if (isOpen) {
      dropdown.classList.remove('open');
    } else {
      dropdown.classList.add('open');
      setTimeout(() => {
        const handler = (e) => {
          document.removeEventListener('click', handler);
          if (!document.getElementById('user-menu-wrap')?.contains(e.target)) {
            dropdown.classList.remove('open');
          }
        };
        document.addEventListener('click', handler);
      }, 10);
    }
  },

  setUserSettingsTab(tab) {
    state.userSettingsTab = tab;
    renderUserSettings(tab);
  },

  copySSHKey() {
    const el = document.getElementById('ssh-pub-key');
    if (!el) return;
    navigator.clipboard.writeText(el.textContent).then(() => toast('SSH key copied', 'success')).catch(() => toast('Copy failed', 'error'));
  },

  copySessionToken() {
    const tok = localStorage.getItem('igloo_token') || '';
    navigator.clipboard.writeText(tok).then(() => toast('Token copied', 'success')).catch(() => toast('Copy failed', 'error'));
  },

  // ─── Workspace detail tabs ──────────────────────────────────────────────
  setWsDetailTab(tab) {
    state.wsDetailTab = tab;
    const ws = state.workspaces.find(w => w.id === state.selectedWsId);
    if (!ws) return;
    const pane = document.getElementById('ws-detail-pane');
    if (pane) pane.innerHTML = renderWorkspaceDetail(ws);
  },

  async _loadActivity(id) {
    const refresh = async () => {
      const body = document.getElementById('activity-body');
      if (!body) return false;                       // panel closed or another tab: stop refreshing
      try {
        const [events, pol] = await Promise.all([api.workspaces.activity(id), api.workspaces.guardPolicy(id)]);
        const p = pol.policy || {};
        const allow = (p.allow || []).map(h => `<code>${esc(h)}</code>`).join(' ') || '<span class="activity-none">nothing</span>';
        const routes = (p.routes || []).map(r => `<code>${esc(r.name)}</code>`).join(' ');
        document.getElementById('activity-policy').innerHTML =
          `<div><span class="activity-label">Can reach</span> ${allow}${p.allow_private ? ' <span class="activity-warn">including internal addresses</span>' : ''}</div>` +
          (routes ? `<div><span class="activity-label">Credential routes</span> ${routes}</div>` : '');
        body.innerHTML = renderActivityRows(events);
        const denied = events.filter(e => e.decision === 'deny').length;
        document.getElementById('activity-meta').textContent = `${events.length} recent · ${denied} blocked`;
      } catch (e) {
        body.innerHTML = `<div class="activity-empty"><div class="activity-empty-title">Couldn’t load activity</div><div class="activity-empty-hint">${esc(e.message || '')}</div></div>`;
      }
      return true;
    };
    if (!(await refresh())) return;
    const iv = setInterval(async () => { if (!(await refresh())) clearInterval(iv); }, 5000);
  },

  async _loadLogsInline(id, type) {
    const el = document.getElementById('inline-log-' + id);
    const meta = document.getElementById('inline-log-meta-' + id);
    if (!el) return;
    try {
      const text = type === 'terraform' ? await api.workspaceLogs(id) : await api.agentLogs(id);
      const out = (text || '').trim();
      el.textContent = out || 'No output.';
      if (meta) meta.textContent = out ? `${out.split('\n').length} lines` : '';
      el.scrollTop = el.scrollHeight;
    } catch (e) {
      el.textContent = 'No logs available yet.';
      if (meta) meta.textContent = '';
    }
  },

  // ─── Command palette ────────────────────────────────────────────────────
  openPalette() {
    if (!state.user) return;
    if (document.getElementById('cmdk-overlay')) return;
    const isAdmin = !!state.user?.is_admin;
    const overlay = document.createElement('div');
    overlay.id = 'cmdk-overlay';
    overlay.className = 'cmdk-overlay';
    overlay.innerHTML = `
      <div class="cmdk-panel" role="dialog" aria-label="Command palette">
        <div class="cmdk-input-row">
          <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round"><circle cx="11" cy="11" r="7"/><path d="m21 21-4.3-4.3"/></svg>
          <input id="cmdk-input" type="text" placeholder="Search workspaces, templates, actions…" autocomplete="off" spellcheck="false" />
          <kbd class="cmdk-esc">ESC</kbd>
        </div>
        <div id="cmdk-results" class="cmdk-results"></div>
        <div class="cmdk-footer">
          <span><kbd>↑↓</kbd> navigate</span>
          <span><kbd>↵</kbd> select</span>
          <span><kbd>ESC</kbd> close</span>
        </div>
      </div>
    `;
    document.body.appendChild(overlay);
    overlay.addEventListener('click', (e) => { if (e.target === overlay) this.closePalette(); });

    this._paletteIndex = 0;
    this._paletteFilter = '';
    this._buildPaletteCommands(isAdmin);
    this._renderPaletteResults();

    const input = document.getElementById('cmdk-input');
    input.focus();
    input.addEventListener('input', (e) => {
      this._paletteFilter = e.target.value;
      this._paletteIndex = 0;
      this._renderPaletteResults();
    });
    input.addEventListener('keydown', (e) => {
      const items = this._paletteVisible || [];
      if (e.key === 'ArrowDown') { e.preventDefault(); this._paletteIndex = Math.min(items.length-1, this._paletteIndex+1); this._renderPaletteResults(); }
      else if (e.key === 'ArrowUp') { e.preventDefault(); this._paletteIndex = Math.max(0, this._paletteIndex-1); this._renderPaletteResults(); }
      else if (e.key === 'Enter') { e.preventDefault(); const item = items[this._paletteIndex]; if (item) this._runPaletteItem(item); }
      else if (e.key === 'Escape') { e.preventDefault(); this.closePalette(); }
    });
  },

  closePalette() {
    document.getElementById('cmdk-overlay')?.remove();
  },

  _buildPaletteCommands(isAdmin) {
    const cmds = [];
    // Navigation
    cmds.push({ type: 'nav', label: 'Go to My Igloos',     hint: 'Workspaces', icon: icon.igloo,     run: () => this.setView('igloos') });
    cmds.push({ type: 'nav', label: 'Go to Templates',     hint: 'Browse',     icon: icon.templates, run: () => this.setView('templates') });
    cmds.push({ type: 'nav', label: 'Go to Organizations', hint: 'Teams',      icon: icon.users,     run: () => this.setView('orgs') });
    cmds.push({ type: 'nav', label: 'Open Settings',       hint: 'Account',    icon: icon.settings,  run: () => this.setView('userSettings') });
    if (isAdmin) {
      cmds.push({ type: 'nav', label: 'Admin: Users',         hint: 'Manage', icon: icon.users, run: () => this.setView('users') });
      cmds.push({ type: 'nav', label: 'Admin: All Workspaces', hint: 'Manage', icon: icon.igloo, run: () => this.setView('admin-workspaces') });
    }
    // Quick actions
    cmds.push({ type: 'action', label: 'New workspace',  hint: 'Create',  icon: '+', run: () => this.showNewIgloo() });
    cmds.push({ type: 'action', label: 'Sign out',       hint: 'Account', icon: '⏻', run: () => this.logout() });
    // Workspaces
    (state.workspaces || []).forEach(w => {
      const statusLabel = w.status === 'running' ? '● running' : w.status === 'sleeping' ? '○ sleeping' : w.status;
      cmds.push({ type: 'workspace', label: w.name, hint: statusLabel, icon: icon.igloo, run: () => { state.selectedWsId = w.id; this.setView('igloos'); } });
    });
    // Templates
    (state.templates || []).forEach(t => {
      cmds.push({ type: 'template', label: `Create from ${t.name}`, hint: 'Template', icon: icon.templates, run: () => { this.showNewIgloo(); setTimeout(() => this.selectTemplate(t.id), 80); } });
    });
    this._paletteCommands = cmds;
  },

  _renderPaletteResults() {
    const q = (this._paletteFilter || '').trim().toLowerCase();
    const matches = q
      ? this._paletteCommands.filter(c => {
          const hay = (c.label + ' ' + (c.hint || '')).toLowerCase();
          return q.split(/\s+/).every(part => hay.includes(part));
        })
      : this._paletteCommands;
    this._paletteVisible = matches;
    if (this._paletteIndex >= matches.length) this._paletteIndex = Math.max(0, matches.length - 1);

    const results = document.getElementById('cmdk-results');
    if (!results) return;
    if (!matches.length) {
      results.innerHTML = '<div class="cmdk-empty">No matches</div>';
      return;
    }
    // Group by type for visual separation
    const groups = { nav: 'Navigation', action: 'Actions', workspace: 'Workspaces', template: 'Templates' };
    let html = '';
    let lastType = null;
    matches.forEach((c, i) => {
      if (c.type !== lastType) {
        html += `<div class="cmdk-group-label">${groups[c.type] || c.type}</div>`;
        lastType = c.type;
      }
      html += `
        <button class="cmdk-item ${i === this._paletteIndex ? 'cmdk-item--active' : ''}" data-idx="${i}" onmouseenter="app._paletteHover(${i})" onclick="app._paletteClick(${i})">
          <span class="cmdk-item-icon">${c.icon || ''}</span>
          <span class="cmdk-item-label">${esc(c.label)}</span>
          <span class="cmdk-item-hint">${esc(c.hint || '')}</span>
        </button>
      `;
    });
    results.innerHTML = html;
    // Auto-scroll active into view
    const active = results.querySelector('.cmdk-item--active');
    if (active) active.scrollIntoView({ block: 'nearest' });
  },

  _paletteHover(idx) {
    if (this._paletteIndex === idx) return;
    this._paletteIndex = idx;
    // Cheap update — just toggle classes instead of full re-render
    document.querySelectorAll('.cmdk-item').forEach((el, i) => {
      el.classList.toggle('cmdk-item--active', i === idx);
    });
  },

  _paletteClick(idx) {
    const item = (this._paletteVisible || [])[idx];
    if (item) this._runPaletteItem(item);
  },

  _runPaletteItem(item) {
    this.closePalette();
    try { item.run(); } catch (e) { toast(e.message || 'Action failed', 'error'); }
  },
};

// ─── Auth pages ───────────────────────────────────────────────────────────────

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
      <div class="auth-footer">Already have an account? <a href="javascript:void(0)" onclick="renderLogin()">Sign in →</a></div>
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
    history.replaceState({}, '', '/');
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
    history.replaceState({}, '', '/');
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

// ─── Command palette hotkey ────────────────────────────────────────────────
function installPaletteHotkey() {
  if (window.__paletteHotkeyInstalled) return;
  window.__paletteHotkeyInstalled = true;
  document.addEventListener('keydown', (e) => {
    if ((e.metaKey || e.ctrlKey) && e.key.toLowerCase() === 'k') {
      e.preventDefault();
      if (document.getElementById('cmdk-overlay')) app.closePalette();
      else app.openPalette();
    }
  });
}

// ─── Bootstrap ────────────────────────────────────────────────────────────────
async function refreshWorkspaces() {
  const updated = await api.workspaces.list().catch(() => null);
  if (!updated) return;
  const map = new Map(updated.map(w => [w.id, w]));
  state.workspaces = state.workspaces
    .map(w => map.get(w.id) ?? w)
    .filter(w => map.has(w.id))
    .concat(updated.filter(w => !state.workspaces.find(e => e.id === w.id)));
  if (state.view === 'igloos') renderIgloosView();
}

async function boot() {
  // Restore view from URL on load/refresh.
  const initView = pathToView[window.location.pathname] || 'igloos';
  state.view = initView;
  history.replaceState({ view: initView }, '', window.location.pathname);

  // Render shell + skeleton immediately — don't block on the API.
  state.templates = state.templates || [];
  state.workspaces = state.workspaces || [];
  state._loading = true;
  renderShell();
  renderSkeletonForView(initView);

  // Fetch in parallel, then swap in real content.
  const [templates, workspaces] = await Promise.all([
    api.templates.list().catch(() => []),
    api.workspaces.list().catch(() => []),
  ]);
  state.templates = templates;
  state.workspaces = workspaces;
  state._loading = false;
  applyView(initView);
  term.init();
  startLatencyPolling();
  installPaletteHotkey();
  clearInterval(state.poll);
  state.poll = setInterval(async () => {
    const updated = await api.workspaces.list().catch(() => null);
    if (!updated) return;

    // Detect meaningful changes before merging.
    const oldMap = new Map(state.workspaces.map(w => [w.id, w.status]));
    const changed = updated.length !== state.workspaces.length ||
      updated.some(w => oldMap.get(w.id) !== w.status);

    const map = new Map(updated.map(w => [w.id, w]));
    // If selected workspace was deleted, clear selection.
    if (state.selectedWsId && !map.has(state.selectedWsId)) {
      state.selectedWsId = null;
    }
    state.workspaces = state.workspaces
      .map(w => map.get(w.id) ?? w)
      .filter(w => map.has(w.id))
      .concat(updated.filter(w => !state.workspaces.find(e => e.id === w.id)));

    if (changed && state.view === 'igloos') renderIgloosView(document.getElementById('ws-search')?.value || '', { noAnim: true });
  }, 5000);
}

// ─── OIDC callback: pick up ?token= or ?oidc_error= from the URL ────────────
(function handleOIDCRedirect() {
  const params = new URLSearchParams(window.location.search);
  const tok = params.get('token');
  const err = params.get('oidc_error');
  if (tok) {
    localStorage.setItem('igloo_token', tok);
    // Clean URL so a refresh doesn't re-process the token.
    history.replaceState({}, '', '/');
  }
  if (err) {
    // Stash so renderLogin can display it.
    window.__oidcError = decodeURIComponent(err);
    history.replaceState({}, '', '/');
  }
})();

// Cache of /api/auth/config response
let _authConfig = null;
async function getAuthConfig() {
  if (!_authConfig) {
    try {
      const res = await fetch('/api/auth/config');
      _authConfig = await res.json();
    } catch { _authConfig = { password_auth_enabled: true, oidc_enabled: false }; }
  }
  return _authConfig;
}

function renderLogin(errorMsg) {
  const err = errorMsg || window.__oidcError || '';
  window.__oidcError = null;
  getAuthConfig().then(cfg => {
    const oidcBtn = cfg.oidc_enabled ? `
      <a href="/api/auth/oidc/login" class="btn btn-secondary" style="display:flex;align-items:center;gap:8px;justify-content:center;text-decoration:none;margin-bottom:12px;">
        ${cfg.oidc_icon_url ? `<img src="${esc(cfg.oidc_icon_url)}" style="height:18px">` : ''}
        ${esc(cfg.oidc_button_text || 'Sign in with SSO')}
      </a>` : '';
    const divider = cfg.oidc_enabled && cfg.password_auth_enabled ? `
      <div style="display:flex;align-items:center;gap:8px;margin:4px 0 12px;color:var(--muted);font-size:12px">
        <div style="flex:1;height:1px;background:var(--border)"></div>or<div style="flex:1;height:1px;background:var(--border)"></div>
      </div>` : '';
    const pwFields = cfg.password_auth_enabled ? `
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
      ${cfg.signup_enabled ? `<div class="auth-footer">Don't have an account? <a href="javascript:void(0)" onclick="renderRegister()">Create one</a></div>` : `<div class="auth-footer">Need an account? Ask your admin to create one.</div>`}` : '';
    document.getElementById('root').innerHTML = `
    <div class="auth-page">
      <div class="auth-logo">
        <div class="auth-logo-icon">${icon.logo}</div>
        igloo
      </div>
      <div class="auth-card">
        <h1>Welcome back</h1>
        <p>Sign in to your account to continue.</p>
        ${err ? `<div class="auth-error visible">${esc(err)}</div>` : '<div class="auth-error" id="auth-err"></div>'}
        ${oidcBtn}
        ${divider}
        ${pwFields}
      </div>
    </div>`;
    document.getElementById('email')?.focus();
  });
}

async function init() {
  // Pick up token from OIDC redirect (already stored by handleOIDCRedirect above).
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
