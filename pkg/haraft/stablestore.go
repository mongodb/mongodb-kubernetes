package haraft

import (
	"context"
	"encoding/base64"
	"encoding/binary"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// ConfigMapStableStore implements raft.StableStore on top of a single
// ConfigMap (raft-state). Values are base64-encoded so binary data round-trips
// through ConfigMap data strings.
type ConfigMapStableStore struct {
	client    client.Client
	namespace string
}

func NewConfigMapStableStore(c client.Client, namespace string) *ConfigMapStableStore {
	return &ConfigMapStableStore{client: c, namespace: namespace}
}

func (s *ConfigMapStableStore) Set(key, val []byte) error {
	ctx := context.Background()
	cm := &corev1.ConfigMap{}
	if err := s.client.Get(ctx, types.NamespacedName{Name: StateConfigMapName, Namespace: s.namespace}, cm); err != nil {
		return err
	}
	if cm.Data == nil {
		cm.Data = map[string]string{}
	}
	cm.Data["kv-"+string(key)] = base64.StdEncoding.EncodeToString(val)
	return s.client.Update(ctx, cm)
}

func (s *ConfigMapStableStore) Get(key []byte) ([]byte, error) {
	ctx := context.Background()
	cm := &corev1.ConfigMap{}
	if err := s.client.Get(ctx, types.NamespacedName{Name: StateConfigMapName, Namespace: s.namespace}, cm); err != nil {
		return nil, err
	}
	v, ok := cm.Data["kv-"+string(key)]
	if !ok {
		return nil, nil
	}
	return base64.StdEncoding.DecodeString(v)
}

func (s *ConfigMapStableStore) SetUint64(key []byte, val uint64) error {
	buf := make([]byte, 8)
	binary.BigEndian.PutUint64(buf, val)
	return s.Set(key, buf)
}

func (s *ConfigMapStableStore) GetUint64(key []byte) (uint64, error) {
	v, err := s.Get(key)
	if err != nil || v == nil {
		return 0, err
	}
	return binary.BigEndian.Uint64(v), nil
}
