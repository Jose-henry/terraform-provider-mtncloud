resource "mtncloud_reverse_text_task" "hello" {
  name           = "reverse-hello"
  text           = "hello"
  execute_target = "local"
  result_type    = "value"
}
