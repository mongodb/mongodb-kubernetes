package mapping

import (
	"github.com/10gen/ops-manager-kubernetes/pkg/util"
	"github.com/10gen/ops-manager-kubernetes/pkg/util/env"
	"github.com/10gen/ops-manager-kubernetes/pkg/util/kube"
	"github.com/mongodb/mongodb-kubernetes-operator/pkg/kube/configmap"
)

const VersionToTagConfigMap = "VERSIONS_TO_TAGS_CONFIGMAP"

// GetCustomMappingForVersion checks for the existence of a custom mapping between a given version and a tag.
// It returns the mapped tag or the original one, if no mapping exists
func GetCustomMappingForVersion(getter configmap.Getter, configMapKey string, originalTag string) string {
	customMappingConfigMap, exists := env.Read(VersionToTagConfigMap)
	if exists {
		objectKey := kube.ObjectKey(env.ReadOrDefault(util.CurrentNamespace, ""), customMappingConfigMap)
		mappedTag, err := configmap.ReadFileLikeField(getter, objectKey, configMapKey, originalTag)
		if err == nil {
			return mappedTag
		}
	}
	return originalTag
}
