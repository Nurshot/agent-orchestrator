# Coder flow on Azure

Run cloud sessions with `provider=coder` on **Azure** while the AO control plane
and its database stay on **AWS**. Motivation: dogfood internally and demo on
Azure credits without moving the control plane or data. All AO application code
is unchanged; the work is Azure infrastructure plus a one-line CP config repoint.

## Why this needs almost no code

The AO coder provider (`cloud/internal/sandbox/coder/`) talks to a **Coder API
endpoint** (`AO_CLOUD_CODER_URL`), not to any cloud. And `cloud/coder/main.tf`
is a **Docker** template: it provisions a workspace **container** via a Docker
socket, with the agent reaching Coder over `host.docker.internal`. So it already
runs on any Docker host. "Coder on Azure" is therefore **infra only**: stand up
Coder + Docker on an Azure VM and point `AO_CLOUD_CODER_URL` at it.

## Architecture (single VM, TLS-terminated)

```
App -> CP (AWS eu-north-1) --HTTPS--> Caddy (Azure VM) --> Coder (:3000, unpublished)
          |                                                    |
          |                        Terraform docker provider -> workspace CONTAINER
          v                          (ao-coder-workspace:local, baked ao-worker, mem-capped)
        RDS (AWS, same region as CP)      |
          ^                               |
          +--------- HTTPS (worker token, terminal) <-- AO worker dials the CP's public URL
```

One Azure VM runs, all as `--restart unless-stopped` containers (survive
reboots): **Caddy** (auto Let's Encrypt TLS) in front of **Coder**, plus Coder's
**Postgres**. The Coder provisioner uses the VM's own Docker socket, so each
workspace is a memory-capped container on that VM. The **database is untouched**
- workspaces never talk to it; only the CP does, and CP↔RDS stays intra-region
on AWS.

**Region:** provision in **Sweden Central** (next to AWS `eu-north-1`) so the
workspace↔CP terminal relay is intra-Europe (~10-30ms). Farther regions add
noticeable terminal lag because the terminal relays through the CP.

## Provision

```bash
cd cloud
# 1. VM + Docker + Caddy(TLS) + Coder + Postgres + admin + long-lived API token
AO_AZURE_LOCATION=swedencentral ./scripts/provision-coder-azure.sh
#    -> prints CODER_URL (https://<fqdn>); stores PGPW/CODER_ADMIN_PW/CODER_TOKEN on the VM

# 2. build the workspace image (bakes the release-matched worker) + publish the
#    template with per-workspace limits
AO_CLOUD_CP_IMAGE=<acct>.dkr.ecr.eu-north-1.amazonaws.com/ao-cloud-control-plane:<tag>-linux-amd64 \
  ./scripts/provision-coder-azure-template.sh
#    -> builds ao-coder-workspace:local, publishes ao-linux-docker, stores CODER_TEMPLATE_ID
```

Both scripts are parameterized (see the env vars at the top of each) and safe to
re-run. Retrieve the token/template id from the VM when wiring the CP:

```bash
az vm run-command invoke -g ao-coder-azure -n ao-coder-azure-vm --command-id RunShellScript \
  --scripts '. /etc/ao-coder.env; echo "$CODER_TOKEN"; echo "$CODER_TEMPLATE_ID"' --query 'value[0].message' -o tsv
```

## Wire a control plane at it

```
AO_CLOUD_CODER_URL=https://<fqdn>
AO_CLOUD_CODER_TOKEN=<CODER_TOKEN from the VM>
AO_CLOUD_CODER_TEMPLATE_ID=<CODER_TEMPLATE_ID from the VM>
AO_CLOUD_CODER_OWNER=aoadmin
AO_CLOUD_CODER_AGENT_NAME=main
```

A session created with `provider=coder` then provisions a workspace container on
the Azure VM, and its baked `ao-worker` dials the CP's public URL as usual. An
orchestrator's workers **inherit its provider**, so a coder orchestrator spawns
coder workers - both land on Azure.

> **Guardrail:** the coder config is a single endpoint per CP, so **every** coder
> session on a given CP goes wherever its `AO_CLOUD_CODER_URL` points. Repointing
> a CP that other people use (e.g. shared staging) moves *their* coder workspaces
> too. Point only a CP you control at this Azure Coder.

## Hardening status (for a customer handover)

Handled + validated live in `swedencentral`:

- **TLS** - Caddy fronts Coder with a Let's Encrypt cert on a stable FQDN; the
  plain `:3000` port is unpublished and closed at the NSG. The CP↔Coder token
  never crosses the internet in cleartext.
- **Per-workspace limits** - each workspace container gets a hard memory cap
  (`workspace_memory_mb`, default 4096) + CPU weight (`workspace_cpu_shares`), so
  one workspace cannot OOM the shared VM. Optional vars in `main.tf`, no-op for
  the existing single-tenant (AWS) deployments.
- **Restart resilience** - Caddy/Coder/Postgres are `--restart unless-stopped`
  and recover automatically on a VM reboot (verified: workspace provisions again
  post-reboot).
- **NSG** - steady state is inbound **80/443 only**. SSH (22) is needed only for
  the one-time template build; restrict it with `AO_AZURE_SSH_SOURCE` at
  provision time, or close it afterward (done on the live deployment - day-2
  admin is via `az vm run-command`, no SSH). Re-open 22 briefly only to rebake
  the image.
- **Worker SHA fast path** - the workspace image bakes `/ao-worker` from
  `AO_CLOUD_CP_IMAGE`; the CP you wire up must run that same image or the coder
  bootstrap falls back to the slow PTY upload. Re-run
  provision-coder-azure-template.sh with the new `AO_CLOUD_CP_IMAGE` whenever the
  CP image changes (the Azure analogue of publish-coder-workspace.sh on AWS).

Deferred (production-scale, **not** blockers for a bounded customer test):

- **Coder's Postgres -> Azure Database for PostgreSQL.** It is a container with a
  persistent volume today; it survives reboots (verified). Migrate before real
  production for managed backups/HA.
- **Push the workspace image to ACR.** It is a local tag on the one VM; it
  survives reboots but is lost if the VM is *recreated*. Push to ACR before you
  scale to more than one VM or want VM-recreation resilience.
- **Multi-VM autoscale.** One VM (4 vCPU / 16 GB) holds a handful of memory-
  capped workspaces; add hosts / a bigger VM only at higher concurrency.
- **Rotate the Coder admin token.** The CP↔Coder token in `/etc/ao-coder.env` is
  admin-scoped and long-lived (`CODER_MAX_ADMIN_TOKEN_LIFETIME=8760h`); rotate it
  (`coder tokens create` + update the `ao-cloud/staging/coder` secret) periodically
  or on suspicion of VM compromise.
- **SSH host-key pinning.** provision-coder-azure-template.sh uses
  `StrictHostKeyChecking=no`; for a rebake over an untrusted network, pin the host
  key first (it carries a short-lived, read-only ECR pull token via stdin).

## Teardown

```bash
az group delete -n ao-coder-azure --yes --no-wait
```
