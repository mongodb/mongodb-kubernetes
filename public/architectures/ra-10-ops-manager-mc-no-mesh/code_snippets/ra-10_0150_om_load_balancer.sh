gcloud compute firewall-rules create "${OM_FIREWALL_RULE_NAME}" \
   --action=allow \
   --direction=ingress \
   --target-tags=mongodb \
   --source-ranges=130.211.0.0/22,35.191.0.0/16 \
   --rules=tcp:8443

gcloud compute health-checks create https "${OM_HEALTHCHECK_NAME}" \
    --use-serving-port \
    --request-path=/monitor/health

gcloud compute backend-services create "${OM_BACKEND_SERVICE_NAME}" \
    --protocol HTTPS \
    --health-checks "${OM_HEALTHCHECK_NAME}" \
    --global

gcloud compute url-maps create "${OM_URL_MAP_NAME}" \
    --default-service "${OM_BACKEND_SERVICE_NAME}"

gcloud compute target-https-proxies create "${OM_LB_PROXY_NAME}" \
    --url-map "${OM_URL_MAP_NAME}" \
    --ssl-certificates="${OM_CERTIFICATE_NAME}"

gcloud compute forwarding-rules create "${OM_FORWARDING_RULE_NAME}" \
    --global \
    --target-https-proxy="${OM_LB_PROXY_NAME}" \
    --ports=443
