# Local development and E2E testing

## Prepare local environment

* For running tests locally (not in a pod), you need Python 3.9 virtual env created from docker/mongodb-enterprise-tests/requirements.txt

## Multi-cluster development
### Context variables

```bash

export NAMESPACE=<your-namespace>
export PROJECT_DIR=<path-to-ops-manager-kubernetes>
export RED_HAT_TOKEN=<>
export AWS_ACCESS_KEY_ID=<>
export AWS_SECRET_ACCESS_KEY=<>
# repo used as OPERATOR_REGISTRY
export BASE_REPO_URL=268558157000.dkr.ecr.us-east-1.amazonaws.com/<your-ecr-registry>

# comment this to use local kind clusters
export EVG_HOST_NAME=<evg-host-name>

# set to ubi or ubuntu
export IMAGE_TYPE=ubi
# Set to true if using ubi images in ECR registry. Set to false if using ubi from quay.
export UBI_IMAGE_WITHOUT_SUFFIX=true
# True if running operator locally and not in a pod
export LOCAL_OPERATOR=true

# changing variables below should not be necessary

# these are fixed when using scripts/dev/recreate_kind_clusters.sh
export CLUSTER_NAME=kind-e2e-operator
export test_pod_cluster=kind-e2e-cluster-1
export member_clusters="kind-e2e-cluster-1 kind-e2e-cluster-2 kind-e2e-cluster-3"


# when using EVG ec2 instance, we copy kubeconfig locally and use it
if [[ "${EVG_HOST_NAME}" != "" ]]; then
  export KUBECONFIG=~/.operator-dev/evg-host.kubeconfig
else
  export KUBECONFIG=~/.kube/config
fi

export kube_environment_name=multi
# kubeconfig of remote kind clusters copied locally
export central_cluster=${CLUSTER_NAME}
export MEMBER_CLUSTERS=${member_clusters}
export CENTRAL_CLUSTER=${central_cluster}
export MULTI_CLUSTER_CREATE_SERVICE_ACCOUNT_TOKEN_SECRETS=true
export MULTI_CLUSTER_CONFIG_DIR=${PROJECT_DIR}/.multi_cluster_local_test_files
export MULTI_CLUSTER_KUBE_CONFIG_CREATOR_PATH=${PROJECT_DIR}/docker/mongodb-enterprise-tests/multi-cluster-kube-config-creator

# override for /etc/config/kubeconfig file mounted in operator's pod 
if [[ "${LOCAL_OPERATOR}" == "true" ]]; then
  export KUBE_CONFIG_PATH=$HOME/.operator-dev/multicluster_kubeconfig
  export PERFORM_FAILOVER=false
  export OPERATOR_CLUSTER_SCOPED=false
fi

export PROJECT_NAMESPACE=${NAMESPACE}
export OPERATOR_REGISTRY=${BASE_REPO_URL}/${IMAGE_TYPE}
export INIT_IMAGES_REGISTRY=268558157000.dkr.ecr.us-east-1.amazonaws.com/dev/${IMAGE_TYPE}

export QUAY_REGISTRY=quay.io/mongodb

export CLUSTER_TYPE="kind"
export INIT_APPDB_REGISTRY=${INIT_IMAGES_REGISTRY}
export INIT_APPDB_VERSION=latest
export INIT_OPS_MANAGER_REGISTRY=${INIT_IMAGES_REGISTRY}
export INIT_OPS_MANAGER_VERSION=latest
export INIT_DATABASE_REGISTRY=${INIT_IMAGES_REGISTRY}
export INIT_DATABASE_VERSION=latest
export DATABASE_REGISTRY=${INIT_IMAGES_REGISTRY}
export DATABASE_VERSION=latest
export OPS_MANAGER_REGISTRY=${QUAY_REGISTRY}
export APPDB_REGISTRY=${QUAY_REGISTRY}

export agent_version=12.0.4.7554-1

# values from .evergreen.yml
export CUSTOM_APPDB_VERSION=5.0.7-ent
export CUSTOM_OM_VERSION=6.0.7
export CUSTOM_OM_PREV_VERSION=5.0.1
export CUSTOM_MDB_VERSION=5.0.5
export CUSTOM_MDB_PREV_VERSION=5.0.1
```

### Example context file - run local tests against local Kind clusters
File `~/.operator-dev/contexts/multi-kind-evg`:
```bash
export NAMESPACE=lsierant-10
export PROJECT_DIR=/Users/lukasz.sierant/mdb/ops-manager-kubernetes
export RED_HAT_TOKEN=eyJhb...
export AWS_ACCESS_KEY_ID=ABC...
export AWS_SECRET_ACCESS_KEY=e4af...
# repo used as OPERATOR_REGISTRY
export BASE_REPO_URL=268558157000.dkr.ecr.us-east-1.amazonaws.com/lsierant-kops2

# comment this to use local kind clusters
# export EVG_HOST_NAME=""

# set to ubi or ubuntu
export IMAGE_TYPE=ubi
# Set to true if using ubi images in ECR registry. Set to false if using ubi from quay.
export UBI_IMAGE_WITHOUT_SUFFIX=true
# True if running operator locally and not in a pod
export LOCAL_OPERATOR=true

# changing variables below should not be necessary

# these are fixed when using scripts/dev/recreate_kind_clusters.sh
export CLUSTER_NAME=kind-e2e-operator # this has to be central-cluster
export test_pod_cluster=kind-e2e-cluster-1
export member_clusters="kind-e2e-cluster-1 kind-e2e-cluster-2 kind-e2e-cluster-3"


# when using EVG ec2 instance, we copy kubeconfig locally and use it
if [[ "${EVG_HOST_NAME}" != "" ]]; then
  export KUBECONFIG=~/.operator-dev/evg-host.kubeconfig
else
  export KUBECONFIG=~/.kube/config
fi

export kube_environment_name=multi
# kubeconfig of remote kind clusters copied locally
export central_cluster=${CLUSTER_NAME}
export MEMBER_CLUSTERS=${member_clusters}
export CENTRAL_CLUSTER=${central_cluster}
export MULTI_CLUSTER_CREATE_SERVICE_ACCOUNT_TOKEN_SECRETS=true
export MULTI_CLUSTER_CONFIG_DIR=${PROJECT_DIR}/.multi_cluster_local_test_files
export MULTI_CLUSTER_KUBE_CONFIG_CREATOR_PATH=${PROJECT_DIR}/docker/mongodb-enterprise-tests/multi-cluster-kube-config-creator

# override for /etc/config/kubeconfig file mounted in operator's pod
if [[ "${LOCAL_OPERATOR}" == "true" ]]; then
  export KUBE_CONFIG_PATH=$HOME/.operator-dev/multicluster_kubeconfig
  export PERFORM_FAILOVER=false
  export OPERATOR_CLUSTER_SCOPED=false
fi

export CLUSTER_TYPE="kind"
export PROJECT_NAMESPACE=${NAMESPACE}
export OPERATOR_REGISTRY=${BASE_REPO_URL}/${IMAGE_TYPE}
export INIT_IMAGES_REGISTRY=268558157000.dkr.ecr.us-east-1.amazonaws.com/dev/${IMAGE_TYPE}

export QUAY_REGISTRY=quay.io/mongodb

export INIT_APPDB_REGISTRY=${INIT_IMAGES_REGISTRY}
export INIT_APPDB_VERSION=latest
export INIT_OPS_MANAGER_REGISTRY=${INIT_IMAGES_REGISTRY}
export INIT_OPS_MANAGER_VERSION=latest
export INIT_DATABASE_REGISTRY=${INIT_IMAGES_REGISTRY}
export INIT_DATABASE_VERSION=latest
export DATABASE_REGISTRY=${INIT_IMAGES_REGISTRY}
export DATABASE_VERSION=latest
export OPS_MANAGER_REGISTRY=${QUAY_REGISTRY}
export APPDB_REGISTRY=${QUAY_REGISTRY}

export agent_version=12.0.4.7554-1

# values from .evergreen.yml
export CUSTOM_APPDB_VERSION=5.0.7-ent
export CUSTOM_OM_VERSION=6.0.7
export CUSTOM_OM_PREV_VERSION=5.0.1
export CUSTOM_MDB_VERSION=5.0.5
export CUSTOM_MDB_PREV_VERSION=5.0.1
```

### Example context file - run local tests against Kind clusters on remove EVG host
The only difference is in `EVG_HOST` env var:

File `~/.operator-dev/contexts/multi-kind-evg`
```bash
export EVG_HOST_NAME=multi-cluster
source ~/.operator-dev/contexts/multi-kind
```

### Running against local kind clusters

#### Prepare env

* `make switch context=<context-name>` - this generates the following files containing environment variables:
  * `~/.operator-dev/context.env` - all env variables from context file in the form: `VAR="VALUE"`. E2E test requires these env variables. 
  * `~/.operator-dev/context.export.env` - same as above, but with exported form : `export VAR="VALUE"`, which can be sourced to current session.
  * `~/.operator-dev/context.operator.env` - env variables from the context required by the operator binary in not-exported env form. 
  * `~/.operator-dev/context.operator.export.env` - same as above, but in exported form. 
 
* `scripts/dev/recreate_kind_clusters.sh` - recreates all kind clusters
* `make aws_login` - important to make sure you have up-to-date tokens for ECR.
* `scripts/dev/scripts/dev/prepare_local_e2e_run.sh` - cleans/creates current namespace in central and all member clusters; executes multi-cluster cli; installs CRD in central cluster. 

#### Run the operator locally
Make sure you have `LOCAL_OPERATOR=true` set in context. Otherwise the tests will assume the operator is running in a pod. Also when running the operator locally, some additional steps are performed when preparing the environment in `scripts/dev/scripts/dev/prepare_local_e2e_run.sh`, e.g. executing multi-cluster cli tool.  

##### From the command line
Source `~/.operator-dev/context.export.operator.env` file generated by `make switch`: 
```bash
source ~/.operator-dev/context.operator.export.env
go run main.go -watch-resource=mongodb -watch-resource=opsmanagers -watch-resource=mongodbusers -watch-resource=mongodbmulti -cluster-names=kind-e2e-cluster-1,kind-e2e-cluster-2,kind-e2e-cluster-3
```

##### From the IDE
Depending on your IDE, set environment variables either from: 
* `~/.operator-dev/context.export.operator.env` - exported variables
* `~/.operator-dev/context.operator.env`- plain 

Run main.go with arguments:
```
-watch-resource=mongodb -watch-resource=opsmanagers -watch-resource=mongodbusers -watch-resource=mongodbmulti -cluster-names=kind-e2e-cluster-1,kind-e2e-cluster-2,kind-e2e-cluster-3
```

#### Run the tests locally

Make sure you're working in tests venv.

##### From the command line

Source `~/.operator-dev/context.export.env` file generated by `make switch`: 
```bash
source ~/.operator-dev/context.export.env
```

Run a whole test file: 
```bash
pytest --setup-show tests/multicluster/multi_cluster_replica_set.py
```

Run only one test function (or class) from a file:
```bash
tests/multicluster/multi_cluster_replica_set.py::test_create_mongodb_multi
```

#### Run the operator locally
##### From the command line
To set environment variables, source `~/.operator-dev/context.export.operator.env` file generated by `make switch`:
```bash
source ~/.operator-dev/context.operator.export.env
go run main.go -watch-resource=mongodb -watch-resource=opsmanagers -watch-resource=mongodbusers -watch-resource=mongodbmulti -cluster-names=kind-e2e-cluster-1,kind-e2e-cluster-2,kind-e2e-cluster-3
```

##### From the IDE
Depending on your IDE, set environment variables either from:
* `~/.operator-dev/context.export.operator.env` - exported variables
* `~/.operator-dev/context.operator.env`- plain

Run `main.go` with arguments:
```
-watch-resource=mongodb -watch-resource=opsmanagers -watch-resource=mongodbusers -watch-resource=mongodbmulti -cluster-names=kind-e2e-cluster-1,kind-e2e-cluster-2,kind-e2e-cluster-3
```

### Running tests against Evergreen host

Testing on Kind clusters deployed on remote host works similar to locally deployed Kind clusters. Tests and the operator is running locally. The differences are:
* kubeconfig generated for remote clusters is copied locally to `~/.operator-dev/evg-host.kubeconfig`.
  * kubeconfig contains api endpoints pointing to `127.0.0.1`.
* ssh tunnels are used to expose these api endpoints at the same ports locally, so for kubectl it's no different from running against local Kind. 
* When running tests in pods (operator and tests in pods, not locally), prepare script (`scripts/dev/prepare_multi_cluster_e2e_run.sh`) is converting localhost api endpoints from `evg-host.kubeconfig` to Kind node endpoints, which are accessible from inside the cluster thanks to Istio. This converted kubeconfig is then used as a secret `test-pod-kubeconfig` for the tests.
* When using kubectl/k9s you need to provide --kubeconfig `~/.operator-dev/evg-host.kubeconfig` parameter.

#### Prepare EVG host
* Go to https://spruce.mongodb.com/spawn/host and create EC2 instance:
  * Distro: `ubuntu-2004-large`. 
  * Use existing key or add new one. Make sure it's used by default by ssh on Mac.
  * ![spawn-new-evg-host](spawn-new-evg-host.png)
  * After creating, edit the name, set e.g. `multi-cluster`.
  * Set this host name into `EVG_HOST_NAME` env var in context.
* Run `scripts/dev/evg_host.sh configure` to configure host by installing necessary software and copying scripts.
* Run `scripts/dev/evg_host.sh recreate-kind-clusters` to create kind clusters on remote host and copy kubeconfig to `~/.operator-dev/evg-host.kubeconfig`.
* Run `scripts/dev/evg_host.sh tunnel` to expose locally all api servers.

#### Run the tests locally
Running tests looks identical to running tests against local Kind clusters.

#### Other uses of `evg_host.sh` script
* To retrieve again remote kubeconfig without recreating all kind clusters run: `scripts/dev/evg_host.sh get-kubeconfig`.
* To ssh into remote host use: `scripts/dev/evg_host.sh ssh`.

#### Known issues
* When host is restarted it loses some routing configuration. Recreate clusters from scratch in case of any connectivity problems.
* After running `scripts/dev/scripts/dev/prepare_local_e2e_run.sh` you need to always restart the operator binary. 
