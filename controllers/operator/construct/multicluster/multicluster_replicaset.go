package multicluster

import (
	"fmt"

	"github.com/mongodb/mongodb-kubernetes-operator/pkg/util/merge"

	appsv1 "k8s.io/api/apps/v1"

	mdbv1 "github.com/10gen/ops-manager-kubernetes/api/v1/mdb"
	mdbmultiv1 "github.com/10gen/ops-manager-kubernetes/api/v1/mdbmulti"
	"github.com/10gen/ops-manager-kubernetes/controllers/operator/certs"
	"github.com/10gen/ops-manager-kubernetes/controllers/operator/construct"
	"github.com/10gen/ops-manager-kubernetes/pkg/handler"
	"github.com/10gen/ops-manager-kubernetes/pkg/util"
)

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
			Labels:                        mdbm.GetOwnerLabels(),
			MultiClusterMode:              true,
			HostNameOverrideConfigmapName: mdbm.GetHostNameOverrideConfigmapName(),
			StatefulSetSpecOverride:       &stsSpec,
			StsType:                       construct.MultiReplicaSet,
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
		finalSpec := merge.StatefulSetSpecs(*options.StatefulSetSpecOverride, *stsOverride)
		options.StatefulSetSpecOverride = &finalSpec
	}
}

func WithAnnotations(resourceName string, certHash string) func(options *construct.DatabaseStatefulSetOptions) {
	return func(options *construct.DatabaseStatefulSetOptions) {
		options.Annotations = statefulSetAnnotations(resourceName, certHash)
	}
}

func statefulSetName(mdbmName string, clusterNum int) string {
	return fmt.Sprintf("%s-%d", mdbmName, clusterNum)
}

func statefulSetAnnotations(mdbmName string, certHash string) map[string]string {
	return map[string]string{
		handler.MongoDBMultiResourceAnnotation: mdbmName,
		certs.CertHashAnnotationKey:            certHash,
	}
}

func PodLabel(mdbmName string) map[string]string {
	return map[string]string{
		util.OperatorLabelName:            util.OperatorName,
		construct.PodAntiAffinityLabelKey: mdbmName,
	}
}

func MultiClusterStatefulSet(mdbm mdbmultiv1.MongoDBMultiCluster, stsOptFunc func(mdbm mdbmultiv1.MongoDBMultiCluster) construct.DatabaseStatefulSetOptions) appsv1.StatefulSet {
	stsOptions := stsOptFunc(mdbm)
	dbSts := construct.DatabaseStatefulSetHelper(&mdbm, &stsOptions, nil)

	if len(stsOptions.Annotations) > 0 {
		dbSts.Annotations = merge.StringToStringMap(dbSts.Annotations, stsOptions.Annotations)
	}

	if len(stsOptions.Labels) > 0 {
		dbSts.Labels = merge.StringToStringMap(dbSts.Labels, stsOptions.Labels)
	}

	if stsOptions.StatefulSetSpecOverride != nil {
		dbSts.Spec = merge.StatefulSetSpecs(dbSts.Spec, *stsOptions.StatefulSetSpecOverride)
	}

	return dbSts
}
