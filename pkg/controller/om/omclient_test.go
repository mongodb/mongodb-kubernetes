package om

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"go.uber.org/zap"
)

func init() {
	logger, _ := zap.NewDevelopment()
	zap.ReplaceGlobals(logger)
}

func TestReadProjectsInOrganizationByName(t *testing.T) {
	projects := []*Project{{ID: "111", Name: "The Project"}}
	srv := serverMock(projectsInOrganizationByName("testOrgId", projects))
	defer srv.Close()

	connection := NewOpsManagerConnection(&OMContext{BaseURL: srv.URL})

	data, err := connection.ReadProjectsInOrganizationByName("testOrgId", "The Project")
	assert.NoError(t, err)
	assert.Equal(t, projects, data)
}

func TestReadOrganizationsByName(t *testing.T) {
	organizations := []*Organization{{ID: "111", Name: "The Organization"}}
	srv := serverMock(organizationsByName(organizations))
	defer srv.Close()

	connection := NewOpsManagerConnection(&OMContext{BaseURL: srv.URL})

	data, err := connection.ReadOrganizationsByName("The Organization")
	assert.NoError(t, err)
	assert.Equal(t, organizations, data)
}

// ******************************* Mock HTTP Server methods *****************************************************

type handleFunc func(mux *http.ServeMux)

func serverMock(handlers ...handleFunc) *httptest.Server {
	handler := http.NewServeMux()
	for _, h := range handlers {
		h(handler)
	}

	srv := httptest.NewServer(handler)

	return srv
}

func projectsInOrganizationByName(orgId string, projects []*Project) handleFunc {
	return func(mux *http.ServeMux) {
		mux.HandleFunc(fmt.Sprintf("/api/public/v1.0/orgs/%s/groups", orgId),
			func(w http.ResponseWriter, r *http.Request) {
				found := false
				for _, p := range projects {
					if p.Name == r.URL.Query()["name"][0] {
						found = true
					}
				}
				if !found {
					w.WriteHeader(http.StatusNotFound)
					return
				}
				response := ProjectsResponse{Groups: projects}
				data, _ := json.Marshal(response)
				_, _ = w.Write(data)
			})
	}
}

func organizationsByName(organizations []*Organization) handleFunc {
	return func(mux *http.ServeMux) {
		mux.HandleFunc("/api/public/v1.0/orgs",
			func(w http.ResponseWriter, r *http.Request) {
				found := false
				for _, p := range organizations {
					if p.Name == r.URL.Query()["name"][0] {
						found = true
					}
				}
				if !found {
					w.WriteHeader(http.StatusNotFound)
					return
				}
				response := OrganizationsResponse{Organizations: organizations}
				data, _ := json.Marshal(response)
				_, _ = w.Write(data)
			})
	}
}
