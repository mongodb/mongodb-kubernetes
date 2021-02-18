SHELL := /bin/bash

all: manager

export MAKEFLAGS="-j 16" # enable parallelism

usage:
	@ echo "Development utility to work with Operator on daily basis. Just edit your configuration in '~/.operator-dev/contexts', "
	@ echo "switch to it using 'make switch', make sure Ops Manager is running (use 'make om' or 'make om-evg') "
	@ echo "and call 'make' to start the Kubernetes cluster and the Operator"
	@ echo
	@ echo "More information can be found by the link https://github.com/10gen/ops-manager-kubernetes/blob/master/docs/dev/dev-start-guide.md"
	@ echo
	@ echo "Usage:"
	@ echo "  prerequisites:              installs the command line applications necessary for working with this tool and adds git pre-commit hook."
	@ echo "  init:                       prepares operator environment."
	@ echo "  switch:                     switch current dev context, e.g 'make switch context=kops'. Note, that it switches"
	@ echo "                              kubectl context as well and sets the current namespace to the one configured as the default"
	@ echo "                              one"
	@ echo "  contexts:                   list all available contexts"
	@ echo "  operator:                   build and push Operator image, deploy it to the Kubernetes cluster"
	@ echo "                              Use the 'debug' flag to build and deploy the Operator in debug mode - you need"
	@ echo "                              to ensure the 30042 port on the K8s node is open"
	@ echo "                              Use the 'watch_namespace' flag to specify a namespace to watch or leave empty to watch project namespace."
	@ echo "  database:                   build and push Database image"
	@ echo "  full:                       ('make' is an alias for this command) ensures K8s cluster is up, cleans Kubernetes"
	@ echo "                              resources, build-push-deploy operator, push-deploy database, create secrets, "
	@ echo "                              config map, resources etc"
	@ echo "  appdb:                      build and push AppDB image. Specify 'om_version' in format '4.2.1' to provide the already released Ops Manager"
	@ echo "                              version which will be used to find the matching tag and find the Automation Agent version. Add 'om_branch' "
	@ echo "                              if Ops Manager is not released yet and you want to have some git branch as the source "
	@ echo "  om:                         install Test Ops Manager into Kubernetes if it's not installed yet. Initializes the connection"
	@ echo "                              parameters in ~/operator-dev/om"
	@ echo "  om-evg:                     install Ops Manager into Evergreen if it's not installed yet. Initializes the connection"
	@ echo "                              parameters in ~/operator-dev/om. You can pass custom Ubuntu Debian package url using 'url' parameter"
	@ echo "  reset:                      cleans all Operator related state from Kubernetes and Ops Manager. Pass the 'light=true'"
	@ echo "                              to perform a \"light\" cleanup - delete only Mongodb resources"
	@ echo "  e2e:                        runs the e2e test, e.g. 'make e2e test=e2e_sharded_cluster_pv'. The Operator is redeployed before"
	@ echo "                              the test, the namespace is cleaned. The e2e app image is built and pushed. Use a 'light=true'"
	@ echo "                              in case you are developing tests and not changing the application code - this will allow to"
	@ echo "                              avoid rebuilding Operator/Database/Init images. Use 'debug=true' to run operator in debug mode."
	@ echo "                              Use a 'local=true' to run the test locally using 'pytest'."
	@ echo "                              Use a 'skip=true' to skip cleaning resources (this may help developing long-running tests like for Ops Manager)"
	@ echo "                              Sometimes you may need to pass some custom configuration, this can be done this way:"
	@ echo "                              make e2e test=e2e_om_ops_manager_upgrade custom_om_version=4.2.8"
	@ echo "  recreate-e2e-kops:          deletes and creates a specified e2e cluster 'cluster' using kops (note, that you don't need to switch to the correct"
	@ echo "                              kubectl context - the script will handle everything). Pass the flag 'imsure=yes' to make it work."
	@ echo "                              Pass 'cluster' parameter for a cluster name if it's different from default ('e2e.mongokubernetes.com')"
	@ echo "                              Possible values are: 'e2e.om.mongokubernetes.com', 'e2e.multinamespace.mongokubernetes.com'"
	@ echo "  recreate-e2e-openshift:     deletes and creates an e2e Openshift cluster"
	@ echo "  log:                        reads the Operator log"
	@ echo "  status:                     prints the current context and the state of Kubernetes cluster"
	@ echo "  dashboard:                  opens the Kubernetes dashboard. Make sure the cluster was installed using current Makefile as"
	@ echo "                              dashboard is not installed by default and the script ensures it's installed and permissions"
	@ echo "                              are configured."
	@ echo "  open-automation-config/ac:   displays the contents of the Automation Config in in $EDITOR using ~/.operator-dev configuration"


# install all necessary software, must be run only once
prerequisites:
	@ scripts/dev/install.sh

# prepare default configuration context files
init:
	@ mkdir -p ~/.operator-dev/contexts
	@ cp -n scripts/dev/samples/* ~/.operator-dev/contexts || true
	@ echo "Initialized dev environment (~/.operator-dev)"
	@ make switch context=dev

switch:
	@ scripts/dev/switch_context.sh $(context)

# prints all current contexts
contexts:
	@ scripts/dev/print_contexts

# builds the Operator binary file and docker image and pushes it to the remote registry if using a remote registry. Deploys it to
# k8s cluster
operator: configure-operator build-and-push-operator-image
	@ $(MAKE) deploy-operator

# build-push, (todo) restart database
database: aws_login
	@ ./pipeline.py --include database

# ensures cluster is up, cleans Kubernetes + OM, build-push-deploy operator,
# push-deploy database, create secrets, config map, resources etc
full: ensure-k8s-and-reset build-and-push-images
	@ $(MAKE) deploy-and-configure-operator
	@ scripts/dev/apply_resources

# build-push appdb image
appdb: aws_login
	@ ./pipeline.py --include appdb

# install OM in Evergreen
om-evg:
	@ scripts/dev/ensure_ops_manager_evg $(url)

log:
	@ . scripts/dev/read_context.sh
	@ kubectl logs -f deployment/mongodb-enterprise-operator --tail=1000

# runs the e2e test: make e2e test=e2e_sharded_cluster_pv. The Operator is redeployed before the test, the namespace is cleaned.
# The e2e test image is built and pushed together with all main ones (operator, database, init containers)
# Use 'light=true' parameter to skip images rebuilding - use this mode when you are focused on e2e tests development only
e2e: build-and-push-test-image
	@ if [[ -z "$(skip)" ]]; then \
		$(MAKE) reset; \
	fi
	@ if [[ -z "$(light)" ]]; then \
		$(MAKE) build-and-push-images; \
	fi
	@ scripts/dev/launch_e2e.sh

# deletes and creates a kops e2e cluster
recreate-e2e-kops:
	@ scripts/dev/recreate_e2e_kops.sh $(imsure) $(cluster)

# TODO: Automate this process
# deletes and creates a openshift e2e cluster
recreate-e2e-openshift:
	@ echo "Please follow instructions in docs/openshift4.md to install the Openshift4 cluster."

# clean all kubernetes cluster resources and OM state. "light=true" to clean only Mongodb resources
reset:
	@ scripts/dev/reset.sh $(light)

status:
	@ scripts/dev/status

dashboard:
	@ scripts/dev/kube-dashboard

# opens the automation config in your editor
open-automation-config: ac
ac:
	@ scripts/dev/print_automation_config

###############################################################################
# Internal Targets
# These won't do anything bad if you call them, they just aren't the ones that
# were designed to be helpful by themselves. Anything below won't be documented
# in the usage target above.
###############################################################################

# dev note on '&> /dev/null || true': if the 'aws_login' is run in parallel (e.g. 'make' launches builds for images
# in parallel and both call 'aws_login') then Docker login may return an error "Error saving credentials:..The
# specified item already exists in the keychain". Seems this allows to ignore the error
aws_login:
	@ . scripts/dev/set_env_context.sh; \
 	  scripts/dev/configure_docker_auth.sh

build-and-push-operator-image: aws_login
	@ ./pipeline.py --include operator-quick

build-and-push-database-image: aws_login
	@ scripts/dev/build_push_database_image

build-and-push-test-image: aws_login
	@ if [[ -z "$(local)" ]]; then \
		./pipeline.py --include test; \
	fi

# builds all app images in parallel
# note that we cannot build both appdb and database init images in parallel as they change the same docker file
build-and-push-images: build-and-push-operator-image appdb-init-image om-init-image
	@ $(MAKE) database-init-image

database-init-image:
	@ ./pipeline.py --include init-database

appdb-init-image:
	@ ./pipeline.py --include init-appdb

om-init-image:
	@ ./pipeline.py --include init-ops-manager

deploy-operator:
	@ scripts/dev/deploy_operator.sh $(debug)

configure-operator:
	@ scripts/dev/configure_operator.sh

deploy-and-configure-operator: deploy-operator configure-operator

ensure-k8s:
	@ scripts/dev/ensure_k8s.sh

ensure-k8s-and-reset: ensure-k8s
	@ $(MAKE) reset


####################################
## operator-sdk provided Makefile ##
####################################
#
# The next section is the Makefile provided by operator-sdk.
# We'll start moving to use some of the targets provided by it, like
# `manifests` and `bundle`. For now we'll try to maintain both.


# VERSION defines the project version for the bundle. 
# Update this value when you upgrade the version of your project.
# To re-generate a bundle for another specific version without changing the standard setup, you can:
# - use the VERSION as arg of the bundle target (e.g make bundle VERSION=0.0.2)
# - use environment variables to overwrite this value (e.g export VERSION=0.0.2)
VERSION ?= 0.0.1

# CHANNELS define the bundle channels used in the bundle. 
# Add a new line here if you would like to change its default config. (E.g CHANNELS = "preview,fast,stable")
# To re-generate a bundle for other specific channels without changing the standard setup, you can:
# - use the CHANNELS as arg of the bundle target (e.g make bundle CHANNELS=preview,fast,stable)
# - use environment variables to overwrite this value (e.g export CHANNELS="preview,fast,stable")
ifneq ($(origin CHANNELS), undefined)
BUNDLE_CHANNELS := --channels=$(CHANNELS)
endif

# DEFAULT_CHANNEL defines the default channel used in the bundle. 
# Add a new line here if you would like to change its default config. (E.g DEFAULT_CHANNEL = "stable")
# To re-generate a bundle for any other default channel without changing the default setup, you can:
# - use the DEFAULT_CHANNEL as arg of the bundle target (e.g make bundle DEFAULT_CHANNEL=stable)
# - use environment variables to overwrite this value (e.g export DEFAULT_CHANNEL="stable")
ifneq ($(origin DEFAULT_CHANNEL), undefined)
BUNDLE_DEFAULT_CHANNEL := --default-channel=$(DEFAULT_CHANNEL)
endif
BUNDLE_METADATA_OPTS ?= $(BUNDLE_CHANNELS) $(BUNDLE_DEFAULT_CHANNEL)

# BUNDLE_IMG defines the image:tag used for the bundle. 
# You can use it as an arg. (E.g make bundle-build BUNDLE_IMG=<some-registry>/<project-name-bundle>:<tag>)
BUNDLE_IMG ?= controller-bundle:$(VERSION)

# Image URL to use all building/pushing image targets
IMG ?= mongodb-enterprise-operator:latest
# Produce CRDs that work back to Kubernetes 1.11 (no version conversion)
CRD_OPTIONS ?= "crd:trivialVersions=true,preserveUnknownFields=false"

# Get the currently used golang install path (in GOPATH/bin, unless GOBIN is set)
ifeq (,$(shell go env GOBIN))
GOBIN=$(shell go env GOPATH)/bin
else
GOBIN=$(shell go env GOBIN)
endif


# Run tests
ENVTEST_ASSETS_DIR=$(shell pwd)/testbin
test: generate fmt vet manifests
	/bin/bash -o pipefail -c 'go test ./... -coverprofile cover.out | tee -a ops-manager-kubernetes.suite'

# Build manager binary
manager: generate fmt vet
	go build -o docker/mongodb-enterprise-operator/content/mongodb-enterprise-operator main.go

# Run against the configured Kubernetes cluster in ~/.kube/config
run: generate fmt vet manifests
	go run ./main.go

# Install CRDs into a cluster
install: manifests kustomize
	$(KUSTOMIZE) build config/crd | kubectl apply -f -

# Uninstall CRDs from a cluster
uninstall: manifests kustomize
	$(KUSTOMIZE) build config/crd | kubectl delete -f -

# Deploy controller in the configured Kubernetes cluster in ~/.kube/config
deploy: manifests kustomize
	cd config/manager && $(KUSTOMIZE) edit set image controller=$(IMG)
	$(KUSTOMIZE) build config/default | kubectl apply -f -

# UnDeploy controller from the configured Kubernetes cluster in ~/.kube/config
undeploy:
	$(KUSTOMIZE) build config/default | kubectl delete -f -

# Generate manifests e.g. CRD, RBAC etc.
manifests: controller-gen
	$(CONTROLLER_GEN) $(CRD_OPTIONS) rbac:roleName=manager-role webhook paths="./..." output:crd:artifacts:config=config/crd/bases

# Run go fmt against code
fmt:
	go fmt ./...

# Run go vet against code
vet:
	go vet ./...

# Generate code
generate: controller-gen
	$(CONTROLLER_GEN) object:headerFile="hack/boilerplate.go.txt" paths="./..."

# Build the docker image
docker-build: test
	docker build -t $(IMG) .

# Push the docker image
docker-push:
	docker push $(IMG)

# Download controller-gen locally if necessary
CONTROLLER_GEN = $(shell pwd)/bin/controller-gen
controller-gen:
	$(call go-get-tool,$(CONTROLLER_GEN),sigs.k8s.io/controller-tools/cmd/controller-gen@v0.4.1)

# Download kustomize locally if necessary
KUSTOMIZE = $(shell pwd)/bin/kustomize
kustomize:
	$(call go-get-tool,$(KUSTOMIZE),sigs.k8s.io/kustomize/kustomize/v3@v3.8.7)

# go-get-tool will 'go get' any package $2 and install it to $1.
PROJECT_DIR := $(shell dirname $(abspath $(lastword $(MAKEFILE_LIST))))
define go-get-tool
@[ -f $(1) ] || { \
set -e ;\
TMP_DIR=$$(mktemp -d) ;\
cd $$TMP_DIR ;\
go mod init tmp ;\
echo "Downloading $(2)" ;\
GOBIN=$(PROJECT_DIR)/bin go get $(2) ;\
rm -rf $$TMP_DIR ;\
}
endef

# Generate bundle manifests and metadata, then validate generated files.
.PHONY: bundle
bundle: manifests kustomize
	operator-sdk generate kustomize manifests -q
	cd config/manager && $(KUSTOMIZE) edit set image controller=$(IMG)
	$(KUSTOMIZE) build config/manifests | operator-sdk generate bundle -q --overwrite --version $(VERSION) $(BUNDLE_METADATA_OPTS)
	operator-sdk bundle validate ./bundle

# Build the bundle image.
.PHONY: bundle-build
bundle-build:
	docker build -f bundle.Dockerfile -t $(BUNDLE_IMG) .
