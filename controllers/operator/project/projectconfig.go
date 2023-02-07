package project

import (
	"fmt"

	"github.com/10gen/ops-manager-kubernetes/pkg/util/env"

	"github.com/mongodb/mongodb-kubernetes-operator/pkg/kube/configmap"

	mdbv1 "github.com/10gen/ops-manager-kubernetes/api/v1/mdb"
	"github.com/10gen/ops-manager-kubernetes/pkg/util"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func validateProjectConfig(cmGetter configmap.Getter, projectConfigMap client.ObjectKey) (map[string]string, error) {
	data, err := configmap.ReadData(cmGetter, projectConfigMap)
	if err != nil {
		return nil, err
	}

	requiredFields := []string{util.OmBaseUrl, util.OmOrgId}

	for _, requiredField := range requiredFields {
		if _, ok := data[requiredField]; !ok {
			return nil, fmt.Errorf(`property "%s" is not specified in ConfigMap %s`, requiredField, projectConfigMap)
		}
	}
	return data, nil
}

// ReadProjectConfig returns a "Project" config build from a ConfigMap with a series of attributes
// like `projectName`, `baseUrl` and a series of attributes related to SSL.
// If configMap doesn't have a projectName defined - the name of MongoDB resource is used as a name of project
func ReadProjectConfig(cmGetter configmap.Getter, projectConfigMap client.ObjectKey, mdbName string) (mdbv1.ProjectConfig, error) {
	data, err := validateProjectConfig(cmGetter, projectConfigMap)
	if err != nil {
		return mdbv1.ProjectConfig{}, err
	}

	baseURL := data[util.OmBaseUrl]
	orgID := data[util.OmOrgId]

	projectName := data[util.OmProjectName]
	if projectName == "" {
		projectName = mdbName
	}

	sslRequireValid := true
	sslRequireValidData, ok := data[util.SSLRequireValidMMSServerCertificates]
	if ok {
		sslRequireValid = sslRequireValidData != "false"
	}

	sslCaConfigMap, ok := data[util.SSLMMSCAConfigMap]
	caFile := ""
	if ok {
		sslCaConfigMapKey := types.NamespacedName{Name: sslCaConfigMap, Namespace: projectConfigMap.Namespace}

		cacrt, err := configmap.ReadData(cmGetter, sslCaConfigMapKey)
		if err != nil {
			return mdbv1.ProjectConfig{}, fmt.Errorf("failed to read the specified ConfigMap %s (%e)", sslCaConfigMapKey, err)
		}
		for k, v := range cacrt {
			if k == util.CaCertMMS {
				caFile = v
				break
			}
		}
	}

	var useCustomCA bool
	useCustomCAData, ok := data[util.UseCustomCAConfigMap]
	if ok {
		useCustomCA = useCustomCAData != "false"
	}

	return mdbv1.ProjectConfig{
		BaseURL:     baseURL,
		ProjectName: projectName,
		OrgID:       orgID,

		// Options related with SSL on OM side.
		SSLProjectConfig: env.SSLProjectConfig{
			// Relevant to
			// + operator (via golang http configuration)
			// + curl (via command line argument [--insecure])
			// + automation-agent (via env variable configuration [SSL_REQUIRE_VALID_MMS_CERTIFICATES])
			// + EnvVarSSLRequireValidMMSCertificates and automation agent option
			// + -sslRequireValidMMSServerCertificates
			SSLRequireValidMMSServerCertificates: sslRequireValid,

			// SSLMMSCAConfigMap is name of the configmap with the CA. This CM
			// will be mounted in the database Pods.
			SSLMMSCAConfigMap: sslCaConfigMap,

			// This needs to be passed for the operator itself to be able to
			// recognize the CA -- as it can't be mounted on an already running
			// Pod.
			SSLMMSCAConfigMapContents: caFile,
		},

		Credentials: data[util.OmCredentials],

		UseCustomCA: useCustomCA,
	}, nil
}
