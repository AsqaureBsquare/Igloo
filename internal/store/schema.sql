CREATE TABLE IF NOT EXISTS users (
    id              TEXT PRIMARY KEY,
    email           TEXT UNIQUE NOT NULL,
    name            TEXT NOT NULL DEFAULT '',
    password_hash   TEXT NOT NULL DEFAULT '',
    ssh_public_key  TEXT NOT NULL DEFAULT '',
    tailscale_auth_key TEXT NOT NULL DEFAULT '',
    is_admin        BOOLEAN NOT NULL DEFAULT FALSE,
    created_at      TIMESTAMPTZ NOT NULL
);

CREATE TABLE IF NOT EXISTS workspaces (
    id            TEXT PRIMARY KEY,
    user_id       TEXT NOT NULL,
    name          TEXT NOT NULL,
    template_id   TEXT NOT NULL,
    template_name TEXT NOT NULL,
    provider      TEXT NOT NULL,
    status        TEXT NOT NULL,
    agent_token   TEXT NOT NULL DEFAULT '',
    cost_per_hr   DOUBLE PRECISION NOT NULL DEFAULT 0,
    created_at    TIMESTAMPTZ NOT NULL,
    updated_at    TIMESTAMPTZ NOT NULL,
    last_active_at TIMESTAMPTZ NOT NULL,
    vars          JSONB NOT NULL DEFAULT '{}',
    meta          JSONB NOT NULL DEFAULT '{}'
);
CREATE INDEX IF NOT EXISTS workspaces_user_id_idx ON workspaces(user_id);

CREATE TABLE IF NOT EXISTS templates (
    id          TEXT PRIMARY KEY,
    name        TEXT NOT NULL,
    description TEXT NOT NULL DEFAULT '',
    provider    TEXT NOT NULL,
    icon        TEXT NOT NULL DEFAULT '',
    builtin     BOOLEAN NOT NULL DEFAULT FALSE,
    variables   JSONB NOT NULL DEFAULT '[]',
    files       JSONB NOT NULL DEFAULT '{}',
    created_at  TIMESTAMPTZ NOT NULL
);

CREATE TABLE IF NOT EXISTS jobs (
    id             TEXT PRIMARY KEY,
    workspace_id   TEXT NOT NULL,
    type           TEXT NOT NULL,
    status         TEXT NOT NULL,
    provisioner_id TEXT NOT NULL DEFAULT '',
    created_at     TIMESTAMPTZ NOT NULL,
    updated_at     TIMESTAMPTZ NOT NULL,
    heartbeat      TIMESTAMPTZ,
    error          TEXT NOT NULL DEFAULT '',
    log            TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS jobs_status_created_at_idx ON jobs(status, created_at);
CREATE INDEX IF NOT EXISTS jobs_workspace_id_idx ON jobs(workspace_id);

CREATE TABLE IF NOT EXISTS provisioner_keys (
    id           TEXT PRIMARY KEY,
    name         TEXT NOT NULL,
    token_hash   TEXT UNIQUE NOT NULL,
    created_at   TIMESTAMPTZ NOT NULL,
    last_used_at TIMESTAMPTZ
);

CREATE TABLE IF NOT EXISTS platform_config (
    id   TEXT PRIMARY KEY DEFAULT 'singleton',
    data JSONB NOT NULL DEFAULT '{}'
);

CREATE TABLE IF NOT EXISTS organizations (
    id               TEXT PRIMARY KEY,
    name             TEXT NOT NULL,
    slug             TEXT UNIQUE NOT NULL,
    description      TEXT NOT NULL DEFAULT '',
    created_by       TEXT NOT NULL,
    created_at       TIMESTAMPTZ NOT NULL,
    group_claims     JSONB NOT NULL DEFAULT '[]',
    group_mapping    JSONB NOT NULL DEFAULT '{}',
    group_auto_create BOOLEAN NOT NULL DEFAULT FALSE
);

CREATE TABLE IF NOT EXISTS org_members (
    org_id     TEXT NOT NULL,
    user_id    TEXT NOT NULL,
    user_email TEXT NOT NULL DEFAULT '',
    user_name  TEXT NOT NULL DEFAULT '',
    role       TEXT NOT NULL,
    joined_at  TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (org_id, user_id)
);
CREATE INDEX IF NOT EXISTS org_members_user_id_idx ON org_members(user_id);

CREATE TABLE IF NOT EXISTS teams (
    id          TEXT PRIMARY KEY,
    org_id      TEXT NOT NULL,
    name        TEXT NOT NULL,
    description TEXT NOT NULL DEFAULT '',
    created_by  TEXT NOT NULL,
    created_at  TIMESTAMPTZ NOT NULL
);
CREATE INDEX IF NOT EXISTS teams_org_id_idx ON teams(org_id);

CREATE TABLE IF NOT EXISTS team_members (
    team_id    TEXT NOT NULL,
    user_id    TEXT NOT NULL,
    user_email TEXT NOT NULL DEFAULT '',
    user_name  TEXT NOT NULL DEFAULT '',
    joined_at  TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (team_id, user_id)
);
CREATE INDEX IF NOT EXISTS team_members_user_id_idx ON team_members(user_id);
