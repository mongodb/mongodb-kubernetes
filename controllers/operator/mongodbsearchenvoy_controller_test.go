package operator

import (
	"testing"

	searchv1 "github.com/mongodb/mongodb-kubernetes/api/v1/search"
	"github.com/stretchr/testify/assert"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestBuildReplicaSetRoute(t *testing.T) {
	search := &searchv1.MongoDBSearch{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "mdb-search",
			Namespace: "test-ns",
		},
	}

	route := buildReplicaSetRoute(search)

	assert.Equal(t, "rs", route.Name)
	assert.Equal(t, "rs", route.NameSafe)
	assert.Equal(t, "mdb-search-search-lb-svc.test-ns.svc.cluster.local", route.SNIHostname)
	assert.Equal(t, "mdb-search-search-svc.test-ns.svc.cluster.local", route.UpstreamHost)
	assert.Equal(t, int32(27028), route.UpstreamPort)
}
