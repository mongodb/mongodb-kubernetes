kubectl apply --context "${K8S_CTX}" -n "${MDB_NS}" -f - <<YAML
apiVersion: v1
kind: Service
metadata:
  name: ${MDB_SEARCH_HOSTNAME}
spec:
  type: LoadBalancer
  selector:
    app: mdbs-search-svc
  ports:
    - name: mongot
      port: 27027
      targetPort: 27027
YAML
