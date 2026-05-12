#!/usr/bin/env bash

set -Eeou pipefail
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


# This means we are running on evergreen, in this case we need the environment variables from evg expansions.
# If running locally, we don't need them since they are defined in the private-context already, so we don't need
# any kind of current env var expansions
if [ -n "${EVR_TASK_ID-}" ]; then
    # site-context expects PROJECT_DIR to be set; on EVG we derive it from
    # the script location (the EVG runner has its own filesystem layout).
    : "${PROJECT_DIR:=$(realpath "${script_dir}/../..")}"
    export PROJECT_DIR
    # site-context computes other site-derived bytes for THIS side
    # (the EVG runner — which has no /.dockerenv → side=host).
    # shellcheck disable=SC1091
    source "${contexts_dir}/site-context"
    # shellcheck disable=SC1090
    source "${context_file}"
    # shellcheck disable=SC2207
    export CURRENT_VARIANT_CONTEXT="${context}"
    current_envs=$(export -p)
else
    # env -i makes sure to start the shell with an empty shell, such that we only save into context.env the env vars we have
    # defined.

    # Step (a): capture site exports alone. site-context introspects the
    # running shell (PROJECT_DIR, GOROOT, /.dockerenv → KUBECONFIG variant,
    # K8S_FWD_PROXY, s390x conditionals).
    site_envs=$(env -i \
        PWD="${PWD}" \
        PATH="${PATH}" \
        HOME="${HOME}" \
        MCK_DEVC_NET_PREFIX="${MCK_DEVC_NET_PREFIX:-}" \
        K8S_FWD_PROXY="${K8S_FWD_PROXY:-}" \
        EVG_HOST_NAME="${EVG_HOST_NAME:-}" \
        LOCAL_OPERATOR="${LOCAL_OPERATOR:-}" \
        bash -c "source ${contexts_dir}/site-context && export -p")

    # Step (b): capture site + logical together. Source site first (so
    # PROJECT_DIR is set for root-context), then local-defaults, then the
    # per-context profile (which sources root-context), then any override
    # file. The result includes both site and logical exports.
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
    # Execute the command in a clean environment and capture exported variables.
    # Use our PATH as a base so utilities are available.
    all_envs=$(env -i \
        PWD="${PWD}" \
        PATH="${PATH}" \
        HOME="${HOME}" \
        MCK_DEVC_NET_PREFIX="${MCK_DEVC_NET_PREFIX:-}" \
        K8S_FWD_PROXY="${K8S_FWD_PROXY:-}" \
        EVG_HOST_NAME="${EVG_HOST_NAME:-}" \
        LOCAL_OPERATOR="${LOCAL_OPERATOR:-}" \
        CURRENT_VARIANT_CONTEXT="${context}" \
        bash -c "${base_command} && export -p")

    # `export -p` instead of `env` ensures we can safely re-source variables
    # which we rely on further below like print_operator_env.sh.
    # eval ensures we only use the exports and don't run the whole script
    # again as done in base_command.
    eval "${all_envs}"

    # Keep current_envs as the canonical "everything captured" view for the
    # post-processing below.
    current_envs="${all_envs}"
fi

# convert declare -x key=value or export key=value into key=value
# filter out variables that don't have value (missing '=')
current_envs=$(echo "${current_envs[@]}" | grep '=' | sed 's/^declare -x //g' | sed 's/^export //g' | LC_ALL=C sort | uniq)

if [ -z "${EVR_TASK_ID-}" ]; then
    # Same normalization for the site-only capture (local-dev branch only;
    # the EVG-CI branch doesn't use site_envs).
    site_envs=$(echo "${site_envs[@]}" | grep '=' | sed 's/^declare -x //g' | sed 's/^export //g' | LC_ALL=C sort | uniq)

    # Drop the env -i passthrough keys from both captures. These are vars
    # we forwarded into the env -i subshell so site-context could read
    # them as inputs; they show up in `export -p` because env -i marks
    # them exported, but they are NOT site-derived and we don't want
    # them written to either context.<side>.env (PATH, HOME, PWD, SHLVL)
    # or stripped from context.env when they are logically configured
    # (EVG_HOST_NAME, MCK_DEVC_NET_PREFIX, LOCAL_OPERATOR, CURRENT_VARIANT_CONTEXT).
    passthrough_re='^(PWD|PATH|HOME|SHLVL|_|MCK_DEVC_NET_PREFIX|EVG_HOST_NAME|LOCAL_OPERATOR|CURRENT_VARIANT_CONTEXT)='
    site_envs=$(echo "${site_envs}" | grep -Ev "${passthrough_re}" || true)
    current_envs=$(echo "${current_envs}" | grep -Ev '^(PWD|PATH|HOME|SHLVL|_)=' || true)

    # Logical = current_envs minus any line whose KEY appears in site_envs.
    # Variable names per POSIX are [A-Za-z_][A-Za-z0-9_]*, so '|' can never
    # appear in a name — safe to use as the alternation separator.
    site_keys=$(echo "${site_envs}" | sed 's/=.*//' | LC_ALL=C sort -u)
    if [ -n "${site_keys}" ]; then
        keys_alt=$(echo "${site_keys}" | paste -sd '|' -)
        logical_envs=$(echo "${current_envs}" | awk -F= -v keys="${keys_alt}" '
            $1 !~ "^("keys")$" { print }
        ')
        # Recompute site_envs from the FULL chain (current_envs) so any
        # user overrides applied later in the source chain (private-context,
        # override files) are preserved in context.<side>.env. Without
        # this, e.g. `export GOPATH=~/gosd` in private-context would be
        # silently replaced by site-context's default in the written file.
        site_envs=$(echo "${current_envs}" | awk -F= -v keys="${keys_alt}" '
            $1 ~ "^("keys")$" { print }
        ')
    else
        logical_envs="${current_envs}"
    fi
else
    # EVG-CI branch: no per-side split. Treat everything as logical.
    logical_envs="${current_envs}"
    site_envs=""
fi

echo -e "## This file is automatically generated by switch_context.sh\n## Do not edit it!" > "${destination_envs_file}.env"
# shellcheck disable=SC2129
echo -e "## Regenerated at $(date)\n" >> "${destination_envs_file}.env"
echo "${logical_envs}" >> "${destination_envs_file}.env"

# EVG-CI fallback: `workdir` is a logical alias used by evergreen build
# scripts when they run on EVG runners (no per-side split there). For
# local development, workdir is site-derived (set by site-context to
# ${PROJECT_DIR}) and lives in context.<side>.env — the EVR_TASK_ID branch
# above writes everything to context.env, so this fallback only fires for
# CI when neither chain set workdir.
if [ -n "${EVR_TASK_ID-}" ] && ! echo "${logical_envs}" | grep -q "^workdir="; then
    echo "workdir=\"${workdir:-.}\"" >> "${destination_envs_file}.env"
fi

# Write the per-side site bytes to .generated/context.<side>.env. The other
# side's file is left untouched, so a host `make switch` does not clobber the
# devc's site bytes (and vice versa).
if [ -z "${EVR_TASK_ID-}" ]; then
    site_destination="${destination_envs_dir}/context.${side}.env"
    echo -e "## This file is automatically generated by switch_context.sh\n## Do not edit it!" > "${site_destination}"
    # shellcheck disable=SC2129
    echo -e "## Regenerated at $(date)\n## Side: ${side}\n" >> "${site_destination}"
    echo "${site_envs}" >> "${site_destination}"
fi

# Generate the operator overlay, stripping site-derived bytes
# (KUBECONFIG and KUBE_CONFIG_PATH come from .generated/context.<side>.env).
scripts/dev/print_operator_env.sh \
    | LC_ALL=C sort | uniq \
    | grep -Ev '^(KUBECONFIG|KUBE_CONFIG_PATH)=' \
    > "${destination_envs_file}.operator.env"

# Drop legacy .export.env files on regenerate so stale copies from prior runs
# (when the generator still emitted them) don't get sourced by accident.
# All consumers now go through scripts/dev/devenv (or set_env_context.sh which
# wraps it) and load context.env + context.<side>.env directly.
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

# Prefer kubectl from bin directory if it exists, otherwise use system kubectl
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
