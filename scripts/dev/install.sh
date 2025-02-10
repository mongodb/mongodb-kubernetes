#!/usr/bin/env bash
set -Eeou pipefail

source scripts/funcs/printing
source scripts/dev/set_env_context.sh

title "The following tools will be installed: kubectl, kops, helm, coreutils"
echo "Note, that you must download 'go' and Docker by yourself"

grep -a "KOPS_STATE_STORE='s3://kube-om-state-store'" ~/.bashrc || echo "export KOPS_STATE_STORE='s3://kube-om-state-store'" >> ~/.bashrc
grep -a "/usr/local/opt/coreutils/libexec/gnubin:\$PATH" ~/.bashrc || echo "PATH=\"/usr/local/opt/coreutils/libexec/gnubin:\$PATH\"" >> ~/.bashrc

if [ "$(uname)" = "Darwin" ] ; then
  # kubectl
  brew install kubectl

  # kops
  brew install kops  || true

  # helm
  brew install helm  || true

  # coreutils
  brew install coreutils  || true

  # kind
  brew install kind  || true

  # jq
  brew install jq

  brew install shellcheck

elif [ "$(uname)" = "Linux" ] ; then # Ubuntu only
  sudo snap install kubectl --classic  || true

  kops_version="$(curl -s https://api.github.com/repos/kubernetes/kops/releases/latest | grep tag_name | cut -d '"' -f 4)"
  curl -Lo kops "https://github.com/kubernetes/kops/releases/download/${kops_version}/kops-linux-amd64"
  echo "hi"
  chmod +x kops
  mv kops "${GOBIN}"  || true

  sudo snap install helm --classic  || true

  # Kind
  go install sigs.k8s.io/kind

  sudo snap install --channel=edge shellcheck

else
  echo "This only works on OSX & Ubuntu - please install the tools yourself. Sorry!"
  exit 1
fi

echo "Installing Python packages"
PIP_CONSTRAINT=constraints.txt python -m pip install -r requirements.txt

title "Tools are installed"
