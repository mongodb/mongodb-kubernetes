package searchcontroller

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	searchv1 "github.com/mongodb/mongodb-kubernetes/api/v1/search"
	"github.com/mongodb/mongodb-kubernetes/pkg/statefulset"
)

type mockSearchSource struct{}

func (m *mockSearchSource) KeyfileSecretName() string   { return "" }
func (m *mockSearchSource) TLSConfig() *TLSSourceConfig { return nil }
func (m *mockSearchSource) HostSeeds() []string         { return []string{"host1:27017"} }
func (m *mockSearchSource) Validate() error             { return nil }

func TestCreateSearchStatefulSetFunc_JVMFlags(t *testing.T) {
	testCases := []struct {
		name             string
		jvmFlags         []string
		expectedJVMFlags string
		notExpected      string
	}{
		{
			name:             "no jvm flags",
			jvmFlags:         nil,
			notExpected:      "--jvm-flags",
			expectedJVMFlags: "/mongot-community/mongot --config /mongot/config.yml",
		},
		{
			name:             "single jvm flag",
			jvmFlags:         []string{"-Xmx2g"},
			expectedJVMFlags: `--jvm-flags "-Xmx2g"`,
		},
		{
			name:             "multiple jvm flags",
			jvmFlags:         []string{"-Xmx2g", "-Xms512m", "-XX:+UseG1GC"},
			expectedJVMFlags: `--jvm-flags "-Xmx2g -Xms512m -XX:+UseG1GC"`,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			search := newTestMongoDBSearch("test-search", "default", func(s *searchv1.MongoDBSearch) {
				s.Spec.JVMFlags = tc.jvmFlags
			})

			stsModification := CreateSearchStatefulSetFunc(search, &mockSearchSource{}, "mongot:latest")
			sts := statefulset.New(stsModification)

			// Find the mongot container
			var mongotContainer *containerInfo
			for i := range sts.Spec.Template.Spec.Containers {
				if sts.Spec.Template.Spec.Containers[i].Name == MongotContainerName {
					mongotContainer = &containerInfo{
						args: sts.Spec.Template.Spec.Containers[i].Args,
					}
					break
				}
			}

			require.NotNil(t, mongotContainer, "mongot container not found in StatefulSet")
			require.Len(t, mongotContainer.args, 2, "the args are of form ['-c', '<script>'], that's why 2 is expected")

			script := mongotContainer.args[1]

			if tc.expectedJVMFlags != "" {
				assert.True(t, strings.Contains(script, tc.expectedJVMFlags),
					"expected args to contain %q, got %q", tc.expectedJVMFlags, script)
			}

			if tc.notExpected != "" {
				assert.False(t, strings.Contains(script, tc.notExpected),
					"expected args to NOT contain %q, got %q", tc.notExpected, script)
			}
		})
	}
}

type containerInfo struct {
	args []string
}
