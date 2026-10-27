---
subcategory: ""
page_title: "Getting started with the MTN Cloud Terraform provider"
description: |-
    Set up your MTN Cloud account, then build a network, a router and a server with Terraform.
---

# Getting Started with the MTN Cloud Provider

This guide takes you from a new MTN Cloud account to a running server with a public IP, built with
Terraform.

## 1. Set up your account in the MTN Cloud console

Do these once, in the console, before you use Terraform.

1. **Create your account** at [cloud.mtn.ng](https://cloud.mtn.ng), then log in to the console at
   [console.cloud.mtn.ng](https://console.cloud.mtn.ng). You log in as `subdomain\username`: note
   your subdomain, Terraform needs it.
2. **Create a project.** Everything you build (networks, routers, security groups, servers) lives in
   a project. In the console, open the catalog and order **Create Project**, with a name such as
   `web-prod`. MTN Cloud names it `web-prod-<your tenant>`; in Terraform you can use the short name.
   Allow **up to an hour** before using it: the project, its default security group, external
   networks and floating IP pools sync in during that time, and until then Terraform reports them as
   not found. (`mtncloud_project` can order a project too, but creating your first one in the console
   is simpler: you see it arrive.)
3. **Set your login details for servers.** Open **User Settings** and fill in the **Linux settings**
   (username, password, SSH key) and the **Windows settings** (username, password). Every server you
   create, from the console or from Terraform, takes its login from these. You can also manage SSH
   keys with `mtncloud_key_pair`, but setting them here first means your first server is reachable.

## 2. Install Terraform

[Install Terraform](https://developer.hashicorp.com/terraform/install) 1.5 or later (or OpenTofu).
This guide assumes you know the basics: `init`, `plan` and `apply`.

## 3. Configure the provider

Create a `provider.tf` file:

```terraform
terraform {
  required_providers {
    mtncloud = {
      source  = "jose-henry/mtncloud"
      version = "~> 0.3"
    }
  }
}

# Credentials come from the environment (see below), so none are written here.
provider "mtncloud" {
  url = "https://console.cloud.mtn.ng"
}
```

Give it your login through environment variables, so no password is ever written in a file:

```shell
export MTNCLOUD_API_TENANT="your-subdomain"
export MTNCLOUD_API_USERNAME="your-username"
export MTNCLOUD_API_PASSWORD="your-password"
```

An [access token](auth.md#access-token) works too. See [Authentication](auth.md) for every option.

## 4. Build a server

Create a `main.tf` file. It makes a private network, a router that connects it to the internet, a
security group (firewall) and an Ubuntu server with a public IP, all in your `web-prod` project:

```terraform
# "web-prod" is the project you created in the console.

# A private network for the servers.
resource "mtncloud_network" "app" {
  name    = "app-net"
  project = "web-prod"
  cidr    = "10.20.1.0/24"
  gateway = "10.20.1.1"
}

# Connects the network to the internet, so the server can have a public IP.
resource "mtncloud_router" "edge" {
  name             = "edge"
  project          = "web-prod"
  networks         = [mtncloud_network.app.id]
  external_network = "public-network-02"
}

# Lets in HTTPS from anywhere, and SSH from your own address only.
resource "mtncloud_security_group" "web" {
  name    = "web"
  project = "web-prod"

  rules = [
    { direction = "ingress", protocol = "tcp", port_range = "443", cidr = "0.0.0.0/0" },
    { direction = "ingress", protocol = "tcp", port_range = "22", cidr = "203.0.113.10/32" },
  ]
}

# An Ubuntu server. You log in with the Linux settings from your User Settings.
resource "mtncloud_instance" "web" {
  name             = "web-1"
  group            = "MTNNG_CLOUD_AZ_1"
  project          = "web-prod"
  image            = "MTN Ubuntu Server 24.04.3LTS"
  plan             = "G2S4"
  networks         = [mtncloud_network.app.id]
  security_groups  = [mtncloud_security_group.web.name]
  floating_ip_pool = "public-network-02"

  # The public IP needs the router first.
  depends_on = [mtncloud_router.edge]
}

output "web_public_ip" {
  value = mtncloud_instance.web.public_ip
}
```

Replace `203.0.113.10` with your own public IP address, so only you can reach the server over SSH.
The [Features](features.md) page lists what else you can build, and each resource's page lists the
names you can use (images, plans, external networks).

Then run:

```shell
terraform init     # downloads the provider
terraform plan     # shows what will be created, and checks every name against your account
terraform apply    # creates it
```

`terraform plan` checks names (project, image, plan, external network) against your account before
anything is created, and lists the valid ones when a name is wrong. `terraform apply` waits for each
part to be ready, then prints the server's public IP. Log in with the username and SSH key from your
User Settings:

```shell
ssh your-linux-username@<web_public_ip>
```

Run `terraform plan` again: it says `No changes.` Your configuration and MTN Cloud match.

## 5. Clean up

`terraform destroy` removes the server, the security group, the router and the network. The project
stays: projects are managed in the console.
