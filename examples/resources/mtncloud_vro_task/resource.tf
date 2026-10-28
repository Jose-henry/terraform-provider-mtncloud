data "mtncloud_integration" "vro" {
  name = "My vRO Integration"
}

data "mtncloud_vro_workflow" "tf_example_vro_workflow" {
  name = "My vRO Workflow Name"
}

resource "mtncloud_vro_task" "tf_example_vro_task" {
  name               = "tfexample vro-task"
  code               = "tfexample-vro-task"
  labels             = ["demo", "terraform"]
  vro_integration_id = data.mtncloud_integration.vro.id
  vro_workflow_value = data.mtncloud_vro_workflow.tf_example_vro_workflow.value
  body               = <<EOF
{
    "parameters": [
        {
            "name": "vmName",
            "type": "string",
            "value": {
                "string": {
                    "value": "<%=instance.hostname%>"
                }
            }
        }
    ]
}
EOF
  execute_target     = "local"
  retryable          = false
}
