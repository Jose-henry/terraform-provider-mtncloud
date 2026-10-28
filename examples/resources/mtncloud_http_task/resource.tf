resource "mtncloud_http_task" "notify" {
  name        = "notify-deploy"
  url         = "https://hooks.example.com/deploy"
  method      = "POST"
  body        = jsonencode({ instance = "<%=instance.name%>" })
  headers     = { "Content-Type" = "application/json" }
  credential  = "deploy-hook"
  result_type = "json"
}
