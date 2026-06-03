'use strict';

// ─── API client ───────────────────────────────────────────────────────────────
const api = (() => {
  const base = '';

  function token() { return localStorage.getItem('igloo_token'); }

  async function req(method, path, body) {
    const headers = { 'Content-Type': 'application/json' };
    if (token()) headers['Authorization'] = 'Bearer ' + token();
    const res = await fetch(base + path, {
      method,
      headers,
      body: body ? JSON.stringify(body) : undefined,
    });
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
    },
    workspaces: {
      list:    ()              => req('GET',    '/api/workspaces'),
      create:  (name, tplId)  => req('POST',   '/api/workspaces',          { name, template_id: tplId }),
      get:     (id)           => req('GET',    `/api/workspaces/${id}`),
      start:   (id)           => req('POST',   `/api/workspaces/${id}/start`),
      stop:    (id)           => req('POST',   `/api/workspaces/${id}/stop`),
      del:     (id)           => req('DELETE', `/api/workspaces/${id}`),
      terminal:    (id) => req('GET', `/api/workspaces/${id}/terminal`),
      vsCodeToken: (id) => req('GET', `/api/workspaces/${id}/vscode-token`),
    },
    templates: {
      list:   ()          => req('GET',  `/api/templates`),
      files:  (id)        => req('GET',  `/api/templates/${id}/files`),
      create: (body)      => req('POST', `/api/templates`, body),
      update: (id, body)  => req('PUT',  `/api/templates/${id}`, body),
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
const state = {
  user:       null,
  workspaces: [],
  templates:  [],
  view:       'workspaces', // workspaces | templates | settings
  poll:       null,
};

// ─── Terminal ─────────────────────────────────────────────────────────────────
const term = (() => {
  let xterm, fit, ws, activeWorkspaceId;

  function init() {
    xterm = new Terminal({
      cursorBlink: true,
      scrollback: 10000,
      fontFamily: "'JetBrains Mono', 'Cascadia Code', monospace",
      fontSize: 13,
      theme: {
        background:  '#0d0d0d',
        foreground:  '#e5e5e5',
        cursor:      '#e5e5e5',
        black:       '#1a1a1a',
        red:         '#f44747',
        green:       '#4ec9b0',
        yellow:      '#dcdcaa',
        blue:        '#569cd6',
        magenta:     '#c586c0',
        cyan:        '#9cdcfe',
        white:       '#d4d4d4',
        brightBlack: '#808080',
      },
    });
    fit = new FitAddon.FitAddon();
    xterm.loadAddon(fit);
    xterm.open(document.getElementById('xterm-container'));
    fit.fit();
    window.addEventListener('resize', () => fit.fit());

    xterm.onData(data => {
      if (ws && ws.readyState === WebSocket.OPEN) {
        const encoded = btoa(String.fromCharCode(...new TextEncoder().encode(data)));
        ws.send(JSON.stringify({ type: 'pty_data', data: encoded }));
      }
    });

    xterm.onResize(({ cols, rows }) => {
      if (ws && ws.readyState === WebSocket.OPEN)
        ws.send(JSON.stringify({ type: 'pty_resize', cols, rows }));
    });
  }

  function connect(workspaceId, workspaceName, wsUrl, wsToken) {
    activeWorkspaceId = workspaceId;
    document.getElementById('terminal-workspace-name').textContent = workspaceName;

    if (ws) ws.close();
    xterm.clear();
    xterm.writeln('\x1b[2m[igloo] connecting…\x1b[0m');

    const url = wsUrl.replace(/^http/, 'ws') + `?token=${encodeURIComponent(wsToken)}`;
    ws = new WebSocket(url);

    ws.onopen = () => {
      ws.send(JSON.stringify({ type: 'pty_spawn', cols: xterm.cols, rows: xterm.rows }));
    };

    ws.onmessage = evt => {
      let msg;
      try { msg = JSON.parse(evt.data); } catch { return; }

      if (msg.type === 'pty_output' && msg.data) {
        try {
          const bytes = Uint8Array.from(atob(msg.data), c => c.charCodeAt(0));
          xterm.write(bytes);
        } catch { xterm.write(msg.data); }
      } else if (msg.type === 'pty_ready') {
        xterm.clear();
        xterm.focus();
      } else if (msg.type === 'pty_exit') {
        xterm.writeln('\r\n\x1b[2m[session ended]\x1b[0m');
      } else if (msg.type === 'pty_error') {
        xterm.writeln(`\r\n\x1b[31m[error: ${msg.data}]\x1b[0m`);
      }
    };

    ws.onclose = () => {
      xterm.writeln('\r\n\x1b[2m[disconnected]\x1b[0m');
    };
  }

  function open(workspaceId, workspaceName) {
    api.workspaces.terminal(workspaceId)
      .then(({ token: t, ws_url }) => {
        openPanel();
        connect(workspaceId, workspaceName, ws_url, t);
      })
      .catch(e => toast(e.message, 'error'));
  }

  function openPanel() {
    document.getElementById('terminal-panel').classList.add('open');
    setTimeout(() => { if (fit) fit.fit(); }, 260);
  }

  function closePanel() {
    document.getElementById('terminal-panel').classList.remove('open');
    if (ws) { ws.close(); ws = null; }
  }

  return { init, open, close: closePanel };
})();

// ─── Toast ────────────────────────────────────────────────────────────────────
function toast(msg, type = 'info') {
  const stack = document.getElementById('toast-stack');
  const el = document.createElement('div');
  el.className = `toast ${type}`;
  el.textContent = msg;
  stack.appendChild(el);
  setTimeout(() => el.remove(), 3500);
}

// ─── SVG icons ────────────────────────────────────────────────────────────────
const icon = {
  workspaces: `<svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><rect x="2" y="3" width="20" height="14" rx="2"/><line x1="8" y1="21" x2="16" y2="21"/><line x1="12" y1="17" x2="12" y2="21"/></svg>`,
  templates:  `<svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><path d="M4 4h6v6H4zM14 4h6v6h-6zM4 14h6v6H4zM14 14h6v6h-6z"/></svg>`,
  settings:   `<svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><circle cx="12" cy="12" r="3"/><path d="M19.4 15a1.65 1.65 0 0 0 .33 1.82l.06.06a2 2 0 0 1-2.83 2.83l-.06-.06a1.65 1.65 0 0 0-1.82-.33 1.65 1.65 0 0 0-1 1.51V21a2 2 0 0 1-4 0v-.09A1.65 1.65 0 0 0 9 19.4a1.65 1.65 0 0 0-1.82.33l-.06.06a2 2 0 0 1-2.83-2.83l.06-.06A1.65 1.65 0 0 0 4.68 15a1.65 1.65 0 0 0-1.51-1H3a2 2 0 0 1 0-4h.09A1.65 1.65 0 0 0 4.6 9a1.65 1.65 0 0 0-.33-1.82l-.06-.06a2 2 0 0 1 2.83-2.83l.06.06A1.65 1.65 0 0 0 9 4.68a1.65 1.65 0 0 0 1-1.51V3a2 2 0 0 1 4 0v.09a1.65 1.65 0 0 0 1 1.51 1.65 1.65 0 0 0 1.82-.33l.06-.06a2 2 0 0 1 2.83 2.83l-.06.06A1.65 1.65 0 0 0 19.4 9a1.65 1.65 0 0 0 1.51 1H21a2 2 0 0 1 0 4h-.09a1.65 1.65 0 0 0-1.51 1z"/></svg>`,
  plus:       `<svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.5"><line x1="12" y1="5" x2="12" y2="19"/><line x1="5" y1="12" x2="19" y2="12"/></svg>`,
  logo:       `<svg width="22" height="22" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><polygon points="12 2 15.09 8.26 22 9.27 17 14.14 18.18 21.02 12 17.77 5.82 21.02 7 14.14 2 9.27 8.91 8.26 12 2"/></svg>`,
  users:      `<svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><path d="M17 21v-2a4 4 0 0 0-4-4H5a4 4 0 0 0-4 4v2"/><circle cx="9" cy="7" r="4"/><path d="M23 21v-2a4 4 0 0 0-3-3.87"/><path d="M16 3.13a4 4 0 0 1 0 7.75"/></svg>`,
};

// ─── Renderers ────────────────────────────────────────────────────────────────

function statusBadge(status) {
  const labels = { running: 'Running', sleeping: 'Sleeping', creating: 'Starting…', error: 'Error' };
  return `<span class="status-badge status-${status}">
    <span class="status-dot"></span>${labels[status] || status}
  </span>`;
}

function costLabel(ws) {
  if (ws.status === 'running' && ws.cost_per_hr > 0)
    return `<span class="cost-badge">$${ws.cost_per_hr.toFixed(4)}/hr</span>`;
  if (ws.status === 'sleeping')
    return `<span class="cost-badge" style="color:var(--green)">$0.00/hr</span>`;
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

function renderWorkspaceCard(ws) {
  const isDeleting = ws.status === 'deleting';
  const canConnect = ws.status === 'running';
  const canStart   = ws.status === 'sleeping' || ws.status === 'error';
  const canStop    = ws.status === 'running' || ws.status === 'creating';

  return `
  <div class="workspace-card" data-id="${ws.id}">
    <div class="workspace-card-header">
      <div>
        <div class="workspace-name">${esc(ws.name)}</div>
        <div class="workspace-provider">${esc(ws.template_name)}</div>
      </div>
      ${statusBadge(ws.status)}
    </div>
    <div class="workspace-meta">
      <span class="text-muted text-sm">${timeAgo(ws.last_active_at)}</span>
      ${costLabel(ws)}
    </div>
    <div class="workspace-actions">
      ${canConnect ? `<button class="btn btn-primary" onclick="app.openTerminal('${ws.id}','${esc(ws.name)}')">Terminal</button>` : ''}
      ${canConnect ? `<button class="btn btn-secondary btn-sm" onclick="app.openVSCode('${ws.id}')">VS Code</button>` : ''}
      ${canStart   ? `<button class="btn btn-ghost" onclick="app.startWorkspace('${ws.id}')">Wake</button>` : ''}
      ${canStop    ? `<button class="btn btn-ghost" onclick="app.stopWorkspace('${ws.id}')">Sleep</button>` : ''}
      ${renderWorkspaceCardLogs(ws)}
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

function renderWorkspaceCardLogs(ws) {
  const isTerraform = ws.provider === 'docker-terraform' || ws.provider === 'aws-fargate' || ws.provider === 'fly' || ws.provider === 'gcp-cloudrun';
  const tab = isTerraform ? 'terraform' : 'agent';
  return `<button class="btn btn-ghost" onclick="app.showLogs('${ws.id}','${esc(ws.name)}','${tab}')">Logs</button>`;
}

function esc(s) {
  return String(s || '').replace(/&/g,'&amp;').replace(/</g,'&lt;').replace(/>/g,'&gt;').replace(/"/g,'&quot;');
}

// ─── App shell ────────────────────────────────────────────────────────────────

function renderShell() {
  const u = state.user;
  const initials = u ? (u.name || u.email).slice(0,2).toUpperCase() : '??';

  document.getElementById('root').innerHTML = `
  <div class="app">
    <aside class="sidebar">
      <div class="sidebar-logo">
        ${icon.logo} igloo
      </div>
      <nav class="sidebar-nav">
        <button class="nav-item ${state.view==='workspaces'?'active':''}" onclick="app.setView('workspaces')">
          ${icon.workspaces} Workspaces
        </button>
        <button class="nav-item ${state.view==='templates'?'active':''}" onclick="app.setView('templates')">
          ${icon.templates} Templates
        </button>
        ${u?.is_admin ? `<button class="nav-item ${state.view==='users'?'active':''}" onclick="app.setView('users')">
          ${icon.users} Users
        </button>` : ''}
        <button class="nav-item ${state.view==='settings'?'active':''}" onclick="app.setView('settings')">
          ${icon.settings} Settings
        </button>
      </nav>
      <div class="sidebar-footer">
        <div class="user-pill">
          <div class="user-avatar">${initials}</div>
          <div style="overflow:hidden">
            <div class="user-name" style="display:flex;align-items:center;gap:6px">
              ${esc(u?.name || '')}
              ${u?.is_admin ? '<span class="admin-badge">Admin</span>' : ''}
            </div>
            <div class="user-email text-sm text-muted">${esc(u?.email || '')}</div>
          </div>
        </div>
      </div>
    </aside>
    <div class="main">
      <div class="topbar">
        <h1 id="view-title">Workspaces</h1>
        <div id="topbar-actions"></div>
      </div>
      <div class="content" id="content"></div>
    </div>
  </div>`;
}

function renderWorkspacesView() {
  document.getElementById('view-title').textContent = 'Workspaces';
  document.getElementById('topbar-actions').innerHTML =
    `<button class="btn btn-primary" onclick="app.showNewWorkspace()">${icon.plus} New workspace</button>`;

  const el = document.getElementById('content');
  if (!state.workspaces.length) {
    el.innerHTML = `
      <div class="empty-state">
        <div class="empty-icon">❄️</div>
        <h2>No workspaces yet</h2>
        <p>Create your first workspace from a template and connect instantly.</p>
        <button class="btn btn-primary" style="margin-top:8px" onclick="app.showNewWorkspace()">${icon.plus} New workspace</button>
      </div>`;
    return;
  }

  el.innerHTML = `<div class="workspace-grid">${state.workspaces.map(renderWorkspaceCard).join('')}</div>`;
}

function renderTemplatesView(selectedId) {
  const isAdmin = state.user?.is_admin;
  document.getElementById('view-title').textContent = 'Templates';
  document.getElementById('topbar-actions').innerHTML = isAdmin
    ? `<button class="btn btn-primary" onclick="app.showNewTemplate()">${icon.plus} New template</button>`
    : '';

  const sel = selectedId || state.templates[0]?.id;
  const el = document.getElementById('content');
  el.innerHTML = `
  <div class="templates-layout">
    <div class="templates-list" id="tpl-list">
      ${state.templates.map(t => `
        <div class="tpl-list-item${t.id === sel ? ' selected' : ''}" onclick="app.selectTemplateView('${t.id}')">
          <span class="template-icon" style="font-size:1.2em">${t.icon || '📦'}</span>
          <div style="flex:1;min-width:0">
            <div class="template-name">${esc(t.name)}</div>
            <div class="template-desc text-sm text-muted">${esc(t.description)}</div>
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
    <div style="max-width:540px">
      <div class="workspace-card">
        <div class="workspace-name">Account</div>
        <div class="divider"></div>
        <div class="field"><label>Name</label>
          <input type="text" value="${esc(state.user?.name)}" readonly style="opacity:0.7" />
        </div>
        <div class="field"><label>Email</label>
          <input type="text" value="${esc(state.user?.email)}" readonly style="opacity:0.7" />
        </div>
      </div>
      <div class="workspace-card" style="margin-top:12px">
        <div class="workspace-name">SSH Public Key</div>
        <div class="divider"></div>
        <p style="font-size:13px;color:var(--text-muted);margin:0 0 12px">
          Your public key is automatically injected into every workspace you start.
          Add it once here — no manual setup needed.
        </p>
        <div class="field">
          <label>Public key (paste your <code>~/.ssh/id_ed25519.pub</code>)</label>
          <textarea id="ssh-key-input" rows="3" placeholder="ssh-ed25519 AAAA..." style="width:100%;font-family:monospace;font-size:12px;resize:vertical">${esc(state.user?.ssh_public_key || '')}</textarea>
        </div>
        <button class="btn btn-primary btn-sm" onclick="app.saveSSHKey()">Save key</button>
      </div>
      <div style="margin-top:12px">
        <button class="btn btn-ghost" onclick="app.logout()">Sign out</button>
      </div>
    </div>`;
}


// ─── Users view (admin only) ──────────────────────────────────────────────────

async function renderUsersView() {
  document.getElementById('view-title').textContent = 'Users';
  document.getElementById('topbar-actions').innerHTML = '';
  const el = document.getElementById('content');
  el.innerHTML = `<div class="text-muted" style="padding:24px">Loading…</div>`;
  try {
    const users = await api.admin.listUsers();
    el.innerHTML = `
    <div style="max-width:760px">
      <table class="users-table">
        <thead>
          <tr>
            <th>Name</th><th>Email</th><th>Role</th><th></th>
          </tr>
        </thead>
        <tbody>
          ${users.map(u => `
          <tr>
            <td>${esc(u.name || '—')}</td>
            <td>${esc(u.email)}</td>
            <td>${u.is_admin ? '<span class="admin-badge">Admin</span>' : '<span class="text-muted">Member</span>'}</td>
            <td style="text-align:right;white-space:nowrap">
              ${u.id !== state.user.id ? `
                ${u.is_admin
                  ? `<button class="btn btn-ghost btn-sm" onclick="app.demoteUser('${u.id}')">Remove admin</button>`
                  : `<button class="btn btn-ghost btn-sm" onclick="app.promoteUser('${u.id}')">Make admin</button>`}
                <button class="btn btn-danger btn-sm" style="margin-left:6px" onclick="app.deleteUserAdmin('${u.id}','${esc(u.name || u.email)}')">Delete</button>
              ` : '<span class="text-muted text-sm">you</span>'}
            </td>
          </tr>`).join('')}
        </tbody>
      </table>
    </div>`;
  } catch(e) {
    el.innerHTML = `<div class="text-muted" style="padding:24px">Failed to load users: ${esc(e.message)}</div>`;
  }
}

// ─── Template edit / create modals (admin) ────────────────────────────────────

function templateFilesHTML(files) {
  const entries = Object.entries(files || {}).sort(([a],[b]) => a.localeCompare(b));
  if (!entries.length) entries.push(['main.tf', '']);
  return entries.map(([name, content], i) => `
    <div class="tf-editor-block" data-idx="${i}">
      <div style="display:flex;align-items:center;gap:8px;margin-bottom:6px">
        <input class="tf-filename" type="text" value="${esc(name)}" placeholder="main.tf" style="font-family:var(--mono);font-size:12px;flex:1" />
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
  if (!isNew) {
    try { files = await api.templates.files(template.id); } catch {}
  }

  document.getElementById('tpl-modal')?.remove();
  document.body.insertAdjacentHTML('beforeend', `
  <div class="modal-overlay" id="tpl-modal">
    <div class="modal modal-wide" style="max-width:740px">
      <div class="modal-header">
        <h2>${isNew ? 'New template' : 'Edit template'}</h2>
        <button class="btn-icon" onclick="document.getElementById('tpl-modal').remove()">✕</button>
      </div>
      <div style="display:grid;grid-template-columns:1fr 1fr 80px;gap:10px;margin-bottom:16px">
        <div class="field" style="margin:0">
          <label>Name</label>
          <input id="tpl-name" type="text" value="${esc(template?.name || '')}" placeholder="My Template" />
        </div>
        <div class="field" style="margin:0">
          <label>Provider</label>
          <input id="tpl-provider" type="text" value="${esc(template?.provider || '')}" placeholder="aws-fargate" ${!isNew ? 'readonly style="opacity:.6"' : ''} />
        </div>
        <div class="field" style="margin:0">
          <label>Icon</label>
          <input id="tpl-icon" type="text" value="${esc(template?.icon || '')}" placeholder="🚀" style="font-size:18px;text-align:center" />
        </div>
      </div>
      <div class="field">
        <label>Description</label>
        <input id="tpl-desc" type="text" value="${esc(template?.description || '')}" placeholder="Short description shown to users" />
      </div>
      <div style="display:flex;align-items:center;justify-content:space-between;margin:16px 0 8px">
        <label style="font-weight:500">Terraform files</label>
        <button class="btn btn-ghost btn-sm" onclick="app._addTfFile()">+ Add file</button>
      </div>
      <div id="tpl-files">${templateFilesHTML(files)}</div>
      <div class="modal-footer">
        <button class="btn btn-ghost" onclick="document.getElementById('tpl-modal').remove()">Cancel</button>
        <button class="btn btn-primary" onclick="app.saveTemplate('${template?.id || ''}')">
          ${isNew ? 'Create template' : 'Save changes'}
        </button>
      </div>
    </div>
  </div>`);
}

// ─── New workspace modal ──────────────────────────────────────────────────────

let selectedTemplateId = null;

function showNewWorkspaceModal() {
  selectedTemplateId = state.templates[0]?.id || null;

  document.body.insertAdjacentHTML('beforeend', `
  <div class="modal-overlay" id="new-ws-modal">
    <div class="modal">
      <div class="modal-header">
        <h2>New workspace</h2>
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
        <button class="btn btn-primary" style="min-width:120px" onclick="app.createWorkspace()">Create</button>
      </div>
    </div>
  </div>`);
}

// ─── Main app object (exposed to HTML onclick) ────────────────────────────────

window.app = {
  setView(v) {
    state.view = v;
    renderShell();
    if (v === 'workspaces') renderWorkspacesView();
    else if (v === 'templates') renderTemplatesView();
    else if (v === 'settings') renderSettingsView();
    else if (v === 'users') renderUsersView();
  },

  async promoteUser(id) {
    try {
      await api.admin.updateUser(id, { is_admin: true });
      toast('User promoted to admin', 'success');
      renderUsersView();
    } catch(e) { toast(e.message, 'error'); }
  },

  async demoteUser(id) {
    try {
      await api.admin.updateUser(id, { is_admin: false });
      toast('Admin access removed', 'success');
      renderUsersView();
    } catch(e) { toast(e.message, 'error'); }
  },

  async deleteUserAdmin(id, name) {
    if (!confirm(`Delete user "${name}"? This cannot be undone.`)) return;
    try {
      await api.admin.deleteUser(id);
      toast(`User deleted`);
      renderUsersView();
    } catch(e) { toast(e.message, 'error'); }
  },

  async selectTemplateView(id) {
    // Highlight selected in list
    document.querySelectorAll('.tpl-list-item').forEach(el => {
      el.classList.toggle('selected', el.getAttribute('onclick')?.includes(`'${id}'`));
    });
    const codeEl = document.getElementById('tpl-code');
    if (!codeEl) return;
    codeEl.innerHTML = `<div class="tpl-code-loading">Loading…</div>`;
    try {
      const files = await api.templates.files(id);
      const entries = Object.entries(files).sort(([a],[b]) => a.localeCompare(b));
      if (!entries.length) {
        codeEl.innerHTML = `<div class="tpl-code-loading text-muted">No Terraform files for this template.</div>`;
        return;
      }
      codeEl.innerHTML = entries.map(([name, content]) => `
        <div class="tf-file">
          <div class="tf-file-name">${esc(name)}</div>
          <pre class="tf-code">${esc(content)}</pre>
        </div>`).join('');
    } catch(e) {
      codeEl.innerHTML = `<div class="tpl-code-loading text-muted">No Terraform files for this template.</div>`;
    }
  },

  async showLogs(id, name, initialTab) {
    document.getElementById('logs-modal')?.remove();
    const ws = state.workspaces.find(w => w.id === id);
    const hasTerraform = ws && (ws.provider === 'docker-terraform' || ws.provider === 'aws-fargate' || ws.provider === 'fly' || ws.provider === 'gcp-cloudrun');
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

    const logsInterval = setInterval(async () => {
      if (!document.getElementById('logs-modal')) { clearInterval(logsInterval); return; }
      const tab = document.querySelector('.logs-tab.active')?.textContent?.toLowerCase();
      const cur = state.workspaces.find(w => w.id === id);
      await this._loadLogs(id, tab === 'terraform' ? 'terraform' : 'agent');
      if (cur && cur.status !== 'creating') clearInterval(logsInterval);
    }, 2000);
  },

  async _loadLogs(id, tab) {
    const el = document.getElementById('logs-content');
    if (!el) return;
    try {
      const text = tab === 'terraform'
        ? await api.workspaceLogs(id)
        : await api.agentLogs(id);
      el.textContent = text || '(no output yet)';
      el.scrollTop = el.scrollHeight;
    } catch(e) {
      el.textContent = e.message || '(no logs available)';
    }
  },

  async switchLogsTab(id, name, tab) {
    document.querySelectorAll('.logs-tab').forEach(b => {
      b.classList.toggle('active', b.textContent.toLowerCase() === tab);
    });
    await this._loadLogs(id, tab);
  },

  closeLogs() {
    document.getElementById('logs-modal')?.remove();
  },

  selectTemplate(id) {
    selectedTemplateId = id;
    document.querySelectorAll('#modal-templates .template-card').forEach(el => {
      el.classList.toggle('selected', el.onclick.toString().includes(`'${id}'`));
    });
    // Re-render modal templates cleanly
    const grid = document.getElementById('modal-templates');
    if (grid) grid.innerHTML = state.templates.map(t => renderTemplateCard(t, t.id === id)).join('');
  },

  showNewWorkspace() { showNewWorkspaceModal(); },

  async createWorkspace() {
    const name = document.getElementById('ws-name')?.value?.trim();
    if (!name) { toast('Enter a workspace name', 'error'); return; }
    if (!selectedTemplateId) { toast('Pick a template', 'error'); return; }
    try {
      const ws = await api.workspaces.create(name, selectedTemplateId);
      document.getElementById('new-ws-modal')?.remove();
      state.workspaces.unshift(ws);
      renderWorkspacesView();
      toast(`Workspace "${ws.name}" created`, 'success');
    } catch(e) { toast(e.message, 'error'); }
  },

  async startWorkspace(id) {
    const ws = state.workspaces.find(w => w.id === id);
    if (ws) { ws.status = 'creating'; renderWorkspacesView(); }
    try {
      await api.workspaces.start(id);
      await refreshWorkspaces();
      toast('Workspace starting…', 'success');
    } catch(e) { toast(e.message, 'error'); await refreshWorkspaces(); }
  },

  async stopWorkspace(id) {
    try {
      await api.workspaces.stop(id);
      const ws = state.workspaces.find(w => w.id === id);
      if (ws) ws.status = 'sleeping';
      renderWorkspacesView();
      toast('Workspace sleeping');
    } catch(e) { toast(e.message, 'error'); }
  },

  async deleteWorkspace(id, name) {
    if (!confirm(`Destroy "${name}"? This cannot be undone.`)) return;
    try {
      await api.workspaces.del(id);
      state.workspaces = state.workspaces.filter(w => w.id !== id);
      renderWorkspacesView();
      toast(`Workspace destroyed`);
    } catch(e) { toast(e.message, 'error'); }
  },

  showNewTemplate() { showTemplateModal(null); },

  async editTemplate(id) {
    const t = state.templates.find(t => t.id === id);
    await showTemplateModal(t);
  },

  _addTfFile() {
    const container = document.getElementById('tpl-files');
    const idx = container.querySelectorAll('.tf-editor-block').length;
    const div = document.createElement('div');
    div.innerHTML = `
      <div class="tf-editor-block" data-idx="${idx}">
        <div style="display:flex;align-items:center;gap:8px;margin-bottom:6px">
          <input class="tf-filename" type="text" value="" placeholder="variables.tf" style="font-family:var(--mono);font-size:12px;flex:1" />
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
    const icon     = document.getElementById('tpl-icon').value.trim();
    const desc     = document.getElementById('tpl-desc').value.trim();
    const files    = collectTemplateFiles(modal);

    if (!name) { toast('Name is required', 'error'); return; }

    try {
      if (id) {
        const updated = await api.templates.update(id, { name, description: desc, icon, files });
        const idx = state.templates.findIndex(t => t.id === id);
        if (idx !== -1) state.templates[idx] = updated;
        toast('Template saved', 'success');
      } else {
        if (!provider) { toast('Provider is required', 'error'); return; }
        const created = await api.templates.create({ name, provider, description: desc, icon, files });
        state.templates.push(created);
        toast('Template created', 'success');
      }
      modal.remove();
      renderTemplatesView(id || state.templates[state.templates.length - 1]?.id);
    } catch(e) { toast(e.message, 'error'); }
  },

  openTerminal(id, name) { term.open(id, name); },
  openVSCode(id) {
    api.workspaces.vsCodeToken(id)
      .then(data => window.open(data.url, '_blank'))
      .catch(err => alert('VS Code unavailable: ' + (err.message || err)));
  },
  closeTerminal()        { term.close(); },
  maximiseTerminal() {
    const p = document.getElementById('terminal-panel');
    p.style.height = p.style.height === '100vh' ? '420px' : '100vh';
    setTimeout(() => term.init && window.dispatchEvent(new Event('resize')), 10);
  },

  async saveSSHKey() {
    const key = document.getElementById('ssh-key-input').value.trim();
    try {
      const user = await req('PUT', '/api/auth/me/ssh-key', { ssh_public_key: key });
      state.user = user;
      toast('SSH key saved — new workspaces will have it automatically', 'success');
    } catch(e) {
      toast('Failed to save SSH key: ' + e.message, 'error');
    }
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
    <div class="auth-logo">${icon.logo} igloo</div>
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
      <div class="auth-footer">
        Don't have an account? <a href="#" onclick="renderRegister()">Create one →</a>
      </div>
    </div>
  </div>`;
  document.getElementById('email')?.focus();
}

function renderRegister() {
  document.getElementById('root').innerHTML = `
  <div class="auth-page">
    <div class="auth-logo">${icon.logo} igloo</div>
    <div class="auth-card">
      <h1>Create your account</h1>
      <p>Start building with serverless dev workspaces.</p>
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
      <div class="auth-footer">
        Already have an account? <a href="#" onclick="renderLogin()">Sign in →</a>
      </div>
    </div>
  </div>`;
  document.getElementById('name')?.focus();
}

async function handleLogin() {
  const btn = document.getElementById('login-btn');
  const errEl = document.getElementById('auth-err');
  const email = document.getElementById('email').value.trim();
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
  const name = document.getElementById('name').value.trim();
  const email = document.getElementById('email').value.trim();
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
  if (state.view === 'workspaces') renderWorkspacesView();
}

async function boot() {
  // Load templates + workspaces in parallel
  [state.templates, state.workspaces] = await Promise.all([
    api.templates.list().catch(() => []),
    api.workspaces.list().catch(() => []),
  ]);

  renderShell();
  renderWorkspacesView();
  term.init();

  // Poll workspace status every 5s so status badges update automatically
  clearInterval(state.poll);
  state.poll = setInterval(async () => {
    const updated = await api.workspaces.list().catch(() => null);
    if (!updated) return;
    state.workspaces = updated;
    if (state.view === 'workspaces') renderWorkspacesView();
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
