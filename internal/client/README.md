# client

The only package that does HTTP or imports the external SDK (alias `sdk`). It provides typed methods
and typed errors (NotFound, Conflict, Forbidden, RateLimited), retries with capped backoff and jitter,
client-side rate limiting, `ctx` on every call, redacted logging, and a lookup cache. It knows nothing
about Terraform. Tests are httptest contract tests against `testdata/`.
