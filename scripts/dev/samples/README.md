
## Configuration parameters for dev context files

This is a list of all configuration options that can be set by the developer inside the dev context file
(all the context files are located in `~/.operator-dev/contexts/` directory)

* `CLUSTER_TYPE`: the type of Kubernetes cluster. Possible values are "kops", "openshift", "kind"
* `CLUSTER_NAME`: the name of kubectl context. Will be created as soon as relevant kubernetes cluster is created
* `kube_environment_name`: use `vanilla` for kops cluster, `multi` for multi cluster, `kind` for kind cluster type 
* `BASE_REPO_URL`: the url of docker repository used to store docker images
* `agent_version`: version of mms-automation agent
* Registry and version configuration. Reference versions can be taken from the [yaml file](../../../public/mongodb-enterprise.yaml)
  * `INIT_OPS_MANAGER_REGISTRY` 
  * `INIT_OPS_MANAGER_VERSION`
  * `INIT_DATABASE_REGISTRY`
  * `INIT_DATABASE_VERSION`
  * `INIT_APPDB_REGISTRY`
  * `INIT_APPDB_VERSION`
  * `OPS_MANAGER_REGISTRY`
  * `OPS_MANAGER_VERSION`
  * `DATABASE_REGISTRY`
  * `DATABASE_VERSION`
* `NAMESPACE`: (optional) the name of Kubernetes namespace that will be created. Note, that the group name
            in Ops Manager will have the same name. It's recommended to have different namespace names for different
            contexts to avoid Ops Manager clashing. "mongodb" by default
            If you change Namespace for existing configuration - make sure you delete the previous namespace!
* `KOPS_ZONES`: (optional, only if CLUSTER_TYPE is set to 'kops'). Overrides default zones for kops cluster. May be
            necessary if the default region (us-east) has engaged all VPCs up to the limit (5 by default). Example: "eu-west-1b"
* `KOPS_K8S_VERSION`: (optional, only if CLUSTER_TYPE is set to 'kops'). Overrides the default K8s version used for 
e2e and dev K8s clusters. 
* `OM_HOST`: (optional) the OM base url that will be used to create a `connection` `ConfigMap` used by `MongoDB` resources. E.g. `https://cloud-qa.mongodb.com`
* `OM_ORGID`: (optional) the id of organization in OM to be used. Will be used to create a `connection` `ConfigMap` used by `MongoDB` resources.
* `OM_USER`: (optional) the userName/public programmatic key used to authenticate to the OM instance. Will be used to create a `credentials` `Secret`.
* `OM_API_KEY`: (optional) the public API key/private programmatic key used to authenticate to the OM instance. Will be used to create a `credentials` `Secret`.
* `OPERATOR_VERSION`: (optional) the version of images to be downloaded. Must be specified only if official image
            registry is used (quay.io)
* `MONGODB_RESOURCES`: (optional) the list of yaml config files that will be applied automatically after each "make full"
             run. For example "MONGODB_RESOURCES="rs.yaml; rs2.yaml"
