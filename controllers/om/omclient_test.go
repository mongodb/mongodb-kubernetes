package om

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"

	"github.com/mongodb/mongodb-kubernetes/controllers/om/api"
	"github.com/mongodb/mongodb-kubernetes/controllers/om/apierror"
	"github.com/mongodb/mongodb-kubernetes/pkg/test/httprecorder"
	"github.com/mongodb/mongodb-kubernetes/pkg/util"
)

func init() {
	logger, _ := zap.NewDevelopment()
	zap.ReplaceGlobals(logger)
}

func TestReadProjectsInOrganizationByName(t *testing.T) {
	ctx := t.Context()
	projects := []*Project{{ID: "111", Name: "The Project"}}
	srv := serverMock(projectsInOrganizationByName("testOrgId", projects))
	defer srv.Close()

	connection := NewOpsManagerConnection(&OMContext{BaseURL: srv.URL})

	data, err := connection.ReadProjectsInOrganizationByName(ctx, "testOrgId", "The Project")
	assert.NoError(t, err)
	assert.Equal(t, projects, data)
}

func TestReadOrganizationsByName(t *testing.T) {
	ctx := t.Context()
	organizations := []*Organization{{ID: "111", Name: "The Organization"}}
	srv := serverMock(organizationsByName(organizations))
	defer srv.Close()

	connection := NewOpsManagerConnection(&OMContext{BaseURL: srv.URL})

	data, err := connection.ReadOrganizationsByName(ctx, "The Organization")
	assert.NoError(t, err)
	assert.Equal(t, organizations, data)
}

func TestGettingAutomationConfig(t *testing.T) {
	ctx := t.Context()
	testAutomationConfig := getTestAutomationConfig()
	handleFunc, _ := automationConfig("1", automationConfigResponse{config: testAutomationConfig})
	srv := serverMock(handleFunc)
	defer srv.Close()

	connection := NewOpsManagerConnection(&OMContext{BaseURL: srv.URL, GroupID: "1"})
	data, err := connection.ReadAutomationConfig(ctx)

	assert.NoError(t, err)
	assert.Equal(t, testAutomationConfig.Deployment, data.Deployment)
}

func TestGetAgentAuthMode(t *testing.T) {
	for _, tt := range []struct {
		name, body, mode, errorText string
		status                      int
	}{
		{"SCRAM", `{"auth":{"autoAuthMechanism":"SCRAM-SHA-256"}}`, "SCRAM-SHA-256", "", http.StatusOK},
		{"X509", `{"auth":{"autoAuthMechanism":"MONGODB-X509"}}`, "MONGODB-X509", "", http.StatusOK},
		{"missing auth", `{}`, "", "", http.StatusOK},
		{"null auth", `{"auth":null}`, "", "", http.StatusOK},
		{"missing mechanism", `{"auth":{}}`, "", "", http.StatusOK},
		{"null mechanism", `{"auth":{"autoAuthMechanism":null}}`, "", "", http.StatusOK},
		{"invalid auth", `{"auth":42}`, "", "cannot unmarshal number", http.StatusOK},
		{"invalid mechanism", `{"auth":{"autoAuthMechanism":42}}`, "", "cannot unmarshal number", http.StatusOK},
		{"malformed JSON", `{"auth":`, "", "unexpected end of JSON input", http.StatusOK},
		{"empty body", "", "", "unexpected end of JSON input", http.StatusOK},
		{"unrelated users", `{"auth":{"autoAuthMechanism":"SCRAM-SHA-256","usersWanted":[{"user":42}]}}`, "SCRAM-SHA-256", "", http.StatusOK},
		{"unrelated config", `{"auth":{"autoAuthMechanism":"MONGODB-X509"},"tls":{"CAFilePath":42},"ldap":42,"oidcProviderConfigs":42}`, "MONGODB-X509", "", http.StatusOK},
		{"API error", `{"detail":"invalid automation config"}`, "", "invalid automation config", http.StatusBadRequest},
	} {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				assert.Equal(t, http.MethodGet, r.Method)
				assert.Equal(t, "/api/public/v1.0/groups/1/automationConfig", r.URL.Path)
				w.Header().Set("X-MongoDB-Service-Version", "versionString=8.0.0")
				w.WriteHeader(tt.status)
				_, _ = w.Write([]byte(tt.body))
			}))
			t.Cleanup(srv.Close)
			connection := NewOpsManagerConnection(&OMContext{BaseURL: srv.URL, GroupID: "1"})
			mode, err := connection.GetAgentAuthMode(t.Context())
			if tt.errorText != "" {
				require.ErrorContains(t, err, tt.errorText)
				var apiErr *apierror.Error
				require.ErrorAs(t, err, &apiErr)
				assert.Empty(t, mode)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.mode, mode)
			assert.Equal(t, "8.0.0", connection.OpsManagerVersion().VersionString)
		})
	}
}

func TestGetAgentAuthMode_CanceledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	connection := NewOpsManagerConnection(&OMContext{BaseURL: "http://127.0.0.1:1", GroupID: "1"})
	mode, err := connection.GetAgentAuthMode(ctx)
	require.Error(t, err)
	assert.Contains(t, err.Error(), context.Canceled.Error())
	assert.Empty(t, mode)
}

func TestReadAutomationConfig_WrapsBuilderError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"tls":{"CAFilePath":42}}`))
	}))
	t.Cleanup(srv.Close)
	connection := NewOpsManagerConnection(&OMContext{BaseURL: srv.URL, GroupID: "1"})
	_, err := connection.ReadAutomationConfig(t.Context())
	require.Error(t, err)
	var apiErr *apierror.Error
	require.True(t, errors.As(err, &apiErr))
	assert.Contains(t, apiErr.Detail, "CAFilePath")
}

func TestNotSendingRequestOnNonModifiedAutomationConfig(t *testing.T) {
	ctx := t.Context()
	logger := zap.NewNop().Sugar()
	testAutomationConfig := getTestAutomationConfig()
	handleFunc, counters := automationConfig("1", automationConfigResponse{config: testAutomationConfig})
	srv := serverMock(handleFunc)
	defer srv.Close()

	connection := NewOpsManagerConnection(&OMContext{BaseURL: srv.URL, GroupID: "1"})
	err := connection.ReadUpdateAutomationConfig(ctx, func(ac *AutomationConfig) error {
		return nil
	}, logger)

	assert.NoError(t, err)
	assert.Equal(t, 1, counters.getHitCount)
	assert.Equal(t, 0, counters.putHitCount)
}

// TestNotSendingRequestOnNonModifiedAutomationConfigWithMergoDelete verifies that util.MergoDelete will be ignored during equality comparisons
func TestNotSendingRequestOnNonModifiedAutomationConfigWithMergoDelete(t *testing.T) {
	ctx := t.Context()
	logger := zap.NewNop().Sugar()
	testAutomationConfig := getTestAutomationConfig()
	handleFunc, counters := automationConfig("1", automationConfigResponse{config: testAutomationConfig})
	srv := serverMock(handleFunc)
	defer srv.Close()

	connection := NewOpsManagerConnection(&OMContext{BaseURL: srv.URL, GroupID: "1"})
	err := connection.ReadUpdateAutomationConfig(ctx, func(ac *AutomationConfig) error {
		ac.AgentSSL = &AgentSSL{
			AutoPEMKeyFilePath: util.MergoDelete,
		}
		return nil
	}, logger)

	assert.NoError(t, err)
	assert.Equal(t, 1, counters.getHitCount)
	assert.Equal(t, 0, counters.putHitCount)
}

func OptionRetryConfig(retryWaitMin, retryWaitMax time.Duration, retryMax int) func(client *api.Client) error {
	return func(client *api.Client) error {
		client.RetryWaitMin = retryWaitMin
		client.RetryWaitMax = retryWaitMax
		client.RetryMax = retryMax
		return nil
	}
}

func TestRetriesOnWritingAutomationConfig(t *testing.T) {
	ctx := t.Context()
	logger := zap.NewNop().Sugar()
	testAutomationConfig := getTestAutomationConfig()
	successfulResponse := automationConfigResponse{config: testAutomationConfig}
	errorResponse := automationConfigResponse{errorCode: 500, errorString: "testing"}
	handleFunc, counters := automationConfig("1", errorResponse, errorResponse, successfulResponse)
	srv := serverMock(handleFunc)
	defer srv.Close()

	connection := NewOpsManagerConnectionWithOptions(
		&OMContext{BaseURL: srv.URL, GroupID: "1"},
		OptionRetryConfig(0, 0, 3), // No delay between retries, still retry 3 times
	)
	err := connection.ReadUpdateAutomationConfig(ctx, func(ac *AutomationConfig) error {
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

	for range 5 {
		wg.Go(func() {
			_, err := connection.getHTTPClient()
			assert.NoError(t, err)
		})
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

func TestReadOrganization_OrgIDIsPathEscaped(t *testing.T) {
	ctx := t.Context()
	orgJSON, err := json.Marshal(&Organization{ID: "real-org", Name: "real-org"})
	require.NoError(t, err)

	// Each payload is a distinct injection primitive an attacker could place in the
	// ConfigMap's "orgId" field.
	payloads := []string{
		"inject/groups?envelope=true",     // '/' path-segment injection + '?' query injection
		"real-org?pretty=true",            // pure query-parameter injection
		"real-org#/api/public/v1.0/admin", // '#' fragment: truncates any suffix client-side
		"x/../../api/public/v1.0/groups",  // '..' traversal out of /orgs/
	}

	for _, payload := range payloads {
		t.Run(payload, func(t *testing.T) {
			srv, rec := httprecorder.NewServer(http.StatusOK, orgJSON)
			defer srv.Close()

			conn := NewOpsManagerConnectionWithOptions(&OMContext{BaseURL: srv.URL}, OptionRetryConfig(0, 0, 1))

			_, err := conn.ReadOrganization(ctx, payload)
			require.NoError(t, err)

			got := rec.Last()
			t.Logf("ReadOrganization(%q) -> OM received: %s %s (RawQuery=%q)",
				payload, got.Method, got.RequestURI, got.RawQuery)

			want := "/api/public/v1.0/orgs/" + url.PathEscape(payload)
			assert.Equalf(t, want, got.RequestURI,
				"orgID must remain a single URL-escaped path segment; a different request-target means "+
					"the authenticated request was forged to an attacker-chosen OM endpoint")
			assert.Emptyf(t, got.RawQuery,
				"orgID must not be able to inject query parameters into the OM request); got %q", got.RawQuery)
		})
	}
}

func TestReadProjectsInOrganization_OrgIDIsPathEscaped(t *testing.T) {
	ctx := t.Context()
	projectsJSON, err := json.Marshal(&ProjectsResponse{Groups: []*Project{{ID: "111", Name: "The Project"}}})
	require.NoError(t, err)

	// A '/'+'?' payload that, unescaped, would break out of /orgs/{id} into a different
	// endpoint and smuggle query parameters.
	const orgID = "inject/agentapikeys?itemsPerPage=1"

	t.Run("ReadProjectsInOrganizationByName", func(t *testing.T) {
		srv, rec := httprecorder.NewServer(http.StatusOK, projectsJSON)
		defer srv.Close()

		conn := NewOpsManagerConnectionWithOptions(&OMContext{BaseURL: srv.URL}, OptionRetryConfig(0, 0, 1))

		_, err := conn.ReadProjectsInOrganizationByName(ctx, orgID, "The Project")
		require.NoError(t, err)

		got := rec.Last()
		t.Logf("ReadProjectsInOrganizationByName -> OM received: %s %s", got.Method, got.RequestURI)

		want := "/api/public/v1.0/orgs/" + url.PathEscape(orgID) + "/groups?name=" + url.QueryEscape("The Project")
		assert.Equalf(t, want, got.RequestURI,
			"orgID must remain a single escaped path segment so the request stays on the intended "+
				"/orgs/{orgID}/groups endpoint")
	})

	t.Run("ReadProjectsInOrganization", func(t *testing.T) {
		srv, rec := httprecorder.NewServer(http.StatusOK, projectsJSON)
		defer srv.Close()

		conn := NewOpsManagerConnectionWithOptions(&OMContext{BaseURL: srv.URL}, OptionRetryConfig(0, 0, 1))

		_, err := conn.ReadProjectsInOrganization(context.Background(), orgID, 0)
		require.NoError(t, err)

		got := rec.Last()
		t.Logf("ReadProjectsInOrganization -> OM received: %s %s", got.Method, got.RequestURI)

		want := "/api/public/v1.0/orgs/" + url.PathEscape(orgID) + "/groups?itemsPerPage=500&pageNum=0"
		assert.Equalf(t, want, got.RequestURI,
			"orgID must remain a single escaped path segment so the request stays on the intended "+
				"/orgs/{orgID}/groups endpoint")
	})
}
