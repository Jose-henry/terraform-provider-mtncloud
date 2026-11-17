package legacy

import (
	"context"
	"net/http"
	"strconv"
	"strings"

	"log"

	sdk "github.com/gomorpheus/morpheus-go-sdk"
	"github.com/hashicorp/terraform-plugin-sdk/v2/diag"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/schema"
)

func resourceNestedWorkflowTask() *schema.Resource {
	return &schema.Resource{
		Description:   "Provides an MTN Cloud nested workflow task resource: a task that runs another workflow.",
		CreateContext: resourceNestedWorkflowTaskCreate,
		ReadContext:   resourceNestedWorkflowTaskRead,
		UpdateContext: resourceNestedWorkflowTaskUpdate,
		DeleteContext: resourceNestedWorkflowTaskDelete,

		Schema: map[string]*schema.Schema{
			"id": {
				Type:        schema.TypeString,
				Description: "The ID of the nested workflow task",
				Computed:    true,
			},
			"name": {
				Type:        schema.TypeString,
				Description: "The name of the nested workflow task",
				Required:    true,
			},
			"code": {
				Type:        schema.TypeString,
				Description: "The code of the nested workflow task",
				Optional:    true,
				Computed:    true,
			},
			"labels": {
				Type:        schema.TypeSet,
				Description: "The organization labels associated with the task (Only supported on MTN Cloud 5.5.3 or higher)",
				Optional:    true,
				Computed:    true,
				Elem:        &schema.Schema{Type: schema.TypeString},
			},
			"operational_workflow_id": {
				Type:        schema.TypeInt,
				Description: "The ID of the operational workflow",
				Optional:    true,
				Computed:    true,
			},
			"operational_workflow_name": {
				Type:        schema.TypeString,
				Description: "The name of the operational workflow. Give this or `operational_workflow_id`.",
				Optional:    true,
				Computed:    true,
				// Found ignoring case; the console's spelling is stored.
				DiffSuppressFunc: func(_, old, new string, _ *schema.ResourceData) bool { return strings.EqualFold(old, new) },
			},
			"retryable": {
				Type:        schema.TypeBool,
				Description: "Whether to retry the task if there is a failure",
				Optional:    true,
				Computed:    true,
			},
			"retry_count": {
				Type:        schema.TypeInt,
				Description: "The number of times to retry the task if there is a failure",
				Optional:    true,
				Default:     5,
			},
			"retry_delay_seconds": {
				Type:        schema.TypeInt,
				Description: "The number of seconds to wait between retry attempts",
				Optional:    true,
				Default:     10,
			},
			"allow_custom_config": {
				Type:        schema.TypeBool,
				Description: "Custom configuration data to pass during the execution of the shell script",
				Optional:    true,
				Computed:    true,
			},
		},
		Importer: &schema.ResourceImporter{
			StateContext: schema.ImportStatePassthroughContext,
		},
	}
}

func resourceNestedWorkflowTaskCreate(ctx context.Context, d *schema.ResourceData, meta interface{}) diag.Diagnostics {
	client := meta.(*sdk.Client)

	// Warning or errors can be collected in a slice type
	var diags diag.Diagnostics

	name := d.Get("name").(string)

	taskType := map[string]interface{}{
		"code": "nestedWorkflow",
	}

	taskOptions, nd := nestedWorkflowOptions(client, d)
	if nd != nil {
		return nd
	}

	labelsPayload := make([]string, 0)
	if attr, ok := d.GetOk("labels"); ok {
		for _, s := range attr.(*schema.Set).List() {
			labelsPayload = append(labelsPayload, s.(string))
		}
	}

	req := &sdk.Request{
		Body: map[string]interface{}{
			"task": map[string]interface{}{
				"name":              name,
				"code":              d.Get("code").(string),
				"labels":            labelsPayload,
				"taskType":          taskType,
				"taskOptions":       taskOptions,
				"executeTarget":     "local",
				"retryable":         d.Get("retryable"),
				"retryCount":        d.Get("retry_count"),
				"retryDelaySeconds": d.Get("retry_delay_seconds"),
				"allowCustomConfig": d.Get("allow_custom_config"),
			},
		},
	}
	resp, err := client.CreateTask(req)
	if err != nil {
		log.Printf("API FAILURE: %s - %s", resp, err)
		return diag.FromErr(err)
	}
	log.Printf("API RESPONSE: %s", resp)

	result := resp.Result.(*sdk.CreateTaskResult)
	task := result.Task
	// Successfully created resource, now set id
	d.SetId(int64ToString(task.ID))
	log.Printf("Task ID: %s", int64ToString(task.ID))

	resourceShellScriptTaskRead(ctx, d, meta)
	return diags
}

func resourceNestedWorkflowTaskRead(ctx context.Context, d *schema.ResourceData, meta interface{}) diag.Diagnostics {
	client := meta.(*sdk.Client)

	// Warning or errors can be collected in a slice type
	var diags diag.Diagnostics

	id := d.Id()
	name := d.Get("name").(string)

	// lookup by name if we do not have an id yet
	var resp *sdk.Response
	var err error
	if id == "" && name != "" {
		resp, err = client.FindTaskByName(name)
	} else if id != "" {
		resp, err = client.GetTask(toInt64(id), &sdk.Request{})
	} else {
		return diag.Errorf("Task cannot be read without name or id")
	}

	if err != nil {
		// 404 is ok?
		if resp != nil && resp.StatusCode == http.StatusNotFound {
			log.Printf("API 404: %s - %s", resp, err)
			log.Printf("Forcing recreation of resource")
			d.SetId("")
			return diags
		} else {
			log.Printf("API FAILURE: %s - %s", resp, err)
			return diag.FromErr(err)
		}
	}
	log.Printf("API RESPONSE: %s", resp)

	// store resource data
	result := resp.Result.(*sdk.GetTaskResult)
	nestedWorkflowTask := result.Task
	d.SetId(int64ToString(nestedWorkflowTask.ID))
	setAttr(ctx, d, "name", nestedWorkflowTask.Name)
	setAttr(ctx, d, "code", nestedWorkflowTask.Code)
	setAttr(ctx, d, "labels", nestedWorkflowTask.Labels)
	setIntFromString(ctx, d, "operational_workflow_id", nestedWorkflowTask.TaskOptions.OperationalWorkflowId)
	setAttr(ctx, d, "operational_workflow_name", nestedWorkflowTask.TaskOptions.OperationalWorkflowName)
	setAttr(ctx, d, "retryable", nestedWorkflowTask.Retryable)
	setAttr(ctx, d, "retry_count", nestedWorkflowTask.RetryCount)
	setAttr(ctx, d, "retry_delay_seconds", nestedWorkflowTask.RetryDelaySeconds)
	setAttr(ctx, d, "allow_custom_config", nestedWorkflowTask.AllowCustomConfig)
	return diags
}

func resourceNestedWorkflowTaskUpdate(ctx context.Context, d *schema.ResourceData, meta interface{}) diag.Diagnostics {
	client := meta.(*sdk.Client)
	id := d.Id()
	name := d.Get("name").(string)

	taskType := map[string]interface{}{
		"code": "nestedWorkflow",
	}

	taskOptions, nd := nestedWorkflowOptions(client, d)
	if nd != nil {
		return nd
	}

	labelsPayload := make([]string, 0)
	if attr, ok := d.GetOk("labels"); ok {
		for _, s := range attr.(*schema.Set).List() {
			labelsPayload = append(labelsPayload, s.(string))
		}
	}

	req := &sdk.Request{
		Body: map[string]interface{}{
			"task": map[string]interface{}{
				"name":              name,
				"code":              d.Get("code").(string),
				"labels":            labelsPayload,
				"taskType":          taskType,
				"taskOptions":       taskOptions,
				"executeTarget":     "local",
				"retryable":         d.Get("retryable"),
				"retryCount":        d.Get("retry_count"),
				"retryDelaySeconds": d.Get("retry_delay_seconds"),
				"allowCustomConfig": d.Get("allow_custom_config"),
			},
		},
	}
	resp, err := client.UpdateTask(toInt64(id), req)
	if err != nil {
		log.Printf("API FAILURE: %s - %s", resp, err)
		return diag.FromErr(err)
	}
	log.Printf("API RESPONSE: %s", resp)
	result := resp.Result.(*sdk.UpdateTaskResult)
	shellScriptTask := result.Task
	// Successfully updated resource, now set id
	// err, it should not have changed though..
	d.SetId(int64ToString(shellScriptTask.ID))
	return resourceShellScriptTaskRead(ctx, d, meta)
}

func resourceNestedWorkflowTaskDelete(ctx context.Context, d *schema.ResourceData, meta interface{}) diag.Diagnostics {
	client := meta.(*sdk.Client)

	// Warning or errors can be collected in a slice type
	var diags diag.Diagnostics

	id := d.Id()
	req := &sdk.Request{}
	resp, err := client.DeleteTask(toInt64(id), req)
	if err != nil {
		if resp != nil && resp.StatusCode == http.StatusNotFound {
			log.Printf("API 404: %s - %s", resp, err)
			return nil
		} else {
			log.Printf("API FAILURE: %s - %s", resp, err)
			return diag.FromErr(err)
		}
	}
	log.Printf("API RESPONSE: %s", resp)
	d.SetId("")
	return diags
}

// nestedWorkflowOptions picks the workflow as the console's form does: from the workflows MTN offers
// for nesting (/api/options/operationWorkflows), sending its name and its ID as text. Before, an ID
// alone was sent with an empty name ("Operational Workflow is required") and a name alone with ID 0
// ("must be a valid value"): tester's fix 18.1.
func nestedWorkflowOptions(client *sdk.Client, d *schema.ResourceData) (map[string]interface{}, diag.Diagnostics) {
	name, id := configuredWorkflow(d)
	resp, err := client.GetOptionSource("operationWorkflows", &sdk.Request{QueryParams: map[string]string{"max": "1000"}})
	if err != nil {
		return nil, diag.Errorf("Couldn't list the workflows you can nest: %v", err)
	}
	option, wid, err := findOptionSourceOption(resp.Result.(*sdk.GetOptionSourceResult).Data, "operational workflow you can nest", name, id)
	if err != nil {
		return nil, diag.FromErr(err)
	}
	return map[string]interface{}{
		"operationalWorkflowId":   strconv.FormatInt(wid, 10),
		"operationalWorkflowName": option.Name,
	}, nil
}

// configuredWorkflow is the workflow the configuration names: its name when given (it decides when
// both are), else its ID. State is not used, because the name it holds doesn't follow a rename in
// the console, so an update of another argument would look for the old name.
func configuredWorkflow(d *schema.ResourceData) (string, int) {
	cfg := d.GetRawConfig()
	if cfg.IsNull() || !cfg.IsKnown() { // no raw configuration (unit tests): what is set
		if name := d.Get("operational_workflow_name").(string); name != "" {
			return name, 0
		}
		return "", d.Get("operational_workflow_id").(int)
	}
	if v := cfg.GetAttr("operational_workflow_name"); v.IsKnown() && !v.IsNull() && v.AsString() != "" {
		return v.AsString(), 0
	}
	if v := cfg.GetAttr("operational_workflow_id"); v.IsKnown() && !v.IsNull() {
		n, _ := v.AsBigFloat().Int64()
		return "", int(n)
	}
	return d.Get("operational_workflow_name").(string), d.Get("operational_workflow_id").(int)
}
