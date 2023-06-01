#!/usr/bin/env bash
set -Eeou pipefail

if [[ "${kube_environment_name-}" != "minikube" ]]; then
  echo "Skiping download of Minikube"
  exit 0
fi

if ! command -v minikube &> /dev/null ; then
  mkdir -p "${workdir:?}/bin/"
  curl --retry 3 --silent -L https://storage.googleapis.com/minikube/releases/latest/minikube-linux-amd64 -o "${workdir}/bin/minikube"
  chmod +x "${workdir}/bin/minikube"
  echo "Installed Minikube in ${workdir}/bin"
else
  echo "Minikube is already present in this host"
  minikube version
fi
