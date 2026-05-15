package haraft

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"sync"

	"github.com/hashicorp/raft"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// ConfigMapLogStore implements raft.LogStore. Log entries are stored as
// data["log-<index>"] = base64(json(raft.Log)) on the raft-state ConfigMap.
// A mutex protects against concurrent updates that hashicorp/raft would not
// otherwise serialize at the storage level.
type ConfigMapLogStore struct {
	client    client.Client
	namespace string
	mu        sync.Mutex
}

func NewConfigMapLogStore(c client.Client, namespace string) *ConfigMapLogStore {
	return &ConfigMapLogStore{client: c, namespace: namespace}
}

func (s *ConfigMapLogStore) load() (*corev1.ConfigMap, error) {
	cm := &corev1.ConfigMap{}
	err := s.client.Get(context.Background(), types.NamespacedName{Name: StateConfigMapName, Namespace: s.namespace}, cm)
	return cm, err
}

func (s *ConfigMapLogStore) FirstIndex() (uint64, error) {
	cm, err := s.load()
	if err != nil {
		return 0, err
	}
	return logBoundary(cm.Data, true), nil
}

func (s *ConfigMapLogStore) LastIndex() (uint64, error) {
	cm, err := s.load()
	if err != nil {
		return 0, err
	}
	return logBoundary(cm.Data, false), nil
}

func logBoundary(data map[string]string, first bool) uint64 {
	var found uint64
	have := false
	for k := range data {
		if !strings.HasPrefix(k, "log-") {
			continue
		}
		idx, err := strconv.ParseUint(strings.TrimPrefix(k, "log-"), 10, 64)
		if err != nil {
			continue
		}
		if !have {
			found = idx
			have = true
			continue
		}
		if first && idx < found {
			found = idx
		}
		if !first && idx > found {
			found = idx
		}
	}
	return found
}

func (s *ConfigMapLogStore) GetLog(idx uint64, out *raft.Log) error {
	cm, err := s.load()
	if err != nil {
		return err
	}
	raw, ok := cm.Data[fmt.Sprintf("log-%d", idx)]
	if !ok {
		return raft.ErrLogNotFound
	}
	dec, err := base64.StdEncoding.DecodeString(raw)
	if err != nil {
		return err
	}
	return json.Unmarshal(dec, out)
}

func (s *ConfigMapLogStore) StoreLog(log *raft.Log) error {
	return s.StoreLogs([]*raft.Log{log})
}

func (s *ConfigMapLogStore) StoreLogs(logs []*raft.Log) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	cm, err := s.load()
	if err != nil {
		return err
	}
	if cm.Data == nil {
		cm.Data = map[string]string{}
	}
	for _, l := range logs {
		buf, err := json.Marshal(l)
		if err != nil {
			return err
		}
		cm.Data[fmt.Sprintf("log-%d", l.Index)] = base64.StdEncoding.EncodeToString(buf)
	}
	return s.client.Update(context.Background(), cm)
}

func (s *ConfigMapLogStore) DeleteRange(min, max uint64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	cm, err := s.load()
	if err != nil {
		return err
	}
	for i := min; i <= max; i++ {
		delete(cm.Data, fmt.Sprintf("log-%d", i))
	}
	return s.client.Update(context.Background(), cm)
}
