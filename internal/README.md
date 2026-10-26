# internal

| Package | Role |
|---|---|
| `legacy` | Upstream SDKv2 resources, renamed. Shrinks as resources migrate. |
| `provider` | Plugin Framework provider and mux |
| `client` | HTTP: the only layer that calls the API |
| `tenancy` | Master vs sub-tenant gating |
| `services/<area>` | Framework resources and data sources |
| `validators`, `planmodifiers`, `normalize` | Shared schema helpers |
| `acctest`, `sweep` | Acceptance test support |

Folders without Go files hold only a README stating their contract until code lands.
