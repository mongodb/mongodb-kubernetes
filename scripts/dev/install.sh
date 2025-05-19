#!/usr/bin/env bash
set -Eeou pipefail

source scripts/funcs/printing
source scripts/dev/set_env_context.sh

tools="kubectl helm coreutils kind jq shellcheck python@${PYTHON_VERSION}"
echo "The following tools will be installed using homebrew: ${tools}"
echo "Note, that you must download 'go' and Docker by yourself"

grep -a "/usr/local/opt/coreutils/libexec/gnubin:\$PATH" ~/.bashrc || echo "PATH=\"/usr/local/opt/coreutils/libexec/gnubin:\$PATH\"" >> ~/.bashrc

if [ "$(uname)" = "Darwin" ] ; then
  # shellcheck disable=SC2086
  brew install ${tools}  2>&1 | prepend "brew install"
elif [ "$(uname)" = "Linux" ] ; then # Ubuntu only
  sudo snap install kubectl --classic  || true

  kops_version="$(curl -s https://api.github.com/repos/kubernetes/kops/releases/latest | grep tag_name | cut -d '"' -f 4)"
  curl -Lo kops "https://github.com/kubernetes/kops/releases/download/${kops_version}/kops-linux-amd64"
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
scripts/dev/recreate_python_venv.sh 2>&1 | prepend "recreate_python_venv.sh"

title "Tools are installed"
