// Contains the wrapped types which are needed for generating
// CRD yamls using kubebuilder. They prevent each of the fields showing up in CRD yaml thereby
// resulting in a relatively smaller file.
package mdb

import (
	"encoding/json"

	corev1 "k8s.io/api/core/v1"
)

type ClientCertificateSecretRefWrapper struct {
	ClientCertificateSecretRef corev1.SecretKeySelector `json:"-"`
}

// MarshalJSON defers JSON encoding to the wrapped map
func (c *ClientCertificateSecretRefWrapper) MarshalJSON() ([]byte, error) {
	return json.Marshal(c.ClientCertificateSecretRef)
}

// UnmarshalJSON will decode the data into the wrapped map
func (c *ClientCertificateSecretRefWrapper) UnmarshalJSON(data []byte) error {
	return json.Unmarshal(data, &c.ClientCertificateSecretRef)
}

func (c *ClientCertificateSecretRefWrapper) DeepCopy() *ClientCertificateSecretRefWrapper {
	return &ClientCertificateSecretRefWrapper{
		ClientCertificateSecretRef: c.ClientCertificateSecretRef,
	}
}

type PodTemplateSpecWrapper struct {
	PodTemplate *corev1.PodTemplateSpec `json:"-"`
}

// MarshalJSON defers JSON encoding to the wrapped map
func (m *PodTemplateSpecWrapper) MarshalJSON() ([]byte, error) {
	return json.Marshal(m.PodTemplate)
}

// UnmarshalJSON will decode the data into the wrapped map
func (m *PodTemplateSpecWrapper) UnmarshalJSON(data []byte) error {
	return json.Unmarshal(data, &m.PodTemplate)
}

func (m *PodTemplateSpecWrapper) DeepCopy() *PodTemplateSpecWrapper {
	return &PodTemplateSpecWrapper{
		PodTemplate: m.PodTemplate,
	}
}

type PodAffinityWrapper struct {
	PodAffinity *corev1.PodAffinity `json:"-"`
}

// MarshalJSON defers JSON encoding to the wrapped map
func (m *PodAffinityWrapper) MarshalJSON() ([]byte, error) {
	return json.Marshal(m.PodAffinity)
}

// UnmarshalJSON will decode the data into the wrapped map
func (m *PodAffinityWrapper) UnmarshalJSON(data []byte) error {
	return json.Unmarshal(data, &m.PodAffinity)
}

func (m *PodAffinityWrapper) DeepCopy() *PodAffinityWrapper {
	return &PodAffinityWrapper{
		PodAffinity: m.PodAffinity,
	}
}

type NodeAffinityWrapper struct {
	NodeAffinity *corev1.NodeAffinity `json:"-"`
}

// MarshalJSON defers JSON encoding to the wrapped map
func (m *NodeAffinityWrapper) MarshalJSON() ([]byte, error) {
	return json.Marshal(m.NodeAffinity)
}

// UnmarshalJSON will decode the data into the wrapped map
func (m *NodeAffinityWrapper) UnmarshalJSON(data []byte) error {
	return json.Unmarshal(data, &m.NodeAffinity)
}

func (m *NodeAffinityWrapper) DeepCopy() *NodeAffinityWrapper {
	return &NodeAffinityWrapper{
		NodeAffinity: m.NodeAffinity,
	}
}
