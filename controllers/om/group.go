package om

type ProjectsResponse struct {
	OMPaginated
	Groups []*Project `json:"results"`
}

func (o ProjectsResponse) Results() []any {
	// Lack of covariance in Go... :(
	ans := make([]any, len(o.Groups))
	for i, org := range o.Groups {
		ans[i] = org
	}
	return ans
}

type Project struct {
	ID          string   `json:"id,omitempty"`
	Name        string   `json:"name"`
	OrgID       string   `json:"orgId,omitempty"`
	Tags        []string `json:"tags,omitempty"`
	AgentAPIKey string   `json:"agentApiKey,omitempty"`
}
