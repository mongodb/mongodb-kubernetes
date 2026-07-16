echo "=============================================================================="
echo "WARNING: this step talks to the Ops Manager Automation Config REST API DIRECTLY,"
echo "bypassing the operator's own reconcile. The source is a single-cluster sharded"
echo "MongoDB with no per-process additionalMongodConfig for mongotHost in this model,"
echo "and no operator sets it for you. Because the operator never learns this value"
echo "(it never appears in any CR spec), it never overwrites it on a later reconcile."
echo "Re-run this script with a different TARGET_CLUSTER_INDEX to flip which search"
echo "cluster serves the source."
echo "=============================================================================="

# Ops Manager connection details come from what ra-06 already created on
# ${K8S_CLUSTER_0_CONTEXT_NAME} -- there are no OPS_MANAGER_API_* env vars in this
# reference-architecture chain (same derivation as scenario 12's 12_0400).
om_public_key=$(kubectl get secret mdb-org-owner-credentials -n "${MDB_NAMESPACE}" \
  --context "${K8S_CLUSTER_0_CONTEXT_NAME}" -o jsonpath='{.data.publicKey}' | base64 -d)
om_private_key=$(kubectl get secret mdb-org-owner-credentials -n "${MDB_NAMESPACE}" \
  --context "${K8S_CLUSTER_0_CONTEXT_NAME}" -o jsonpath='{.data.privateKey}' | base64 -d)
om_creds="${om_public_key}:${om_private_key}"

om_org_id=$(kubectl get configmap mdb-org-project-config -n "${MDB_NAMESPACE}" \
  --context "${K8S_CLUSTER_0_CONTEXT_NAME}" -o jsonpath='{.data.orgId}')

om_ca_cert=$(kubectl get configmap ca-issuer -n "${OM_NAMESPACE}" \
  --context "${K8S_CLUSTER_0_CONTEXT_NAME}" -o jsonpath="{.data['mms-ca\.crt']}")

# We're using the load balancer IP, hence --insecure below; normally OM should be
# accessed from inside the cluster or exposed through a properly named endpoint.
om_ip_address=$(kubectl get svc om-svc-ext -n "${OM_NAMESPACE}" \
  --context "${K8S_CLUSTER_0_CONTEXT_NAME}" -o jsonpath='{.status.loadBalancer.ingress[0].ip}')
if [[ -z "${om_ip_address}" ]]; then
  echo "Error: om-svc-ext has no LoadBalancer IP yet -- without it every OM call below fails." >&2
  echo "Check the LB provisioner (cloud LB / MetalLB on kind) and re-run once the IP is assigned." >&2
  exit 1
fi
om_base_url="https://${om_ip_address}:8443/api/public/v1.0"

om_request() {
  local method=$1
  local path=$2
  local body=${3:-}
  if [[ -n "${body}" ]]; then
    curl -s --insecure --user "${om_creds}" --digest \
      --header 'Accept: application/json' --header 'Content-Type: application/json' \
      --cacert <(echo -n "${om_ca_cert}") \
      --request "${method}" --data @- "${om_base_url}${path}" <<< "${body}"
  else
    curl -s --insecure --user "${om_creds}" --digest \
      --header 'Accept: application/json' --header 'Content-Type: application/json' \
      --cacert <(echo -n "${om_ca_cert}") \
      --request "${method}" "${om_base_url}${path}"
  fi
}

# The MongoDB CR's opsManager.configMapRef (mdb-org-project-config) has no
# projectName key, so the operator named the OM project after the MongoDB
# resource itself (ReadProjectConfig in controllers/operator/project/projectconfig.go:
# "If configMap doesn't have a projectName defined - the name of MongoDB resource
# is used as a name of project") -- i.e. MDB_RESOURCE_NAME, not MDBS_RESOURCE_NAME.
project_id=$(om_request GET "/orgs/${om_org_id}/groups?name=${MDB_RESOURCE_NAME}" | jq -r '.results[0].id')
if [[ -z "${project_id}" || "${project_id}" == "null" ]]; then
  echo "Error: could not resolve project id for ${MDB_RESOURCE_NAME}" >&2
  exit 1
fi

ac_path="/groups/${project_id}/automationConfig"

# The source is a SINGLE-cluster sharded MongoDB -- there is no per-cluster
# subset of its processes to route locally. Instead every process is pointed
# at the SAME chosen search cluster (TARGET_CLUSTER_INDEX). jq classifies
# each AC process by name:
#   {MDB_RESOURCE_NAME}-mongos-{pod}        -> that cluster's routerHostname (cluster-level proxy)
#   {MDB_RESOURCE_NAME}-config-{member}     -> untouched (config servers don't talk to mongot)
#   {MDB_RESOURCE_NAME}-{shardIdx}-{member} -> that cluster's per-shard proxy-svc
# shellcheck disable=SC2016
patch_filter='
  ($mdb + "-") as $prefix
  | def patch:
      if (.name | startswith($prefix)) then
        (.name[($prefix|length):] | split("-")) as $parts
        | if $parts[0] == "config" then .
          elif $parts[0] == "mongos" then
            ($mdbs + "-search-" + $target + "-proxy-svc." + $ns + ".svc.cluster.local:" + $port) as $host
            | .args2_6.setParameter.mongotHost = $host
            | .args2_6.setParameter.searchIndexManagementHostAndPort = $host
          else
            ($mdbs + "-search-" + $target + "-" + $mdb + "-" + $parts[0] + "-proxy-svc." + $ns + ".svc.cluster.local:" + $port) as $host
            | .args2_6.setParameter.mongotHost = $host
            | .args2_6.setParameter.searchIndexManagementHostAndPort = $host
          end
      else . end;
  .processes = [.processes[] | patch] | .version += 1
'

ac=$(om_request GET "${ac_path}")
patched_ac=$(echo "${ac}" | jq \
  --arg mdb "${MDB_RESOURCE_NAME}" \
  --arg mdbs "${MDBS_RESOURCE_NAME}" \
  --arg ns "${MDB_NAMESPACE}" \
  --arg port "27028" \
  --arg target "${TARGET_CLUSTER_INDEX}" \
  "${patch_filter}")

changed=$(diff <(echo "${ac}" | jq -S '.processes[].args2_6.setParameter') \
               <(echo "${patched_ac}" | jq -S '.processes[].args2_6.setParameter') | grep -c '^[<>]' || true)
test "${changed}" -gt 0 || { echo "ERROR: the patch changed nothing -- either no AC process matched ${MDB_RESOURCE_NAME}-* naming (got: $(echo "${ac}" | jq -r '[.processes[].name]')), or every process already points at TARGET_CLUSTER_INDEX=${TARGET_CLUSTER_INDEX}" >&2; exit 1; }

# The operator re-asserts EXTERNALLY_MANAGED_LOCK on every reconcile, so clear
# it and PUT in immediate succession; retry a few times if a reconcile in
# between re-locks it (surfaces as an error object on the PUT response).
attempts=3
for attempt in $(seq 1 "${attempts}"); do
  om_request PUT "/groups/${project_id}/controlledFeature" \
    '{"externalManagementSystem":{"name":"mongodb-kubernetes-operator"},"policies":[]}' > /dev/null

  response=$(om_request PUT "${ac_path}" "${patched_ac}")
  if echo "${response}" | jq -e '.error' > /dev/null 2>&1; then
    echo "attempt ${attempt}/${attempts}: automationConfig PUT rejected (operator likely re-locked); retrying" >&2
    [[ "${attempt}" == "${attempts}" ]] && { echo "${response}" >&2; exit 1; }
    continue
  fi

  echo "[ok] automationConfig PUT accepted on attempt ${attempt}"
  break
done

echo "Waiting for agents to apply the new goal state..."
behind=1
for _ in $(seq 1 60); do
  status=$(om_request GET "/groups/${project_id}/automationStatus")
  goal=$(echo "${status}" | jq -r '.goalVersion')
  behind=$(echo "${status}" | jq -r --argjson goal "${goal}" '[.processes[] | select(.lastGoalVersionAchieved < $goal)] | length')
  [[ "${behind}" -eq 0 ]] && break
  sleep 5
done
if [[ "${behind}" -ne 0 ]]; then
  echo "Error: the agents did not reach the new automation goal within 5 minutes -- stop here." >&2
  echo "Inspect the automation agent logs before continuing." >&2
  exit 1
fi

echo "[ok] mongod/mongos processes now point mongotHost at search cluster index ${TARGET_CLUSTER_INDEX}'s proxy endpoints"
