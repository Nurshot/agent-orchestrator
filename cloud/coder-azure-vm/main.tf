# AO Coder template: one dedicated Azure VM per workspace, running the AO worker
# + harness NATIVELY on the VM (no Docker, no container) - mirroring eleven_x's
# AWS EC2-per-workspace dev-kit with a baked AMI. The VM boots from a baked
# managed image (ao-coder-workspace-image: Ubuntu + node + claude + ao-worker/ao
# + a coder user with NOPASSWD sudo), and cloud-init starts the coder agent as a
# native systemd service. The CP then bootstraps the AO worker natively over the
# agent, exactly like the eleven_x setup. Stop deallocates the VM; start brings
# it back (the systemd agent restarts on boot).

terraform {
  required_providers {
    coder   = { source = "coder/coder" }
    azurerm = { source = "hashicorp/azurerm" }
    tls     = { source = "hashicorp/tls" }
  }
}

provider "azurerm" {
  features {}
}

variable "resource_group" {
  type    = string
  default = "ao-coder-azure"
}
variable "location" {
  type    = string
  default = "swedencentral"
}
variable "subnet_id" {
  type = string
}
variable "vm_size" {
  type    = string
  default = "Standard_D2s_v5" # ~2 vCPU / 8 GB, close to eleven_x t3.medium
}
variable "image_id" {
  type = string # the baked managed image (native worker + harness)
}
variable "admin_username" {
  type    = string
  default = "azureadmin"
}

data "coder_provisioner" "me" {}
data "coder_workspace" "me" {}
data "coder_workspace_owner" "me" {}

resource "coder_agent" "main" {
  arch = "amd64"
  os   = "linux"

  startup_script = <<-EOT
    set -e
    if [ ! -f ~/.init_done ]; then
      cp -rT /etc/skel ~ 2>/dev/null || true
      touch ~/.init_done
    fi
    claude --version || true
  EOT

  env = {
    GIT_AUTHOR_NAME     = coalesce(data.coder_workspace_owner.me.full_name, data.coder_workspace_owner.me.name)
    GIT_AUTHOR_EMAIL    = data.coder_workspace_owner.me.email
    GIT_COMMITTER_NAME  = coalesce(data.coder_workspace_owner.me.full_name, data.coder_workspace_owner.me.name)
    GIT_COMMITTER_EMAIL = data.coder_workspace_owner.me.email
  }
}

# Throwaway key so azurerm is satisfied; the VM has no inbound SSH (the agent
# dials out to Coder), so this key never reaches it.
resource "tls_private_key" "vm" {
  algorithm = "RSA"
  rsa_bits  = 4096
}

locals {
  name = lower("ao-${substr(data.coder_workspace.me.id, 0, 18)}")

  # Run the coder agent as the (baked) coder user via a systemd service so it
  # survives stop/start reboots. The AO worker is baked into the image and gets
  # bootstrapped natively by the control plane over the agent.
  agent_unit = <<-UNIT
    [Unit]
    Description=Coder Agent
    After=network-online.target
    Wants=network-online.target
    [Service]
    User=coder
    Environment=CODER_AGENT_TOKEN=${coder_agent.main.token}
    Environment=HOME=/home/coder
    WorkingDirectory=/home/coder
    ExecStart=/bin/bash /opt/coder-init.sh
    Restart=always
    RestartSec=5
    [Install]
    WantedBy=multi-user.target
  UNIT

  custom_data = base64encode(join("\n", [
    "#cloud-config",
    "write_files:",
    "  - path: /opt/coder-init.sh",
    "    encoding: b64",
    "    permissions: '0755'",
    "    content: ${base64encode(coder_agent.main.init_script)}",
    "  - path: /etc/systemd/system/coder-agent.service",
    "    encoding: b64",
    "    permissions: '0644'",
    "    content: ${base64encode(local.agent_unit)}",
    "runcmd:",
    "  - [ systemctl, daemon-reload ]",
    "  - [ systemctl, enable, --now, coder-agent ]",
  ]))
}

resource "azurerm_public_ip" "main" {
  count               = data.coder_workspace.me.start_count
  name                = "${local.name}-ip"
  resource_group_name = var.resource_group
  location            = var.location
  allocation_method   = "Static"
  sku                 = "Standard"
}

resource "azurerm_network_interface" "main" {
  count               = data.coder_workspace.me.start_count
  name                = "${local.name}-nic"
  resource_group_name = var.resource_group
  location            = var.location

  ip_configuration {
    name                          = "internal"
    subnet_id                     = var.subnet_id
    private_ip_address_allocation = "Dynamic"
    public_ip_address_id          = azurerm_public_ip.main[0].id
  }
}

resource "azurerm_linux_virtual_machine" "main" {
  count                 = data.coder_workspace.me.start_count
  name                  = local.name
  resource_group_name   = var.resource_group
  location              = var.location
  size                  = var.vm_size
  admin_username        = var.admin_username
  network_interface_ids = [azurerm_network_interface.main[0].id]
  custom_data           = local.custom_data
  source_image_id       = var.image_id

  admin_ssh_key {
    username   = var.admin_username
    public_key = tls_private_key.vm.public_key_openssh
  }

  os_disk {
    caching              = "ReadWrite"
    storage_account_type = "StandardSSD_LRS"
  }

  tags = {
    "coder.workspace_id"   = data.coder_workspace.me.id
    "coder.workspace_name" = data.coder_workspace.me.name
    "coder.owner"          = data.coder_workspace_owner.me.name
  }
}
