package om

import (
	"encoding/json"
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
	"github.com/mongodb/mongodb-kubernetes/pkg/util"
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

func OptionRetryConfig(retryWaitMin, retryWaitMax time.Duration, retryMax int) func(client *api.Client) error {
	return func(client *api.Client) error {
		client.RetryWaitMin = retryWaitMin
		client.RetryWaitMax = retryWaitMax
		client.RetryMax = retryMax
		return nil
	}
}

func TestRetriesOnWritingAutomationConfig(t *testing.T) {
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

// ============================ SECBUG-4043 ============================
// SECBUG-4043: the organization ID is read verbatim from the user-editable project
// ConfigMap (controllers/operator/project/projectconfig.go) and spliced into the Ops
// Manager REST path with a bare fmt.Sprintf (controllers/om/omclient.go). Without
// url.PathEscape, an attacker-chosen orgID containing '/', '?', '#' or '..' redirects the
// operator's (digest-authenticated) request to an arbitrary OM endpoint and the reply is
// read back - an SSRF / authenticated request-forgery.
//
// These tests assert the SECURE behaviour: the whole orgID must stay a single URL-escaped
// path segment, injecting neither extra path segments nor query parameters. On the UNFIXED
// code they FAIL, and the failure (plus the t.Logf line) prints the forged request URI the
// operator actually sent - that is the concrete SECBUG-4043 repro. Once ReadOrganization
// and its siblings wrap orgID in url.PathEscape, they PASS.
//
// Credentials are intentionally left empty so digest auth is skipped (see the guard in
// api/http.go Request); the digest, when present, is computed over this same path
// (api/http.go authorizeRequest -> api/digest.go getDigestAuthorization), so a forged path
// arrives at OM validly authenticated.

type requestInfo struct {
	method     string
	path       string // r.URL.Path (percent-decoded by the server)
	rawQuery   string // r.URL.RawQuery
	requestURI string // raw request-target exactly as received on the wire
	count      int
}

type requestRecorder struct {
	mu   sync.Mutex
	last requestInfo
}

func (rec *requestRecorder) record(r *http.Request) {
	rec.mu.Lock()
	defer rec.mu.Unlock()
	rec.last.count++
	rec.last.method = r.Method
	rec.last.path = r.URL.Path
	rec.last.rawQuery = r.URL.RawQuery
	rec.last.requestURI = r.RequestURI
}

func (rec *requestRecorder) get() requestInfo {
	rec.mu.Lock()
	defer rec.mu.Unlock()
	return rec.last
}

// capturingServer records the raw request-target of every request and replies with the
// given status and body. It deliberately uses a bare http.HandlerFunc (not http.ServeMux)
// so that path traversal (`..`) and escaped separators are observed exactly as sent,
// without the mux's path-cleaning/redirects masking the injection.
func capturingServer(status int, body []byte) (*httptest.Server, *requestRecorder) {
	rec := &requestRecorder{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.record(r)
		w.WriteHeader(status)
		_, _ = w.Write(body)
	}))
	return srv, rec
}

func TestReadOrganization_OrgIDIsPathEscaped_SECBUG4043(t *testing.T) {
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
			srv, rec := capturingServer(http.StatusOK, orgJSON)
			defer srv.Close()

			conn := NewOpsManagerConnectionWithOptions(&OMContext{BaseURL: srv.URL}, OptionRetryConfig(0, 0, 1))

			_, err := conn.ReadOrganization(payload)
			require.NoError(t, err)

			got := rec.get()
			t.Logf("SECBUG-4043 ReadOrganization(%q) -> OM received: %s %s (RawQuery=%q)",
				payload, got.method, got.requestURI, got.rawQuery)

			want := "/api/public/v1.0/orgs/" + url.PathEscape(payload)
			assert.Equalf(t, want, got.requestURI,
				"orgID must remain a single URL-escaped path segment; a different request-target means "+
					"the authenticated request was forged to an attacker-chosen OM endpoint (SECBUG-4043)")
			assert.Emptyf(t, got.rawQuery,
				"orgID must not be able to inject query parameters into the OM request (SECBUG-4043); got %q", got.rawQuery)
		})
	}
}

func TestReadProjectsInOrganization_OrgIDIsPathEscaped_SECBUG4043(t *testing.T) {
	projectsJSON, err := json.Marshal(&ProjectsResponse{Groups: []*Project{{ID: "111", Name: "The Project"}}})
	require.NoError(t, err)

	// A '/'+'?' payload that, unescaped, would break out of /orgs/{id} into a different
	// endpoint and smuggle query parameters.
	const orgID = "inject/agentapikeys?itemsPerPage=1"

	t.Run("ReadProjectsInOrganizationByName", func(t *testing.T) {
		srv, rec := capturingServer(http.StatusOK, projectsJSON)
		defer srv.Close()

		conn := NewOpsManagerConnectionWithOptions(&OMContext{BaseURL: srv.URL}, OptionRetryConfig(0, 0, 1))

		_, err := conn.ReadProjectsInOrganizationByName(orgID, "The Project")
		require.NoError(t, err)

		got := rec.get()
		t.Logf("SECBUG-4043 ReadProjectsInOrganizationByName -> OM received: %s %s", got.method, got.requestURI)

		want := "/api/public/v1.0/orgs/" + url.PathEscape(orgID) + "/groups?name=" + url.QueryEscape("The Project")
		assert.Equalf(t, want, got.requestURI,
			"orgID must remain a single escaped path segment so the request stays on the intended "+
				"/orgs/{orgID}/groups endpoint (SECBUG-4043)")
	})

	t.Run("ReadProjectsInOrganization", func(t *testing.T) {
		srv, rec := capturingServer(http.StatusOK, projectsJSON)
		defer srv.Close()

		conn := NewOpsManagerConnectionWithOptions(&OMContext{BaseURL: srv.URL}, OptionRetryConfig(0, 0, 1))

		_, err := conn.ReadProjectsInOrganization(orgID, 0)
		require.NoError(t, err)

		got := rec.get()
		t.Logf("SECBUG-4043 ReadProjectsInOrganization -> OM received: %s %s", got.method, got.requestURI)

		want := "/api/public/v1.0/orgs/" + url.PathEscape(orgID) + "/groups?itemsPerPage=500&pageNum=0"
		assert.Equalf(t, want, got.requestURI,
			"orgID must remain a single escaped path segment so the request stays on the intended "+
				"/orgs/{orgID}/groups endpoint (SECBUG-4043)")
	})
}
