## Starting guide for developers

This is a guide about setting all necessary environment for developing the MongoDB Enterprise Operator

### Prerequisites

* [Go](https://golang.org/doc/install): (the latest version is usually fine)
* Checkout this project locally:
```bash
mkdir -p $HOME/workspace/repos/
cd $HOME/workspace/repos
git clone git@github.com:10gen/ops-manager-kubernetes.git
```
* [Docker](https://docs.docker.com/docker-for-mac/install/)
* [Evergreen command line client](https://evergreen.mongodb.com/settings)
* [mms-utils](https://github.com/10gen/mms/tree/master/scripts/python#one-time-set-up).
  You will need to clone the `mms` project.
* [Generate a github token](https://github.com/settings/tokens/new) with "repo"
  permissions and set `GITHUB_TOKEN` environment variable in `~/.bashrc`
* [Set up git commit signing](https://wiki.corp.mongodb.com/display/MMS/Setup+Git+Commit+Signing)
* AWS 
    * Install the latest version of AWS CLI: `brew install awscli`
    * Get the access to AWS account  "MMS Engineering Test" (268558157000)":
        1. Ask your colleagues to add the user (and have them to send you your password)
           * You will have to be connected to the VPN to change your password
        2. Generate the access and secret keys for your user and save them in
           ~/.aws/credentials under **default** section. (or use `aws
           configure`) Please specify `eu-west-1` as the default region in
           `~/.aws/config`.
### Development workflow

The development workflow is almost fully automated - just use `make` from the
root of the project. You can have many configurations - local, remote etc, you
can easily switch between them using `make switch context=some` Execute `make
usage` to see detailed description of all targets.

#### Install necessary tools and commit hook

Initialize python virtual environment.
```bash
python3 -m venv venv
source venv/bin/activate
```

This will install different tools used for development: kubectl, kops, helm,
coreutils, also initialise necessary environment variables.

```bash
make prerequisites
```

#### Initialize development context

Prepare default configuration context files. All context files reside in
`~/.operator-dev/contexts` and describe different dev environments (Kubernetes
clusters, image repositories). The context file is a simple bash script
exporting environment variables sourced by the development bash scripts.

```bash
make init
```

#### Edit the context file

`~/.operator-dev/contexts/dev` context file is configured to work with kops
clusters by default. Edit the file:

1. Change all ECR registry URLs: change "myname" to something more meaningful
   (we usually use some last name abbreviation)
2. Change the `CLUSTER_NAME` to `<myname>.mongokubernetes.com`
3. Specify the `RED_HAT_TOKEN` property ("Token" on
   https://access.redhat.com/terms-based-registry/#/token/openshift3-test-cluster -
   ask your colleagues for credentials)
4. Set `KOPS_ZONES` to the AWS zone with available VPCs. (Specify the zone that
   you selected to create the cluster)
  * Note that if you set this you will need to provide the full zone and not
    just the region name (if your AWS zone is `eu-west-1` you should have, for
    example, `eu-west-1a`)

You can edit the other context files or copy them to new ones.

See [additional documentation](../../scripts/dev/samples/README.md) for the context variables.

#### Switching between contexts 

This will update the symlink `~/.operator-dev/context` to point to the relevant
context file in `~/.operator-dev/contexts` - it will be used by all `make`
commands for building and deploying images

```bash
make switch context=e2e-openshift
```

#### Cloud-qa integration

First of all, follow [this
doc](https://wiki.corp.mongodb.com/display/MMS/Cloud+IAM%27s+Okta+Usage) to set
up `cloud-qa` account if you haven't already.

The easiest way to test MongoDB resources is by using cloud-qa environment
instead of the custom Ops Manager. To do this login to
https://cloud-qa.mongodb.com/ and create a test organization.

Generate the programmatic API keys. Add the following two masks in the
`whitelist` option:

* `0.0.0.0/1`
* `128.0.0.0/1` 

Put all the relevant information into either `~/.operator-dev/om` (so it will
be used by all contexts) or append to a specific context file:

```bash
export OM_HOST=https://cloud-qa.mongodb.com
export OM_USER=<public_key>
export OM_API_KEY=<private_key>
export OM_ORGID=<org_id>
```

Note, that if you plan to use one kops cluster with different Ops Manager
installations and/or Cloud-qa you'll need to specify the non-default namespace
which will be the namespace where the Operator and all the resources will be
created. So switching between different context will result in having different
working namespaces in the same K8s cluster

```bash
export NAMESPACE=cloudqa
```

#### Example context file
This is example context file with initial configuration allowing to execute [initial dev workflow](#initial-dev-workflow).
Before that `lsierant-kops2` cluster was created. Specific configuration was left intentionally.     

```bash
# configure
export NAMESPACE=lsierant-kops2
export CLUSTER_NAME=lsierant-kops2.mongokubernetes.com
export BASE_REPO_URL=268558157000.dkr.ecr.us-east-1.amazonaws.com/lsierant-kops2

export IMAGE_TYPE=ubuntu
export CLUSTER_TYPE=kops
export kube_environment_name=vanilla
export KOPS_ZONES=eu-west-1a
export KOPS_K8S_VERSION=1.23.7
export agent_version=10.29.0.6830-1
export REGISTRY=quay.io/mongodb

export INIT_OPS_MANAGER_REGISTRY=${REGISTRY}/ubuntu
export INIT_OPS_MANAGER_VERSION=1.0.7

export INIT_DATABASE_REGISTRY=${REGISTRY}
export INIT_DATABASE_VERSION=1.0.9

export OPS_MANAGER_REGISTRY=${REGISTRY}

export INIT_APPDB_REGISTRY="${REGISTRY}"
export APPDB_REGISTRY=${REGISTRY}
export DATABASE_REGISTRY=${REGISTRY}
export DATABASE_VERSION=2.0.2
export KUBECONFIG=~/.kube/config

export RED_HAT_TOKEN=eyJhb...
```

#### Initial dev workflow
These steps allow to execute the first e2e test. For the simplest case only the operator and e2e tests images are built locally.

```bash
# ensure cluster&credentials
make ensure-k8s
make aws_login

# build operator image locally
./pipeline.py --include operator-quick

# build test image and run e2e test
# light=true is for building test image only, otherwise it will build all images
make e2e test=e2e_replica_set light=true  
```

#### Dev workflow

This includes the major commands that are used during development

```bash
# ensures Kubernetes cluster is alive (for kops will spawn a new cluster - takes ~5-10 minutes, for Kind starts a new
# cluster locally) and build+deploy all the necessary artifacts. All the K8s resources will be cleaned
make

# create Mongodb resource (note, that it's the best not to specify namespace inside yaml file as it will be defined by
# current namespace)
kubectl apply -f public/samples/mongodb/minimal/replica-set.yaml

# create Ops Manager resource
kubectl apply -f public/samples/ops-manager/ops-manager.yaml

# check the Operator logs
make log

# check statuses of Custom Resources
kubectl get mdb -o yaml -w
kubectl get om -o yaml -w

# build and deploy the Operator only - all existing K8s resources will be left untouched
make operator

# run an e2e test (specify 'light=true' to avoid rebuilding the Operator, Database and init images)
# Will build and deploy the test image to the current K8s cluster, wait until it's finished
make e2e test=e2e_replica_set

# prints some information about current context and Kubernetes cluster
make status

```

Please note that you will have to be connected to the VPN to succesfully run
`make` the first time, when the `kops` cluster is created.

If kops cluster fails to get created because of VPC limits, you can change
KOPS_ZONES in `~/.operator-dev/contexts/dev` (or the context you are currently
using) to point to the other zones which have free VPCs (look at the values in
`scripts/dev/ensure_k8s.sh`).

At the end of the script you might get the following error:

`Unable to connect to the server: dial tcp: lookup api.<yourname>.mongokubernetes.com on 8.8.8.8:53: no such host`

This is normal as it will take a few minutes for the DNS to behave correctly.
You can try to run `kops validate cluster <yourname>.mongokubernetes.com` a few
times: if the DNS is still flaky it will sometimes return this `tcp` error.

### Examples
#### Using an Openshift cluster to run E2E tests

(note, that you need to ensure the OM connectivity details are specified in
either `~/.operator-dev/contexts/openshift` file or `~/.operator-dev/om`
configuration files)

1. `make switch context=openshift`
2. `make e2e test=e2e_replica_set`

#### Using an old Kubernetes cluster

This example shows how the new context can be created and used to solve a
specific task - testing the Operator on some older versions of Kubernetes:

1. Copy the `dev` context file to a new one
2. Change the `CLUSTER_NAME` to a new cluster (e.g.
   `CLUSTER_NAME=legacy.mongokubernetes.com`)
3. Change the `KOPS_K8S_VERSION` to a Kubernetes version needed (e.g. `v1.11.0`)
4. `make switch context=legacy`
5. `make`

This will create a new K8s cluster in AWS using kops (only on the first run),
will create all necessary secrets and config maps and build+deploy the Operator
there. Now it's possible to either create new CRs there or run e2e tests.

#### Testing on Ops Manager 4.0

If there's an E2E test failing only on Ops Manager 4.0 build variant - there's
no need to start the 4.0 OpsManager anywhere (also Ops Manager 4.0 is not
supported in Kubernetes).

The easy solution is to reuse the Kops cluster used for e2e tests which already
has an Ops Manager 4.0 instance running. This is an example of the context file
that you may edit:

```
export CLUSTER_TYPE=kops
export CLUSTER_NAME=e2e.mongokubernetes.com
export REPO_URL=268558157000.dkr.ecr.eu-west-1.amazonaws.com/alis
export INIT_OPS_MANAGER_REGISTRY=${REPO_URL}
export INIT_APPDB_REGISTRY=${REPO_URL}
export NAMESPACE=anton
export OM_HOST=http://ops-manager.operator-testing-40-first.svc.cluster.local:8080
export OM_USER=admin
export OM_API_KEY=b26dfb22-3e14-472a-a0e6-e04982a57192
```

Note, that for the `OM_USER` and `OM_API_KEY` may look different way - you can
check the output of the test of the test in evergreen.

After this you can run `make e2e test=<name>` for the context above and this way
it's possible to debug problems with Ops Manager 4.0. The test will be run in an
isolated namespace (`anton` for this example) and won't affect the existing
namespaces

#### Multi Cluster

In order to run e2e test for multi cluster, you will need to create a multi cluster context file.

Add the following to this context file.

```bash
export kube_environment_name=multi
export member_clusters="e2e.cluster1.mongokubernetes.com e2e.cluster2.mongokubernetes.com e2e.cluster3.mongokubernetes.com"
export central_cluster=e2e.operator.mongokubernetes.com
export test_pod_cluster=e2e.cluster1.mongokubernetes.com
```

Run a test with

```bash
make e2e test=e2e_multi_cluster_replica_set
```

This test will deploy the operator in the `central_cluster`, the test pod in `test_pod_cluster` and configure
the operator to have api access to all clusters in `member_clusters`

If you want to run tests using pytest locally without building the test image. You can run tests using:

```bash
make e2e test=e2e_multi_cluster_replica_set local=true
```

You will need to ensure that you have the following env var set in your context

```bash
export MULTI_CLUSTER_CONFIG_DIR=/absolute/path/to/directory/ops-manager-kubernetes/.multi_cluster_local_test_files
```

### Troubleshooting

#### Error with find
If you run into the following error while running `make`:

`find: -printf: unknown primary or operator`

you have to make sure to use GNU's `find`:

`brew install findutils`

and then add it to your `PATH` by adding the following line to your `.zshrc` (or
analogous if you are using a shell different from `zsh`):

`PATH="/usr/local/opt/findutils/libexec/gnubin:$PATH"`


#### Not enough free space

If you encounter an error like the following when running `make` or otherwise
building docker images locally, this means that docker has run out of space for
more images.

```
E: You don't have enough free space in /var/cache/apt/archives/.
```

This can usually be solved by running an appropriate docker prune command:
https://docs.docker.com/config/pruning.
