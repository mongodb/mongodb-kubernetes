package multicluster

import (
	"context"
	"net/http"

	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/cluster"
)

var _ cluster.Cluster = &MockedCluster{}

type MockedCluster struct {
	client client.Client
}

func (m *MockedCluster) GetHTTPClient() *http.Client {
	panic("implement me")
}

func (m *MockedCluster) SetFields(interface{}) error {
	return nil
}

func (m *MockedCluster) GetConfig() *rest.Config {
	return nil
}

func (m *MockedCluster) GetScheme() *runtime.Scheme {
	return nil
}

func (m *MockedCluster) GetClient() client.Client {
	return m.client
}

func (m *MockedCluster) GetFieldIndexer() client.FieldIndexer {
	return nil
}

func (m *MockedCluster) GetCache() cache.Cache {
	return nil
}

func (m *MockedCluster) GetEventRecorderFor(name string) record.EventRecorder {
	return nil
}

func (m *MockedCluster) GetRESTMapper() meta.RESTMapper {
	return nil
}

func (m *MockedCluster) GetAPIReader() client.Reader {
	return nil
}

func (m *MockedCluster) Start(ctx context.Context) error {
	return nil
}

func New(client client.Client) *MockedCluster {
	return &MockedCluster{
		client: client,
	}
}
