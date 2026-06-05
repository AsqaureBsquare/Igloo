#!/bin/bash
# Local OIDC test with Keycloak in Docker — no minikube needed.
# Starts Keycloak, configures realm/client/groups/users via admin API,
# then prints the env vars to run igloo locally.
#
# Usage:
#   ./dev/local-oidc.sh          # start & configure
#   ./dev/local-oidc.sh stop     # stop and remove the container
set -e

KC_PORT=8888
KC_URL="http://localhost:$KC_PORT"
KC_ADMIN=admin
KC_PASS=admin
REALM=igloo
CLIENT_ID=igloo-local
CLIENT_SECRET=igloo-local-secret
REDIRECT_URL="http://localhost:8081/api/auth/oidc/callback"

if [ "${1}" = "stop" ]; then
  docker rm -f igloo-keycloak 2>/dev/null && echo "Stopped."
  exit 0
fi

# ── 1. Start Keycloak ────────────────────────────────────────────────────────
if ! docker ps --format '{{.Names}}' | grep -q "^igloo-keycloak$"; then
  echo "Starting Keycloak on port $KC_PORT..."
  docker run -d --name igloo-keycloak \
    -p "$KC_PORT:8080" \
    -e KC_BOOTSTRAP_ADMIN_USERNAME="$KC_ADMIN" \
    -e KC_BOOTSTRAP_ADMIN_PASSWORD="$KC_PASS" \
    quay.io/keycloak/keycloak:26.6.3 start-dev
else
  echo "Keycloak already running."
fi

# ── 2. Wait for Keycloak ─────────────────────────────────────────────────────
echo -n "Waiting for Keycloak..."
until curl -sf "$KC_URL/realms/master" > /dev/null 2>&1; do
  sleep 2; echo -n "."
done
echo " ready."

# ── 3. Get admin token ───────────────────────────────────────────────────────
TOKEN=$(curl -sf -X POST "$KC_URL/realms/master/protocol/openid-connect/token" \
  -d "client_id=admin-cli&grant_type=password&username=$KC_ADMIN&password=$KC_PASS" \
  | python3 -c "import sys,json; print(json.load(sys.stdin)['access_token'])")

auth() { curl -sf -H "Authorization: Bearer $TOKEN" "$@"; }
authj() { curl -sf -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" "$@"; }

# ── 4. Create realm ──────────────────────────────────────────────────────────
if ! auth "$KC_URL/admin/realms/$REALM" > /dev/null 2>&1; then
  echo "Creating realm '$REALM'..."
  authj -X POST "$KC_URL/admin/realms" \
    -d "{\"realm\":\"$REALM\",\"enabled\":true,\"registrationAllowed\":false}"
fi

# ── 5. Create OIDC client ────────────────────────────────────────────────────
CLIENT_UUID=$(auth "$KC_URL/admin/realms/$REALM/clients?clientId=$CLIENT_ID" \
  | python3 -c "import sys,json; d=json.load(sys.stdin); print(d[0]['id'] if d else '')")

if [ -z "$CLIENT_UUID" ]; then
  echo "Creating client '$CLIENT_ID'..."
  authj -X POST "$KC_URL/admin/realms/$REALM/clients" -d "{
    \"clientId\": \"$CLIENT_ID\",
    \"secret\": \"$CLIENT_SECRET\",
    \"protocol\": \"openid-connect\",
    \"publicClient\": false,
    \"redirectUris\": [\"$REDIRECT_URL\"],
    \"webOrigins\": [\"http://localhost:8081\"],
    \"standardFlowEnabled\": true
  }"
  CLIENT_UUID=$(auth "$KC_URL/admin/realms/$REALM/clients?clientId=$CLIENT_ID" \
    | python3 -c "import sys,json; print(json.load(sys.stdin)[0]['id'])")
fi

# ── 6. Add groups mapper (puts group names in the 'groups' token claim) ──────
MAPPER_EXISTS=$(auth "$KC_URL/admin/realms/$REALM/clients/$CLIENT_UUID/protocol-mappers/models" \
  | python3 -c "import sys,json; ms=json.load(sys.stdin); print(any(m['name']=='groups' for m in ms))")
if [ "$MAPPER_EXISTS" != "True" ]; then
  echo "Adding groups mapper..."
  authj -X POST "$KC_URL/admin/realms/$REALM/clients/$CLIENT_UUID/protocol-mappers/models" -d '{
    "name": "groups",
    "protocol": "openid-connect",
    "protocolMapper": "oidc-group-membership-mapper",
    "config": {
      "full.path": "false",
      "id.token.claim": "true",
      "access.token.claim": "true",
      "claim.name": "groups",
      "userinfo.token.claim": "true"
    }
  }'
fi

# ── 7. Create groups ─────────────────────────────────────────────────────────
for GROUP in engineering platform admin; do
  EXISTS=$(auth "$KC_URL/admin/realms/$REALM/groups?search=$GROUP" \
    | python3 -c "import sys,json; d=json.load(sys.stdin); print(any(g['name']=='$GROUP' for g in d))")
  if [ "$EXISTS" != "True" ]; then
    echo "Creating group '$GROUP'..."
    authj -X POST "$KC_URL/admin/realms/$REALM/groups" -d "{\"name\":\"$GROUP\"}"
  fi
done

get_group_id() {
  auth "$KC_URL/admin/realms/$REALM/groups?search=$1" \
    | python3 -c "import sys,json; d=json.load(sys.stdin); print(next(g['id'] for g in d if g['name']=='$1'))"
}

ENG_ID=$(get_group_id engineering)
PLAT_ID=$(get_group_id platform)

# ── 8. Create users ──────────────────────────────────────────────────────────
create_user() {
  local EMAIL=$1 NAME=$2 PASS=$3
  local EXISTS
  EXISTS=$(auth "$KC_URL/admin/realms/$REALM/users?email=$EMAIL" \
    | python3 -c "import sys,json; print(bool(json.load(sys.stdin)))")
  if [ "$EXISTS" = "False" ]; then
    echo "Creating user $EMAIL..."
    authj -X POST "$KC_URL/admin/realms/$REALM/users" -d "{
      \"email\": \"$EMAIL\",
      \"username\": \"$EMAIL\",
      \"firstName\": \"$(echo $NAME | cut -d' ' -f1)\",
      \"lastName\": \"$(echo $NAME | cut -d' ' -f2)\",
      \"enabled\": true,
      \"emailVerified\": true,
      \"credentials\": [{\"type\":\"password\",\"value\":\"$PASS\",\"temporary\":false}]
    }"
  fi
  auth "$KC_URL/admin/realms/$REALM/users?email=$EMAIL" \
    | python3 -c "import sys,json; print(json.load(sys.stdin)[0]['id'])"
}

ALICE_ID=$(create_user "alice@example.com" "Alice Engineer" "password")
BOB_ID=$(create_user "bob@example.com" "Bob Ops" "password")

# ── 9. Assign users to groups ────────────────────────────────────────────────
echo "Assigning group memberships..."
authj -X PUT "$KC_URL/admin/realms/$REALM/users/$ALICE_ID/groups/$ENG_ID" -d '{}' || true
authj -X PUT "$KC_URL/admin/realms/$REALM/users/$BOB_ID/groups/$PLAT_ID" -d '{}' || true

# ── 10. Print env vars ───────────────────────────────────────────────────────
cat <<EOF

────────────────────────────────────────────────────
Keycloak ready: $KC_URL/admin  (admin / admin)
Realm:          $KC_URL/realms/$REALM

Test users (password: 'password'):
  alice@example.com  → group: engineering
  bob@example.com    → group: platform

Run igloo locally:

  export IGLOO_JWT_SECRET=dev-secret-change-me
  export IGLOO_OIDC_ISSUER_URL=$KC_URL/realms/$REALM
  export IGLOO_OIDC_CLIENT_ID=$CLIENT_ID
  export IGLOO_OIDC_CLIENT_SECRET=$CLIENT_SECRET
  export IGLOO_OIDC_REDIRECT_URL=$REDIRECT_URL
  go run ./cmd/igloo server --local --port 8081

Then open http://localhost:8081 and click "Sign in with SSO".
────────────────────────────────────────────────────
EOF
