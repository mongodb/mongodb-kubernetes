#!/usr/bin/env bash

set -Eeou pipefail

test "${MDB_BASH_DEBUG:-0}" -eq 1 && set -x

# script prepares environment variables relevant for the current context

source scripts/funcs/errors

# Side detection: /.dockerenv exists only inside containers.
# Used to pick which .generated/context.<side>.env file we write here,
# and is the same rule used by scripts/dev/devenv at source time.
side=$([[ -f /.dockerenv ]] && echo devc || echo host)

script_name=$(readlink -f "${BASH_SOURCE[0]}")
script_dir=$(dirname "${script_name}")

destination_envs_dir="${script_dir}/../../.generated"
destination_envs_file="${destination_envs_dir}/context"

contexts_dir="scripts/dev/contexts"

context="${1:-}"
additional_override="${2:-}"

if [[ "${context}" == "" ]]; then
  # shellcheck disable=SC2012
  contexts=$(ls -1 "${contexts_dir}")
  if [[ -f "${destination_envs_dir}/.current_context" ]]; then
    current_context=$(cat "${destination_envs_dir}/.current_context")
    contexts=$(printf "${current_context}\n%s" "${contexts}")
  fi
  context="$(fzf --sort <<< "${contexts}")"
fi

if [[ "${additional_override}" != *"private-context-"* && -n "${additional_override}" ]]; then
  # shellcheck disable=SC2010
  additional_override="$(ls -1 "${contexts_dir}" | grep "private-context-" | fzf --sort)"
fi

context_file="${contexts_dir}/${context}"
local_development_default_file="${contexts_dir}/local-defaults-context"
override_context_file="${contexts_dir}/private-context-override"
additional_override_file="${contexts_dir}/${additional_override}"

mkdir -p "${destination_envs_dir}"

if [[ ! -f "${context_file}" ]]; then
	fatal "Cannot switch context: File ${context_file} does not exist."
fi

echo "Generating context files from: ${context}"


# Two-step env capture:
#   (a) site_envs — source site-context alone in an `env -i` subshell,
#       forwarding only the inputs it reads (PWD/PATH/HOME + the wt-ctl stack
#       vars + LOCAL_OPERATOR), yielding the site-derived bytes for this side.
#   (b) current_envs — source the full chain (site + local-defaults +
#       context_file + optional overrides). Locally in a second `env -i`
#       subshell; on EVG-CI in the current shell so evergreen expansion vars
#       (BUILD_VARIANT, EVR_TASK_ID, otel_*, TASK_NAME, IS_PATCH...) are
#       visible to root-context + the per-variant context file.
# logical_envs = current_envs − site_keys; both .env files are written below.

# Step (a): capture site exports alone. site-context introspects the
# running shell (PROJECT_DIR, GOROOT, /.dockerenv → KUBECONFIG variant,
# K8S_FWD_PROXY, s390x conditionals).
site_envs=$(env -i \
    PWD="${PWD}" \
    PATH="${PATH}" \
    HOME="${HOME}" \
    MCK_DEVC_NET_PREFIX="${MCK_DEVC_NET_PREFIX:-}" \
    K8S_FWD_PROXY="${K8S_FWD_PROXY:-}" \
    LOCAL_OPERATOR="${LOCAL_OPERATOR:-}" \
    bash -c "source ${contexts_dir}/site-context && export -p")

if [ -n "${EVR_TASK_ID-}" ]; then
    # EVG-CI branch (step b): run the source chain in the current shell so
    # evergreen expansion env vars (BUILD_VARIANT, IS_PATCH, ...) reach
    # site-context / root-context / evg-private-context. site-context infers
    # PROJECT_DIR from its own script location when not pre-set; we pre-set
    # it here to the EVG-runner repo root so workdir-derived defaults are
    # canonical regardless of where the script was invoked from.
    : "${PROJECT_DIR:=$(realpath "${script_dir}/../..")}"
    export PROJECT_DIR
    # shellcheck disable=SC1091
    source "${contexts_dir}/site-context"
    # shellcheck disable=SC1090
    source "${context_file}"
    # shellcheck disable=SC2207
    export CURRENT_VARIANT_CONTEXT="${context}"
    current_envs=$(export -p)
else
    # Local-dev branch (step b): env -i clean capture, so context.env only
    # picks up what the chain explicitly exports (no inherited login-shell
    # cruft from the user's terminal).
    base_command="source ${contexts_dir}/site-context"
    base_command+=" && source ${local_development_default_file}"
    base_command+=" && source ${context_file}"
    if [ -n "${additional_override}" ]; then
        echo "Using additional override file: ${additional_override_file}."
        base_command+=" && source ${additional_override_file}"
    elif [ -f "${override_context_file}" ]; then
        echo "Using override file: ${override_context_file}. If you do not want to use one, remove the file or its contents."
        base_command+=" && source ${override_context_file}"
    fi
    all_envs=$(env -i \
        PWD="${PWD}" \
        PATH="${PATH}" \
        HOME="${HOME}" \
        MCK_DEVC_NET_PREFIX="${MCK_DEVC_NET_PREFIX:-}" \
        K8S_FWD_PROXY="${K8S_FWD_PROXY:-}" \
        LOCAL_OPERATOR="${LOCAL_OPERATOR:-}" \
        CURRENT_VARIANT_CONTEXT="${context}" \
        bash -c "${base_command} && export -p")
    # `export -p` instead of `env` so we can safely eval (declare -x ... syntax)
    # to re-source the captures into our own shell for downstream consumers
    # (print_operator_env.sh).
    eval "${all_envs}"
    current_envs="${all_envs}"
fi

# Normalize both captures: drop the `declare -x ` / `export ` prefix and sort.
current_envs=$(echo "${current_envs[@]}" | grep '=' | sed 's/^declare -x //g' | sed 's/^export //g' | LC_ALL=C sort | uniq)
site_envs=$(echo "${site_envs[@]}" | grep '=' | sed 's/^declare -x //g' | sed 's/^export //g' | LC_ALL=C sort | uniq)

# Drop env -i passthrough / shell-noise keys from both captures. PWD/HOME/
# SHLVL/_ are inherited every time the script runs (they aren't site-derived),
# and the explicitly-forwarded inputs (MCK_DEVC_NET_PREFIX, LOCAL_OPERATOR,
# CURRENT_VARIANT_CONTEXT) are logical configuration — they belong in
# context.env, not context.<side>.env.
#
# PATH is special: in EVG-CI the agent's shell PATH does NOT include
# /opt/golang/go1.25/bin — evg-private-context prepends it at source time.
# Filtering PATH there would strip the prepend, leaving downstream tasks
# (build_kubectl_plugin's `go build`, etc.) without `go` on PATH. Local-dev
# shells already have go on PATH via the user's profile, so the strip is
# safe — keep it filtered there.
if [ -n "${EVR_TASK_ID-}" ]; then
    passthrough_re='^(PWD|HOME|SHLVL|_|MCK_DEVC_NET_PREFIX|LOCAL_OPERATOR|CURRENT_VARIANT_CONTEXT)='
    current_passthrough_re='^(PWD|HOME|SHLVL|_)='
else
    passthrough_re='^(PWD|PATH|HOME|SHLVL|_|MCK_DEVC_NET_PREFIX|LOCAL_OPERATOR|CURRENT_VARIANT_CONTEXT)='
    current_passthrough_re='^(PWD|PATH|HOME|SHLVL|_)='
fi
site_envs=$(echo "${site_envs}" | grep -Ev "${passthrough_re}" || true)
current_envs=$(echo "${current_envs}" | grep -Ev "${current_passthrough_re}" || true)

# Logical = current_envs minus any line whose KEY appears in site_envs.
# Variable names per POSIX are [A-Za-z_][A-Za-z0-9_]*, so '|' can never
# appear in a name — safe to use as the alternation separator. We also
# recompute site_envs from the FULL chain so any later overrides
# (private-context's GOPATH=~/gosd, etc.) land in context.<side>.env.
site_keys=$(echo "${site_envs}" | sed 's/=.*//' | LC_ALL=C sort -u)
if [ -n "${site_keys}" ]; then
    keys_alt=$(echo "${site_keys}" | paste -sd '|' -)
    logical_envs=$(echo "${current_envs}" | awk -F= -v keys="${keys_alt}" '
        $1 !~ "^("keys")$" { print }
    ')
    site_envs=$(echo "${current_envs}" | awk -F= -v keys="${keys_alt}" '
        $1 ~ "^("keys")$" { print }
    ')
else
    logical_envs="${current_envs}"
fi

# Write env files atomically: stream stdin into a tmpfile in the same
# directory, then rename over the destination. A concurrent reader (any of
# the ~40 scripts that source set_env_context.sh, some in parallel make
# targets / background jobs) then always sees either the old file or the new
# one — never a half-written truncation.
write_atomic() {
    local dest="$1" tmp
    tmp="$(mktemp "${dest}.XXXXXX")"
    cat > "${tmp}"
    mv -f "${tmp}" "${dest}"
}

{
    echo -e "## This file is automatically generated by switch_context.sh\n## Do not edit it!"
    echo -e "## Regenerated at $(date)\n"
    echo "${logical_envs}"
} | write_atomic "${destination_envs_file}.env"

# Write the per-side site bytes to .generated/context.<side>.env. The other
# side's file is left untouched, so a host `make switch` does not clobber the
# devc's site bytes (and vice versa). EVG-CI lands on side=host (no /.dockerenv
# on the runner) and gets context.host.env exactly like a laptop host shell.
site_destination="${destination_envs_dir}/context.${side}.env"
{
    echo -e "## This file is automatically generated by switch_context.sh\n## Do not edit it!"
    echo -e "## Regenerated at $(date)\n## Side: ${side}\n"
    echo "${site_envs}"
} | write_atomic "${site_destination}"

# Generate the operator overlay, stripping site-derived bytes
# (KUBECONFIG and KUBE_CONFIG_PATH come from .generated/context.<side>.env).
scripts/dev/print_operator_env.sh \
    | LC_ALL=C sort | uniq \
    | grep -Ev '^(KUBECONFIG|KUBE_CONFIG_PATH)=' \
    | write_atomic "${destination_envs_file}.operator.env"

# This generator does not emit .export.env files; consumers load context.env +
# context.<side>.env via scripts/dev/devenv (or set_env_context.sh). Remove any
# such files so a stale copy can't be sourced by accident.
rm -f "${destination_envs_file}.export.env" "${destination_envs_file}.operator.export.env"

echo -n "${context}" > "${destination_envs_dir}/.current_context"

# Persist the resolved (prefix-suffixed) namespace as a single source of
# truth for downstream tools, analogous to .current-evg-host. Cheaper than
# re-deriving from context.env via sed/grep, and clearer in `ls .generated/`.
if [ -n "${NAMESPACE:-}" ]; then
    echo -n "${NAMESPACE}" > "${destination_envs_dir}/.current-namespace"
fi

echo "Generated env files in $(readlink -f "${destination_envs_dir}"):"
# shellcheck disable=SC2010
ls -l1 "${destination_envs_dir}" | grep "context"

# kubectl current-context/namespace mutation. Skipped when
# MCK_SWITCH_NO_KUBECTL=1 (set by scripts/dev/devenv's opt-in auto-regen path),
# so that merely sourcing set_env_context.sh only rewrites the env files and
# never silently repoints the user's kubectl context/namespace.
if [[ "${MCK_SWITCH_NO_KUBECTL:-}" == "1" ]]; then
    echo "MCK_SWITCH_NO_KUBECTL set — skipping kubectl context/namespace mutation."
    exit 0
fi

KUBECTL_CMD="kubectl"
if [[ -n "${PROJECT_DIR:-}" && -x "${PROJECT_DIR}/bin/kubectl" ]]; then
    KUBECTL_CMD="${PROJECT_DIR}/bin/kubectl"
fi

if [[ "${KUBECTL_CMD}" != "kubectl" ]] || which kubectl > /dev/null; then
    if [ "${CLUSTER_NAME-}" ]; then
        # The convention: the cluster name must match the name of kubectl context.
        # Tolerated failure modes: kubernetes cluster still to be created
        # (minikube/kops), or this runs inside the devcontainer at on-create time
        # before the devc-side kubeconfig has been populated by the orchestrator's
        # later phases — silence stderr so the kubectl error doesn't look like a
        # real failure in the log.
        if ! "${KUBECTL_CMD}" config use-context "${CLUSTER_NAME}" 2>/dev/null; then
            echo "Warning: failed to switch kubectl context to: ${CLUSTER_NAME}"
            echo "Does a matching Kubernetes context exist?"
        fi

        # Set the default namespace on the current context if there is one.
        # Split the read so a failing inner command substitution (no contexts
        # at all) doesn't trip set -e under newer bash.
        current_ctx="$("${KUBECTL_CMD}" config current-context 2>/dev/null || true)"
        if [[ -n "${current_ctx}" ]]; then
            "${KUBECTL_CMD}" config set-context "${current_ctx}" "--namespace=${NAMESPACE}" &>/dev/null || true
        fi

        # shellcheck disable=SC2153
        echo "Generated context: ${context}, set default kubectl context: ${CLUSTER_NAME}, namespace=${NAMESPACE}"
    fi
else
    echo "Kubectl doesn't exist, skipping setting the context"
fi
