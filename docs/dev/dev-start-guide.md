## Starting guide for developers

This is a guide about how to do daily Operator dev tasks using `make` utility. The other way is using the `redo.sh` script
that works with Minikube and allows to do incremental Operator deployments (just pass `--watch` parameter)

### Prerequisites

* Make sure to checkout this project into the `src/github.com/10gen` folder in the `$GOPATH` directory as described
 [here](https://golang.org/doc/code.html). So if your `$GOPATH` variable points to `/home/user/go` then the project
 must be checked out into `/Users/user/go/src/github.com/10gen/ops-manager-kubernetes`
* [Go](https://golang.org/doc/install): Go programming language (we use the latest current version which is `1.9.4`)
* [Docker](https://docs.docker.com/docker-for-mac/install/)
* (For Mac) Install `coreutils`:
```bash
brew install coreutils
# add to ~/.bashrc
PATH="/usr/local/opt/coreutils/libexec/gnubin:$PATH"
```
* [evergreen](https://evergreen.mongodb.com/settings)
* [mms-utils](https://wiki.corp.mongodb.com/display/MMS/Ops+Manager+Release+setup+guide#OpsManagerReleasesetupguide-First-timeonly)
note, that you should switch to python virtual environment in most cases when you work with `make`
* [Generate a github token](https://github.com/settings/tokens/new) with "repo" permissions and set `GITHUB_TOKEN`
environment variable in `~/.bashrc`
* Get the access to AWS account  "MMS Engineering Test" (268558157000)" - ask your collegues to add the user account there
* Add the following environment variable export to your `~/.bashrc`: `export KOPS_STATE_STORE=s3://kube-om-state-store`


### Development workflow

The development workflow is almost fully automated - just use `make` from the root of the project.
You can have many configurations - local, remote etc, you can easily switch between them using `make switch..`
Execute `make usage` to see detailed description of all targets

```bash
# install all necessary software (dep, minikube, kubectl, aws-cli, kops, helm)
make prerequisites

# prepare default configuration context files. Switches to 'minikube' context.
make init

# add a configuration file to ~/.operator-dev/contexts if necessary
# it's highly recommended to work with kops cluster instead of Minikube so just copy 'kops' configuration and change
# "dev." to some other name
# .....

# switch to the necessary context
make switch context=dev

# spawn Ops Manager in Evergreen. This will take up to 20 minitues
# the best is to extend host expiration via UI later to avoid frequent spawning
# (automatic expiration extending is not implemented by EG CLI: https://jira.mongodb.org/browse/EVG-5725)
make om-evg

# initialize Kubernetes cluster (for kops will spawn a new cluster - takes ~5-10 minutes, for minikube starts a new
# cluster locally)
make

# create Mongodb resource (note, that it's the best not to specify namespace inside yaml file as it will be defined by
# current namespace)
kubectl apply -f my-replica-set.yaml

# prints some information about current context and Kubernetes cluster
make status

```

### Troubleshooting

If you encounter an error like the following when running `make` or otherwise
building docker images locally, this means that docker has run out of space for
more images.

```
E: You don't have enough free space in /var/cache/apt/archives/.
```

This can usually be solved by running an appropriate docker prune command:
https://docs.docker.com/config/pruning.
