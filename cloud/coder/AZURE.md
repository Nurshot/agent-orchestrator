# Coder flow on Azure

Run cloud sessions with `provider=coder` on **Azure** while the AO control plane
and its database stay on **AWS**. Motivation: dogfood internally + demo on Azure
credits without moving the control plane or data. All AO application code is
unchanged; the work is Azure infrastructure plus a one-line CP config repoint.

## Why this needs almost no code

The AO coder provider (`cloud/internal/sandbox/coder/`) talks to a **Coder API
endpoint** (`AO_CLOUD_CODER_URL`), not to any cloud. And `cloud/coder/main.tf`
is a **Docker** template: it provisions a workspace **container** via a Docker
socket, with the agent reaching Coder over `host.docker.internal`. So it already
runs on any Docker host. "Coder on Azure" is therefore **infra only**: stand up
Coder + Docker on an Azure VM and point `AO_CLOUD_CODER_URL` at it. No AO Go
change; the same `main.tf` + `Sandbox.Dockerfile` are reused verbatim.

## Architecture (single VM)

```
App -> CP (AWS eu-north-1) --HTTPS--> Coder server (Azure VM)
          |                                |
          |                                +- Terraform docker provider -> workspace CONTAINER
          v                                   (ao-coder-workspace:local, baked ao-worker)
        RDS (AWS, same region as CP)             |
          ^                                       |
          +----------- HTTPS (worker token, terminal) <-- AO worker dials the CP's public URL
```

One Azure VM runs Docker + the Coder server (container) + Coder's Postgres
(container). The Coder provisioner uses the VM's own Docker socket, so each
workspace is a container on that VM. The **database is untouched** - workspaces
never talk to it; only the CP does, and CP↔RDS stays intra-region on AWS.

**Region:** provision in **Sweden Central** (next to AWS `eu-north-1`) so the
workspace↔CP terminal relay is intra-Europe (~10-30ms). Farther regions add
noticeable terminal lag because the terminal relays through the CP.

## Provision

```bash
cd cloud
# 1. VM + Docker + Coder + Postgres + admin + long-lived API token
AO_AZURE_LOCATION=swedencentral ./scripts/provision-coder-azure.sh
#    -> prints CODER_URL; stores PGPW/CODER_ADMIN_PW/CODER_TOKEN on the VM at /etc/ao-coder.env

# 2. build the workspace image (bakes the release-matched worker) + publish the template
AO_CLOUD_CP_IMAGE=<acct>.dkr.ecr.eu-north-1.amazonaws.com/ao-cloud-control-plane:<tag>-linux-amd64 \
  ./scripts/provision-coder-azure-template.sh
#    -> builds ao-coder-workspace:local, publishes template ao-linux-docker, stores CODER_TEMPLATE_ID
```

Both scripts are parameterized (see the env vars at the top of each) and safe to
re-run. Retrieve the token/template id from the VM when wiring the CP:

```bash
az vm run-command invoke -g ao-coder-azure -n ao-coder-azure-vm --command-id RunShellScript \
  --scripts '. /etc/ao-coder.env; echo "$CODER_TOKEN"; echo "$CODER_TEMPLATE_ID"' --query 'value[0].message' -o tsv
```

## Wire a control plane at it

Point a control plane's coder config at the Azure Coder:

```
AO_CLOUD_CODER_URL=http://<vm-ip>:3000
AO_CLOUD_CODER_TOKEN=<CODER_TOKEN from the VM>
AO_CLOUD_CODER_TEMPLATE_ID=<CODER_TEMPLATE_ID from the VM>
AO_CLOUD_CODER_OWNER=aoadmin
AO_CLOUD_CODER_AGENT_NAME=main
```

Then a session created with `provider=coder` provisions a workspace container on
the Azure VM, and its baked `ao-worker` dials the CP's public URL as usual.

> **Guardrail:** do **not** repoint the shared staging CP at this Azure Coder -
> that would move eleven_x's coder workspaces onto it. Use a separate/dev
> control plane for dogfooding; the coder config is a single endpoint, so every
> coder session on a given CP goes wherever that CP's `AO_CLOUD_CODER_URL`
> points.

## Hardening TODO (dogfood -> production)

- TLS + a DNS name in front of Coder (currently plain `http://<ip>:3000`).
- Move Coder's Postgres to Azure Database for PostgreSQL (currently a container).
- Lock the NSG (3000 is open to the internet for the CP; scope to the CP egress).
- Bake the workspace image into the VM image / push to ACR for multi-VM scale.

## Teardown

```bash
az group delete -n ao-coder-azure --yes --no-wait
```
