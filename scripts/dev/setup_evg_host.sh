#!/usr/bin/env bash

# this script downloads necessary tooling in EVG host

set -Eeou pipefail

echo "Increasing fs.inotify.max_user_instances"
sudo sysctl -w fs.inotify.max_user_instances=8192

download_kind() {
  echo "Downloading kind..."
  curl -s -o ./kind -L https://kind.sigs.k8s.io/dl/v0.19.0/kind-linux-amd64
  chmod +x ./kind
  sudo mv ./kind /usr/local/bin/kind
}

download_curl() {
  echo "Downloading curl..."
  curl -s -o kubectl -L https://dl.k8s.io/release/"$(curl -L -s https://dl.k8s.io/release/stable.txt)"/bin/linux/amd64/kubectl
  chmod +x kubectl
  sudo mv kubectl /usr/local/bin/kubectl
}

download_helm() {
  echo "Downloading helm..."
  curl -s -o helm.tar.gz -L https://get.helm.sh/helm-v3.10.3-linux-amd64.tar.gz
  tar -xf helm.tar.gz 2>/dev/null
  sudo mv linux-amd64/helm /usr/local/bin/helm
  rm helm.tar.gz
  rm -rf linux-amd64/
}

download_kind &
download_curl &
download_helm &

wait
