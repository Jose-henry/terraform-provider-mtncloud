# Isolation

Isolation is a core requirement. It applies to five things.

1. **Tenants.** Acceptance tests run only in a dedicated test sub-tenant and a dedicated test project.
   Production tenants are never used. Master-only tests use a separate, explicitly configured master
   test account and are opt-in (`MTNCLOUD_ACC_MASTER=1`).
2. **Tests.** Every acceptance object is named `tf-acc-<area>-<random>`. Tests never depend on each
   other or on run order. `CheckDestroy` verifies each deletion through the API. Sweepers delete leaked
   `tf-acc-*` objects older than 1 hour, and nothing else, matched on the prefix.
3. **Code.** Only `internal/client` does HTTP. Only `internal/tenancy` decides master vs sub-tenant.
   `services/<area>` packages never import each other; shared logic goes in `normalize`,
   `validators` or `planmodifiers`. No package-level mutable state: the client is safe for 50
   concurrent goroutines (`terraform apply -parallelism=50`).
4. **Credentials.** Credentials come only from env vars or CI secrets. Secrets are `Sensitive` in the
   schema and redacted in logs (`Authorization`, `password`, `secret`, `token`, private keys, Cypher
   values).
5. **Changes.** Legacy and new code share no mutable helpers. Migrating one resource never changes
   another.
