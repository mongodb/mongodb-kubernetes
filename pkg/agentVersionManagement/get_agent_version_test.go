package agentVersionManagement

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/10gen/ops-manager-kubernetes/controllers/om"
)

var jsonContents = `
{
  "supportedImages": {
    "mongodb-agent": {
      "Description": "Agents corresponding to OpsManager 5.x and 6.x series",
      "Description for specific versions": {
        "11.0.5.6963-1": "An upgraded version for OM 5.0 we use for Operator-only deployments",
        "12.0.28.7763-1": "OM 6 basic version",
        "12.0.15.7646-1": "Community and Helm Charts version"
      },
      "versions": [
        "12.0.4.7554-1",
        "12.0.15.7646-1",
        "12.0.21.7698-1",
        "12.0.24.7719-1",
        "12.0.25.7724-1",
        "12.0.28.7763-1",
        "12.0.29.7785-1",
        "107.0.0.8465-1",
        "107.0.1.8507-1"
      ],
      "opsManagerMapping": {
        "cloud_manager": "13.10.0.8620-1",
        "cloud_manager_tools": "100.9.4",
        "ops_manager": {
          "6.0.0": {
            "agent_version": "12.0.30.7791-1",
            "tools_version": "100.9.4"
          },
          "7.0.0": {
            "agent_version": "107.0.1.8507-1",
            "tools_version": "100.9.4"
          },
          "7.0.1": {
            "agent_version": "107.0.1.8507-1",
            "tools_version": "100.9.4"
          },
          "7.0.2": {
            "agent_version": "107.0.2.8531-1",
            "tools_version": "100.9.4"
          },
          "8.0.0-rc1": {
            "agent_version": "108.0.0.8676-1",
            "tools_version": "100.10.0"
          }
        }
      },
      "legacyMonitoringOpsManagerMapping": {
        "5.9": {
          "agent_version": "12.0.4.7554-1"
        },
        "6.0": {
          "agent_version": "12.0.4.7554-1"
        },
        "7.0": {
          "agent_version": "107.0.0.8465-1"
        }
      },
      "variants": [
        "ubi"
      ]
    }
  }
}
`

func TestGetAgentVersionManager(t *testing.T) {
	type args struct {
		omConnection    om.Connection
		omVersion       string
		readFromMapping bool
	}

	tempFilePath, closer := createTempMapping(t)
	defer func() {
		_ = closer()
	}()

	getAgentVersionTests := []struct {
		name    string
		args    args
		want    string
		wantErr bool
	}{
		{
			name:    "When OM Admin is not reachable reconcile again",
			wantErr: true,
		},
		{
			name: "For om, return direct match if supported for static for version 7",
			args: args{
				omConnection: om.NewEmptyMockedOmConnectionWithAgentVersion("11.0.5.6963-1", "11.0.0.11-1"),
				omVersion:    "7.0.0",
			},
			want: "11.0.5.6963-1",
		},
		{
			name: "For om, return direct match if supported for static for version 6",
			args: args{
				omConnection: om.NewEmptyMockedOmConnectionWithAgentVersion("11.0.5.6963-1", "11.0.0.11-1"),
				omVersion:    "6.0.21",
			},
			want: "11.0.5.6963-1",
		},
		{
			name: "When we use CM, we query the 'cloud_manager' key in mapping",
			args: args{
				omVersion: "v13.0.4.5666-1",
			},
			want: "13.10.0.8620-1",
		},
		{
			name: "When read from mapping (appdb) and match, return direct match",
			args: args{
				readFromMapping: true,
				omVersion:       "7.0.0",
			},
			want: "107.0.1.8507-1",
		},
		{
			name: "Too old OM version",
			args: args{
				omVersion: "6.0.10",
			},
			wantErr: true,
		},
		{
			name: "Too old OM version 5",
			args: args{
				omVersion: "5.0.10",
			},
			wantErr: true,
		},
		{
			name: "Ops Manager RC interpreted correctly",
			args: args{
				readFromMapping: true,
				omVersion:       "8.0.0-rc1",
			},
			want: "108.0.0.8676-1",
		},
		{
			name: "Version not in mapping, does not matter since using connection",
			args: args{
				omConnection: om.NewEmptyMockedOmConnectionWithAgentVersion("11.0.5.6963-1", "11.0.0.11-1"),
				omVersion:    "7.10.0",
			},
			want: "11.0.5.6963-1",
		},
		{
			name: "When AppDB and no match, return latest for same major",
			args: args{
				readFromMapping: true,
				omVersion:       "v13.11.4.5666-1",
			},
			want: "13.10.0.8620-1",
		},
	}
	versionManager, err := InitializeAgentVersionManager(tempFilePath)
	assert.NoError(t, err)

	for _, tt := range getAgentVersionTests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := versionManager.GetAgentVersion(tt.args.omConnection, tt.args.omVersion, tt.args.readFromMapping)
			if tt.wantErr {
				assert.Error(t, err, "GetAgentVersion() should return an error")
			} else {
				assert.NoError(t, err, "GetAgentVersion() should not return an error")
			}
			assert.Equal(t, tt.want, got)
		})
	}
}

func createTempMapping(t *testing.T) (string, func() error) {
	tempDirectory := t.TempDir()
	tempFileName := "version_mapping.json"
	tempFilePath := filepath.Join(tempDirectory, tempFileName)

	file, err := os.Create(tempFilePath)
	if err != nil {
		t.Errorf("Creating temp file failed: %s", err)
	}

	_, err = file.Write([]byte(jsonContents))
	if err != nil {
		t.Errorf("Writing JSON to file failed: %s", err)
	}
	_, err = InitializeAgentVersionManager(tempFilePath)
	if err != nil {
		t.Errorf("Initializing version manager failed: %s", err)
	}
	return tempFilePath, file.Close
}
