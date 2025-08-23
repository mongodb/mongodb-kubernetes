package search_controller

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

func TestMongoDBSearchReconcileHelper_ValidateSearchSource(t *testing.T) {
	mdbcMeta := metav1.ObjectMeta{
		Name:      "test-mongodb",
		Namespace: "test",
	}

	cases := []struct {
		name          string
		mdbc          mdbcv1.MongoDBCommunity
		expectedError string
	}{
		{
			name: "Invalid version",
			mdbc: mdbcv1.MongoDBCommunity{
				ObjectMeta: mdbcMeta,
				Spec: mdbcv1.MongoDBCommunitySpec{
					Version: "4.4.0",
				},
			},
			expectedError: "MongoDB version must be 8.0.10 or higher",
		},
		{
			name: "Valid version",
			mdbc: mdbcv1.MongoDBCommunity{
				ObjectMeta: mdbcMeta,
				Spec: mdbcv1.MongoDBCommunitySpec{
					Version: "8.0.10",
				},
			},
		},
		{
			name: "TLS enabled",
			mdbc: mdbcv1.MongoDBCommunity{
				ObjectMeta: mdbcMeta,
				Spec: mdbcv1.MongoDBCommunitySpec{
					Version: "8.0.10",
					Security: mdbcv1.Security{
						TLS: mdbcv1.TLS{
							Enabled: true,
						},
					},
				},
			},
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			db := NewSearchSourceDBResourceFromMongoDBCommunity(&c.mdbc)
			err := ValidateSearchSource(db)
			if c.expectedError == "" {
				assert.NoError(t, err)
			} else {
				assert.EqualError(t, err, c.expectedError)
			}
		})
	}
}

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

			helper := NewMongoDBSearchReconcileHelper(kubernetesClient.NewClient(clientBuilder.Build()), mdbSearch, NewSearchSourceDBResourceFromMongoDBCommunity(mdbc), OperatorSearchConfig{})
			err := helper.ValidateSingleMongoDBSearchForSearchSource(t.Context())
			if c.expectedError == "" {
				assert.NoError(t, err)
			} else {
				assert.EqualError(t, err, c.expectedError)
			}
		})
	}
}
