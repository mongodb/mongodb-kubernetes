package haraft

import (
	"context"

	corev1 "k8s.io/api/core/v1"
	apiErrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// PublishLeader writes the local cluster name as the leader of record into
// every peer's raft-leader ConfigMap. Called only when this node becomes
// leader (NOT on every reconcile — that would be wasteful).
func PublishLeader(ctx context.Context, localID, namespace string, peerClients map[string]client.Client) error {
	for _, cl := range peerClients {
		cm := &corev1.ConfigMap{}
		key := types.NamespacedName{Name: LeaderConfigMapName, Namespace: namespace}
		err := cl.Get(ctx, key, cm)
		switch {
		case apiErrors.IsNotFound(err):
			create := &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{Name: LeaderConfigMapName, Namespace: namespace},
				Data:       map[string]string{LeaderKeyClusterName: localID},
			}
			if err := cl.Create(ctx, create); err != nil {
				return err
			}
		case err != nil:
			return err
		default:
			if cm.Data == nil {
				cm.Data = map[string]string{}
			}
			cm.Data[LeaderKeyClusterName] = localID
			if err := cl.Update(ctx, cm); err != nil {
				return err
			}
		}
	}
	return nil
}
