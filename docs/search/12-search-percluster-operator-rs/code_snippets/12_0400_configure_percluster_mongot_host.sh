echo "=============================================================================="
echo "WARNING: this step talks to the Ops Manager Automation Config REST API DIRECTLY,"
echo "bypassing the operator's own reconcile. It deliberately sets mongotHost /"
echo "searchIndexManagementHostAndPort per mongod PROCESS -- a per-process granularity"
echo "no MongoDBMultiCluster CR field exposes. Because the operator never learns these"
echo "values (they never appear in any CR spec), it never overwrites them on a later"
echo "reconcile. This is expected, deliberate, and the only way to get each cluster's"
echo "mongod talking to ITS OWN local mongot in this deployment model."
echo "=============================================================================="

# Ops Manager connection details come from what ra-06/ra-07 already created on
# ${K8S_CLUSTER_0_CONTEXT_NAME} -- there are no OPS_MANAGER_API_* env vars in this
# reference-architecture chain (unlike the self-contained docs/search/1x scenarios).
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

project_id=$(om_request GET "/orgs/${om_org_id}/groups?name=${RS_RESOURCE_NAME}" | jq -r '.results[0].id')
if [[ -z "${project_id}" || "${project_id}" == "null" ]]; then
  echo "Error: could not resolve project id for ${RS_RESOURCE_NAME}" >&2
  exit 1
fi

ac_path="/groups/${project_id}/automationConfig"

# Build the per-process mongotHost patch. Process names for a MongoDBMultiCluster
# replica set are "<RS_RESOURCE_NAME>-<clusterIndex>-<memberIndex>"; clusterIndex
# here is the SAME index used in spec.clusters[].index on the MongoDBSearch CR
# (see env_variables.sh), so no separate cluster-name -> index lookup is needed.
patch_ac() {
  jq --arg prefix "${RS_RESOURCE_NAME}-" \
    --arg mdbs "${SEARCH_RESOURCE_NAME}" \
    --arg ns "${MDB_NAMESPACE}" \
    --argjson port "${ENVOY_PROXY_PORT}" \
    '
    .version += 1
    | .processes |= map(
        if (.name | startswith($prefix)) then
          ((.name[($prefix | length):] | split("-")[0]) as $idx
           | ($mdbs + "-search-" + $idx + "-proxy-svc." + $ns + ".svc.cluster.local:" + ($port | tostring)) as $host
           | .args2_6.setParameter.mongotHost = $host
           | .args2_6.setParameter.searchIndexManagementHostAndPort = $host)
        else .
        end
      )
    '
}

# Fail loudly if the source's process names don't match the expected scheme:
# a patch that matches nothing would otherwise PUT an unchanged config and
# still look successful, and the problem would only surface much later as
# queries returning no results.
matched=$(om_request GET "${ac_path}" | jq --arg prefix "${RS_RESOURCE_NAME}-" \
  '[.processes[] | select(.name | startswith($prefix))] | length')
if [[ "${matched}" -eq 0 ]]; then
  echo "Error: no automation config process is named ${RS_RESOURCE_NAME}-<clusterIndex>-<memberIndex>." >&2
  echo "Check RS_RESOURCE_NAME against the deployment's actual process names in OM before continuing." >&2
  exit 1
fi

attempts=3
for attempt in $(seq 1 "${attempts}"); do
  # clear_feature_controls: OM's EXTERNALLY_MANAGED_LOCK otherwise rejects a direct
  # automationConfig PUT. The operator re-asserts the lock on its next reconcile, so
  # this only opens a window for the PUT immediately below.
  om_request PUT "/groups/${project_id}/controlledFeature" \
    '{"externalManagementSystem":{"name":"mongodb-kubernetes-operator"},"policies":[]}' > /dev/null

  ac=$(om_request GET "${ac_path}")
  patched_ac=$(echo "${ac}" | patch_ac)

  response=$(om_request PUT "${ac_path}" "${patched_ac}")
  if echo "${response}" | jq -e '.error' > /dev/null 2>&1; then
    echo "attempt ${attempt}/${attempts}: automationConfig PUT rejected (operator likely re-locked); retrying" >&2
    [[ "${attempt}" == "${attempts}" ]] && { echo "${response}" >&2; exit 1; }
    continue
  fi

  echo "[ok] automationConfig PUT accepted with per-cluster mongotHost"
  break
done

echo "Waiting for the agents to apply the change (each mongod restarts to pick up the new setParameter values)..."
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
echo "[ok] every process reports the new goal version; spot-check one mongod:"
echo "  kubectl exec -n ${MDB_NAMESPACE} --context ${K8S_CLUSTER_0_CONTEXT_NAME} ${RS_RESOURCE_NAME}-0-0 -- cat /data/automation-mongod.conf"
