BINARY  := terraform-provider-mtncloud
PKGS    := ./...
TF      ?= terraform

.PHONY: build install test test-race lint fmt fmt-check vet docs docs-check names examples vuln ci testacc

build:
	go build -o bin/$(BINARY) .

install:
	go install .

test:
	go test $(PKGS)

test-race:
	go test -race $(PKGS)

vet:
	go vet $(PKGS)

lint:
	golangci-lint run $(PKGS)

fmt:
	golangci-lint fmt
	$(TF) fmt -recursive examples

fmt-check:
	@test -z "$$(golangci-lint fmt --diff)" || (golangci-lint fmt --diff; exit 1)
	$(TF) fmt -check -recursive examples

docs:
	go run github.com/hashicorp/terraform-plugin-docs/cmd/tfplugindocs generate --provider-name mtncloud
	go run github.com/hashicorp/terraform-plugin-docs/cmd/tfplugindocs validate --provider-name mtncloud

docs-check: docs
	@git diff --exit-code -- docs/ || (echo "docs/ is out of date: run 'make docs' and commit"; exit 1)

names:
	bash scripts/check-names.sh

examples:
	TERRAFORM=$(TF) bash scripts/validate-examples.sh

vuln:
	go run golang.org/x/vuln/cmd/govulncheck@latest $(PKGS)

# Everything CI runs on a pull request, in the same order.
ci: fmt-check vet lint test build docs-check names examples

# Real MTN Cloud. Only with the owner's explicit permission, against the test tenant.
testacc:
	@test -n "$$MTNCLOUD_API_URL" || (echo "MTNCLOUD_API_URL is not set"; exit 1)
	TF_ACC=1 go test $(PKGS) -v -run '^TestAcc' -timeout 120m
