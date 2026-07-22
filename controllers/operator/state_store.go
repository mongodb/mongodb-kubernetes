package operator

import (
	"context"
	"encoding/json"
	"fmt"

	"go.uber.org/zap"
	"golang.org/x/xerrors"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	v1 "github.com/mongodb/mongodb-kubernetes/api/mongodb/v1"
	"github.com/mongodb/mongodb-kubernetes/pkg/kube"
	kubernetesClient "github.com/mongodb/mongodb-kubernetes/pkg/kube/client"
	"github.com/mongodb/mongodb-kubernetes/pkg/kube/configmap"
)

const (
	stateKey         = "state"
	stateOwnerUIDKey = "ownerUID"
)

// StateStore is a wrapper for a custom, per-resource deployment state required for the operator to reconciler the resource correctly.
// It handles serialization/deserialization of any deployment state structure of type S.
// The deployment state is saved to a config map <resourceName>-state in the resource's namespace in the operator's cluster.
type StateStore[S any] struct {
	namespace       string
	resourceName    string
	ownerLabels     map[string]string
	ownerReferences []metav1.OwnerReference
	ownerUID        string
	client          kubernetesClient.Client

	data                map[string]string
	readOwnerReferences []metav1.OwnerReference
}

// NewStateStore constructs a new instance of the StateStore.
// It is intended to be instantiated with each execution of the Reconcile method and therefore it is not
// designed to be thread safe.
// - owner provides the namespace, name, and labels for the config map
// - ownerReferences are set on the config map to enable garbage collection when the owner is deleted
func NewStateStore[S any](owner v1.ResourceOwner, ownerReferences []metav1.OwnerReference, client kubernetesClient.Client, ownerUID string) *StateStore[S] {
	return &StateStore[S]{
		namespace:       owner.GetNamespace(),
		resourceName:    owner.GetName(),
		ownerLabels:     owner.GetOwnerLabels(),
		ownerReferences: ownerReferences,
		ownerUID:        ownerUID,
		client:          client,
		data:            map[string]string{},
	}
}

func (s *StateStore[S]) read(ctx context.Context) error {
	return s.readWithReader(ctx, s.client)
}

func (s *StateStore[S]) readWithReader(ctx context.Context, reader client.Reader) error {
	cm := corev1.ConfigMap{}
	if err := reader.Get(ctx, kube.ObjectKey(s.namespace, s.getStateConfigMapName()), &cm); err != nil {
		return err
	}

	s.data = cm.Data
	s.readOwnerReferences = append([]metav1.OwnerReference(nil), cm.OwnerReferences...)
	return nil
}

func (s *StateStore[S]) write(ctx context.Context, log *zap.SugaredLogger) error {
	if s.data == nil {
		s.data = map[string]string{}
	}
	if s.ownerUID != "" {
		s.data[stateOwnerUIDKey] = s.ownerUID
	}

	dataCM := configmap.Builder().
		SetName(s.getStateConfigMapName()).
		SetLabels(s.ownerLabels).
		SetOwnerReferences(s.ownerReferences).
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
	return s.ReadStateWithReader(ctx, s.client)
}

func (s *StateStore[S]) ReadStateWithReader(ctx context.Context, reader client.Reader) (*S, error) {
	state := new(S)

	// If we don't find the state ConfigMap, return an error
	if err := s.readWithReader(ctx, reader); err != nil {
		return nil, err
	}
	if s.isStaleOwnerUID() {
		return nil, errors.NewNotFound(schema.GroupResource{}, s.getStateConfigMapName())
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

func (s *StateStore[S]) isStaleOwnerUID() bool {
	if s.ownerUID == "" {
		return false
	}
	recordedUID, ok := s.data[stateOwnerUIDKey]
	if ok {
		return recordedUID != s.ownerUID
	}
	for _, expected := range s.ownerReferences {
		if expected.Controller == nil || !*expected.Controller || string(expected.UID) != s.ownerUID {
			continue
		}
		for _, actual := range s.readOwnerReferences {
			// Released 1.9.x state can have blank GVK fields after typed reads; any populated field must still match.
			if actual.Controller != nil && *actual.Controller &&
				(actual.APIVersion == "" || actual.APIVersion == expected.APIVersion) &&
				(actual.Kind == "" || actual.Kind == expected.Kind) &&
				actual.Name == expected.Name &&
				actual.UID == expected.UID {
				return false
			}
		}
	}
	return true
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
