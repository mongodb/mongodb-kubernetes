package setup

import (
	"github.com/10gen/ops-manager-kubernetes/mongodb-community-operator/controllers/construct"
	"github.com/10gen/ops-manager-kubernetes/mongodb-community-operator/pkg/util/envvar"
)

const (
	testNamespaceEnvName            = "WATCH_NAMESPACE"
	testCertManagerNamespaceEnvName = "TEST_CERT_MANAGER_NAMESPACE"
	testCertManagerVersionEnvName   = "TEST_CERT_MANAGER_VERSION"
	operatorImageRepoEnvName        = "BASE_REPO_URL"
	clusterWideEnvName              = "CLUSTER_WIDE"
	performCleanupEnvName           = "PERFORM_CLEANUP"
	LocalOperatorEnvName            = "LOCAL_OPERATOR"
	versionIdEnv                    = "VERSION_ID"
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
	MongoDBImage            string
	MongoDBRepoUrl          string
	LocalOperator           bool
	OperatorImageRepoUrl    string
	OperatorVersion         string
	OperatorImage           string
}

func LoadTestConfigFromEnv() TestConfig {
	return TestConfig{
		OperatorImage: "mongodb-enterprise-operator-ubi",
		Namespace:     envvar.GetEnvOrDefault(testNamespaceEnvName, "mongodb"), // nolint:forbidigo
		// The operator version is based on the versionID, which context sets either locally manually or evg per patch
		OperatorVersion:      envvar.GetEnvOrDefault(versionIdEnv, ""),                                // nolint:forbidigo
		CertManagerNamespace: envvar.GetEnvOrDefault(testCertManagerNamespaceEnvName, "cert-manager"), // nolint:forbidigo
		CertManagerVersion:   envvar.GetEnvOrDefault(testCertManagerVersionEnvName, "v1.5.3"),         // nolint:forbidigo
		OperatorImageRepoUrl: envvar.GetEnvOrDefault(operatorImageRepoEnvName, "quay.io/mongodb"),     // nolint:forbidigo
		// TODO: MCK
		MongoDBImage:            envvar.GetEnvOrDefault("MDB_COMMUNITY_IMAGE", "mongodb-community-server"),                                                                         // nolint:forbidigo
		MongoDBRepoUrl:          envvar.GetEnvOrDefault(construct.MongodbRepoUrlEnv, "quay.io/mongodb"),                                                                            // nolint:forbidigo
		VersionUpgradeHookImage: envvar.GetEnvOrDefault(construct.VersionUpgradeHookImageEnv, "quay.io/mongodb/mongodb-kubernetes-operator-version-upgrade-post-start-hook:1.0.2"), // nolint:forbidigo
		// TODO: MCK better way to decide default agent image.
		AgentImage:          envvar.GetEnvOrDefault("MDB_COMMUNITY_AGENT_IMAGE", "quay.io/mongodb/mongodb-agent-ubi:108.0.2.8729-1"),             // nolint:forbidigo
		ClusterWide:         envvar.ReadBool(clusterWideEnvName),                                                                                 // nolint:forbidigo
		PerformCleanup:      envvar.ReadBool(performCleanupEnvName),                                                                              // nolint:forbidigo
		ReadinessProbeImage: envvar.GetEnvOrDefault(construct.ReadinessProbeImageEnv, "quay.io/mongodb/mongodb-kubernetes-readinessprobe:1.0.3"), // nolint:forbidigo
		HelmChartPath:       "../../../../helm_chart",                                                                                            // TODO: MCK update this later once we change folder or choose a different solution, alternatives, copy helm chart to test folder/search for helm_chart folder
		LocalOperator:       envvar.ReadBool(LocalOperatorEnvName),                                                                               // nolint:forbidigo // TODO MCK: combine with meko one
	}
}
