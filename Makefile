SHELL := /bin/bash

all: full

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
	@ echo "  om-batch                    builds both Init Ops Manager and AppDB images."
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
	@ scripts/dev/build_push_database_image

# ensures cluster is up, cleans Kubernetes + OM, build-push-deploy operator,
# push-deploy database, create secrets, config map, resources etc
full: ensure-k8s-and-reset build-and-push-images
	@ $(MAKE) deploy-and-configure-operator
	@ scripts/dev/apply_resources

om-batch: aws_login
	@ scripts/dev/batch_init_om_appdb_images.sh

# build-push appdb image
appdb: aws_login
	@ om_version=$(om_version) scripts/dev/build_push_appdb_image.sh

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
		scripts/dev/build_push_tests_image; \
	fi

# builds all app images in parallel
# note that we cannot build both appdb and database init images in parallel as they change the same docker file
build-and-push-images: build-and-push-operator-image appdb-init-image om-init-image
	@ $(MAKE) database-init-image

database-init-image:
	@ scripts/dev/build_push_init_database_image.sh

appdb-init-image:
	@ scripts/dev/build_push_init_appdb_image.sh

om-init-image:
	@ scripts/dev/build_push_init_opsmanager_image.sh

deploy-operator:
	@ scripts/dev/deploy_operator.sh $(debug)

configure-operator:
	@ scripts/dev/configure_operator.sh

deploy-and-configure-operator: deploy-operator configure-operator

ensure-k8s:
	@ scripts/dev/ensure_k8s.sh

ensure-k8s-and-reset: ensure-k8s
	@ $(MAKE) reset

