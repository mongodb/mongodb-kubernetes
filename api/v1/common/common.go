// +kubebuilder:object:generate=true
package common

import (
	"encoding/json"

	"github.com/stretchr/objx"

	"k8s.io/apimachinery/pkg/runtime"

	"github.com/mongodb/mongodb-kubernetes/pkg/automationconfig"
)

// SecretKeyReference is a reference to a secret containing a key.
type SecretKeyReference struct {
	// Name is the name of the secret storing this user's password
	Name string `json:"name"`

	// Key is the key in the secret storing this password. Defaults to "password"
	// +optional
	Key string `json:"key"`
}

type LogLevel string

const (
	LogLevelDebug LogLevel = "DEBUG"
	LogLevelInfo  LogLevel = "INFO"
	LogLevelWarn  LogLevel = "WARN"
	LogLevelError LogLevel = "ERROR"
	LogLevelFatal LogLevel = "FATAL"
)

// MapWrapper is a wrapper for a map to be used by other structs.
// The CRD generator does not support map[string]interface{}
// on the top level and hence we need to work around this with
// a wrapping struct.
type MapWrapper struct {
	Object map[string]interface{} `json:"-"`
}

// MarshalJSON defers JSON encoding to the wrapped map
func (m *MapWrapper) MarshalJSON() ([]byte, error) {
	return json.Marshal(m.Object)
}

// UnmarshalJSON will decode the data into the wrapped map
func (m *MapWrapper) UnmarshalJSON(data []byte) error {
	if m.Object == nil {
		m.Object = map[string]interface{}{}
	}

	// Handle keys like net.port to be set as nested maps.
	// Without this after unmarshalling there is just key "net.port" which is not
	// a nested map and methods like GetPort() cannot access the value.
	tmpMap := map[string]interface{}{}
	err := json.Unmarshal(data, &tmpMap)
	if err != nil {
		return err
	}

	for k, v := range tmpMap {
		m.SetOption(k, v)
	}

	return nil
}

func (m *MapWrapper) DeepCopy() *MapWrapper {
	if m != nil && m.Object != nil {
		return &MapWrapper{
			Object: runtime.DeepCopyJSON(m.Object),
		}
	}
	c := NewMapWrapper()
	return &c
}

// NewMapWrapper returns an empty MapWrapper
func NewMapWrapper() MapWrapper {
	return MapWrapper{Object: map[string]interface{}{}}
}

// SetOption updates the MapWrapper with a new option
func (m MapWrapper) SetOption(key string, value interface{}) MapWrapper {
	m.Object = objx.New(m.Object).Set(key, value)
	return m
}

// MongodConfiguration holds the optional mongod configuration
// that should be merged with the operator created one.
type MongodConfiguration struct {
	MapWrapper `json:"-"`
}

// NewMongodConfiguration returns an empty MongodConfiguration
func NewMongodConfiguration() MongodConfiguration {
	return MongodConfiguration{MapWrapper{map[string]interface{}{}}}
}

// GetDBDataDir returns the db path which should be used.
func (m MongodConfiguration) GetDBDataDir() string {
	return objx.New(m.Object).Get("storage.dbPath").Str(automationconfig.DefaultMongoDBDataDir)
}

// GetDBPort returns the port that should be used for the mongod process.
// If port is not specified, the default port of 27017 will be used.
func (m MongodConfiguration) GetDBPort() int {
	portValue := objx.New(m.Object).Get("net.port")

	// Underlying map could be manipulated in code, e.g. via SetDBPort (e.g. in unit tests) - then it will be as int,
	// or it could be deserialized from JSON and then integer in an untyped map will be deserialized as float64.
	// It's behavior of https://pkg.go.dev/encoding/json#Unmarshal that is converting JSON integers as float64.
	if portValue.IsInt() {
		return portValue.Int(automationconfig.DefaultDBPort)
	} else if portValue.IsFloat64() {
		return int(portValue.Float64(float64(automationconfig.DefaultDBPort)))
	}

	return automationconfig.DefaultDBPort
}

// SetDBPort ensures that port is stored as float64
func (m MongodConfiguration) SetDBPort(port int) MongodConfiguration {
	m.SetOption("net.port", float64(port))
	return m
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

// AutomationConfigOverride contains fields which will be overridden in the operator created config.
type AutomationConfigOverride struct {
	Processes  []OverrideProcess  `json:"processes,omitempty"`
	ReplicaSet OverrideReplicaSet `json:"replicaSet,omitempty"`
}

// OverrideReplicaSet holds replica set override fields for the AutomationConfig.
type OverrideReplicaSet struct {
	// Id can be used together with additionalMongodConfig.replication.replSetName
	// to manage clusters where replSetName differs from the MongoDBCommunity resource name
	Id *string `json:"id,omitempty"`
	// +kubebuilder:validation:Type=object
	// +kubebuilder:pruning:PreserveUnknownFields
	Settings MapWrapper `json:"settings,omitempty"`
}

// OverrideProcess contains fields that we can override on the AutomationConfig processes.
type OverrideProcess struct {
	Name      string                         `json:"name"`
	Disabled  bool                           `json:"disabled"`
	LogRotate *automationconfig.CrdLogRotate `json:"logRotate,omitempty"`
}
