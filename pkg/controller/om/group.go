package om

type GroupsResponse struct {
	Groups []*Group `json:"results"`
}

type Group struct {
	Id          string   `json:"id,omitempty"`
	Name        string   `json:"name"`
	OrgId       string   `json:"orgId,omitempty"`
	Tags        []string `json:"tags,omitempty"`
	AgentApiKey string   `json:"agentApiKey,omitempty"`
}
