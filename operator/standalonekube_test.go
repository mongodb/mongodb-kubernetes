package operator

import (
	"testing"

	"github.com/10gen/ops-manager-kubernetes/pkg/apis/mongodb.com/v1"
	"github.com/10gen/ops-manager-kubernetes/util"
	"github.com/stretchr/testify/assert"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestCreateOmProcess(t *testing.T) {
	process := createProcess(defaultSetHelper().BuildStatefulSet(), DefaultStandaloneBuilder().Build())

	// Note, that for standalone the name of process is the name of statefulset - not the pod inside it.
	assert.Equal(t, "dublin", process.Name())
	assert.Equal(t, "dublin-0.test-service.mongodb.svc.cluster.local", process.HostName())
	assert.Equal(t, "4.0.0", process.Version())
}

type StandaloneBuilder struct {
	*v1.MongoDbStandalone
}

func DefaultStandaloneBuilder() *StandaloneBuilder {
	spec := &v1.MongoDbStandaloneSpec{
		Version:     "4.0.0",
		Persistent:  util.BooleanRef(false),
		Project:     "my-project",
		Credentials: "my-credentials",
	}
	standalone := &v1.MongoDbStandalone{
		ObjectMeta: metav1.ObjectMeta{Name: "dublin", Namespace: "mongodb"},
		Spec:       *spec}
	return &StandaloneBuilder{standalone}
}

func (b *StandaloneBuilder) SetName(name string) *StandaloneBuilder {
	b.Name = name
	return b
}
func (b *StandaloneBuilder) SetVersion(version string) *StandaloneBuilder {
	b.Spec.Version = version
	return b
}
func (b *StandaloneBuilder) SetPersistent(p *bool) *StandaloneBuilder {
	b.Spec.Persistent = p
	return b
}
func (b *StandaloneBuilder) Build() *v1.MongoDbStandalone {
	return b.MongoDbStandalone
}
