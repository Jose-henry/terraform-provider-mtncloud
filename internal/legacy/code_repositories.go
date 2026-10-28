package legacy

// CodeRepositories is an /api/options list reply, read by the Git integration and ServiceNow
// workflow data sources.
type CodeRepositories struct {
	Success bool `json:"success"`
	Data    []struct {
		Name  string `json:"name"`
		Value int    `json:"value"`
	} `json:"data"`
}
