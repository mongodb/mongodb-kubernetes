package setup

import (
	"github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/controllers/construct"
	"github.com/mongodb/mongodb-kubernetes/pkg/util"
	"github.com/mongodb/mongodb-kubernetes/pkg/util/env"
)

const (
	testNamespaceEnvName            = "WATCH_NAMESPACE"
	testCertManagerNamespaceEnvName = "TEST_CERT_MANAGER_NAMESPACE"
	testCertManagerVersionEnvName   = "TEST_CERT_MANAGER_VERSION"
	operatorImageRepoEnvName        = "OPERATOR_REGISTRY"
	clusterWideEnvName              = "CLUSTER_WIDE"
	performCleanupEnvName           = "PERFORM_CLEANUP"
	LocalOperatorEnvName            = "LOCAL_OPERATOR"
	operatorVersionEnvName          = "OPERATOR_VERSION"
)

type TestConfig struct {
	Namespace               string
	CertManagerNamespace    string
	CertManagerVersion      string
	VersionUpgradeHookImage string
	ClusterWide             bool
	PerformCleanup          bool
	AgentImage              string
	ReadinessProbeImage     string
	HelmChartPath           string
	TestPKIChartPath        string
	MongoDBImage            string
	MongoDBRepoUrl          string
	LocalOperator           bool
	OperatorImageRepoUrl    string
	OperatorVersion         string
	OperatorImage           string
}

func LoadTestConfigFromEnv() TestConfig {
	return TestConfig{
		OperatorImage: "mongodb-kubernetes",
		Namespace:     env.ReadOrDefault(testNamespaceEnvName, "mongodb-test"), // nolint:forbidigo
		// The operator version is based on the versionID, which context sets either locally manually or evg per patch
		OperatorVersion:      env.ReadOrDefault(operatorVersionEnvName, ""),                      // nolint:forbidigo
		CertManagerNamespace: env.ReadOrDefault(testCertManagerNamespaceEnvName, "cert-manager"), // nolint:forbidigo
		CertManagerVersion:   env.ReadOrDefault(testCertManagerVersionEnvName, "v1.5.3"),         // nolint:forbidigo
		OperatorImageRepoUrl: env.ReadOrDefault(operatorImageRepoEnvName, "quay.io/mongodb"),     // nolint:forbidigo
		// TODO: MCK
		MongoDBImage:            env.ReadOrDefault("MDB_COMMUNITY_IMAGE", "mongodb-community-server"),                                                                         // nolint:forbidigo
		MongoDBRepoUrl:          env.ReadOrDefault(util.MongodbRepoUrlEnv, "quay.io/mongodb"),                                                                                 // nolint:forbidigo
		VersionUpgradeHookImage: env.ReadOrDefault(construct.VersionUpgradeHookImageEnv, "quay.io/mongodb/mongodb-kubernetes-operator-version-upgrade-post-start-hook:1.0.2"), // nolint:forbidigo
		// TODO: MCK better way to decide default agent image.
		AgentImage:          env.ReadOrDefault("MDB_COMMUNITY_AGENT_IMAGE", "quay.io/mongodb/mongodb-agent:108.0.23.8997-1"),                // nolint:forbidigo
		ClusterWide:         env.ReadBoolOrDefault(clusterWideEnvName, false),                                                               // nolint:forbidigo
		PerformCleanup:      env.ReadBoolOrDefault(performCleanupEnvName, false),                                                            // nolint:forbidigo
		ReadinessProbeImage: env.ReadOrDefault(construct.ReadinessProbeImageEnv, "quay.io/mongodb/mongodb-kubernetes-readinessprobe:1.0.3"), // nolint:forbidigo
		HelmChartPath:       "../../../../helm_chart",                                                                                       // TODO: MCK update this later once we change folder or choose a different solution, alternatives, copy helm chart to test folder/search for helm_chart folder
		TestPKIChartPath:    "../../test-pki",
		LocalOperator:       env.ReadBoolOrDefault(LocalOperatorEnvName, false), // nolint:forbidigo // TODO MCK: combine with meko one
	}
}
