SHELL := /bin/bash

all: full

export MAKEFLAGS="-j 16" # enable parallelism

usage:
	@ echo "Development utility to work with Operator on daily basis. Just edit your configuration in '~/.operator-dev/contexts', "
	@ echo "switch to it using 'make switch', make sure Ops Manager is running (use 'make om' or 'make om-evg') "
	@ echo "and call 'make' to start the Kubernetes cluster and the Operator"
	@ echo
	@ echo "Usage:"
	@ echo "  prerequisites:    installs the command line applications necessary for working with this tool and adds git pre-commit hook."
	@ echo "  init:             prepares operator environment. Switches to 'minikube' context."
	@ echo "  switch:           switch current dev context, e.g 'make switch context=minikube'. Note, that it switches"
	@ echo "                    kubectl context as well and sets the current namespace to the one configured as the default"
	@ echo "                    one"
	@ echo "  contexts:         list all available contexts"
	@ echo "  operator:         build and push Operator image, deploy it to the Kubernetes cluster"
	@ echo "                    Use the 'watch_namespace' flag to specify a namespace to watch or leave empty to watch project namespace."
	@ echo "  database:         build and push Database image"
	@ echo "  full:             ('make' is an alias for this command) ensures K8s cluster is up, cleans Kubernetes"
	@ echo "                    resources, build-push-deploy operator, push-deploy database, create secrets, "
	@ echo "                    config map, resources etc"
	@ echo "  om:               install Ops Manager into Kubernetes if it's not installed yet. Initializes the connection"
	@ echo "                    parameters in ~/operator-dev/om"
	@ echo "  om-evg:           install Ops Manager into Evergreen if it's not installed yet. Initializes the connection"
	@ echo "                    parameters in ~/operator-dev/om"
	@ echo "  reset:            cleans all Operator related state from Kubernetes and Ops Manager. Pass the 'light=true'"
	@ echo "                    to perform a \"light\" cleanup - delete only Mongodb resources"
	@ echo "  e2e:              runs the e2e test, e.g. 'make e2e test=sharded_cluster_pv'. The Operator is redeployed before"
	@ echo "                    the test, the namespace is cleaned. The e2e app image is built and pushed. Use a 'light=true'"
	@ echo "                    in case you are developing tests and not changing the Operator code - this will allow to"
	@ echo "                    avoid redeploying the Operator"
	@ echo "  recreate-e2e:     deletes and creates an e2e cluster using kops (note, that you don't need to switch to the correct"
	@ echo "                    kubectl context - the script will handle everything. Pass the flag 'imsure=yes' to make it work."
	@ echo "                    So far only kops (vanilla) cluster can be recreated this way, Openshift one should be"
	@ echo "                    done manually using instructions from e2e-faq.md"
	@ echo "  log:              reads the Operator log"
	@ echo "  status:           prints the current context and the state of Kubernetes cluster"
	@ echo "  dashboard:        opens the Kubernetes dashboard. Make sure the cluster was installed using current Makefile as"
	@ echo "                    dashboard is not installed by default and the script ensures it's installed and permissions"
	@ echo "                    are configured."

# install all necessary software, must be run only once
prerequisites:
	@ scripts/dev/install

# prepare default configuration context files
init:
	@ mkdir -p ~/.operator-dev/contexts
	@ cp scripts/dev/templates/* ~/.operator-dev/contexts
	@ $(MAKE) switch context="minikube"
	@ echo "Initialized dev environment (~/.operator-dev)"

# update current context: 'make switch context=minikube'
switch:
	@ scripts/dev/switch_context $(context)

# prints all current contexts
contexts:
	@ scripts/dev/print_contexts

# builds the Operator binary file and docker image and pushes it to the remote registry if using a remote registry. Deploys it to
# k8s cluster
operator: build-and-push-operator-image
	@ $(MAKE) deploy-operator

# build-push, (todo) restart database
database:
	@ scripts/dev/build_push_database_image

# ensures cluster is up, cleans Kubernetes + OM, build-push-deploy operator,
# push-deploy database, create secrets, config map, resources etc
full: ensure-k8s-and-reset build-and-push-images
	@ $(MAKE) deploy-and-configure-operator
	@ scripts/dev/apply_resources

# install OM in Kubernetes if it's not running
om:
	@ scripts/dev/ensure_ops_manager_k8s

# install OM in Evergreen
om-evg:
	@ scripts/dev/ensure_ops_manager_evg

log:
	@ . scripts/dev/read_context
	@ kubectl logs -f deployment/mongodb-enterprise-operator --tail=1000

# runs the e2e test: make e2e test=sharded_cluster_pv. The Operator is redeployed before the test, the namespace is cleaned.
# The e2e app image is built and pushed.
# Use 'light=true' parameter to skip Operator rebuilding - use this mode when you are focused on e2e tests development only
# Note, that this may be not perfectly the same what is done in evergreen e2e tests as the OM instance may be external
# (in Evergreen)
e2e: build-and-push-test-image reset
	@ if [[ -z "$(light)" ]]; then \
		$(MAKE) operator; \
		scripts/dev/configure_operator; \
	fi
	@ scripts/dev/launch_e2e $(test)

# deletes and creates a kops e2e cluster
recreate-e2e:
	@ scripts/dev/recreate_e2e_kops $(imsure)

# clean all kubernetes cluster resources and OM state. "light=true" to clean only Mongodb resources
reset:
	@ scripts/dev/reset $(light)

status:
	@ scripts/dev/status

dashboard:
	@ scripts/dev/kube-dashboard


###############################################################################
# Internal Targets
# These won't do anything bad if you call them, they just aren't the ones that
# were designed to be helpful by themselves. Anything below won't be documented
# in the usage target above.
###############################################################################

aws_login:
	@ eval "$(shell aws ecr get-login --no-include-email --region us-east-1)"

build-and-push-operator-image: aws_login
	@ scripts/dev/build_push_operator_image

build-and-push-database-image: aws_login
	@ scripts/dev/build_push_database_image

build-and-push-test-image: aws_login
	@ scripts/dev/build_push_tests_image

build-and-push-images: build-and-push-database-image build-and-push-operator-image

deploy-operator:
	@ scripts/dev/deploy_operator

configure-operator:
	@ scripts/dev/configure_operator

deploy-and-configure-operator: deploy-operator configure-operator

ensure-k8s:
	@ scripts/dev/ensure_k8s

ensure-k8s-and-reset: ensure-k8s
	@ $(MAKE) reset
