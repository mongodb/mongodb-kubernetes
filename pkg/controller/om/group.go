package om

// ProjectsResponse
type ProjectsResponse struct {
	OMPaginaged
	Groups     []*Project `json:"results"`
	TotalCount int        `json:"totalCount"`
	Next       int        `json:"next,omitempty"`
}

func (o ProjectsResponse) Results() []interface{} {
	// Lack of covariance in Go... :(
	ans := make([]interface{}, len(o.Groups))
	for i, org := range o.Groups {
		ans[i] = org
	}
	return ans
}

// Project
type Project struct {
	ID          string   `json:"id,omitempty"`
	Name        string   `json:"name"`
	OrgID       string   `json:"orgId,omitempty"`
	Tags        []string `json:"tags,omitempty"`
	AgentAPIKey string   `json:"agentApiKey,omitempty"`
}
