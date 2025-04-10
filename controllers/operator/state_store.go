package operator

import (
	"context"
	"encoding/json"
	"fmt"

	"go.uber.org/zap"
	"golang.org/x/xerrors"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/mongodb/mongodb-kubernetes-operator/pkg/kube/configmap"

	kubernetesClient "github.com/mongodb/mongodb-kubernetes-operator/pkg/kube/client"
	corev1 "k8s.io/api/core/v1"

	mdbv1 "github.com/10gen/ops-manager-kubernetes/api/v1/mdb"
	"github.com/10gen/ops-manager-kubernetes/controllers/operator/construct"
	"github.com/10gen/ops-manager-kubernetes/pkg/kube"
	"github.com/10gen/ops-manager-kubernetes/pkg/util"
)

const stateKey = "state"

// StateStore is a wrapper for a custom, per-resource deployment state required for the operator to reconciler the resource correctly.
// It handles serialization/deserialization of any deployment state structure of type S.
// The deployment state is saved to a config map <resourceName>-state in the resource's namespace in the operator's cluster.
type StateStore[S any] struct {
	namespace    string
	resourceName string
	client       kubernetesClient.Client

	data map[string]string
}

// NewStateStore constructs a new instance of the StateStore.
// It is intended to be instantiated with each execution of the Reconcile method and therefore it is not
// designed to be thread safe.
func NewStateStore[S any](namespace string, resourceName string, client kubernetesClient.Client) *StateStore[S] {
	return &StateStore[S]{
		namespace:    namespace,
		resourceName: resourceName,
		client:       client,
		data:         map[string]string{},
	}
}

func (s *StateStore[S]) read(ctx context.Context) error {
	cm := corev1.ConfigMap{}
	if err := s.client.Get(ctx, kube.ObjectKey(s.namespace, s.getStateConfigMapName()), &cm); err != nil {
		return err
	}

	s.data = cm.Data
	return nil
}

func (s *StateStore[S]) write(ctx context.Context, log *zap.SugaredLogger) error {
	dataCM := configmap.Builder().
		SetName(s.getStateConfigMapName()).
		SetLabels(map[string]string{
			construct.ControllerLabelName:   util.OperatorName,
			mdbv1.LabelMongoDBResourceOwner: s.resourceName,
		}).
		SetNamespace(s.namespace).
		SetData(s.data).
		Build()

	log.Debugf("Saving deployment state to %s config map: %s", s.getStateConfigMapName(), s.data)
	return configmap.CreateOrUpdate(ctx, s.client, dataCM)
}

func (s *StateStore[S]) getStateConfigMapName() string {
	return fmt.Sprintf("%s-state", s.resourceName)
}

func (s *StateStore[S]) WriteState(ctx context.Context, state *S, log *zap.SugaredLogger) error {
	if err := s.setDataValue(stateKey, state); err != nil {
		return err
	}
	return s.write(ctx, log)
}

func (s *StateStore[S]) ReadState(ctx context.Context) (*S, error) {
	state := new(S)

	// If we don't find the state ConfigMap, return an error
	if err := s.read(ctx); err != nil {
		return nil, err
	}

	// Deserialize the state
	if ok, err := s.getDataValue(stateKey, state); err != nil {
		return nil, err
	} else if !ok {
		// if we don't have state key it's like we don't have state at all
		return nil, errors.NewNotFound(schema.GroupResource{}, s.getStateConfigMapName())
	} else {
		return state, nil
	}
}

func (s *StateStore[S]) getDataValue(key string, obj any) (bool, error) {
	if jsonStr, ok := s.data[key]; !ok {
		return false, nil
	} else {
		if err := json.Unmarshal([]byte(jsonStr), obj); err != nil {
			return true, xerrors.Errorf("cannot unmarshal deployment state %s/%s key %s from the value: %s: %w", s.namespace, s.getStateConfigMapName(), key, jsonStr, err)
		}
	}

	return true, nil
}

func (s *StateStore[S]) setDataValue(key string, value any) error {
	if jsonBytes, err := json.Marshal(value); err != nil {
		return xerrors.Errorf("cannot marshal deployment state %s/%s key %s from the value: %v: %w", s.namespace, s.getStateConfigMapName(), key, value, err)
	} else {
		s.data[key] = string(jsonBytes)
	}

	return nil
}
