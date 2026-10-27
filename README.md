# Terraform Provider for MTN Cloud

Manage [MTN Cloud](https://console.cloud.mtn.ng) with Terraform: projects, MTN Networks and Routers,
security groups, instances, catalog services, MOS object storage, automation, and tenancy.

> Status: **pre-release**. The provider builds and its schema is tested; MTN-specific resources are
> being designed (see `dev-docs/backlog.md`). Don't use it for production infrastructure yet.

## Use

```hcl
terraform {
  required_providers {
    mtncloud = {
      source = "jose-henry/mtncloud"
    }
  }
}

provider "mtncloud" {
  url          = "https://console.cloud.mtn.ng"
  access_token = var.mtncloud_access_token # or MTNCLOUD_API_TOKEN
}
```

Credentials can come from environment variables instead: `MTNCLOUD_API_URL`, `MTNCLOUD_API_TOKEN`,
or `MTNCLOUD_API_USERNAME` + `MTNCLOUD_API_PASSWORD` + `MTNCLOUD_API_TENANT`. Never commit a
`terraform.tfvars` that holds a token.

A working starter is in [`examples/quickstart`](examples/quickstart). Every resource and data source
is documented in [`docs/`](docs).

## Develop

Requirements: Go (see `go.mod`), [golangci-lint v2](https://golangci-lint.run), Terraform, and Python 3.

```sh
make build       # bin/terraform-provider-mtncloud
make test        # unit and schema tests
make ci          # everything a pull request must pass
```

To try a local build, point Terraform at it with a `dev_overrides` block in `~/.terraformrc`
(`%APPDATA%\terraform.rc` on Windows):

```hcl
provider_installation {
  dev_overrides {
    "jose-henry/mtncloud" = "/path/to/MTN-TERRAFORM-PROVIDER/bin"
  }
  direct {}
}
```

How we work (plan first, branch and PR, tests, isolation) is in [CLAUDE.md](CLAUDE.md) and
[`.claude/rules/`](.claude/rules). Design notes and decisions are in [`dev-docs/`](dev-docs).

## License

MIT. See [LICENSE](LICENSE). Derived from an MIT-licensed upstream provider; its notice is retained.
