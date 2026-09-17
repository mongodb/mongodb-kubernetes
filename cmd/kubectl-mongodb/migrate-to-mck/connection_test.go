package migratetomck

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"

	mdbv1 "github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/mdb"
	"github.com/mongodb/mongodb-kubernetes/controllers/om"
)

func testLogger() *zap.SugaredLogger {
	logger, _ := zap.NewDevelopment()
	return logger.Sugar()
}

func newMockConnection(orgsAndProjects map[*om.Organization][]*om.Project) *om.MockedOmConnection {
	conn := om.NewMockedOmConnection(nil)
	conn.OrganizationsWithGroups = orgsAndProjects
	return conn
}

func mdbv1ProjectConfig(baseURL, orgID, projectName string) mdbv1.ProjectConfig {
	return mdbv1.ProjectConfig{
		BaseURL:     baseURL,
		OrgID:       orgID,
		ProjectName: projectName,
	}
}

func mdbv1Credentials(pub, priv string) mdbv1.Credentials {
	return mdbv1.Credentials{
		PublicAPIKey:  pub,
		PrivateAPIKey: priv,
	}
}

func TestResolveProjectReadOnly(t *testing.T) {
	org := &om.Organization{ID: "org-1", Name: "my-org"}
	proj := &om.Project{ID: "proj-1", Name: "my-project", OrgID: "org-1"}

	tests := map[string]struct {
		orgsAndProjects map[*om.Organization][]*om.Project
		orgID           string
		projectName     string
		expectedErr     string
	}{
		"project resolved": {
			orgsAndProjects: map[*om.Organization][]*om.Project{org: {proj}},
			orgID:           "org-1",
			projectName:     "my-project",
		},
		"organization not found": {
			orgsAndProjects: map[*om.Organization][]*om.Project{},
			projectName:     "nonexistent",
			expectedErr:     "organization not found",
		},
		"project not found in organization": {
			orgsAndProjects: map[*om.Organization][]*om.Project{org: {}},
			orgID:           "org-1",
			projectName:     "missing-project",
			expectedErr:     "not found in organization",
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			mockConn := newMockConnection(tc.orgsAndProjects)

			origFactory := omConnectionFactory
			t.Cleanup(func() { omConnectionFactory = origFactory })
			omConnectionFactory = func(_ *om.OMContext) om.Connection { return mockConn }

			conn, err := resolveProjectReadOnly(
				mdbv1ProjectConfig("http://localhost:8080", tc.orgID, tc.projectName),
				mdbv1Credentials("pub", "priv"),
				testLogger(),
			)

			if tc.expectedErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tc.expectedErr)
				return
			}
			require.NoError(t, err)
			require.NotNil(t, conn)
		})
	}
}
