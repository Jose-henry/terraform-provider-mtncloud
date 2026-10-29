---
subcategory: ""
page_title: "Configuring the provider: every way to set it up"
description: |-
    Credentials, group, cloud and project, variables, environment variables and files: every way to configure the MTN Cloud provider, and what wins when several are set.
---

# Configuring the provider

This guide lists every way to configure the provider, and the order in which settings win when you
set the same thing in more than one place. For a first run from a new account, read
[Getting started](getting_started.md) first.

## Contents

- [1. Require the provider](#1-require-the-provider)
- [2. The console address](#2-the-console-address)
- [3. Credentials](#3-credentials)
- [4. Group, cloud and project](#4-group-cloud-and-project)
- [5. Variables, files and environment variables](#5-variables-files-and-environment-variables)
- [6. More than one tenant or project](#6-more-than-one-tenant-or-project)
- [7. Choosing things by name](#7-choosing-things-by-name)
- [8. Secrets and write-only arguments](#8-secrets-and-write-only-arguments)
- [9. Things worth knowing](#9-things-worth-knowing)
- [10. Quick reference](#10-quick-reference)

## 1. Require the provider

```terraform
terraform {
  required_version = ">= 1.5"
  required_providers {
    mtncloud = {
      source = "jose-henry/mtncloud"
    }
  }
}
```

Terraform 1.5 or later works, as does OpenTofu. **Terraform 1.11 or later** is needed for the
resources that take a secret as a *write-only* argument (database passwords, keys and so on, see
[section 8](#8-secrets-and-write-only-arguments)).

## 2. The console address

| Setting | Provider block | Environment variable | Default |
|---|---|---|---|
| Console URL | `url` | `MTNCLOUD_API_URL` | none: required |
| Verify the console's TLS certificate | `secure` | `MTNCLOUD_API_SECURE` | `true` |

```terraform
provider "mtncloud" {
  url = "https://console.cloud.mtn.ng"
}
```

Leave `secure` alone. Set it to `false` only for a test console with a self-signed certificate: the
provider then warns on every run, because credentials could be intercepted.

## 3. Credentials

There are two kinds of login, and each can be given in the provider block or through environment
variables.

### Username and password

You log in to the console as `subdomain\username`. Terraform takes the two parts separately:

| Setting | Provider block | Environment variable |
|---|---|---|
| Tenant subdomain | `tenant_subdomain` | `MTNCLOUD_API_TENANT` |
| Username | `username` | `MTNCLOUD_API_USERNAME` |
| Password | `password` | `MTNCLOUD_API_PASSWORD` |

### Access token

An API access token, from **User Settings > API Access** in the console. A token belongs to one
user in one tenant, so no subdomain is needed.

| Setting | Provider block | Environment variable |
|---|---|---|
| Access token | `access_token` | `MTNCLOUD_API_TOKEN` |

### Which login is used

1. A value in the provider block wins over its environment variable.
2. If a **username** is set (in the block or in `MTNCLOUD_API_USERNAME`), the provider logs in with
   username and password, and **ignores any token**. An old `MTNCLOUD_API_TOKEN` left in your shell
   can't override a username and password.
3. Otherwise the access token is used.

### Where to keep them

From most to least recommended:

1. **Environment variables**, set by your shell, a secrets manager or your CI system. Nothing is
   written in a file:

   ```shell
   export MTNCLOUD_API_URL="https://console.cloud.mtn.ng"
   export MTNCLOUD_API_TENANT="acme"
   export MTNCLOUD_API_USERNAME="terraform"
   export MTNCLOUD_API_PASSWORD="..."
   ```

   On Windows PowerShell: `$env:MTNCLOUD_API_PASSWORD = "..."`.

2. **Sensitive input variables**, filled from a variables file you never commit, or from `TF_VAR_`
   environment variables:

   ```terraform
   variable "mtncloud_token" {
     type      = string
     sensitive = true
   }

   provider "mtncloud" {
     url          = "https://console.cloud.mtn.ng"
     access_token = var.mtncloud_token
   }
   ```

   Then any one of:

   - `export TF_VAR_mtncloud_token="..."`
   - a `secrets.auto.tfvars` file (loaded automatically; add `*.tfvars` to `.gitignore`)
   - `terraform apply -var-file=secrets.tfvars`
   - `terraform apply -var="mtncloud_token=..."` (it ends up in your shell history: avoid it)

3. **A file read by Terraform**, for example a token kept in your home directory:

   ```terraform
   provider "mtncloud" {
     url          = "https://console.cloud.mtn.ng"
     access_token = trimspace(file(pathexpand("~/.mtncloud/token")))
   }
   ```

4. **Written in the provider block.** It works, but anyone who can read the file has your login.
   Use it only for a throwaway test.

!> Never commit `*.tfvars` files with secrets, state files or tokens to version control. State
holds the attributes of every resource you manage, so keep it in a protected backend.

## 4. Group, cloud and project

Most resources are placed in a **group** (your tenant's group, for example `MTNNG_CLOUD_AZ_1`), a
**cloud** (for example `MTNNG_CLOUD_AZ_1`) and a **project** (for example `web-prod`). Each can be set
in three places. The first one set wins:

| Order | Where | Group | Cloud | Project |
|---|---|---|---|---|
| 1 | The resource's own argument | `group` | `cloud` | `project` |
| 2 | The provider block | `group` | `cloud` | `project` |
| 3 | An environment variable | `MTNCLOUD_GROUP` | `MTNCLOUD_CLOUD` | `MTNCLOUD_PROJECT` |

If a resource needs one and none of the three is set, the plan fails and names all three places.

```terraform
# Every resource below uses these unless it sets its own.
provider "mtncloud" {
  url     = "https://console.cloud.mtn.ng"
  group   = "MTNNG_CLOUD_AZ_1"
  project = "web-prod"
}

resource "mtncloud_network" "app" {
  name    = "app-net" # in web-prod, from the provider
  cidr    = "10.10.0.0/24"
  gateway = "10.10.0.1"
}

resource "mtncloud_network" "data" {
  name    = "data-net"
  project = "data-prod" # its own project wins
  cidr    = "10.20.0.0/24"
  gateway = "10.20.0.1"
}
```

Good to know:

- **One group and one cloud.** An MTN Cloud tenant normally has one group and one cloud, and the
  provider finds them itself. Set `group` or `cloud` only if your account has several; set `cloud`
  only if a group spans several clouds.
- **Project names.** MTN Cloud names a project `<name>-<your tenant>`, for example `web-prod-acme`. You
  can use either the short name (`web-prod`) or the full one. A name that matches nothing fails the
  plan and lists your projects.
- **A new project takes time.** A project ordered with `mtncloud_project` takes up to an hour to sync
  in (with its default security group and floating IP pools). Resources look their project up when
  they plan, so add the resources that use a new project in a later apply.
- **Changing a default moves nothing.** Resources record their group and project when they are
  created. If you later change the provider's `project`, existing resources show a plan to be rebuilt
  in the new project (or, for catalog orders, an error), never a silent move. Set `project` on the
  resource to pin it.

## 5. Variables, files and environment variables

Everything above can be combined. A common layout:

```text
provider.tf          # terraform {} and provider "mtncloud" {} reading variables
variables.tf         # variable "project" { default = "web-prod" } ...
prod.tfvars          # project = "web-prod"      (no secrets: safe to commit)
staging.tfvars       # project = "web-staging"
secrets.auto.tfvars  # passwords and tokens       (git-ignored)
```

```terraform
variable "project" {
  type = string
}

provider "mtncloud" {
  url     = "https://console.cloud.mtn.ng"
  project = var.project
}
```

```shell
terraform apply -var-file=staging.tfvars
```

Terraform reads variable values in this order, later ones winning: defaults in `variable` blocks,
`TF_VAR_<name>` environment variables, `terraform.tfvars`, `*.auto.tfvars` (alphabetical),
`-var-file` and `-var` on the command line.

In **CI**, set the `MTNCLOUD_API_*` variables (or `TF_VAR_*`) from the CI system's secrets store, and
`MTNCLOUD_PROJECT` per environment, so the same code deploys to each project.

## 6. More than one tenant or project

Use provider aliases, one per login or per default project:

```terraform
provider "mtncloud" {
  url     = "https://console.cloud.mtn.ng"
  project = "web-prod"
}

provider "mtncloud" {
  alias   = "staging"
  url     = "https://console.cloud.mtn.ng"
  project = "web-staging"
}

resource "mtncloud_network" "staging_app" {
  provider = mtncloud.staging
  name     = "app-net"
  cidr     = "10.30.0.0/24"
  gateway  = "10.30.0.1"
}
```

Credentials for an alias can't come from the shared `MTNCLOUD_API_*` variables if they differ:
give each alias its own variables (`access_token = var.staging_token`).

## 7. Choosing things by name

You choose everything by the name the console shows: projects, plans (`G2S4`), images
(`Ubuntu 24.04`), networks, security groups, datastores, floating IP pools, cluster layouts,
registries and catalog choices such as database versions. The provider looks names up **when you
plan**, so a typo fails the plan before anything is built, with a list of the valid names.

To see what you can use, the data sources list it:

| To find | Data source |
|---|---|
| Your projects | `mtncloud_projects` |
| Plans (sizes) | `mtncloud_plans` |
| OS images | `mtncloud_images` |
| Networks, security groups, datastores | `mtncloud_networks`, `mtncloud_security_groups`, `mtncloud_datastores` |
| External networks (for routers and floating IPs) | `mtncloud_external_networks` |
| Catalog services | `mtncloud_catalog_items` |
| Cluster layouts and registries | `mtncloud_cluster_layouts`, `mtncloud_registries` |

```terraform
data "mtncloud_projects" "all" {}

output "projects" {
  value = data.mtncloud_projects.all.projects[*].name
}
```

## 8. Secrets and write-only arguments

Passwords and keys you give the provider (database passwords, an SMB share password, an SSL
certificate's private key and so on) are **write-only** arguments where Terraform supports it: they
are sent to MTN Cloud and never stored in state or shown in plans. They need **Terraform 1.11 or
later**.

Because a write-only value is never stored, Terraform can't notice when you change it. Resources that
can change a secret in place have a matching `*_version` argument: change the secret, bump the
version, and the new value is sent.

```terraform
resource "mtncloud_ssl_certificate" "shop" {
  name                = "shop-example-com"
  certificate         = file("shop.crt")
  private_key         = var.shop_key # write-only
  private_key_version = 2            # was 1: sends the new key
}
```

Values MTN Cloud generates for you (`mtncloud_cypher_password`, `mtncloud_cypher_key`, ...) are
stored in state as sensitive values, because you need to read them back.

## 9. Things worth knowing

- **Destroying a catalog order deletes what it made.** Resources such as `mtncloud_mysql` and
  `mtncloud_self_managed_kubernetes` place a catalog order. Destroy deletes the instances it made, through
  their app for the managed services and Kubernetes, with their disks and floating IPs, then the order's record.
  `mtncloud_letsencrypt_certificate` deletes nothing (the certificate stays on the instance), and
  `mtncloud_project` refuses, as MTN Cloud has no order that deletes a project. To keep what an order
  made, stop managing it with a `removed` block instead:

  ```terraform
  removed {
    from = mtncloud_mysql.db
    lifecycle {
      destroy = false
    }
  }
  ```

  NFS and SMB shares are the exception: their destroy orders the deletion.
- **Orders can't change.** Changing an input of an ordered service fails the plan rather than
  ordering a second one.
- **Long operations.** Instances, clusters, projects and catalog orders wait for MTN Cloud to finish.
  Each has a `timeouts` block if your builds take longer than the defaults. If a wait ends first, what
  was created is kept, with a warning, and is never created twice.
- **Import.** Anything that can be created in the console can be imported:
  `terraform import mtncloud_network.app 1234`, or an `import {}` block. For values MTN Cloud doesn't
  report back (passwords, the inputs of an order), the first apply after an import records what your
  configuration says without changing anything.
- **Parallelism.** The provider limits its own request rate, so `terraform apply -parallelism=50` is
  safe.
- **Master-tenant resources.** Some resources (appliance settings, tenants, roles) are only for the
  master tenant; their descriptions start with "Master tenant only". A customer (sub-tenant) login
  gets a clear error if it tries them.
- **Logs.** `TF_LOG=DEBUG terraform plan` shows every API call. Passwords, tokens and keys are never
  logged.

## 10. Quick reference

| Provider block | Environment variable | Purpose |
|---|---|---|
| `url` | `MTNCLOUD_API_URL` | Console address (required) |
| `secure` | `MTNCLOUD_API_SECURE` | Verify TLS (default `true`) |
| `tenant_subdomain` | `MTNCLOUD_API_TENANT` | Subdomain, for username login |
| `username` | `MTNCLOUD_API_USERNAME` | Username login (wins over a token) |
| `password` | `MTNCLOUD_API_PASSWORD` | Password for username login |
| `access_token` | `MTNCLOUD_API_TOKEN` | Token login |
| `group` | `MTNCLOUD_GROUP` | Default group |
| `cloud` | `MTNCLOUD_CLOUD` | Default cloud |
| `project` | `MTNCLOUD_PROJECT` | Default project |

A value in the provider block always wins over its environment variable, and a resource's own
`group`, `cloud` or `project` wins over both.
