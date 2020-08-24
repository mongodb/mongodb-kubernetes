#!/usr/bin/env bash
set -Eeou pipefail

source scripts/funcs/printing

title "The following tools will be installed: kubectl, kops, helm, coreutils"
echo "Note, that you must download 'go' and Docker by yourself"

grep -a "GO111MODULE=on" ~/.bashrc || echo "export GO111MODULE=on" >> ~/.bashrc
grep -a 'GOFLAGS="-mod=vendor"' ~/.bashrc || echo 'export GOFLAGS="-mod=vendor"' >> ~/.bashrc
grep -a "KOPS_STATE_STORE='s3://kube-om-state-store'" ~/.bashrc || echo "export KOPS_STATE_STORE='s3://kube-om-state-store'" >> ~/.bashrc
grep -a "/usr/local/opt/coreutils/libexec/gnubin:\$PATH" ~/.bashrc || echo "PATH=\"/usr/local/opt/coreutils/libexec/gnubin:\$PATH\"" >> ~/.bashrc

if [ "$(uname)" = "Darwin" ] ; then
  # kubectl 1.16.1
  brew install https://raw.githubusercontent.com/Homebrew/homebrew-core/d9f05abd70f5dc3519a58344f4bd3ec76ccea351/Formula/kubernetes-cli.rb || true

  # kops
  brew install kops  || true

  # helm
  brew install kubernetes-helm  || true

  # coreutils
  brew install coreutils  || true

  # kind
  brew install kind  || true

  # jq
  brew install jq

  # docker-credential-helper-ecr
  brew install docker-credential-helper-ecr

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
  go get sigs.k8s.io/kind

  # docker-credential-helper-ecr
  curl --retry 3 --silent -LO https://amazon-ecr-credential-helper-releases.s3.us-east-2.amazonaws.com/0.3.1/linux-amd64/docker-credential-ecr-login
  chmod +x docker-credential-ecr-login
  mv docker-credential-ecr-login /usr/local/bin

  sudo snap install --channel=edge shellcheck

else
  echo "This only works on OSX & Ubuntu - please install the tools yourself. Sorry!"
  exit 1
fi

echo "Installing Python packages"
pip3 install -r docker/mongodb-enterprise-tests/requirements-dev.txt

echo "Adding git commit hooks"
ln -s -f ../../scripts/dev/commit_hooks/pre-commit.sh .git/hooks/pre-commit

title "Tools are installed"
