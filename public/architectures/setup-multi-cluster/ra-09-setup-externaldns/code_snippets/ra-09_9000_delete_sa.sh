if [[ -z "${MDB_GKE_PROJECT:-}" || -z "${DNS_SA_NAME:-}" || -z "${DNS_SA_EMAIL:-}" ]] \
  || ! [[ "${DNS_SA_NAME}" =~ ^ext-dns-sa(-[a-z0-9]+)*$ ]] \
  || [[ "${DNS_SA_EMAIL}" != "${DNS_SA_NAME}@${MDB_GKE_PROJECT}.iam.gserviceaccount.com" ]]; then
  echo "Invalid ExternalDNS service-account ownership; refusing cleanup" >&2
  exit 1
fi

gcloud projects remove-iam-policy-binding "${MDB_GKE_PROJECT}" --member serviceAccount:"${DNS_SA_EMAIL}" --role roles/dns.admin || true

gcloud iam service-accounts delete "${DNS_SA_EMAIL}" -q || true
