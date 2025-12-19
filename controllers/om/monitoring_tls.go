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
// version's additionalParams.
func ClearTLSParamsFromMonitoringVersion(monitoringVersion map[string]interface{}) {
	params, ok := monitoringVersion["additionalParams"].(map[string]interface{})
	if !ok {
		return
	}
	clearTLSParamsFromMap(params)
}
