package controlledfeature

import (
	"sort"

	mdbv1 "github.com/mongodb/mongodb-kubernetes/api/v1/mdb"
	"github.com/mongodb/mongodb-kubernetes/pkg/util/stringutil"
)

// buildFeatureControlsByMdb builds the controlled feature by MongoDB resource
func buildFeatureControlsByMdb(mdb mdbv1.MongoDB) *ControlledFeature {
	var cf *ControlledFeature
	controlledFeatures := []func(*ControlledFeature){OptionExternallyManaged}
	controlledFeatures = append(controlledFeatures, authentication(mdb)...)
	controlledFeatures = append(controlledFeatures, mongodbParams(mdb)...)
	controlledFeatures = append(controlledFeatures, mdbVersion()...)

	cf = newControlledFeature(controlledFeatures...)

	return cf
}

// mongodbParams enables mongodb options policy if any of additional mongod configurations are specified for MongoDB spec
func mongodbParams(mdb mdbv1.MongoDB) []func(*ControlledFeature) {
	var disabledMongodbParams []string
	disabledMongodbParams = append(disabledMongodbParams, mdb.Spec.AdditionalMongodConfig.ToFlatList()...)
	if mdb.Spec.MongosSpec != nil {
		disabledMongodbParams = append(disabledMongodbParams, mdb.Spec.MongosSpec.AdditionalMongodConfig.ToFlatList()...)
	}
	if mdb.Spec.ConfigSrvSpec != nil {
		disabledMongodbParams = append(disabledMongodbParams, mdb.Spec.ConfigSrvSpec.AdditionalMongodConfig.ToFlatList()...)
	}
	if mdb.Spec.ShardSpec != nil {
		disabledMongodbParams = append(disabledMongodbParams, mdb.Spec.ShardSpec.AdditionalMongodConfig.ToFlatList()...)
	}
	if len(disabledMongodbParams) > 0 {
		// We need to ensure no duplicates
		var deduplicatedParams []string
		for _, v := range disabledMongodbParams {
			if !stringutil.Contains(deduplicatedParams, v) {
				deduplicatedParams = append(deduplicatedParams, v)
			}
		}
		sort.Strings(deduplicatedParams)
		return []func(*ControlledFeature){OptionDisableMongodbConfig(deduplicatedParams)}
	}
	return []func(*ControlledFeature){}
}

// authentication enables authentication feature only if Authentication is enabled in mdb
func authentication(mdb mdbv1.MongoDB) []func(*ControlledFeature) {
	if mdb.Spec.Security.Authentication != nil {
		return []func(*ControlledFeature){OptionDisableAuthenticationMechanism}
	}
	return []func(*ControlledFeature){}
}

func mdbVersion() []func(*ControlledFeature) {
	return []func(*ControlledFeature){OptionDisableMongodbVersion}
}
