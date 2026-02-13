# Deploy Envoy proxy Deployment and Services
#
# This creates:
# 1. Envoy Deployment - runs the Envoy proxy with TLS certificates mounted
# 2. Per-shard proxy Services - one Service per shard, all pointing to the same Envoy pod
#    These services provide the SNI-based routing endpoints
# 3. Admin Service - for health checks and metrics

echo "Deploying Envoy proxy..."

# Create Envoy Deployment
kubectl apply --context "${K8S_CTX}" -n "${MDB_NS}" -f - <<EOF
apiVersion: apps/v1
kind: Deployment
metadata:
  name: envoy-proxy
  labels:
    app: envoy-proxy
    component: search-proxy
spec:
  replicas: 1
  selector:
    matchLabels:
      app: envoy-proxy
  strategy:
    type: RollingUpdate
    rollingUpdate:
      maxSurge: 1
      maxUnavailable: 0
  template:
    metadata:
      labels:
        app: envoy-proxy
        component: search-proxy
      annotations:
        prometheus.io/scrape: "true"
        prometheus.io/port: "9901"
        prometheus.io/path: "/stats/prometheus"
    spec:
      shareProcessNamespace: true
      securityContext:
        fsGroup: 101
        seccompProfile:
          type: RuntimeDefault
      containers:
      - name: envoy
        image: ${ENVOY_IMAGE:-envoyproxy/envoy:v1.31-latest}
        imagePullPolicy: IfNotPresent
        command:
        - /usr/local/bin/envoy
        args:
        - -c
        - /etc/envoy/envoy.yaml
        - --service-cluster
        - envoy-proxy
        - --service-node
        - \$(POD_NAME)
        - --log-level
        - info
        env:
        - name: POD_NAME
          valueFrom:
            fieldRef:
              fieldPath: metadata.name
        - name: POD_NAMESPACE
          valueFrom:
            fieldRef:
              fieldPath: metadata.namespace
        ports:
        - name: grpc
          containerPort: ${ENVOY_PROXY_PORT:-27029}
          protocol: TCP
        - name: admin
          containerPort: 9901
          protocol: TCP
        resources:
          requests:
            cpu: 500m
            memory: 512Mi
          limits:
            cpu: 2000m
            memory: 2Gi
        livenessProbe:
          httpGet:
            path: /ready
            port: 9901
            scheme: HTTP
          initialDelaySeconds: 15
          periodSeconds: 10
          timeoutSeconds: 5
          failureThreshold: 3
        readinessProbe:
          httpGet:
            path: /ready
            port: 9901
            scheme: HTTP
          initialDelaySeconds: 10
          periodSeconds: 5
          timeoutSeconds: 3
          failureThreshold: 2
        startupProbe:
          httpGet:
            path: /ready
            port: 9901
            scheme: HTTP
          initialDelaySeconds: 5
          periodSeconds: 5
          timeoutSeconds: 3
          failureThreshold: 12
        volumeMounts:
        - name: envoy-config
          mountPath: /etc/envoy
          readOnly: true
        - name: envoy-server-cert
          mountPath: /etc/envoy/tls/server
          readOnly: true
        - name: envoy-client-cert
          mountPath: /etc/envoy/tls/client
          readOnly: true
        - name: ca-cert
          mountPath: /etc/envoy/tls/ca
          readOnly: true
        - name: tmp
          mountPath: /tmp
        securityContext:
          allowPrivilegeEscalation: false
          readOnlyRootFilesystem: true
          runAsNonRoot: true
          runAsUser: 101
          capabilities:
            drop:
            - ALL
      volumes:
      - name: envoy-config
        configMap:
          name: envoy-config
          defaultMode: 0444
      - name: envoy-server-cert
        secret:
          secretName: envoy-server-cert-pem
          defaultMode: 0440
          items:
          - key: cert.pem
            path: cert.pem
      - name: envoy-client-cert
        secret:
          secretName: envoy-client-cert-pem
          defaultMode: 0440
          items:
          - key: cert.pem
            path: cert.pem
      - name: ca-cert
        configMap:
          name: ${MDB_TLS_CA_CONFIGMAP}
          defaultMode: 0444
          items:
          - key: ca-pem
            path: ca-pem
      - name: tmp
        emptyDir: {}
      terminationGracePeriodSeconds: 30
      dnsPolicy: ClusterFirst
EOF

echo "  ✓ Envoy Deployment created"

# Create per-shard proxy Services
echo "Creating per-shard proxy Services..."
for i in $(seq 0 $((MDB_SHARD_COUNT - 1))); do
  shard_name="${MDB_EXTERNAL_CLUSTER_NAME}-${i}"
  proxy_svc="${MDB_SEARCH_RESOURCE_NAME}-mongot-${shard_name}-proxy-svc"

  kubectl apply --context "${K8S_CTX}" -n "${MDB_NS}" -f - <<EOF
apiVersion: v1
kind: Service
metadata:
  name: ${proxy_svc}
  labels:
    app: envoy-proxy
    component: search-proxy
    target-shard: ${shard_name}
spec:
  type: ClusterIP
  sessionAffinity: ClientIP
  sessionAffinityConfig:
    clientIP:
      timeoutSeconds: 10800
  selector:
    app: envoy-proxy
  ports:
  - name: grpc
    port: ${ENVOY_PROXY_PORT:-27029}
    targetPort: ${ENVOY_PROXY_PORT:-27029}
    protocol: TCP
EOF
  echo "  ✓ Service ${proxy_svc} created"
done

# Create admin Service for monitoring
kubectl apply --context "${K8S_CTX}" -n "${MDB_NS}" -f - <<EOF
apiVersion: v1
kind: Service
metadata:
  name: envoy-proxy-admin
  labels:
    app: envoy-proxy
    component: search-proxy
spec:
  type: ClusterIP
  selector:
    app: envoy-proxy
  ports:
  - name: admin
    port: 9901
    targetPort: 9901
    protocol: TCP
EOF

echo "  ✓ Admin Service envoy-proxy-admin created"

# Wait for Envoy to be ready
echo "Waiting for Envoy proxy to be ready..."
kubectl rollout status deployment/envoy-proxy -n "${MDB_NS}" --context "${K8S_CTX}" --timeout=120s

echo "✓ Envoy proxy deployed successfully"
