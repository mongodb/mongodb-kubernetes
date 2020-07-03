#!/usr/bin/env bash
set -Eeou pipefail

# setup_kind configures ecr credentials and placee them in
# ~/.docker/kind_config.json a config file in ~/.operator-dev/kind-ecr-config.yaml
# which can be used to with the --config flag when creating a kind cluster

docker_config=$(mktemp)
scripts/dev/configure_docker "${1}" > "${docker_config}"

mkdir -p "$HOME/.operator-dev"
# We make sure this patched docker/config.json file is mounted on each Kind node
cat <<EOF > "${HOME}/.operator-dev/kind-ecr-config.yaml"
kind: Cluster
apiVersion: kind.sigs.k8s.io/v1alpha3
nodes:
- role: control-plane
  extraMounts:
  - containerPath: /var/lib/kubelet/config.json
    hostPath: ${docker_config}
EOF
