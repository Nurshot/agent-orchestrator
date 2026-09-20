# AO Coder template: one dedicated Azure VM per workspace (mirrors the eleven_x
# AWS EC2-per-workspace dev-kit model). Each workspace is its own VM that runs
# the release-matched AO worker image + coder agent, so it has full VM isolation
# (dedicated CPU/RAM/disk) and ~2 min boot, unlike the shared-VM Docker template
# in ../coder/main.tf. Stop deallocates the VM; start brings it back.
#
# Auth: the Coder server passes Azure creds to the azurerm provider via ARM_*
# env (a scoped service principal). Workspace VMs pull the worker image from ACR
# using the acr_* variables.

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
variable "workspace_image" {
  type    = string
  default = "aocoderazure.azurecr.io/ao-coder-workspace:latest"
}
variable "acr_server" {
  type = string
}
variable "acr_username" {
  type = string
}
variable "acr_password" {
  type      = string
  sensitive = true
}
variable "admin_username" {
  type    = string
  default = "coder"
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

# A throwaway key so azurerm is satisfied; the VM has no inbound SSH (the agent
# dials out to Coder), so this key never reaches it.
resource "tls_private_key" "vm" {
  algorithm = "RSA"
  rsa_bits  = 4096
}

locals {
  name = lower("ao-${substr(data.coder_workspace.me.id, 0, 18)}")

  # cloud-config: on first boot install Docker, pull the workspace image from
  # ACR, and run it with the coder agent init as the container command (the AO
  # worker is baked into the image; the CP bootstraps it over the agent). The
  # agent init script is delivered base64 via write_files to avoid any quoting.
  custom_data = base64encode(join("\n", [
    "#cloud-config",
    "write_files:",
    "  - path: /opt/coder-init.sh",
    "    encoding: b64",
    "    permissions: '0755'",
    "    content: ${base64encode(coder_agent.main.init_script)}",
    "runcmd:",
    "  - [ bash, -c, \"command -v docker >/dev/null 2>&1 || (curl -fsSL https://get.docker.com | sh)\" ]",
    "  - [ systemctl, enable, --now, docker ]",
    "  - [ bash, -c, \"echo '${var.acr_password}' | docker login ${var.acr_server} --username '${var.acr_username}' --password-stdin\" ]",
    "  - [ bash, -c, \"docker pull ${var.workspace_image}\" ]",
    "  - [ bash, -c, \"docker run -d --restart unless-stopped --name workspace -e CODER_AGENT_TOKEN='${coder_agent.main.token}' -v /opt/coder-init.sh:/opt/coder-init.sh:ro --entrypoint sh ${var.workspace_image} /opt/coder-init.sh\" ]",
    "  - [ bash, -c, \"rm -f /root/.docker/config.json\" ]",
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

  admin_ssh_key {
    username   = var.admin_username
    public_key = tls_private_key.vm.public_key_openssh
  }

  os_disk {
    caching              = "ReadWrite"
    storage_account_type = "StandardSSD_LRS"
  }

  source_image_reference {
    publisher = "Canonical"
    offer     = "0001-com-ubuntu-server-jammy"
    sku       = "22_04-lts-gen2"
    version   = "latest"
  }

  tags = {
    "coder.workspace_id"   = data.coder_workspace.me.id
    "coder.workspace_name" = data.coder_workspace.me.name
    "coder.owner"          = data.coder_workspace_owner.me.name
  }
}
