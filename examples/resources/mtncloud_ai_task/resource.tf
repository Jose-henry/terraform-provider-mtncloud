resource "mtncloud_ai_task" "summary" {
  name        = "summarise-instance"
  ai_agent    = "ops-assistant"
  prompt      = "Summarise the health of <%=instance.name%> in two sentences."
  result_type = "value"
}
