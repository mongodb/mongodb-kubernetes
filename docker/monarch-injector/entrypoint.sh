#!/bin/bash
set -e

# Required environment variables
: "${SHARD_ID:?SHARD_ID is required}"
: "${REPLSET_NAME:?REPLSET_NAME is required}"
: "${REPLSET_HOSTS:?REPLSET_HOSTS is required}"
: "${CLUSTER_PREFIX:?CLUSTER_PREFIX is required}"
: "${S3_BUCKET:?S3_BUCKET is required}"
: "${S3_ENDPOINT:?S3_ENDPOINT is required}"
: "${AWS_ACCESS_KEY_ID:?AWS_ACCESS_KEY_ID is required}"
: "${AWS_SECRET_ACCESS_KEY:?AWS_SECRET_ACCESS_KEY is required}"

# Optional
BIND_IP="${BIND_IP:-0.0.0.0}"
PORT="${PORT:-9995}"
HEALTH_PORT="${HEALTH_PORT:-8080}"
MONARCH_API_PORT="${MONARCH_API_PORT:-1122}"
AWS_REGION="${AWS_REGION:-eu-north-1}"

echo "============================================"
echo "Monarch Oplog Injector"
echo "============================================"
echo "ShardId:       ${SHARD_ID}"
echo "ReplSetName:   ${REPLSET_NAME}"
echo "ReplSetHosts:  ${REPLSET_HOSTS}"
echo "Port:          ${PORT}"
echo "Health Port:   ${HEALTH_PORT}"
echo "Monarch API:   ${MONARCH_API_PORT}"
echo "S3 Endpoint:   ${S3_ENDPOINT}"
echo "============================================"

/usr/local/bin/monarch --version

# Monarch API binds to localhost only; use socat to forward on all interfaces.
(
    sleep 3
    ETH0_IP=$(hostname -i | awk '{print $1}')
    echo "Starting socat to forward ${ETH0_IP}:${MONARCH_API_PORT} -> localhost:${MONARCH_API_PORT}..."
    socat "TCP-LISTEN:${MONARCH_API_PORT},bind=${ETH0_IP},fork,reuseaddr" "TCP:localhost:${MONARCH_API_PORT}"
) &

exec /usr/local/bin/monarch injector \
    --clusterPrefix "${CLUSTER_PREFIX}" \
    --shardId "${SHARD_ID}" \
    --replSetName "${REPLSET_NAME}" \
    --replSetHosts "${REPLSET_HOSTS}" \
    --bindIp "${BIND_IP}" \
    --port "${PORT}" \
    --healthApiEndpoint "${BIND_IP}:${HEALTH_PORT}" \
    --monarchApiPort "${MONARCH_API_PORT}" \
    --aws.authMode staticCredentials \
    --aws.accessKeyId "${AWS_ACCESS_KEY_ID}" \
    --aws.secretAccessKey "${AWS_SECRET_ACCESS_KEY}" \
    --aws.bucketName "${S3_BUCKET}" \
    --aws.region "${AWS_REGION}" \
    --aws.usePathStyle \
    --aws.customBaseEndpoint "${S3_ENDPOINT}" \
    --logLevel "${LOG_LEVEL:-info}" \
    --logPath /var/log/monarch/injector.log
