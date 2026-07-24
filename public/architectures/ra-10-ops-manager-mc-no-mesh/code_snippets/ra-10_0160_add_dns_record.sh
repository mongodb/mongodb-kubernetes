ip_address=$(gcloud compute forwarding-rules describe "${OM_LB_FORWARDING_RULE_NAME}" --global --format="get(IPAddress)")

gcloud dns record-sets create "${OPS_MANAGER_EXTERNAL_DOMAIN}" --zone="${DNS_ZONE}" --type="A" --ttl="300" --rrdatas="${ip_address}"
