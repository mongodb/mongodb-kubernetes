package om

// GroupsResponse
type GroupsResponse struct {
	Groups []*Group `json:"results"`
}

// Group
type Group struct {
	ID          string   `json:"id,omitempty"`
	Name        string   `json:"name"`
	OrgID       string   `json:"orgId,omitempty"`
	Tags        []string `json:"tags,omitempty"`
	AgentAPIKey string   `json:"agentApiKey,omitempty"`
}
