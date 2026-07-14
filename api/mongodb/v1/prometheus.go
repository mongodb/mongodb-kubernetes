package v1

// SecretKeyReference is a reference to a secret containing a key.
type SecretKeyReference struct {
	// Name is the name of the secret storing this user's password
	Name string `json:"name"`

	// Key is the key in the secret storing this password. Defaults to "password"
	// +optional
	Key string `json:"key"`
}

// Prometheus holds the configuration for the Prometheus metrics endpoint.
type Prometheus struct {
	// Port where metrics endpoint will bind to. Defaults to 9216.
	// +optional
	Port int `json:"port,omitempty"`

	// HTTP Basic Auth Username for metrics endpoint.
	Username string `json:"username"`

	// Name of a Secret containing a HTTP Basic Auth Password.
	PasswordSecretRef SecretKeyReference `json:"passwordSecretRef"`

	// Indicates path to the metrics endpoint.
	// +kubebuilder:validation:Pattern=^\/[a-z0-9]+$
	MetricsPath string `json:"metricsPath,omitempty"`

	// Name of a Secret (type kubernetes.io/tls) holding the certificates to use in the
	// Prometheus endpoint.
	// +optional
	TLSSecretRef SecretKeyReference `json:"tlsSecretKeyRef,omitempty"`
}

func (p Prometheus) GetPasswordKey() string {
	if p.PasswordSecretRef.Key != "" {
		return p.PasswordSecretRef.Key
	}

	return "password"
}

func (p Prometheus) GetPort() int {
	if p.Port != 0 {
		return p.Port
	}

	return 9216
}
