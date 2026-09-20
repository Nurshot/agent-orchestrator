#!/usr/bin/env bash
set -euo pipefail

# Stand up a self-contained Coder deployment on Azure so cloud sessions with
# provider=coder run on Azure VMs/containers while the AO control plane and its
# database stay on AWS. The AO code is unchanged: the coder provider already
# talks to a Coder API endpoint (AO_CLOUD_CODER_URL), so "coder on Azure" is
# purely this infra + repointing that env var at the URL this script prints.
#
# Shape (single-VM, matches cloud/coder/main.tf which is a Docker template):
#   one Azure VM runs Docker + Coder (container) + Postgres (container); the
#   Coder provisioner uses the VM's own Docker socket, so each workspace is a
#   container on that VM. host.docker.internal wires the agent back to Coder.
#
# Everything is parameterized (env vars below) and idempotent where practical.
# Requires: az (logged in). Region defaults to swedencentral to sit next to the
# AWS control plane in eu-north-1 (keeps the workspace<->CP terminal relay fast).
#
# After this prints CODER_URL + CODER_TOKEN + TEMPLATE_ID, build the workspace
# image and publish the template with provision-coder-azure-template.sh, then
# point a (non-shared) dogfood CP at those values.

LOCATION="${AO_AZURE_LOCATION:-swedencentral}"
RG="${AO_AZURE_RG:-ao-coder-azure}"
VM="${AO_AZURE_VM:-ao-coder-azure-vm}"
VM_SIZE="${AO_AZURE_VM_SIZE:-Standard_D4s_v5}"
VM_IMAGE="${AO_AZURE_VM_IMAGE:-Ubuntu2204}"
ADMIN_USER="${AO_AZURE_ADMIN_USER:-azureuser}"
CODER_IMAGE="${AO_CODER_IMAGE:-ghcr.io/coder/coder:latest}"
CODER_ADMIN_EMAIL="${AO_CODER_ADMIN_EMAIL:-admin@ao-coder.dev}"
CODER_ADMIN_USERNAME="${AO_CODER_ADMIN_USERNAME:-aoadmin}"

say() { printf '\n=== %s ===\n' "$*"; }

say "resource group ${RG} (${LOCATION})"
az group create -n "$RG" -l "$LOCATION" -o none

say "VM ${VM} (${VM_SIZE})"
if ! az vm show -g "$RG" -n "$VM" -o none 2>/dev/null; then
  # Retry: az vm create occasionally races on the auto-created NIC
  # (ResourceNotFound ...VMNic) even though the VM ends up created; a retry is
  # idempotent because the second attempt finds the resources and completes.
  for attempt in 1 2 3; do
    if az vm create -g "$RG" -n "$VM" \
        --image "$VM_IMAGE" --size "$VM_SIZE" \
        --admin-username "$ADMIN_USER" --generate-ssh-keys \
        --public-ip-sku Standard --nsg-rule SSH \
        --tags purpose=ao-coder-azure managed-by=provision-coder-azure -o none; then
      break
    fi
    echo "vm create attempt ${attempt} failed (transient NIC race); retrying in 10s..."
    sleep 10
  done
fi
IP="$(az vm show -g "$RG" -n "$VM" -d --query publicIps -o tsv)"
say "VM public IP: ${IP}"

say "open NSG ports 3000/80/443"
NSG="$(az network nsg list -g "$RG" --query '[0].name' -o tsv)"
az network nsg rule show -g "$RG" --nsg-name "$NSG" -n coder -o none 2>/dev/null || \
  az network nsg rule create -g "$RG" --nsg-name "$NSG" -n coder --priority 1010 \
    --destination-port-ranges 3000 80 443 --access Allow --protocol Tcp --direction Inbound -o none

say "install Docker"
az vm run-command invoke -g "$RG" -n "$VM" --command-id RunShellScript --scripts \
  'command -v docker >/dev/null 2>&1 || curl -fsSL https://get.docker.com | sh; systemctl enable --now docker; docker --version' \
  --query 'value[0].message' -o tsv | tail -2

say "bring up Coder + Postgres (idempotent; DB password persisted on the VM)"
az vm run-command invoke -g "$RG" -n "$VM" --command-id RunShellScript --scripts '
set -e
ENVF=/etc/ao-coder.env
if [ ! -f "$ENVF" ]; then echo "PGPW=$(openssl rand -hex 16)" > "$ENVF"; chmod 600 "$ENVF"; fi
. "$ENVF"
IP="'"$IP"'"
docker network create coder 2>/dev/null || true
if ! docker ps -a --format "{{.Names}}" | grep -q "^coder-db$"; then
  docker run -d --name coder-db --network coder --restart unless-stopped \
    -e POSTGRES_USER=coder -e POSTGRES_PASSWORD="$PGPW" -e POSTGRES_DB=coder \
    -v coder-db-data:/var/lib/postgresql/data postgres:16
  sleep 12
fi
if ! docker ps --format "{{.Names}}" | grep -q "^coder$"; then
  docker rm -f coder 2>/dev/null || true
  DOCKGID=$(stat -c %g /var/run/docker.sock)
  docker run -d --name coder --network coder --restart unless-stopped \
    -p 3000:3000 --group-add "$DOCKGID" \
    -v /var/run/docker.sock:/var/run/docker.sock \
    -e CODER_ACCESS_URL=http://'"$IP"':3000 \
    -e CODER_HTTP_ADDRESS=0.0.0.0:3000 \
    -e CODER_MAX_ADMIN_TOKEN_LIFETIME=8760h \
    -e CODER_PG_CONNECTION_URL="postgres://coder:$PGPW@coder-db:5432/coder?sslmode=disable" \
    "'"$CODER_IMAGE"'"
  sleep 20
fi
curl -fsS -o /dev/null -w "coder /healthz: %{http_code}\n" http://localhost:3000/healthz
' --query 'value[0].message' -o tsv | tail -3

say "create first admin + long-lived API token (stored on the VM at /etc/ao-coder.env)"
# Done via the Coder API so it is idempotent and non-interactive:
#  - first user is created via POST /users/first (a no-op once it exists);
#  - we log in with email+password to get a session, then mint an API token.
# Admin (owner) tokens are capped by --max-admin-token-lifetime, which the coder
# container above sets to 8760h; the token prints as "<id>-<secret>".
az vm run-command invoke -g "$RG" -n "$VM" --command-id RunShellScript --scripts '
set -e
ENVF=/etc/ao-coder.env; . "$ENVF"
grep -q "^CODER_ADMIN_PW=" "$ENVF" || echo "CODER_ADMIN_PW=$(openssl rand -hex 16)" >> "$ENVF"
. "$ENVF"
curl -s -X POST http://localhost:3000/api/v2/users/first -H "Content-Type: application/json" \
  -d "{\"email\":\"'"$CODER_ADMIN_EMAIL"'\",\"username\":\"'"$CODER_ADMIN_USERNAME"'\",\"password\":\"$CODER_ADMIN_PW\",\"trial\":false}" >/dev/null 2>&1 || true
if ! grep -q "^CODER_TOKEN=" "$ENVF"; then
  SESSION=$(curl -s -X POST http://localhost:3000/api/v2/users/login -H "Content-Type: application/json" \
    -d "{\"email\":\"'"$CODER_ADMIN_EMAIL"'\",\"password\":\"$CODER_ADMIN_PW\"}" \
    | python3 -c "import sys,json;print(json.load(sys.stdin).get(\"session_token\",\"\"))")
  RAW=$(docker exec -e CODER_URL=http://localhost:3000 -e CODER_SESSION_TOKEN="$SESSION" \
    coder coder tokens create --name ao-cp-primary --lifetime 8760h 2>&1)
  TOK=$(echo "$RAW" | grep -oE "[A-Za-z0-9]+-[A-Za-z0-9]+" | head -1)
  [ -n "$TOK" ] && echo "CODER_TOKEN=$TOK" >> "$ENVF"
fi
. "$ENVF"
if [ -n "${CODER_TOKEN:-}" ]; then
  code=$(curl -s -o /dev/null -w "%{http_code}" -H "Coder-Session-Token: $CODER_TOKEN" http://localhost:3000/api/v2/users/me)
  echo "token ready; /api/v2/users/me -> $code"
else echo "TOKEN NOT CREATED"; fi
' --query 'value[0].message' -o tsv | tail -3

cat <<EOF

================ Coder on Azure is up ================
CODER_URL   = http://${IP}:3000
Region      = ${LOCATION}  (next to AWS eu-north-1)
Admin       = ${CODER_ADMIN_EMAIL} / user ${CODER_ADMIN_USERNAME}
Secrets on the VM at /etc/ao-coder.env (PGPW, CODER_ADMIN_PW, CODER_TOKEN) - not printed here.

Next:
  1. Build the workspace image + publish the template:
       AO_AZURE_RG=${RG} AO_AZURE_VM=${VM} ./scripts/provision-coder-azure-template.sh
  2. Point a NON-SHARED dogfood control plane at:
       AO_CLOUD_CODER_URL=http://${IP}:3000
       AO_CLOUD_CODER_TOKEN=<the CODER_TOKEN from the VM>
       AO_CLOUD_CODER_TEMPLATE_ID=<printed by step 1>
       AO_CLOUD_CODER_OWNER=${CODER_ADMIN_USERNAME}
=====================================================
EOF
