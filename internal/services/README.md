# services

Framework resources and data sources, one package per area (`compute`, `network`, `catalog`,
`storage`, `iam`, ...). Packages never import each other. Shared logic lives in `normalize`,
`validators` and `planmodifiers`.
