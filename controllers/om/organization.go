package om

type OrganizationsResponse struct {
	OMPaginated
	Organizations []*Organization `json:"results"`
}

func (o OrganizationsResponse) Results() []any {
	// Lack of covariance in Go... :(
	ans := make([]any, len(o.Organizations))
	for i, org := range o.Organizations {
		ans[i] = org
	}
	return ans
}

type Organization struct {
	ID   string `json:"id,omitempty"`
	Name string `json:"name"`
}
