all: full

usage:
	@ echo "Development utility to work with Operator on daily basis. Just edit your configuration in '~/.operator-dev/contexts', "
	@ echo "switch to it using 'make switch', make sure Ops Manager is running (use 'make om' or 'make om-evg') "
	@ echo "and call 'make' to start the Kubernetes cluster and the Operator"
	@ echo
	@ echo "Usage:"
	@ echo "  prerequisites:    installs the command line applications necessary for working with this tool."
	@ echo "  init:             prepares operator environment. Switches to 'minikube' context."
	@ echo "  switch:           switch current dev context, e.g 'make switch context=minikube'. Note, that it switches"
	@ echo "                    kubectl context as well and sets the current namespace to the one configured as the default"
	@ echo "                    one"
	@ echo "  contexts:         list all available contexts"
	@ echo "  operator:         build and push Operator image, deploy it to the Kubernetes cluster"
	@ echo "  database:         build and push Database image"
	@ echo "  full:             ('make' is an alias for this command) ensures K8s cluster is up, cleans Kubernetes"
	@ echo "                    resources, build-push-deploy operator, push-deploy database, create secrets, "
	@ echo "                    config map, resources etc"
	@ echo "  om:               install Ops Manager into Kubernetes if it's not installed yet. Initializes the connection"
	@ echo "                    parameters in ~/operator-dev/om"
	@ echo "  om-evg:           install Ops Manager into Evergreen if it's not installed yet. Initializes the connection"
	@ echo "                    parameters in ~/operator-dev/om"
	@ echo "  reset:            cleans all Operator related state from Kubernetes and Ops Manager"
	@ echo "  e2e:              runs the e2e test, e.g. 'make e2e test=sharded_cluster_pv'. The Operator is redeployed before"
	@ echo "                    the test, the namespace is cleaned"
	@ echo "  log:              reads the Operator log"
	@ echo "  status:           prints the current context and the state of Kubernetes cluster"

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
operator:
	@ scripts/dev/build_push_operator_image
	@ scripts/dev/deploy_operator

# build-push, (todo) restart database
database:
	@ scripts/dev/build_push_database_image

# ensures cluster is up, cleans Kubernetes + OM, build-push-deploy operator,
# push-deploy database, create secrets, config map, resources etc
full:
	@ scripts/dev/ensure_k8s
	@ $(MAKE) reset
	@ $(MAKE) operator
	@ $(MAKE) database
	@ scripts/dev/configure_operator
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

# runs the e2e test: make e2e test=sharded_cluster_pv. The Operator is redeployed before the test, the namespace is cleaned
# note, that this may be not perfectly the same what is done in evergreen e2e tests as the OM instance may be external
# (in Evergreen)
e2e:
	@ $(MAKE) reset
	@ $(MAKE) operator
	@ scripts/dev/configure_operator
	@ scripts/dev/launch_e2e $(test)

# clean all kubernetes cluster resources and OM state
reset:
	@ scripts/dev/reset

status:
	@ scripts/dev/status
