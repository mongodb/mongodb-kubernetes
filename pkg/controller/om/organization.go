package om

type OrganizationsResponse struct {
	Organizations []*Organization `json:"results"`
}

type Organization struct {
	Id   string `json:"id,omitempty"`
	Name string `json:"name"`
}
