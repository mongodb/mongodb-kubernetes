#!/bin/bash

set -vex

keytool -importcert -trustcacerts -file /etc/tls-cert/ca.crt -keystore /mongotrust.ts -storepass foofoo -noprompt


# build connection string from secret mounted
# as environment variables
python /connstring-helper-env.py > /uri

MDB_URL=$(cat /uri)
echo "target db: ${MDB_URL}"

export YCSB_HOME="/ycsb"

# make sure all the params are set and go.
if [[ -z ${ACTION} ]]; then
    echo "ACTION env not found, default to 'run'"
    ACTION=run
fi
if [[ -z ${DB} ]]; then
    echo "DB env not found, default to 'mongodb'"
    DB=mongodb
fi


cd ${YCSB_HOME}
echo "YCSB - ACTION=${ACTION} DB=${DB}"
echo "== workload start"
echo "Starting workload/work"
cat /work/workload
echo "== workload end"

./bin/ycsb "${ACTION}" "${DB}" -s -P /work/workload -p mongodb.url="${MDB_URL}" -p maxexecutiontime="900" -jvm-args '-Djavax.net.ssl.trustStore=/mongotrust.ts -Djavax.net.ssl.trustStorePassword=foofoo'
