## Starting guide for developers

This is a guide about setting all necessary environment for developing the MongoDB Enterprise Operator

### Prerequisites

* [Go](https://golang.org/doc/install): (we use the latest `1.13.*` version)
* Ensure environment variables `GOROOT` and `GOPATH` are specified in your `~/.bashrc`. The most common values are:
```bash
export GOROOT=/usr/local/go
export GOPATH=$HOME/go
```
* Checkout this project into the `src/github.com/10gen` folder in the `$GOPATH` directory as described
 [here](https://golang.org/doc/code.html). So if your `$GOPATH` variable points to `/home/user/go` then the project
 must be checked out into `/Users/user/go/src/github.com/10gen/ops-manager-kubernetes`
* [Docker](https://docs.docker.com/docker-for-mac/install/)
* [Evergreen command line client](https://evergreen.mongodb.com/settings)
* [mms-utils](https://wiki.corp.mongodb.com/display/MMS/Ops+Manager+Release+setup+guide#OpsManagerReleasesetupguide-First-timeonly)
* [Generate a github token](https://github.com/settings/tokens/new) with "repo" permissions and set `GITHUB_TOKEN`
environment variable in `~/.bashrc`
* AWS 
    * `brew install awscli` 
    * Get the access to AWS account  "MMS Engineering Test" (268558157000)":
        1. Ask your colleagues to add the user (and have them to send you your password), and then
        2. Generate the access and secret keys for your user and save them in ~/.aws/credentials under **default** section. 
        (or use `aws configure`) Ask your colleagues which AWS region to choose - it should match the region where the K8s cluster and ECR registries
        are located (some regions could reach VPC capacity).

### Development workflow

The development workflow is almost fully automated - just use `make` from the root of the project.
You can have many configurations - local, remote etc, you can easily switch between them using `make switch context=some`
Execute `make usage` to see detailed description of all targets

#### Install necessary tools and commit hook

This will install different tools used for development: kubectl, kops, helm, coreutils, also initiaze necessary 
environment variables

```bash
make prerequisites
```

#### Initialize development context

Prepare default configuration context files. All context files reside in `~/.operator-dev/contexts` and describe different
dev environments (Kubernetes clusters, image repositories). The context file is a simple bash script exporting environment
variables sourced by the development bash scripts.
```bash
make init
```

#### Edit the context file
`~/.operator-dev/contexts/dev` context file is configured to work with kops clusters by default.
Edit the file:
1. Change all ECR registry URLs:
  * change "us-east-1" to the AWS zone where the kops cluster will be created
  * change "myname" to something more meaningful (we usually use some last name abbreviation)
2. Change the `CLUSTER_NAME` to `<myname>.mongokubernetes.com` 
3. (optionally) Set `KOPS_ZONES` to the AWS zone with available VPCs. "us-east-2a" is used by default

You can edit the other context files or copy them to new ones.

#### Switching between contexts 
This will update the symlink `~/.operator-dev/context` to point to the relevant context file in `~/.operator-dev/contexts` -
it will be used by all `make` commands for building and deploying images
```bash
make switch context=e2e-openshift
```

#### Cloud-qa integration

The easiest way to test MongoDB resources is by using cloud-qa environment instead of the custom Ops Manager.
To do this login to https://cloud-qa.mongodb.com/ and create a test organization.
Generate the programmatic API keys and put all the relevant information into either `~/.operator-dev/om` (so it will
be used by all contexts) or append to a specific context file:

```bash
export OM_HOST=https://cloud-qa.mongodb.com
export OM_USER=<public_key>
export OM_API_KEY=<private_key>
export OM_ORGID=<org_id>
```

Note, that if you plan to use one kops cluster with different Ops Manager installations and/or Cloud-qa you'll
need to specify the non-default namespace which will be the namespace where the Operator and all the resources will be
created. So switching between different context will result in having different working namespaces in the same K8s cluster

```bash
export NAMESPACE=cloudqa
```

#### Custom Ops Manager in Evergreen
If the custom version of Ops Manager needs to be tested it's possible to start a standalone Ops Manager in Evergreen:

```bash
# spawn Ops Manager in Evergreen. This will take up to 20 minitues
# the best is to extend host expiration via UI later to avoid frequent spawning
# (automatic expiration extending is not implemented by EG CLI: https://jira.mongodb.org/browse/EVG-5725)
make om-evg
```
This will update `~/.operator-dev/om` bash script with relevant connectivity information to the new OM instance
 and will be used by `make` scripts working with `MongoDB` resources (the connection `Secret` and project `ConfigMap`
will be created based on this information and will be referenced by `MongoDB` resources)

> The alternative to this can be starting the Ops Manager resource in K8s cluster using the Operator. This OM instance
> would be used by the MongoDB resources. Note, that this requires some manual changes to the MongoDB connection `ConfigMap`
> and using the non-default credentials `Secret` (the one generated by the Operator for the OM global admin)

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

If kops cluster fails to get created because of VPC limits, you can change KOPS_ZONES in `~/.operator-dev/contexts/dev` 
(or the context you are currently using) to point to the other zones which have free VPCs (look at the values in `scripts/dev/ensure_k8s`).

### Examples
#### Using an Openshift cluster to run E2E tests
(note, that you need to ensure the OM connectivity details are specified in either `~/.operator-dev/contexts/openshift` 
file or `~/.operator-dev/om` configuration files)
1. `make switch context=openshift`
2. `make e2e test=e2e_replica_set`

#### Using an old Kubernetes cluster
This example shows how the new context can be created and used to solve a specific task - testing the Operator on some 
older versions of Kubernetes:
1. Copy the `dev` context file to a new one
2. Change the `CLUSTER_NAME` to a new cluster (e.g. `CLUSTER_NAME=legacy.mongokubernetes.com`)
3. Change the `KOPS_K8S_VERSION` to a Kubernetes version needed (e.g. `v1.11.0`)
4. `make switch context=legacy`
5. `make`

This will create a new K8s cluster in AWS using kops (only on the first run), will create all necessary secrets and 
config maps and build+deploy the Operator there. Now it's possible to either create new CRs there or run e2e tests.

### Troubleshooting

If you encounter an error like the following when running `make` or otherwise
building docker images locally, this means that docker has run out of space for
more images.

```
E: You don't have enough free space in /var/cache/apt/archives/.
```

This can usually be solved by running an appropriate docker prune command:
https://docs.docker.com/config/pruning.
