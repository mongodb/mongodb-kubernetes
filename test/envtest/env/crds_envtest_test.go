package env_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"

	"github.com/mongodb/mongodb-kubernetes/test/envtest/env"
)

func TestMain(m *testing.M) {
	os.Exit(env.RunShared(m))
}

func TestCRDsAreEstablishedAndStructural(t *testing.T) {
	ctx := context.Background()
	shared := env.Shared(t)

	scheme := runtime.NewScheme()
	require.NoError(t, apiextensionsv1.AddToScheme(scheme))
	apiextensionsClient, err := client.New(shared.Config, client.Options{Scheme: scheme})
	require.NoError(t, err)

	crdNames := []string{
		"mongodb.mongodb.com",
		"mongodbusers.mongodb.com",
		"opsmanagers.mongodb.com",
		"mongodbmulticluster.mongodb.com",
		"clustermongodbroles.mongodb.com",
	}

	for _, name := range crdNames {
		t.Run(name, func(t *testing.T) {
			crd := &apiextensionsv1.CustomResourceDefinition{}
			require.Eventually(t, func() bool {
				if err := apiextensionsClient.Get(ctx, client.ObjectKey{Name: name}, crd); err != nil {
					return false
				}
				for _, condition := range crd.Status.Conditions {
					if condition.Type == apiextensionsv1.Established && condition.Status == apiextensionsv1.ConditionTrue {
						return true
					}
				}
				return false
			}, 30*time.Second, 100*time.Millisecond, "CRD %s was not established", name)

			for _, condition := range crd.Status.Conditions {
				if condition.Type == apiextensionsv1.NonStructuralSchema {
					assert.NotEqual(t, apiextensionsv1.ConditionTrue, condition.Status, "CRD %s is not structural: %s", name, condition.Message)
				}
			}
		})
	}
}
