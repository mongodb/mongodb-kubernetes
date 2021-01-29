package wiredtiger

import (
	"math"

	"github.com/mongodb/mongodb-kubernetes-operator/pkg/kube/container"

	"github.com/10gen/ops-manager-kubernetes/pkg/util"
	"github.com/spf13/cast"
	appsv1 "k8s.io/api/apps/v1"
)

// CalculateCache returns the cache that needs to be dedicated to mongodb engine.
// This was fixed in SERVER-16571 so we don't need to enable this for some latest version of mongodb (see the ticket)
func CalculateCache(set appsv1.StatefulSet, containerName, version string) *float32 {
	shouldCalculate, err := util.VersionMatchesRange(version, ">=4.0.0 <4.0.9 || <3.6.13")

	if err != nil || shouldCalculate {
		// Note, that if the limit is 0 then it's not specified in fact (unbounded)
		if memory := container.GetByName(containerName, set.Spec.Template.Spec.Containers).Resources.Limits.Memory(); memory != nil && (*memory).Value() != 0 {
			// Value() returns size in bytes so we need to transform to Gigabytes
			wt := cast.ToFloat64((*memory).Value()) / 1000000000
			// https://docs.mongodb.com/manual/core/wiredtiger/#memory-use
			wt = math.Max((wt-1)*0.5, 0.256)
			// rounding fractional part to 3 digits
			rounded := float32(math.Floor(wt*1000) / 1000)
			return &rounded
		}
	}
	return nil
}
