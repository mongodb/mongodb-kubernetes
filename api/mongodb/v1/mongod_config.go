package v1

import (
	"github.com/stretchr/objx"

	"github.com/mongodb/mongodb-kubernetes/pkg/automationconfig"
)

type LogLevel string

const (
	LogLevelDebug LogLevel = "DEBUG"
	LogLevelInfo  LogLevel = "INFO"
	LogLevelWarn  LogLevel = "WARN"
	LogLevelError LogLevel = "ERROR"
	LogLevelFatal LogLevel = "FATAL"
)

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
