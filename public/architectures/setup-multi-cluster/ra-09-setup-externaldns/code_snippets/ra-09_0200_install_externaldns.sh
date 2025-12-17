eval "cat <<EOF
$(cat yamls/externaldns.yaml)
EOF" | kubectl --context "${K8S_CLUSTER_0_CONTEXT_NAME}" -n external-dns apply -f -

eval "cat <<EOF
$(cat yamls/externaldns.yaml)
EOF" | kubectl --context "${K8S_CLUSTER_1_CONTEXT_NAME}" -n external-dns apply -f -

eval "cat <<EOF
$(cat yamls/externaldns.yaml)
EOF" | kubectl --context "${K8S_CLUSTER_2_CONTEXT_NAME}" -n external-dns apply -f -
