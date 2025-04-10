package om

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"go.uber.org/zap"

	"github.com/10gen/ops-manager-kubernetes/pkg/util"
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

func TestGettingAutomationConfig(t *testing.T) {
	testAutomationConfig := getTestAutomationConfig()
	handleFunc, _ := automationConfig("1", automationConfigResponse{config: testAutomationConfig})
	srv := serverMock(handleFunc)
	defer srv.Close()

	connection := NewOpsManagerConnection(&OMContext{BaseURL: srv.URL, GroupID: "1"})
	data, err := connection.ReadAutomationConfig()

	assert.NoError(t, err)
	assert.Equal(t, testAutomationConfig.Deployment, data.Deployment)
}

func TestNotSendingRequestOnNonModifiedAutomationConfig(t *testing.T) {
	logger := zap.NewNop().Sugar()
	testAutomationConfig := getTestAutomationConfig()
	handleFunc, counters := automationConfig("1", automationConfigResponse{config: testAutomationConfig})
	srv := serverMock(handleFunc)
	defer srv.Close()

	connection := NewOpsManagerConnection(&OMContext{BaseURL: srv.URL, GroupID: "1"})
	err := connection.ReadUpdateAutomationConfig(func(ac *AutomationConfig) error {
		return nil
	}, logger)

	assert.NoError(t, err)
	assert.Equal(t, 1, counters.getHitCount)
	assert.Equal(t, 0, counters.putHitCount)
}

// TestNotSendingRequestOnNonModifiedAutomationConfigWithMergoDelete verifies that util.MergoDelete will be ignored during equality comparisons
func TestNotSendingRequestOnNonModifiedAutomationConfigWithMergoDelete(t *testing.T) {
	logger := zap.NewNop().Sugar()
	testAutomationConfig := getTestAutomationConfig()
	handleFunc, counters := automationConfig("1", automationConfigResponse{config: testAutomationConfig})
	srv := serverMock(handleFunc)
	defer srv.Close()

	connection := NewOpsManagerConnection(&OMContext{BaseURL: srv.URL, GroupID: "1"})
	err := connection.ReadUpdateAutomationConfig(func(ac *AutomationConfig) error {
		ac.AgentSSL = &AgentSSL{
			AutoPEMKeyFilePath: util.MergoDelete,
		}
		return nil
	}, logger)

	assert.NoError(t, err)
	assert.Equal(t, 1, counters.getHitCount)
	assert.Equal(t, 0, counters.putHitCount)
}

func TestRetriesOnWritingAutomationConfig(t *testing.T) {
	logger := zap.NewNop().Sugar()
	testAutomationConfig := getTestAutomationConfig()
	successfulResponse := automationConfigResponse{config: testAutomationConfig}
	errorResponse := automationConfigResponse{errorCode: 500, errorString: "testing"}
	handleFunc, counters := automationConfig("1", errorResponse, errorResponse, successfulResponse)
	srv := serverMock(handleFunc)
	defer srv.Close()

	connection := NewOpsManagerConnection(&OMContext{BaseURL: srv.URL, GroupID: "1"})
	err := connection.ReadUpdateAutomationConfig(func(ac *AutomationConfig) error {
		return nil
	}, logger)

	assert.NoError(t, err)
	assert.Equal(t, 3, counters.getHitCount)
}

func TestHTTPOmConnectionGetHTTPClientRace(t *testing.T) {
	successfulResponse := automationConfigResponse{config: getTestAutomationConfig()}
	errorResponse := automationConfigResponse{errorCode: 500, errorString: "testing"}
	handleFunc, _ := automationConfig("1", errorResponse, errorResponse, successfulResponse)
	srv := serverMock(handleFunc)
	defer srv.Close()

	connection := NewOpsManagerConnection(&OMContext{BaseURL: srv.URL, GroupID: "1"}).(*HTTPOmConnection)
	wg := sync.WaitGroup{}

	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			_, err := connection.getHTTPClient()
			assert.NoError(t, err)
			wg.Done()
		}()
	}

	wg.Wait()
}

// ******************************* Mock HTTP Server methods *****************************************************

type handleFunc func(mux *http.ServeMux)

type counters struct {
	getHitCount int
	putHitCount int
	totalCount  int
}

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

type automationConfigResponse struct {
	config      *AutomationConfig
	errorCode   int
	errorString string
}

func automationConfig(groupId string, responses ...automationConfigResponse) (handleFunc, *counters) {
	counters := &counters{}
	handle := func(mux *http.ServeMux) {
		mux.HandleFunc(fmt.Sprintf("/api/public/v1.0/groups/%s/automationConfig", groupId),
			func(w http.ResponseWriter, r *http.Request) {
				switch r.Method {
				case "GET":
					counters.getHitCount = counters.getHitCount + 1
					response := responses[counters.totalCount]
					if response.config != nil {
						data, _ := json.Marshal(response.config.Deployment)
						_, _ = w.Write(data)
					} else if response.errorCode != 0 {
						http.Error(w, response.errorString, response.errorCode)
					}
				case "PUT":
					counters.putHitCount = counters.putHitCount + 1
					w.WriteHeader(http.StatusOK)
				}
				counters.totalCount = counters.totalCount + 1
			})
	}
	return handle, counters
}
