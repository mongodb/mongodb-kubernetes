package searchcontroller

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"sigs.k8s.io/controller-runtime/pkg/client"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	searchv1 "github.com/mongodb/mongodb-kubernetes/api/v1/search"
	userv1 "github.com/mongodb/mongodb-kubernetes/api/v1/user"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/mock"
	mdbcv1 "github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/api/v1"
	kubernetesClient "github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/pkg/kube/client"
)

func TestMongoDBSearchReconcileHelper_ValidateSingleMongoDBSearchForSearchSource(t *testing.T) {
	mdbSearchSpec := searchv1.MongoDBSearchSpec{
		Source: &searchv1.MongoDBSource{
			MongoDBResourceRef: &userv1.MongoDBResourceRef{
				Name: "test-mongodb",
			},
		},
	}

	mdbSearch := &searchv1.MongoDBSearch{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-mongodb-search",
			Namespace: "test",
		},
		Spec: mdbSearchSpec,
	}

	mdbc := &mdbcv1.MongoDBCommunity{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-mongodb",
			Namespace: "test",
		},
	}

	cases := []struct {
		name          string
		objects       []*searchv1.MongoDBSearch
		expectedError string
	}{
		{
			name: "No MongoDBSearch",
		},
		{
			name:    "Single MongoDBSearch",
			objects: []*searchv1.MongoDBSearch{mdbSearch},
		},
		{
			name: "Multiple MongoDBSearch",
			objects: []*searchv1.MongoDBSearch{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-mongodb-search-1",
						Namespace: "test",
					},
					Spec: mdbSearchSpec,
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-mongodb-search-2",
						Namespace: "test",
					},
					Spec: mdbSearchSpec,
				},
			},
			expectedError: "Found multiple MongoDBSearch resources for search source 'test-mongodb': test-mongodb-search-1, test-mongodb-search-2",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			clientBuilder := mock.NewEmptyFakeClientBuilder()
			clientBuilder.WithIndex(&searchv1.MongoDBSearch{}, MongoDBSearchIndexFieldName, func(obj client.Object) []string {
				mdbResource := obj.(*searchv1.MongoDBSearch).GetMongoDBResourceRef()
				return []string{mdbResource.Namespace + "/" + mdbResource.Name}
			})
			for _, v := range c.objects {
				// TODO: why doesn't clientBuilder.WithObjects(c.objects...) work?
				clientBuilder.WithObjects(v)
			}

			helper := NewMongoDBSearchReconcileHelper(kubernetesClient.NewClient(clientBuilder.Build()), mdbSearch, NewCommunityResourceSearchSource(mdbc), OperatorSearchConfig{})
			err := helper.ValidateSingleMongoDBSearchForSearchSource(t.Context())
			if c.expectedError == "" {
				assert.NoError(t, err)
			} else {
				assert.EqualError(t, err, c.expectedError)
			}
		})
	}
}

func TestNeedsSearchCoordinatorRolePolyfill(t *testing.T) {
	cases := []struct {
		name     string
		version  string
		expected bool
	}{
		{
			name:     "MongoDB 7.x and below do not require polyfill (unsupported by Search)",
			version:  "7.3.0",
			expected: false,
		},
		{
			name:     "MongoDB 8.0.x requires polyfill",
			version:  "8.0.10",
			expected: true,
		},
		{
			name:     "MongoDB 8.1.x requires polyfill",
			version:  "8.1.0",
			expected: true,
		},
		{
			name:     "MongoDB 8.2.0-rc0 treated as 8.2 (no polyfill)",
			version:  "8.2.0-rc0",
			expected: false,
		},
		{
			name:     "MongoDB 8.2.0 and above do not require polyfill",
			version:  "8.2.0",
			expected: false,
		},
		{
			name:     "MongoDB 8.2.0-ent treated as 8.2 (no polyfill)",
			version:  "8.2.0-ent",
			expected: false,
		},
		{
			name:     "MongoDB 9.0.0 and above do not require polyfill",
			version:  "9.0.0",
			expected: false,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			actual := NeedsSearchCoordinatorRolePolyfill(c.version)
			assert.Equal(t, c.expected, actual)
		})
	}
}
