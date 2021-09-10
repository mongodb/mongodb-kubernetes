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
// If `WATCH_NAMESPACE` has not been set, it watches the same namespace where the Operator is deployed.
//
// If `WATCH_NAMESPACE` has been set to an empty value, `CURRENT_NAMESPACE` is returned instead.
//
// If `WATCH_NAMESPACE` is set, it watches over that namespace, unless there are commas in there, in which
// the namespaces to watch will be a comma-separated list.
//
func GetWatchedNamespace() []string {
	// get watch namespace from environment variable
	watchNamespace, nsSpecified := os.LookupEnv(util.WatchNamespace)

	// if the watch namespace is not specified - we assume the Operator is watching the current namespace
	if !nsSpecified || watchNamespace == "" {
		// the current namespace is expected to be always specified as main.go performs the hard check of this
		return []string{env.ReadOrDefault(util.CurrentNamespace, "")}
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

		return []string{env.ReadOrDefault(util.CurrentNamespace, "")}
	}

	if strings.TrimSpace(watchNamespace) == "*" {
		return []string{""}
	}

	return []string{watchNamespace}
}
