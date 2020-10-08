#!/usr/bin/env bash
set -Eeou pipefail

if [[ "${kube_environment_name-}" != "minikube" ]]; then
  echo "Skiping download of Minikube"
  exit 0
fi

if ! command -v minikube &> /dev/null ; then
  mkdir -p "${workdir:?}/bin/"
  curl -Lo minikube https://storage.googleapis.com/minikube/releases/latest/minikube-linux-amd64
  chmod +x minikube
  echo "Saving Minikube to ${workdir}/bin"
  mv minikube "${workdir}/bin"
  echo "Installed Minikube in ${workdir}/bin"
  which minikube
else
  echo "Minikube is already present in this host"
  minikube version
fi
