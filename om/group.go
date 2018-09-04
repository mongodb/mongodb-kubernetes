package om

type Group struct {
	Id          string   `json:"id,omitempty"`
	Name        string   `json:"name"`
	OrgId       string   `json:"orgId,omitempty"`
	Tags        []string `json:"tags,omitempty"`
	AgentApiKey string   `json:"agentApiKey,omitempty"`
}
