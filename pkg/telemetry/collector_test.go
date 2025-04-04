package telemetry

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"k8s.io/apimachinery/pkg/types"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	kubeclient "sigs.k8s.io/controller-runtime/pkg/client"

	mcov1 "github.com/10gen/ops-manager-kubernetes/mongodb-community-operator/api/v1"
)

type MockClient struct {
	kubeclient.Client
	MockList func(ctx context.Context, list kubeclient.ObjectList, opts ...kubeclient.ListOption) error
}

func (m *MockClient) List(ctx context.Context, list kubeclient.ObjectList, opts ...kubeclient.ListOption) error {
	return m.MockList(ctx, list, opts...)
}

func TestAddCommunityEvents(t *testing.T) {
	operatorUUID := "test-operator-uuid"

	// Those 2 cases are when a customer uses Community reconciler to deploy enterprise or community MDB image
	testCases := []struct {
		name         string
		mongodbImage string
		isEnterprise bool
	}{
		{
			name:         "With community image",
			mongodbImage: "mongodb-community-server",
			isEnterprise: false,
		},
		{
			name:         "With enterprise image",
			mongodbImage: "mongodb-enterprise-server",
			isEnterprise: true,
		},
	}

	now := time.Now()

	for _, tc := range testCases {
		t.Run("With community resources", func(t *testing.T) {
			communityList := &mcov1.MongoDBCommunityList{
				Items: []mcov1.MongoDBCommunity{
					{
						ObjectMeta: metav1.ObjectMeta{
							UID:  types.UID("community-1"),
							Name: "test-community-1",
						},
					},
					{
						ObjectMeta: metav1.ObjectMeta{
							UID:  types.UID("community-2"),
							Name: "test-community-2",
						},
					},
				},
			}

			mockClient := &MockClient{
				MockList: func(ctx context.Context, list kubeclient.ObjectList, opts ...kubeclient.ListOption) error {
					if l, ok := list.(*mcov1.MongoDBCommunityList); ok {
						*l = *communityList
					}
					return nil
				},
			}

			events := addCommunityEvents(context.Background(), mockClient, operatorUUID, tc.mongodbImage, now)

			assert.Len(t, events, 2, "Should return 2 events for 2 community resources")

			assert.Equal(t, now, events[0].Timestamp)
			assert.Equal(t, Deployments, events[0].Source)
			assert.Equal(t, "community-1", events[0].Properties["deploymentUID"])
			assert.Equal(t, operatorUUID, events[0].Properties["operatorID"])
			assert.Equal(t, false, events[0].Properties["isMultiCluster"])
			assert.Equal(t, "Community", events[0].Properties["type"])
			assert.Equal(t, tc.isEnterprise, events[0].Properties["isRunningEnterpriseImage"])

			assert.Equal(t, "community-2", events[1].Properties["deploymentUID"])
			assert.Equal(t, tc.isEnterprise, events[1].Properties["isRunningEnterpriseImage"])
		})

		t.Run("With list error", func(t *testing.T) {
			mockClient := &MockClient{
				MockList: func(ctx context.Context, list kubeclient.ObjectList, opts ...kubeclient.ListOption) error {
					return errors.New("list error")
				},
			}

			events := addCommunityEvents(context.Background(), mockClient, operatorUUID, tc.mongodbImage, now)

			assert.Empty(t, events, "Should return empty slice on list error")
		})

		t.Run("With empty list", func(t *testing.T) {
			mockClient := &MockClient{
				MockList: func(ctx context.Context, list kubeclient.ObjectList, opts ...kubeclient.ListOption) error {
					if l, ok := list.(*mcov1.MongoDBCommunityList); ok {
						*l = mcov1.MongoDBCommunityList{}
					}
					return nil
				},
			}

			events := addCommunityEvents(context.Background(), mockClient, operatorUUID, tc.mongodbImage, now)

			assert.Empty(t, events, "Should return empty slice for empty community list")
		})
	}
}
