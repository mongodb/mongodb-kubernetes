package operator

// resolveReplicaSetAgentCertPaths reconciles two notions of “where is the agent PEM on disk?”:
//
//   - omAutoPEMKeyFilePath: tls.autoPEMKeyFilePath from OM: i.e., what is already configured for agents
//
//   - defaultAgentCertFilePath: the path the operator would set from the current kubernetes.io/tls agent secret alone.
//
// Returns (pathForAuth, pathForItemsMount). Second value is empty when OM matches the default (normal directory mount).
func resolveReplicaSetAgentCertPaths(omAutoPEMKeyFilePath, defaultAgentCertFilePath string, externalMembersLen int) (agentCertPath string, agentCertExternalPath string) {
	if omAutoPEMKeyFilePath == "" || omAutoPEMKeyFilePath == defaultAgentCertFilePath { // normal - non-migration case, use operator default
		return defaultAgentCertFilePath, ""
	}
	if externalMembersLen > 0 { // we are still in the migration and should use the path from OM instead
		return omAutoPEMKeyFilePath, omAutoPEMKeyFilePath
	}
	// we finished migration, om does not fully reflect default path yet. Next iteration omAutoPEMKeyFilePath -> defaultAgentCertFilePath
	return defaultAgentCertFilePath, omAutoPEMKeyFilePath
}
