package searchcontroller

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"

	"golang.org/x/xerrors"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	v1 "github.com/mongodb/mongodb-kubernetes/api/mongodb/v1"
	searchv1 "github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/search"
	"github.com/mongodb/mongodb-kubernetes/pkg/kube"
	kubernetesClient "github.com/mongodb/mongodb-kubernetes/pkg/kube/client"
	"github.com/mongodb/mongodb-kubernetes/pkg/kube/configmap"
)

// searchStateKey is the data key holding the JSON state inside the state ConfigMap.
const searchStateKey = "state"

type SearchDeploymentState struct {
	// RoutingReadyMongotGroups is the one-way routing-ready switch: the set of
	// shard names whose mongot group has EVER met the routing-readiness threshold;
	// a shard is pending iff it is not listed here. Pruned only when a shard no
	// longer exists.
	RoutingReadyMongotGroups []string `json:"routingReadyMongotGroups,omitempty"`
}

func NewSearchDeploymentState() *SearchDeploymentState {
	return &SearchDeploymentState{}
}

// SearchStateCMName returns the search controllers' state ConfigMap name — the
// single place that knows it. Deliberately NOT "<name>-state": that is the source
// MongoDB's StateStore ConfigMap, and a MongoDBSearch commonly shares its source's
// name, so the suffixes must not collide.
func SearchStateCMName(search *searchv1.MongoDBSearch) string {
	return fmt.Sprintf("%s-search-state", search.Name)
}

// searchStateFromCM unmarshals the state key; a missing key yields fresh state.
func searchStateFromCM(cm *corev1.ConfigMap) (*SearchDeploymentState, error) {
	state := NewSearchDeploymentState()
	if raw, ok := cm.Data[searchStateKey]; ok {
		if err := json.Unmarshal([]byte(raw), state); err != nil {
			return nil, xerrors.Errorf("cannot unmarshal search state %s/%s: %w", cm.Namespace, cm.Name, err)
		}
	}
	return state, nil
}

// ReadSearchState reads the per-CR state ConfigMap, treating NotFound as fresh
// state. Strictly read-only: it never creates or updates the ConfigMap, so it is
// safe to call from controllers that must not write state (e.g. the Envoy
// controller); all writes go through MutateSearchState.
func ReadSearchState(
	ctx context.Context,
	c kubernetesClient.Client,
	search *searchv1.MongoDBSearch,
) (*SearchDeploymentState, error) {
	cm := &corev1.ConfigMap{}
	if err := c.Get(ctx, kube.ObjectKey(search.Namespace, SearchStateCMName(search)), cm); err != nil {
		if apierrors.IsNotFound(err) {
			return NewSearchDeploymentState(), nil
		}
		return nil, err
	}
	return searchStateFromCM(cm)
}

// MutateSearchState performs a resourceVersion-checked read-modify-write of the
// search state ConfigMap: a stale base yields 409 Conflict and the reconcile
// requeues, instead of silently losing a concurrent write (do NOT replace this
// with configmap.CreateOrUpdate — that is a blind no-RV Update). mutate returns
// true when the state changed and must be persisted. Every update also repairs
// legacy metadata written by released 1.9.x operators: the ConfigMap gains an
// owner reference to the current CR and the owner labels.
func MutateSearchState(ctx context.Context, c kubernetesClient.Client, search *searchv1.MongoDBSearch, mutate func(*SearchDeploymentState) bool) (*SearchDeploymentState, error) {
	cmName := SearchStateCMName(search)
	cm := &corev1.ConfigMap{}
	err := c.Get(ctx, kube.ObjectKey(search.Namespace, cmName), cm)
	if apierrors.IsNotFound(err) {
		state := NewSearchDeploymentState()
		if !mutate(state) {
			return state, nil
		}
		data, err := json.Marshal(state)
		if err != nil {
			return nil, err
		}
		newCM := configmap.Builder().
			SetName(cmName).
			SetNamespace(search.Namespace).
			SetLabels(searchOwnerLabels(search, "")).
			SetOwnerReferences(kube.BaseOwnerReference(search)).
			SetDataField(searchStateKey, string(data)).
			Build()
		return state, c.Create(ctx, &newCM)
	} else if err != nil {
		return nil, err
	}

	state, err := searchStateFromCM(cm)
	if err != nil {
		return nil, err
	}

	stateChanged := mutate(state)
	metadataChanged := false
	// Adopt legacy metadata without replacing an existing controller owner reference.
	if !slices.ContainsFunc(cm.OwnerReferences, func(ref metav1.OwnerReference) bool {
		return ref.UID == search.UID
	}) {
		cm.OwnerReferences = append(cm.OwnerReferences, metav1.OwnerReference{
			APIVersion: v1.SchemeGroupVersion.String(),
			Kind:       "MongoDBSearch",
			Name:       search.Name,
			UID:        search.UID,
		})
		metadataChanged = true
	}
	if cm.Labels == nil {
		cm.Labels = map[string]string{}
	}
	for key, value := range searchOwnerLabels(search, "") {
		if cm.Labels[key] != value {
			cm.Labels[key] = value
			metadataChanged = true
		}
	}

	if !stateChanged && !metadataChanged {
		return state, nil
	}

	data, err := json.Marshal(state)
	if err != nil {
		return nil, err
	}
	if cm.Data == nil {
		cm.Data = map[string]string{}
	}
	cm.Data[searchStateKey] = string(data)
	// Update on the Get result carries its resourceVersion — stale base → Conflict.
	return state, c.Update(ctx, cm)
}
