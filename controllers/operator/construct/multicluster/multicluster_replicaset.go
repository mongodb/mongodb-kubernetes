package multicluster

import (
	"fmt"

	"go.uber.org/zap"

	appsv1 "k8s.io/api/apps/v1"

	mdbv1 "github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/mdb"
	mdbmultiv1 "github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/mdbmulti"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/construct"
	"github.com/mongodb/mongodb-kubernetes/pkg/handler"
	"github.com/mongodb/mongodb-kubernetes/pkg/util"
	"github.com/mongodb/mongodb-kubernetes/pkg/util/merge"
)

// MultiClusterAgentSettingsSource returns the agent settings source of a multi-cluster member: the spec-level
// StatefulSetConfiguration merged with the cluster-level override, as WithStsOverride merges it.
func MultiClusterAgentSettingsSource(mdbm mdbmultiv1.MongoDBMultiCluster, clusterOverride *appsv1.StatefulSetSpec) construct.AgentSettingsSource {
	stsSpec := appsv1.StatefulSetSpec{}
	if mdbm.Spec.StatefulSetConfiguration != nil {
		stsSpec = mdbm.Spec.StatefulSetConfiguration.SpecWrapper.Spec
	}
	if clusterOverride != nil {
		stsSpec = merge.StatefulSetSpecs(stsSpec, *clusterOverride)
	}
	return construct.AgentSettingsSource{Config: &mdbm.Spec.Agent, Override: &stsSpec}
}

func MultiClusterReplicaSetOptions(additionalOpts ...func(options *construct.DatabaseStatefulSetOptions)) func(mdbm mdbmultiv1.MongoDBMultiCluster) construct.DatabaseStatefulSetOptions {
	return func(mdbm mdbmultiv1.MongoDBMultiCluster) construct.DatabaseStatefulSetOptions {
		stsSpec := appsv1.StatefulSetSpec{}
		if mdbm.Spec.StatefulSetConfiguration != nil {
			stsSpec = mdbm.Spec.StatefulSetConfiguration.SpecWrapper.Spec
		}
		opts := construct.DatabaseStatefulSetOptions{
			Name:                          mdbm.Name,
			ServicePort:                   mdbm.Spec.GetAdditionalMongodConfig().GetPortOrDefault(),
			Persistent:                    mdbm.Spec.Persistent,
			AgentConfig:                   &mdbm.Spec.Agent,
			PodSpec:                       construct.NewDefaultPodSpecWrapper(*mdbv1.NewMongoDbPodSpec()),
			Labels:                        mdbm.Labels,
			StsLabels:                     mdbm.GetOwnerLabels(),
			OwnerReference:                nil,
			MultiClusterMode:              true,
			HostNameOverrideConfigmapName: mdbm.GetHostNameOverrideConfigmapName(),
			StatefulSetSpecOverride:       &stsSpec,
			StsType:                       construct.MultiReplicaSet,
			DownloadBase:                  mdbm.Spec.GetDownloadBase(),
			Annotations:                   handler.MultiClusterStatefulSetAnnotations(mdbm.Name),
		}
		for _, opt := range additionalOpts {
			opt(&opts)
		}

		return opts
	}
}

func WithServiceName(serviceName string) func(options *construct.DatabaseStatefulSetOptions) {
	return func(options *construct.DatabaseStatefulSetOptions) {
		options.ServiceName = serviceName
	}
}

func WithClusterNum(clusterNum int) func(options *construct.DatabaseStatefulSetOptions) {
	return func(options *construct.DatabaseStatefulSetOptions) {
		options.StatefulSetNameOverride = statefulSetName(options.Name, clusterNum)
	}
}

func WithMemberCount(memberCount int) func(options *construct.DatabaseStatefulSetOptions) {
	return func(options *construct.DatabaseStatefulSetOptions) {
		options.Replicas = memberCount
	}
}

func WithStsOverride(stsOverride *appsv1.StatefulSetSpec) func(options *construct.DatabaseStatefulSetOptions) {
	return func(options *construct.DatabaseStatefulSetOptions) {
		options.StatefulSetSpecOverride = new(merge.StatefulSetSpecs(*options.StatefulSetSpecOverride, *stsOverride))
	}
}

func statefulSetName(mdbmName string, clusterNum int) string {
	return fmt.Sprintf("%s-%d", mdbmName, clusterNum)
}

func PodLabel(mdbmName string) map[string]string {
	return map[string]string{
		util.OperatorLabelName:            util.OperatorLabelValue,
		construct.PodAntiAffinityLabelKey: mdbmName,
	}
}

func MultiClusterStatefulSet(mdbm mdbmultiv1.MongoDBMultiCluster, stsOptFunc func(mdbm mdbmultiv1.MongoDBMultiCluster) construct.DatabaseStatefulSetOptions, log *zap.SugaredLogger) appsv1.StatefulSet {
	stsOptions := stsOptFunc(mdbm)
	return construct.DatabaseStatefulSetHelper(&mdbm, &stsOptions, log)
}

func MultiClusterStatefulSetFromOptions(mdbm mdbmultiv1.MongoDBMultiCluster, stsOptions construct.DatabaseStatefulSetOptions, log *zap.SugaredLogger) appsv1.StatefulSet {
	return construct.DatabaseStatefulSetHelper(&mdbm, &stsOptions, log)
}
