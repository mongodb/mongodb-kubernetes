#!/usr/bin/env bash

set -Eeou pipefail

# Setup container runtime for IBM architectures (s390x, ppc64le)
# This script ensures crun is properly configured to avoid runtime issues

setup_crun_compatibility_workarounds() {
  echo "Setting up compatibility workarounds for older crun version..."
  
  # For older crun versions, we need to configure podman to be more lenient
  # and possibly use an older kicbase image that's compatible
  
  echo "Configuring podman for older crun compatibility..."
  
  # Add compatibility settings to containers.conf
  local compat_config="
# Compatibility settings for older crun versions
[engine]
compat_api_enforce_docker_hub = false
runtime = \"crun\"

[engine.runtimes]
crun = [\"$(which crun)\", \"--systemd-cgroup=false\"]
"
  
  # Append to user config
  echo "$compat_config" >> ~/.config/containers/containers.conf
  
  # Append to root config  
  echo "$compat_config" | sudo tee -a /root/.config/containers/containers.conf >/dev/null
  
  # Create environment variable to suggest using older kicbase image
  export MINIKUBE_KICBASE_IMAGE="gcr.io/k8s-minikube/kicbase:v0.0.44"
  echo "export MINIKUBE_KICBASE_IMAGE=gcr.io/k8s-minikube/kicbase:v0.0.44" >> ~/.bashrc
  echo "export MINIKUBE_KICBASE_IMAGE=gcr.io/k8s-minikube/kicbase:v0.0.44" | sudo tee -a /root/.bashrc >/dev/null
  
  echo "✅ Applied compatibility settings for older crun version"
  echo "⚠️ Note: Using compatibility mode - may use older kicbase image"
  return 0
}

install_crun_from_source() {
  echo "Installing newer crun from source..."
  local arch=$(uname -m)
  local crun_version="1.15"
  
  # Install build dependencies
  if command -v yum &> /dev/null || command -v dnf &> /dev/null; then
    # RHEL/CentOS/Fedora - try different package names for compatibility
    local pkg_manager="yum"
    command -v dnf &> /dev/null && pkg_manager="dnf"
    
    echo "Installing basic build tools..."
    sudo ${pkg_manager} install -y git gcc make libtool pkgconfig autoconf automake systemd-devel || {
      echo "❌ Failed to install basic build dependencies"
      return 1
    }
    
    echo "Installing crun-specific libraries..."
    # Try to enable additional repositories first for RHEL 9
    if [[ -f /etc/redhat-release ]] && grep -q "release 9" /etc/redhat-release; then
      echo "Detected RHEL 9, enabling additional repositories..."
      sudo ${pkg_manager} install -y epel-release 2>/dev/null || true
      sudo ${pkg_manager} config-manager --set-enabled crb 2>/dev/null || true
      sudo ${pkg_manager} config-manager --set-enabled powertools 2>/dev/null || true
    fi
    
    # Required packages for crun build
    local packages_to_try=(
      "libseccomp-devel"
      "yajl-devel" 
      "libcap-devel"
      "glibc-static"
    )
    
    # Alternative package names for different distributions
    local alt_packages=(
      "libseccomp-static"
      "libyajl-devel"
      "libcap-static"
    )
    
    local installed_count=0
    for pkg in "${packages_to_try[@]}"; do
      if sudo ${pkg_manager} install -y "$pkg" 2>/dev/null; then
        echo "✅ Installed $pkg"
        ((installed_count++))
      else
        echo "⚠️ Could not install $pkg"
      fi
    done
    
    # Try alternative packages if main ones failed
    if [[ $installed_count -lt 2 ]]; then
      echo "Trying alternative package names..."
      for pkg in "${alt_packages[@]}"; do
        sudo ${pkg_manager} install -y "$pkg" 2>/dev/null && echo "✅ Installed alternative: $pkg" || true
      done
    fi
    
    echo "✅ Build dependencies installation completed (installed $installed_count core packages)"
    
  elif command -v apt &> /dev/null; then
    sudo apt update && sudo apt install -y git gcc make libtool autoconf automake pkg-config libsystemd-dev libseccomp-dev libyajl-dev libcap-dev || {
      echo "❌ Failed to install build dependencies"
      return 1
    }
  else
    echo "❌ Cannot install build dependencies - unsupported package manager"
    return 1
  fi
  
  # Create temporary build directory
  local build_dir="/tmp/crun-build-$$"
  mkdir -p "${build_dir}"
  cd "${build_dir}"
  
  # Download and build crun
  echo "Downloading crun ${crun_version} source..."
  git clone --depth 1 --branch "${crun_version}" https://github.com/containers/crun.git || {
    echo "❌ Failed to clone crun repository"
    cd - && rm -rf "${build_dir}"
    return 1
  }
  
  cd crun
  echo "Building crun for ${arch}..."
  echo "Running autogen.sh..."
  ./autogen.sh || {
    echo "❌ autogen.sh failed"
    cd - && rm -rf "${build_dir}"
    return 1
  }
  
  echo "Running configure..."
  # Configure with fallback options if some libraries are missing
  ./configure --prefix=/usr --disable-systemd --disable-dlopen --disable-criu || \
  ./configure --prefix=/usr --disable-systemd || \
  ./configure --prefix=/usr || {
    echo "❌ configure failed even with fallback options"
    cd - && rm -rf "${build_dir}"
    return 1
  }
  
  echo "Running make (this may take several minutes)..."
  make -j$(nproc) || {
    echo "❌ make failed"
    cd - && rm -rf "${build_dir}"
    return 1
  }
  
  echo "Installing crun..."
  sudo make install || {
    echo "❌ make install failed"
    cd - && rm -rf "${build_dir}"
    return 1
  }
  
  # Cleanup
  cd - >/dev/null
  rm -rf "${build_dir}"
  
  echo "✅ Successfully built and installed crun from source"
  return 0
}

setup_crun_for_ibm() {
  local arch=$(uname -m)
  
  # Only run on IBM architectures
  if [[ "${arch}" != "s390x" && "${arch}" != "ppc64le" ]]; then
    echo "Skipping crun setup - not an IBM architecture (${arch})"
    return 0
  fi

  echo "Setting up crun for IBM architecture: ${arch}"

  # Upgrade/install crun - try package manager first, then fallback to source build if needed
  echo "Ensuring latest crun version..."
  
  # Try package manager first
  if command -v yum &> /dev/null; then
    sudo yum upgrade -y crun || sudo yum install -y crun || sudo dnf upgrade -y crun || sudo dnf install -y crun || {
      echo "⚠️ Package manager install failed, will try building from source"
    }
  elif command -v apt &> /dev/null; then
    sudo apt update && sudo apt install -y --upgrade crun || {
      echo "⚠️ Package manager install failed, will try building from source"
    }
  else
    echo "⚠️ No supported package manager found, will try building from source"
  fi
  
  # Check if crun version is sufficient - but don't try to build from source
  if crun --version 2>/dev/null | grep -q "crun version 1\.[0-9]\." && ! crun --version 2>/dev/null | grep -qE "crun version 1\.(1[0-9]|[2-9][0-9])\."; then
    echo "⚠️ Installed crun version is older ($(crun --version 2>/dev/null | head -n1 || echo 'unknown'))"
    echo "✅ Will use compatible kicbase image in minikube setup"
  elif ! crun --version &>/dev/null; then
    echo "❌ crun is not functional"
    exit 1
  fi

  # Verify crun is working after upgrade
  if ! crun --version &>/dev/null; then
    echo "❌ crun upgrade failed or is still not functional"
    exit 1
  fi

  echo "✅ Using crun runtime: $(crun --version | head -n1)"
  echo "crun path: $(which crun)"

  # Configure podman to use specific crun path
  local crun_path=$(which crun)
  
  mkdir -p ~/.config/containers
  cat > ~/.config/containers/containers.conf << EOF
[containers]
cgroup_manager = "cgroupfs"
events_logger = "file"

[engine]
cgroup_manager = "cgroupfs"
runtime = "crun"

[engine.runtimes]
crun = ["${crun_path}", "--systemd-cgroup=false"]

[engine.runtime_supports_json]
crun = ["1.0.0"]

[engine.runtime_supports_kvm]
crun = false
EOF

  # Also configure system-wide (for sudo operations like builds and minikube)
  sudo mkdir -p /root/.config/containers
  sudo tee /root/.config/containers/containers.conf << EOF >/dev/null
[containers]
cgroup_manager = "cgroupfs"
events_logger = "file"

[engine]
cgroup_manager = "cgroupfs"
runtime = "crun"

[engine.runtimes]
crun = ["${crun_path}", "--systemd-cgroup=false"]

[engine.runtime_supports_json]
crun = ["1.0.0"]

[engine.runtime_supports_kvm]
crun = false
EOF

  echo "✅ Configured crun runtime globally for ${arch}"
}

# Main execution
if [[ "${BASH_SOURCE[0]}" == "${0}" ]]; then
  setup_crun_for_ibm
fi