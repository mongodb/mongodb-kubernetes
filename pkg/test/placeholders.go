package test

import "github.com/mongodb/mongodb-kubernetes/controllers/operator/create"

var MultiClusterAnnotationsWithPlaceholders = map[string]string{
	create.PlaceholderPodIndex:            "{podIndex}",
	create.PlaceholderNamespace:           "{namespace}",
	create.PlaceholderResourceName:        "{resourceName}",
	create.PlaceholderPodName:             "{podName}",
	create.PlaceholderStatefulSetName:     "{statefulSetName}",
	create.PlaceholderExternalServiceName: "{externalServiceName}",
	create.PlaceholderMongodProcessDomain: "{mongodProcessDomain}",
	create.PlaceholderMongodProcessFQDN:   "{mongodProcessFQDN}",
	create.PlaceholderClusterName:         "{clusterName}",
	create.PlaceholderClusterIndex:        "{clusterIndex}",
}

var SingleClusterAnnotationsWithPlaceholders = map[string]string{
	create.PlaceholderPodIndex:            "{podIndex}",
	create.PlaceholderNamespace:           "{namespace}",
	create.PlaceholderResourceName:        "{resourceName}",
	create.PlaceholderPodName:             "{podName}",
	create.PlaceholderStatefulSetName:     "{statefulSetName}",
	create.PlaceholderExternalServiceName: "{externalServiceName}",
	create.PlaceholderMongosProcessDomain: "{mongosProcessDomain}",
	create.PlaceholderMongosProcessFQDN:   "{mongosProcessFQDN}",
}
