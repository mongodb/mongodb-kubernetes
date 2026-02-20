package om

// TLS param keys for monitoring additionalParams.
const (
	TLSParamUseSsl      = "useSslForAllConnections"
	TLSParamTrustedCert = "sslTrustedServerCertificates"
	TLSParamClientCert  = "sslClientCertificate"
)

// NewTLSParams creates and returns a new map with TLS parameters.
func NewTLSParams(caFilePath string, pemKeyFile interface{}) map[string]string {
	params := map[string]string{
		TLSParamUseSsl:      "true",
		TLSParamTrustedCert: caFilePath,
	}
	if pemKeyFile != nil && pemKeyFile.(string) != "" {
		params[TLSParamClientCert] = pemKeyFile.(string)
	}
	return params
}

func clearTLSParamsFromMap[V any](params map[string]V) {
	delete(params, TLSParamUseSsl)
	delete(params, TLSParamTrustedCert)
	delete(params, TLSParamClientCert)
}

// ClearTLSParams removes TLS-specific parameters from the given params map.
func ClearTLSParams(params map[string]string) {
	clearTLSParamsFromMap(params)
}

// ClearTLSParamsFromMonitoringVersion removes TLS-specific fields from the monitoring
// version's additionalParams. If additionalParams becomes empty after removing TLS fields,
// it is deleted from the monitoring version.
func ClearTLSParamsFromMonitoringVersion(monitoringVersion map[string]interface{}) {
	var isEmpty bool
	switch params := monitoringVersion["additionalParams"].(type) {
	case map[string]string:
		clearTLSParamsFromMap(params)
		isEmpty = len(params) == 0
	case map[string]interface{}:
		clearTLSParamsFromMap(params)
		isEmpty = len(params) == 0
	}
	if isEmpty {
		delete(monitoringVersion, "additionalParams")
	}
}
