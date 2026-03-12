package searchcontroller

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"

	searchv1 "github.com/mongodb/mongodb-kubernetes/api/v1/search"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/construct"
	"github.com/mongodb/mongodb-kubernetes/pkg/statefulset"
)

func TestCreateSearchStatefulSetFunc_JVMFlags(t *testing.T) {
	testCases := []struct {
		name             string
		usersJVMFlags    []string
		expectedJVMFlags string
		notExpected      string
	}{
		{
			name:             "no jvm flags - default heap from 4G memory",
			usersJVMFlags:    nil,
			expectedJVMFlags: `--jvm-flags "-Xmx1907m -Xms1907m"`,
		},
		{
			name:             "user provides -Xmx only - no auto heap added",
			usersJVMFlags:    []string{"-Xmx2g"},
			expectedJVMFlags: `--jvm-flags "-Xmx2g"`,
			notExpected:      "-Xms",
		},
		{
			name:             "user provides -Xms only - no auto heap added",
			usersJVMFlags:    []string{"-Xms1g"},
			expectedJVMFlags: `--jvm-flags "-Xms1g"`,
			notExpected:      "-Xmx",
		},
		{
			name:             "user provides both -Xmx and -Xms - no auto heap added",
			usersJVMFlags:    []string{"-Xmx2g", "-Xms512m"},
			expectedJVMFlags: `--jvm-flags "-Xmx2g -Xms512m"`,
		},
		{
			name:             "user provides non-heap flags only - auto heap prepended",
			usersJVMFlags:    []string{"-XX:+UseG1GC"},
			expectedJVMFlags: `--jvm-flags "-Xmx1907m -Xms1907m -XX:+UseG1GC"`,
		},
		{
			name:             "user provides heap and non-heap flags - no auto heap",
			usersJVMFlags:    []string{"-Xmx2g", "-Xms512m", "-XX:+UseG1GC"},
			expectedJVMFlags: `--jvm-flags "-Xmx2g -Xms512m -XX:+UseG1GC"`,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			search := newTestMongoDBSearch("test-search", "default", func(s *searchv1.MongoDBSearch) {
				s.Spec.JVMFlags = tc.usersJVMFlags
			})

			stsModification := CreateSearchStatefulSetFunc(search, "", "", "", "", nil, "mongot:latest")
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

func TestCreateSearchStatefulSetFunc_JVMFlags_CustomMemory(t *testing.T) {
	testCases := []struct {
		name             string
		memory           string
		expectedJVMFlags string
	}{
		{
			name:             "4Gi memory - half is 2048m",
			memory:           "4Gi",
			expectedJVMFlags: `--jvm-flags "-Xmx2048m -Xms2048m"`,
		},
		{
			name:             "4G memory - half is 1907m",
			memory:           "4G",
			expectedJVMFlags: `--jvm-flags "-Xmx1907m -Xms1907m"`,
		},
		{
			name:             "512Mi memory - half is 256m",
			memory:           "512Mi",
			expectedJVMFlags: `--jvm-flags "-Xmx256m -Xms256m"`,
		},
		{
			name:             "8Gi memory - half is 4096m",
			memory:           "8Gi",
			expectedJVMFlags: `--jvm-flags "-Xmx4096m -Xms4096m"`,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			search := newTestMongoDBSearch("test-search", "default", func(s *searchv1.MongoDBSearch) {
				s.Spec.ResourceRequirements = &corev1.ResourceRequirements{
					Requests: corev1.ResourceList{
						corev1.ResourceMemory: resource.MustParse(tc.memory),
						corev1.ResourceCPU:    resource.MustParse("2"),
					},
				}
			})

			stsModification := CreateSearchStatefulSetFunc(search, "", "", "", "", nil, "mongot:latest")
			sts := statefulset.New(stsModification)

			var script string
			for i := range sts.Spec.Template.Spec.Containers {
				if sts.Spec.Template.Spec.Containers[i].Name == MongotContainerName {
					script = sts.Spec.Template.Spec.Containers[i].Args[1]
					break
				}
			}

			assert.Contains(t, script, tc.expectedJVMFlags,
				"expected args to contain %q, got %q", tc.expectedJVMFlags, script)
		})
	}
}

func TestCreateSearchResourceRequirements(t *testing.T) {
	defaultCPU := construct.ParseQuantityOrZero("2")
	defaultMemory := construct.ParseQuantityOrZero("4G")

	testCases := []struct {
		name             string
		userRequirements *corev1.ResourceRequirements
		expectedCPU      resource.Quantity
		expectedMemory   resource.Quantity
	}{
		{
			name:             "nil requirements - full defaults",
			userRequirements: nil,
			expectedCPU:      defaultCPU,
			expectedMemory:   defaultMemory,
		},
		{
			name: "nil Requests - full defaults",
			userRequirements: &corev1.ResourceRequirements{
				Requests: nil,
			},
			expectedCPU:    defaultCPU,
			expectedMemory: defaultMemory,
		},
		{
			name: "only CPU set - memory defaults",
			userRequirements: &corev1.ResourceRequirements{
				Requests: corev1.ResourceList{
					corev1.ResourceCPU: resource.MustParse("4"),
				},
			},
			expectedCPU:    resource.MustParse("4"),
			expectedMemory: defaultMemory,
		},
		{
			name: "only memory set - CPU defaults",
			userRequirements: &corev1.ResourceRequirements{
				Requests: corev1.ResourceList{
					corev1.ResourceMemory: resource.MustParse("8Gi"),
				},
			},
			expectedCPU:    defaultCPU,
			expectedMemory: resource.MustParse("8Gi"),
		},
		{
			name: "both CPU and memory set - no defaults applied",
			userRequirements: &corev1.ResourceRequirements{
				Requests: corev1.ResourceList{
					corev1.ResourceCPU:    resource.MustParse("8"),
					corev1.ResourceMemory: resource.MustParse("16Gi"),
				},
			},
			expectedCPU:    resource.MustParse("8"),
			expectedMemory: resource.MustParse("16Gi"),
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			result := createSearchResourceRequirements(tc.userRequirements)

			assert.True(t, result.Requests.Cpu().Equal(tc.expectedCPU),
				"expected CPU %s, got %s", tc.expectedCPU.String(), result.Requests.Cpu().String())
			assert.True(t, result.Requests.Memory().Equal(tc.expectedMemory),
				"expected memory %s, got %s", tc.expectedMemory.String(), result.Requests.Memory().String())
		})
	}
}

type containerInfo struct {
	args []string
}
