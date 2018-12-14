package om

// OrganizationsResponse
type OrganizationsResponse struct {
	Organizations []*Organization `json:"results"`
}

// Organizations
type Organization struct {
	ID   string `json:"id,omitempty"`
	Name string `json:"name"`
}
