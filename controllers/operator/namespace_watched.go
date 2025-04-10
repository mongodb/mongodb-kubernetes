package operator

import (
	"os"
	"strings"

	"github.com/10gen/ops-manager-kubernetes/pkg/util"
	"github.com/10gen/ops-manager-kubernetes/pkg/util/env"
	"github.com/10gen/ops-manager-kubernetes/pkg/util/stringutil"
)

// GetWatchedNamespace returns a namespace or namespaces to watch.
//
// If `WATCH_NAMESPACE` has not been set, is an empty string or is a star ("*"), it watches all namespaces.
// If `WATCH_NAMESPACE` is set, it watches over that namespace, unless there are commas in there, in which
// the namespaces to watch will be a comma-separated list.
//
// If `WATCH_NAMESPACE` is '*' it will return []string{""}, which means all namespaces will be watched.
func GetWatchedNamespace() []string {
	watchNamespace, nsSpecified := os.LookupEnv(util.WatchNamespace) // nolint:forbidigo

	// If WatchNamespace is not specified - we assume the Operator is watching all namespaces.
	// In contrast to the common way to configure cluster-wide operators we additionally support '*'
	// see: https://sdk.operatorframework.io/docs/building-operators/golang/operator-scope/#configuring-watch-namespaces-dynamically
	if !nsSpecified || len(watchNamespace) == 0 || strings.TrimSpace(watchNamespace) == "" || strings.TrimSpace(watchNamespace) == "*" {
		return []string{""}
	}

	if strings.Contains(watchNamespace, ",") {
		namespaceSplit := strings.Split(watchNamespace, ",")
		namespaceList := []string{}
		for i := range namespaceSplit {
			namespace := strings.TrimSpace(namespaceSplit[i])
			if namespace != "" {
				namespaceList = append(namespaceList, namespace)
			}
		}

		if stringutil.Contains(namespaceList, "*") {
			// If `WATCH_NAMESPACE` contains a single *, then we return a list
			// of "" as a defensive measure, to avoid cases where
			// WATCH_NAMESPACE could be "*,another-namespace" which could at
			// some point make the Operator traverse "another-namespace" twice.
			return []string{""}
		}

		if len(namespaceList) > 0 {
			return namespaceList
		}

		return []string{env.ReadOrDefault(util.CurrentNamespace, "")} // nolint:forbidigo
	}

	return []string{watchNamespace}
}
