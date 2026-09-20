#!/usr/bin/env bash
set -euo pipefail

# Build the AO Coder workspace image on the Azure Coder VM and publish the Coder
# template that references it. Run after provision-coder-azure.sh.
#
# The workspace image (cloud/coder/Sandbox.Dockerfile) bakes the release-matched
# /ao-worker and /ao binaries straight out of the control-plane image, so pass
# the SAME control-plane image the (dogfood) CP runs (AO_CLOUD_CP_IMAGE). The
# image is amd64 (matches the D-series VM) - no cross-arch worker gymnastics.
#
# Single-VM shape: the image is built locally on the VM (tag ao-coder-workspace:
# local) and the template references that tag, so the Coder provisioner - which
# shares the VM's Docker daemon - finds it with no registry round trip.
#
# Requires: az (logged in), aws (for the ECR pull of the CP image), ssh key from
# provision-coder-azure.sh (--generate-ssh-keys -> ~/.ssh/id_rsa).

RG="${AO_AZURE_RG:-ao-coder-azure}"
VM="${AO_AZURE_VM:-ao-coder-azure-vm}"
ADMIN_USER="${AO_AZURE_ADMIN_USER:-azureuser}"
SSH_KEY="${AO_AZURE_SSH_KEY:-$HOME/.ssh/id_rsa}"
AWS_REGION="${AWS_REGION:-eu-north-1}"
CP_IMAGE="${AO_CLOUD_CP_IMAGE:?set AO_CLOUD_CP_IMAGE to the control-plane image whose worker to bake, e.g. <acct>.dkr.ecr.eu-north-1.amazonaws.com/ao-cloud-control-plane:<tag>-linux-amd64}"
TEMPLATE_NAME="${AO_CLOUD_CODER_TEMPLATE_NAME:-ao-linux-docker}"
WORKSPACE_IMAGE="${AO_CLOUD_CODER_WORKSPACE_IMAGE:-ao-coder-workspace:local}"
CODER_ADMIN_EMAIL="${AO_CODER_ADMIN_EMAIL:-admin@ao-coder.dev}"
TEMPLATE_DIR="${AO_CLOUD_CODER_TEMPLATE_DIR:-coder}"
ECR_REGISTRY="${CP_IMAGE%%/*}"

# The public IP is stable; az vm show is occasionally flaky for a fresh VM
# (transient ARM ResourceNotFound), so allow an explicit override.
IP="${AO_AZURE_VM_IP:-}"
if [[ -z "$IP" ]]; then
  for _ in 1 2 3; do IP="$(az vm show -g "$RG" -n "$VM" -d --query publicIps -o tsv 2>/dev/null || true)"; [[ -n "$IP" ]] && break; sleep 5; done
fi
[[ -n "$IP" ]] || { echo "could not resolve VM public IP; set AO_AZURE_VM_IP"; exit 1; }
SSHK=(-i "$SSH_KEY" -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -o ConnectTimeout=20)
echo "=== Coder VM: ${IP} ==="

echo "=== copy template sources ==="
scp "${SSHK[@]}" "${TEMPLATE_DIR}/main.tf" "${TEMPLATE_DIR}/Sandbox.Dockerfile" "${ADMIN_USER}@${IP}:/tmp/"

echo "=== ECR login on the VM (token via stdin) ==="
aws ecr get-login-password --region "$AWS_REGION" \
  | ssh "${SSHK[@]}" "${ADMIN_USER}@${IP}" "sudo docker login --username AWS --password-stdin ${ECR_REGISTRY}" >/dev/null

echo "=== build workspace image ${WORKSPACE_IMAGE} (bakes worker from ${CP_IMAGE}) ==="
ssh "${SSHK[@]}" "${ADMIN_USER}@${IP}" "
  set -e
  mkdir -p ~/ao-coder-template && cp /tmp/main.tf /tmp/Sandbox.Dockerfile ~/ao-coder-template/
  sudo DOCKER_BUILDKIT=1 docker build --build-arg AO_CONTROL_PLANE_IMAGE='${CP_IMAGE}' \
    -t '${WORKSPACE_IMAGE}' -f ~/ao-coder-template/Sandbox.Dockerfile ~/ao-coder-template
  sudo rm -f /root/.docker/config.json ~/.docker/config.json
  sudo docker images '${WORKSPACE_IMAGE}' --format 'built {{.Repository}}:{{.Tag}} {{.Size}}'
"

echo "=== publish the ${TEMPLATE_NAME} template ==="
ssh "${SSHK[@]}" "${ADMIN_USER}@${IP}" "sudo TEMPLATE_NAME='${TEMPLATE_NAME}' WORKSPACE_IMAGE='${WORKSPACE_IMAGE}' ADMIN_EMAIL='${CODER_ADMIN_EMAIL}' bash -s" <<'REMOTE'
set -e
. /etc/ao-coder.env
SESSION=$(curl -s -X POST http://localhost:3000/api/v2/users/login -H "Content-Type: application/json" \
  -d "{\"email\":\"${ADMIN_EMAIL}\",\"password\":\"${CODER_ADMIN_PW}\"}" \
  | python3 -c 'import sys,json;print(json.load(sys.stdin).get("session_token",""))')
[ -z "$SESSION" ] && { echo "LOGIN_FAILED"; exit 1; }
docker exec coder mkdir -p /tmp/ao-tmpl
docker cp /home/azureuser/ao-coder-template/main.tf coder:/tmp/ao-tmpl/main.tf
docker exec -e CODER_URL=http://localhost:3000 -e CODER_SESSION_TOKEN="$SESSION" coder \
  coder templates push "$TEMPLATE_NAME" -d /tmp/ao-tmpl --variable workspace_image="$WORKSPACE_IMAGE" -y 2>&1 | tail -3
ORG=$(curl -s -H "Coder-Session-Token: $SESSION" http://localhost:3000/api/v2/users/me | python3 -c 'import sys,json;print(json.load(sys.stdin)["organization_ids"][0])')
TID=$(curl -s -H "Coder-Session-Token: $SESSION" "http://localhost:3000/api/v2/organizations/$ORG/templates" \
  | python3 -c "import sys,json;[print(t['id']) for t in json.load(sys.stdin) if t['name']=='$TEMPLATE_NAME']")
if [ -n "$TID" ]; then
  grep -q "^CODER_TEMPLATE_ID=" /etc/ao-coder.env && sed -i "/^CODER_TEMPLATE_ID=/d" /etc/ao-coder.env
  echo "CODER_TEMPLATE_ID=$TID" >> /etc/ao-coder.env
  echo "CODER_TEMPLATE_ID=$TID"
else echo "TEMPLATE_ID_NOT_FOUND"; fi
REMOTE

echo
echo "Template published. AO_CLOUD_CODER_TEMPLATE_ID is stored on the VM at /etc/ao-coder.env."
echo "Wire a NON-SHARED dogfood control plane with the values printed by provision-coder-azure.sh + the template id above."
