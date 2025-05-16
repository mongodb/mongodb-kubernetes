SHELL := /bin/bash

all: manager

export MAKEFLAGS="-j 16" # enable parallelism

usage:
	@ echo "Development utility to work with Operator on daily basis. Just edit your configuration in '~/.operator-dev/contexts', "
	@ echo "switch to it using 'make switch', make sure Ops Manager is running (use 'make om') "
	@ echo "and call 'make' to start the Kubernetes cluster and the Operator"
	@ echo
	@ echo "More information can be found by the link https://wiki.corp.mongodb.com/display/MMS/Setting+up+local+development+and+E2E+testing"
	@ echo
	@ echo "Usage:"
	@ echo "  prerequisites:                  installs the command line applications necessary for working with this tool and adds git pre-commit hook."
	@ echo "  switch:                         switch current dev context, e.g 'make switch context=kops'. Note, that it switches"
	@ echo "                                  kubectl context as well and sets the current namespace to the one configured as the default"
	@ echo "                                  one"
	@ echo "  operator:                       build and push Operator image, deploy it to the Kubernetes cluster"
	@ echo "                                  Use the 'debug' flag to build and deploy the Operator in debug mode - you need"
	@ echo "                                  to ensure the 30042 port on the K8s node is open"
	@ echo "                                  Use the 'watch_namespace' flag to specify a namespace to watch or leave empty to watch project namespace."
	@ echo "  database:                       build and push Database image"
	@ echo "  full:                           ('make' is an alias for this command) ensures K8s cluster is up, cleans Kubernetes"
	@ echo "                                  resources, build-push-deploy operator, push-deploy database, create secrets, "
	@ echo "                                  config map, resources etc"
	@ echo "  appdb:                          build and push AppDB image. Specify 'om_version' in format '4.2.1' to provide the already released Ops Manager"
	@ echo "                                  version which will be used to find the matching tag and find the Automation Agent version. Add 'om_branch' "
	@ echo "                                  if Ops Manager is not released yet and you want to have some git branch as the source "
	@ echo "                                  parameters in ~/operator-dev/om"
	@ echo "  reset:                          cleans all Operator related state from Kubernetes and Ops Manager. Pass the 'light=true'"
	@ echo "                                  to perform a \"light\" cleanup - delete only Mongodb resources"
	@ echo "  e2e:                            runs the e2e test, e.g. 'make e2e test=e2e_sharded_cluster_pv'. The Operator is redeployed before"
	@ echo "                                  the test, the namespace is cleaned. The e2e app image is built and pushed. Use a 'light=true'"
	@ echo "                                  in case you are developing tests and not changing the application code - this will allow to"
	@ echo "                                  avoid rebuilding Operator/Database/Init images. Use 'debug=true' to run operator in debug mode."
	@ echo "                                  Use a 'local=true' to run the test locally using 'pytest'."
	@ echo "                                  Use a 'skip=true' to skip cleaning resources (this may help developing long-running tests like for Ops Manager)"
	@ echo "                                  Sometimes you may need to pass some custom configuration, this can be done this way:"
	@ echo "                                  make e2e test=e2e_om_ops_manager_upgrade CUSTOM_OM_VERSION=4.2.8"
	@ echo "  recreate-e2e-kops:              deletes and creates a specified e2e cluster 'cluster' using kops (note, that you don't need to switch to the correct"
	@ echo "                                  kubectl context - the script will handle everything). Pass the flag 'imsure=yes' to make it work."
	@ echo "                                  Pass 'cluster' parameter for a cluster name if it's different from default ('e2e.mongokubernetes.com')"
	@ echo "                                  Possible values are: 'e2e.om.mongokubernetes.com', 'e2e.multinamespace.mongokubernetes.com'"
	@ echo "  recreate-e2e-openshift:         deletes and creates an e2e Openshift cluster"
	@ echo "  recreate-e2e-multicluster-kind  Recreates local (Kind-based) development environment for running tests"
	@ echo "  log:                            reads the Operator log"
	@ echo "  status:                         prints the current context and the state of Kubernetes cluster"
	@ echo "  dashboard:                      opens the Kubernetes dashboard. Make sure the cluster was installed using current Makefile as"
	@ echo "                                  dashboard is not installed by default and the script ensures it's installed and permissions"
	@ echo "                                  are configured."
	@ echo "  open-automation-config/ac:      displays the contents of the Automation Config in in $EDITOR using ~/.operator-dev configuration"


# install all necessary software, must be run only once
prerequisites:
	@ scripts/dev/install.sh

precommit:
	@ EVERGREEN_MODE=true .githooks/pre-commit

switch:
	@ scripts/dev/switch_context.sh $(context) $(additional_override)

# builds the Operator binary file and docker image and pushes it to the remote registry if using a remote registry. Deploys it to
# k8s cluster
operator: configure-operator build-and-push-operator-image
	@ $(MAKE) deploy-operator

# build-push, (todo) restart database
database: aws_login
	@ scripts/evergreen/run_python.sh pipeline.py --image database

readiness_probe: aws_login
	@ scripts/evergreen/run_python.sh pipeline.py --image readiness-probe

upgrade_hook: aws_login
	@ scripts/evergreen/run_python.sh pipeline.py --image upgrade-hook

# ensures cluster is up, cleans Kubernetes + OM, build-push-deploy operator,
# push-deploy database, create secrets, config map, resources etc
full: build-and-push-images
	@ $(MAKE) deploy-and-configure-operator

# build-push appdb image
appdb: aws_login
	@ scripts/evergreen/run_python.sh pipeline.py --image appdb

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

mco-e2e: aws_login build-and-push-mco-test-image
	@ if [[ -z "$(skip)" ]]; then \
		$(MAKE) reset; \
	fi
	@ scripts/dev/launch_e2e.sh

generate-env-file: ## generates a local-test.env for local testing
	mkdir -p .generated
	{ scripts/evergreen/run_python.sh mongodb-community-operator/scripts/dev/get_e2e_env_vars.py ".generated/config.json" | tee >(cut -d' ' -f2 > .generated/mco-test.env) ;} > .generated/mco-test.export.env
	. .generated/mco-test.export.env

reset-helm-leftovers: ## sometimes you didn't cleanly uninstall a helm release, this cleans the existing helm artifacts
	@ scripts/dev/reset_helm.sh

e2e-telepresence: build-and-push-test-image
	telepresence connect --context $(test_pod_cluster); scripts/dev/launch_e2e.sh; telepresence quit

# clean all kubernetes cluster resources and OM state
reset: reset-mco
	go run scripts/dev/reset.go

reset-mco: ## Cleans up e2e test env
	kubectl delete mdbc,all,secrets -l e2e-test=true || true

status:
	@ scripts/dev/status

# opens the automation config in your editor
open-automation-config: ac
ac:
	@ scripts/dev/print_automation_config.sh

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
	@ scripts/dev/configure_docker_auth.sh

# cleans up aws resources, including s3 buckets which are older than 5 hours
aws_cleanup:
	@ scripts/evergreen/prepare_aws.sh

build-and-push-operator-image: aws_login
	@ scripts/evergreen/run_python.sh pipeline.py --image operator-quick

build-and-push-database-image: aws_login
	@ scripts/dev/build_push_database_image

build-and-push-test-image: aws_login build-multi-cluster-binary
	@ if [[ -z "$(local)" ]]; then \
		scripts/evergreen/run_python.sh pipeline.py --image test; \
	fi

build-and-push-mco-test-image: aws_login
	@ if [[ -z "$(local)" ]]; then \
		scripts/evergreen/run_python.sh pipeline.py --image mco-test; \
	fi

build-multi-cluster-binary:
	scripts/evergreen/build_multi_cluster_kubeconfig_creator.sh

# builds all app images in parallel
# note that we cannot build both appdb and database init images in parallel as they change the same docker file
build-and-push-images: build-and-push-operator-image appdb-init-image om-init-image database operator-image database-init-image
	@ $(MAKE) agent-image

# builds all init images
build-and-push-init-images: appdb-init-image om-init-image database-init-image

database-init-image:
	@ scripts/evergreen/run_python.sh pipeline.py --image init-database

appdb-init-image:
	@ scripts/evergreen/run_python.sh pipeline.py --image init-appdb

# Not setting a parallel-factor will default to 0 which will lead to using all CPUs, that can cause docker to die.
# Here we are defaulting to 6, a higher value might work for you.
agent-image:
	@ scripts/evergreen/run_python.sh pipeline.py --image agent --all-agents --parallel --parallel-factor 6

agent-image-slow:
	@ scripts/evergreen/run_python.sh pipeline.py --image agent --parallel-factor 1

operator-image:
	@ scripts/evergreen/run_python.sh pipeline.py --image operator

om-init-image:
	@ scripts/evergreen/run_python.sh pipeline.py --image init-ops-manager

om-image:
	@ scripts/evergreen/run_python.sh pipeline.py --image ops-manager

configure-operator:
	@ scripts/dev/configure_operator.sh

deploy-and-configure-operator: deploy-operator configure-operator

cert:
	@ openssl req  -nodes -new -x509  -keyout ca-tls.key -out ca-tls.crt -extensions v3_ca -days 3650
	@ mv ca-tls.key ca-tls.crt docker/mongodb-kubernetes-tests/tests/opsmanager/fixtures/
	@ cat docker/mongodb-kubernetes-tests/tests/opsmanager/fixtures/ca-tls.crt \
	docker/mongodb-kubernetes-tests/tests/opsmanager/fixtures/mongodb-download.crt \
	> docker/mongodb-kubernetes-tests/tests/opsmanager/fixtures/ca-tls-full-chain.crt

.PHONY: recreate-e2e-multicluster-kind
recreate-e2e-multicluster-kind:
	scripts/dev/recreate_kind_clusters.sh

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
VERSION ?= 1.15.0

# EXPIRES sets a label to expire images (quay specific)
EXPIRES := --label quay.expires-after=48h

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
IMG ?= mongodb-kubernetes-operator:latest
# Produce CRDs that work back to Kubernetes 1.11 (no version conversion)
CRD_OPTIONS ?= "crd"

# Get the currently used golang install path (in GOPATH/bin, unless GOBIN is set)
ifeq (,$(shell go env GOBIN))
GOBIN=$(shell go env GOPATH)/bin
else
GOBIN=$(shell go env GOBIN)
endif


# Run tests
ENVTEST_ASSETS_DIR=$(shell pwd)/testbin

golang-tests:
	scripts/evergreen/unit-tests.sh

golang-tests-race:
	USE_RACE=true scripts/evergreen/unit-tests.sh

sbom-tests:
	@ scripts/evergreen/run_python.sh -m pytest generate_ssdlc_report_test.py

# e2e tests are also in python and we will need to ignore them as they are in the docker/mongodb-kubernetes-tests folder
# additionally, we have one lib which we want to test which is in the =docker/mongodb-kubernetes-tests folder.
python-tests:
	@ scripts/evergreen/run_python.sh -m pytest docker/mongodb-kubernetes-tests/kubeobject
	@ scripts/evergreen/run_python.sh -m pytest --ignore=docker/mongodb-kubernetes-tests

generate-ssdlc-report:
	@ scripts/evergreen/run_python.sh generate_ssdlc_report.py

# test-race runs golang test with race enabled
test-race: generate fmt vet manifests golang-tests-race

test: generate fmt vet manifests golang-tests

# all-tests will run golang and python tests without race (used locally)
all-tests: test python-tests

# Build manager binary
manager: generate fmt vet
	GOOS=linux GOARCH=amd64 go build -o docker/mongodb-kubernetes-operator/content/mongodb-kubernetes-operator main.go

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
	export PATH="$(PATH)"; export GOROOT=$(GOROOT); $(CONTROLLER_GEN) $(CRD_OPTIONS) rbac:roleName=manager-role paths=./... output:crd:artifacts:config=config/crd/bases
	# copy the CRDs to the public folder
	cp config/crd/bases/* helm_chart/crds/
	cat "helm_chart/crds/"* > public/crds.yaml


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
	$(call go-install-tool,$(CONTROLLER_GEN),sigs.k8s.io/controller-tools/cmd/controller-gen@v0.15.0)

# Download kustomize locally if necessary
KUSTOMIZE = $(shell pwd)/bin/kustomize
kustomize:
	$(call go-install-tool,$(KUSTOMIZE),sigs.k8s.io/kustomize/kustomize/v4@v4.5.4)

# go-install-tool will 'go get' any package $2 and install it to $1.
PROJECT_DIR := $(shell dirname $(abspath $(lastword $(MAKEFILE_LIST))))
define go-install-tool
@[ -f $(1) ] || { \
set -e ;\
TMP_DIR=$$(mktemp -d) ;\
cd $$TMP_DIR ;\
go mod init tmp ;\
echo "Downloading $(2)" ;\
GOBIN=$(PROJECT_DIR)/bin go install $(2) ;\
rm -rf $$TMP_DIR ;\
}
endef

# Generate bundle manifests and metadata, then validate generated files.
.PHONY: bundle
bundle: manifests kustomize
	# we want to create a file that only has the deployment. Note: this will not work if something is added
	# after the deployment in openshift.yaml
	operator-sdk generate kustomize manifests -q
	$(KUSTOMIZE) build config/manifests | operator-sdk generate bundle -q --overwrite --version $(VERSION) $(BUNDLE_METADATA_OPTS)\
		--channels=stable --default-channel=stable\
		--output-dir ./bundle/$(VERSION)/
	operator-sdk bundle validate ./bundle/$(VERSION)


# Build the bundle image.
.PHONY: bundle-build
bundle-build:
	docker build $(EXPIRES) --platform linux/amd64 -f ./bundle/$(VERSION)/bundle.Dockerfile -t $(BUNDLE_IMG) .

.PHONY: dockerfiles
dockerfiles:
	python scripts/update_supported_dockerfiles.py
	tar -czvf ./public/dockerfiles-$(VERSION).tgz ./public/dockerfiles

prepare-local-e2e: reset-mco # prepares the local environment to run a local operator
	scripts/dev/prepare_local_e2e_run.sh

prepare-local-olm-e2e:
	DIGEST_PINNING_ENABLED=false VERSION_ID=latest scripts/evergreen/operator-sdk/prepare-openshift-bundles-for-e2e.sh
	scripts/dev/prepare_local_e2e_olm_run.sh

prepare-operator-configmap: # prepares the local environment to run a local operator
	source scripts/dev/set_env_context.sh && source scripts/funcs/printing && source scripts/funcs/operator_deployment && prepare_operator_config_map "$(kubectl config current-context)"
