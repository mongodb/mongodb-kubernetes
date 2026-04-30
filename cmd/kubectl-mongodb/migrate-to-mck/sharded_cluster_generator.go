package migratetomck

import (
	"fmt"
	"strings"

	"sigs.k8s.io/controller-runtime/pkg/client"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8svalidation "k8s.io/apimachinery/pkg/util/validation"

	mdbv1 "github.com/mongodb/mongodb-kubernetes/api/v1/mdb"
	"github.com/mongodb/mongodb-kubernetes/api/v1/status"
	"github.com/mongodb/mongodb-kubernetes/controllers/om"
	"github.com/mongodb/mongodb-kubernetes/pkg/util"
)

func generateShardedCluster(ac *om.AutomationConfig, opts GenerateOptions) (client.Object, string, error) {
	sc := ac.Deployment.GetShardedClusters()[0]
	shards := sc.Shards()
	if len(shards) == 0 {
		return nil, "", fmt.Errorf("sharded cluster %q has no shards", sc.Name())
	}
	configRsName := sc.ConfigServerRsName()
	rsMap := buildReplicaSetMap(ac.Deployment.GetReplicaSets())
	processMap := ac.Deployment.ProcessMap()

	scName := sc.Name()
	resourceName := opts.ResourceNameOverride
	if resourceName == "" {
		resourceName = util.NormalizeName(scName)
		if resourceName == "" {
			return nil, "", fmt.Errorf("sharded cluster name %q cannot be normalized to a valid Kubernetes resource name. Use --resource-name-override to provide one", scName)
		}
	}
	if errs := k8svalidation.IsDNS1123Subdomain(resourceName); len(errs) > 0 {
		return nil, "", fmt.Errorf("resource name %q is not a valid Kubernetes resource name: %s", resourceName, errs[0])
	}

	configRS, ok := rsMap[configRsName]
	if !ok {
		return nil, "", fmt.Errorf("config server replica set %q not found in replicaSets", configRsName)
	}

	shardRSes := make([]om.ReplicaSet, 0, len(shards))
	for _, shard := range shards {
		rs, ok := rsMap[shard.Rs()]
		if !ok {
			return nil, "", fmt.Errorf("shard %q replica set %q not found in replicaSets", shard.Id(), shard.Rs())
		}
		shardRSes = append(shardRSes, rs)
	}

	var mongosProcs []om.Process
	for _, proc := range ac.Deployment.GetProcesses() {
		if proc.ProcessType() != om.ProcessTypeMongos {
			continue
		}
		if proc.IsDisabled() {
			continue
		}
		mongosProcs = append(mongosProcs, proc)
	}

	_, version, fcv := om.ExtractMemberInfo(shardRSes[0].Members(), processMap)

	externalMembers := buildShardedExternalMembers(configRS, shardRSes, mongosProcs, processMap)

	spec, err := buildShardedClusterSpec(ac, opts, resourceName, version, fcv, configRS, shardRSes, mongosProcs, shards, externalMembers)
	if err != nil {
		return nil, "", fmt.Errorf("failed to build MongoDB spec: %w", err)
	}
	cr := &mdbv1.MongoDB{
		TypeMeta:   metav1.TypeMeta{APIVersion: "mongodb.com/v1", Kind: "MongoDB"},
		ObjectMeta: buildCRObjectMeta(resourceName, opts.Namespace),
		Spec:       spec,
	}
	return &yamlCommentCarrier{
		Object:      cr,
		specComment: buildNameOverrides(cr, configRsName, shards),
	}, resourceName, nil
}

// buildNameOverrides aggregates the commented out spec.* override blocks until the CRD adds them as real fields.
func buildNameOverrides(cr *mdbv1.MongoDB, configRsName string, shards []om.Shard) string {
	return buildConfigServerNameOverride(cr, configRsName) + buildShardOverrides(cr, shards)
}

// buildConfigServerNameOverride builds a commented out configServerNameOverride line (field not yet in CRD).
// Returns an empty string when the AC's config-server replica set already matches the operator's default name.
func buildConfigServerNameOverride(cr *mdbv1.MongoDB, configRsName string) string {
	if configRsName == cr.ConfigRsName() {
		return ""
	}
	return fmt.Sprintf("  # configServerNameOverride: %q\n", configRsName)
}

func buildShardedClusterSpec(ac *om.AutomationConfig, opts GenerateOptions, resourceName, version, fcv string, configRS om.ReplicaSet, shardRSes []om.ReplicaSet, mongosProcs []om.Process, shards []om.Shard, externalMembers []mdbv1.ExternalMember) (mdbv1.MongoDbSpec, error) {
	security, err := buildSecurity(ac, opts.CertsSecretPrefix, resourceName)
	if err != nil {
		return mdbv1.MongoDbSpec{}, fmt.Errorf("failed to build security config: %w", err)
	}
	if roles := ac.Deployment.GetRoles(); len(roles) > 0 {
		if security == nil {
			security = &mdbv1.Security{}
		}
		security.Roles = roles
	}

	prom, err := extractPrometheusConfig(ac.Deployment)
	if err != nil {
		return mdbv1.MongoDbSpec{}, fmt.Errorf("failed to extract Prometheus config: %w", err)
	}
	if prom != nil && opts.PrometheusSecretName != "" {
		prom.PasswordSecretRef.Name = opts.PrometheusSecretName
	}

	var additionalConfig *mdbv1.AdditionalMongodConfig
	if opts.SourceProcess != nil {
		additionalConfig = opts.SourceProcess.AdditionalMongodConfig()
	}

	var featureCompatibilityVersion *string
	if fcv != "" {
		featureCompatibilityVersion = &fcv
	}

	return mdbv1.MongoDbSpec{
		DbCommonSpec: mdbv1.DbCommonSpec{
			Version:                     version,
			ResourceType:                mdbv1.ShardedCluster,
			FeatureCompatibilityVersion: featureCompatibilityVersion,
			ConnectionSpec: mdbv1.ConnectionSpec{
				SharedConnectionSpec: mdbv1.SharedConnectionSpec{
					OpsManagerConfig: &mdbv1.PrivateCloudConfig{
						ConfigMapRef: mdbv1.ConfigMapRef{Name: opts.ConfigMapName},
					},
				},
				Credentials: opts.CredentialsSecretName,
			},
			ExternalMembers:        externalMembers,
			Security:               security,
			Prometheus:             prom,
			AdditionalMongodConfig: additionalConfig,
			Agent:                  extractAgentConfig(opts.SourceProcess, opts.ProjectConfigs),
		},
		ShardedClusterSpec: mdbv1.ShardedClusterSpec{
			// ShardOverrides intentionally empty. shardNameOverrides is written
			// as the commented out block returned alongside this spec, since the
			// CRD does not yet support the field.
		},
		MongodbShardedClusterSizeConfig: status.MongodbShardedClusterSizeConfig{
			ShardCount:           len(shards),
			MongodsPerShardCount: len(shardRSes[0].Members()),
			ConfigServerCount:    len(configRS.Members()),
			MongosCount:          len(mongosProcs),
		},
	}, nil
}

// buildShardedExternalMembers assembles the externalMembers list: config server, then shards, then mongos.
func buildShardedExternalMembers(
	configRS om.ReplicaSet,
	shardRSes []om.ReplicaSet,
	mongosProcs []om.Process,
	processMap map[string]om.Process,
) []mdbv1.ExternalMember {
	var members []mdbv1.ExternalMember

	configMembers, _, _ := om.ExtractMemberInfo(configRS.Members(), processMap)
	members = append(members, configMembers...)

	for _, rs := range shardRSes {
		shardMembers, _, _ := om.ExtractMemberInfo(rs.Members(), processMap)
		members = append(members, shardMembers...)
	}

	members = append(members, om.ExtractExternalMembers(mongosProcs)...)

	return members
}

// buildShardOverrides builds a commented out shardNameOverrides block (field not yet in CRD).
// shardName follows the operator's {resource}-{index} pattern, ordered by shard index in [0, shardCount).
// When shard id equals replicaset name, only shardName is written, since the operator can recover
// both from the AC at the same index.
func buildShardOverrides(cr *mdbv1.MongoDB, shards []om.Shard) string {
	var b strings.Builder
	b.WriteString("  # shardNameOverrides:\n")
	for i, shard := range shards {
		shardName := cr.ShardRsName(i)
		if shard.Id() == shard.Rs() {
			fmt.Fprintf(&b, "  #   - shardName: %q\n", shardName)
			continue
		}
		fmt.Fprintf(&b, "  #   - shardId: %q\n", shard.Id())
		fmt.Fprintf(&b, "  #     replicasetName: %q\n", shard.Rs())
		fmt.Fprintf(&b, "  #     shardName: %q\n", shardName)
	}
	return b.String()
}

// buildReplicaSetMap indexes replica sets by name for O(1) lookup.
func buildReplicaSetMap(rsList []om.ReplicaSet) map[string]om.ReplicaSet {
	m := make(map[string]om.ReplicaSet, len(rsList))
	for _, rs := range rsList {
		m[rs.Name()] = rs
	}
	return m
}
