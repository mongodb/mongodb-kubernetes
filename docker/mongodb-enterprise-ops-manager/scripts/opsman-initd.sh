#!/bin/bash

# We need to replicate part of the init-d file due to using $0 instead of $BASH_SOURCE
# https://stackoverflow.com/questions/21792176/0-doesnt-work-when-i-source-a-bash-script

# Define basic Ops Manager env variables
APP_DIR="/opt/mongodb/mms"
LOG_PATH="/data/logs"
JAVA_HOME="${APP_DIR}/jdk"
ENC_KEY_PATH="/etc/mongodb-mms/gen.key"
MMS_USER="mongodb-mms"
APP_NAME="mms-app"
APP_ENV="hosted"
APP_ID="mms"
BASE_PORT="8080"
BASE_SSL_PORT="8443"
SYSCONFIG="${APP_DIR}/conf/mms.conf"

# Maximum number of seconds to wait for the process to start
PROCESS_PERIOD=10

# Define Backup Daemon env variables
DAEMON_APP_ID="bgrid"
LOG_NAME="daemon"
LOG_FILE_BASE="${LOG_PATH}/${LOG_NAME}"
HEAD_BASE_PORT=27500
QUERYABLE_FS_PORT=8087
QUERYABLE_BASE_PORT=27700

[[ -f "${SYSCONFIG}" ]] && . "${SYSCONFIG}"

ensureKey() {
    if [[ ! -f $ENC_KEY_PATH ]]; then
        echo "Generating new Ops Manager private key..."
        if ! ${APP_DIR}/bin/mms-gen-key -u ${MMS_USER} -f ${ENC_KEY_PATH}; then
            echo "Exiting."
            exit 1
        fi
    fi
}

preFlightCheck() {
    local log_file_base="${LOG_PATH}/${APP_ID}0"
    local opts_array=(
        "-Duser.timezone=GMT"
        "-Dfile.encoding=UTF-8"
        "-Dserver-env=${APP_ENV}"
        "-Dapp-id=${APP_ID}"
        "-Dmms.keyfile=${ENC_KEY_PATH}"
        "-Dlog_path=${log_file_base}"
        "-Xmx256m"
        "-XX:-OmitStackTraceInFastThrow"
        "-Dcore.preflight.class=com.xgen.svc.mms.MmsPreFlightCheck"
        "-classpath ${APP_DIR}/classes/mms.jar:${APP_DIR}/conf:${APP_DIR}/lib/*"
    )
    local jvm_opts=$( build_opts "" opts_array[@] )
    local start_cmd="${JAVA_HOME}/bin/${APP_NAME} ${jvm_opts} com.xgen.svc.core.PreFlightCheck"
    /bin/bash -c "${start_cmd}"
    if [[ $? != 0 ]]; then
        echo "Preflight check failed."
        exit 1
    fi
    echo
}

migrate() {
    echo "Migrate Ops Manager data"
    echo -n "   Running migrations..."
    local log_file_base="${LOG_PATH}/${APP_ID}-migration"
    local opts_array=(
        "-Duser.timezone=GMT"
        "-Dfile.encoding=UTF-8"
        "-Dmms.migrate=all"
        "-Dserver-env=${APP_ENV}"
        "-Dapp-id=${APP_ID}"
        "-Dmms.keyfile=${ENC_KEY_PATH}"
        "-Dlog_path=${log_file_base}"
        "-Xmx256m"
        "-XX:-OmitStackTraceInFastThrow"
        "-classpath ${APP_DIR}/classes/mms.jar:${APP_DIR}/conf:${APP_DIR}/lib/*"
    )
    local jvm_opts=$( build_opts "" opts_array[@] )
    local start_cmd="${JAVA_HOME}/bin/${APP_NAME} ${jvm_opts} com.xgen.svc.common.migration.MigrationRunner"
    /bin/bash -c "${start_cmd}"
    if [[ $? != 0 ]]; then
        echo "Migration failed."
        exit 1
    fi
    echo
}

start() {
    ensureKey
    preFlightCheck
    migrate
    echo "Start Ops Manager server"
    local opts_array=(
        "-Duser.timezone=GMT"
        "-Dfile.encoding=UTF-8"
        "-Dsun.net.client.defaultReadTimeout=20000"
        "-Dsun.net.client.defaultConnectTimeout=10000"
        "-Djavax.net.ssl.sessionCacheSize=1"
        "-Dorg.eclipse.jetty.util.UrlEncoding.charset=UTF-8"
        "-Dorg.eclipse.jetty.server.Request.maxFormContentSize=4194304"
        "-Dserver-env=${APP_ENV}"
        "-Dapp-id=${APP_ID}"
        "-Dbase-port=${BASE_PORT}"
        "-Dbase-ssl-port=${BASE_SSL_PORT}"
        "-Dapp-dir=${APP_DIR}"
        "-Dxgen.webServerReuseAddress=true"
        "-Dmms.keyfile=${ENC_KEY_PATH}"
        "-XX:SurvivorRatio=12"
        "-XX:MaxTenuringThreshold=15"
        "-XX:CMSInitiatingOccupancyFraction=62"
        "-XX:+UseCMSInitiatingOccupancyOnly"
        "-XX:+UseConcMarkSweepGC"
        "-XX:+UseParNewGC"
        "-XX:+UseBiasedLocking"
        "-XX:+CMSParallelRemarkEnabled"
        "-XX:-OmitStackTraceInFastThrow"
        "-classpath ${APP_DIR}/classes/mms.jar:${APP_DIR}/agent:${APP_DIR}/agent/backup:${APP_DIR}/agent/monitoring:${APP_DIR}/agent/automation:${APP_DIR}/agent/biconnector:${APP_DIR}/data/unit:${APP_DIR}/conf/:${APP_DIR}/lib/*"
    )
    local jvm_opts=$( build_opts "${JAVA_MMS_UI_OPTS}" opts_array[@] )
    local idx="0"
    local log_file_base="${LOG_PATH}/${APP_ID}${idx}"
    local startup_log_file_base="${LOG_PATH}/${APP_ID}${idx}-startup"
    local pid_file="${APP_DIR}/tmp/${APP_ID}-${idx}.pid"
    local jvm_opts_instance="${jvm_opts} -Dlog_path=${log_file_base} -Dinstance-id=${idx} -Dpid-filename=${pid_file}"
    local start_cmd="${JAVA_HOME}/bin/${APP_NAME} ${jvm_opts_instance} com.xgen.svc.core.ServerMain"
    local pid=$( mms_pid $idx )
    local running=$( is_mms_running $pid )

    if [[ $running == "yes" ]]; then
        echo -n "   Instance ${idx} is already running"

    else
        echo -n "   Instance ${idx} starting..."
        /bin/bash -c "nohup ${start_cmd} &" >> "${startup_log_file_base}.log" 2>&1

        if wait_for_start ${idx} ${PROCESS_PERIOD}; then
            echo "Ops Manager started!"
        else
            echo "Ops Manager failed to start..."
            echo
            echo "   Check ${log_file_base}.log and ${startup_log_file_base}.log for errors"
            exit 1
        fi
    fi
    echo

    # Pod keeps crashing and I suspect it is the backup daemon.
    # start_daemon
}

preFlightCheck_daemon() {
    local opts_array=(
        "-Dmms.extraPropFile=conf-mms.properties"
        "-Duser.timezone=GMT"
        "-Dfile.encoding=UTF-8"
        "-Dserver-env=${APP_ENV}"
        "-Dapp-id=${DAEMON_APP_ID}"
        "-Dmms.keyfile=${ENC_KEY_PATH}"
        "-DDAEMON.MONGODB.RELEASE.DIR=`get_daemon_release_directory`"
        "-Dlog_path=${LOG_PATH}/daemon"
        "-Xmx256m"
        "-XX:-OmitStackTraceInFastThrow"
        "-Dcore.preflight.class=com.xgen.svc.brs.grid.BackupDaemonPreFlightCheck"
        "-classpath ${APP_DIR}/classes/mms.jar:${APP_DIR}/conf:${APP_DIR}/lib/*"
    )

    local jvm_opts=$( build_opts "" opts_array[@] )
    local start_cmd="${JAVA_HOME}/bin/${APP_NAME} ${jvm_opts} com.xgen.svc.core.PreFlightCheck"
    /bin/bash -c "${start_cmd}"
    if [[ $? != 0 ]]; then
        echo "Backup Daemon preflight check failed."
        exit 1
    fi
    echo
}

start_daemon() {
    preFlightCheck_daemon
    local pid_file="${APP_DIR}/tmp/${DAEMON_APP_ID}-0.pid"
    local pid=$( get_pid )

    JAVA_DAEMON_OPTS="${JAVA_DAEMON_OPTS} -Dserver-env=${APP_ENV} -Dapp-id=${DAEMON_APP_ID} -Dapp-dir=${APP_DIR}"
    JAVA_DAEMON_OPTS="${JAVA_DAEMON_OPTS} -Dlog_path=${LOG_FILE_BASE} -Dmms.extraPropFile=conf-mms.properties"
    JAVA_DAEMON_OPTS="${JAVA_DAEMON_OPTS} -DDAEMON.ROOT.DIRECTORY=`get_daemon_root_directory`"
    JAVA_DAEMON_OPTS="${JAVA_DAEMON_OPTS} -Dnum.workers=`get_daemon_num_workers`"
    JAVA_DAEMON_OPTS="${JAVA_DAEMON_OPTS} -DDAEMON.BASE.PORT=${HEAD_BASE_PORT}"
    JAVA_DAEMON_OPTS="${JAVA_DAEMON_OPTS} -DDAEMON.QUERYABLE.FS.PORT=${QUERYABLE_FS_PORT}"
    JAVA_DAEMON_OPTS="${JAVA_DAEMON_OPTS} -DDAEMON.QUERYABLE.BASE_PORT=${QUERYABLE_BASE_PORT} -DDAEMON.QUERYABLE.MAX_MOUNTS=20"
    JAVA_DAEMON_OPTS="${JAVA_DAEMON_OPTS} -DDAEMON.MONGODB.RELEASE.DIR=`get_daemon_release_directory`"
    JAVA_DAEMON_OPTS="${JAVA_DAEMON_OPTS} -Dmms.keyfile=${ENC_KEY_PATH}"
    JAVA_MMS_CLASSPATH="-classpath ${APP_DIR}/classes/mms.jar:${APP_DIR}/conf/:${APP_DIR}/lib/*"

    local start_cmd="${JAVA_HOME}/bin/${APP_NAME} ${JAVA_DAEMON_OPTS} -Dpid-filename=${pid_file} ${JAVA_MMS_CLASSPATH} com.xgen.svc.brs.grid.Daemon"
    echo -n "Start Backup Daemon..."

    local is_running=$( is_instance_running $pid )
    if [[ $is_running == "yes" ]]; then
        echo
        echo "   Backup Daemon is already running."
    else
        /bin/bash -c "nohup ${start_cmd} &" >> "$(get_startup_log).log" 2>&1
        if [[ $? != 0 ]]; then
            echo "Backup Daemon failed to start..."
            echo
            echo "   Check ${LOG_FILE_BASE} and $(get_startup_log) for errors"
            exit 1
        fi
        echo
    fi
}

# Ops Manager Helper functions
build_opts() {
    local opts="$1"
    local arr="${!2}"
    for opt in "${arr[@]}"; do
        opts="${opts} ${opt}"
    done
    echo -n $opts
}

mms_pid() {
    local instance=$1
    local pid_file="${APP_DIR}/tmp/${APP_ID}-${instance}.pid"
    if [[ ! -f ${pid_file} ]]; then
        echo "x"
    else
        cat ${pid_file}
    fi
}

is_mms_running() {
    local pid=$1
    ps -e -o pid,command | grep "$APP_NAME" | awk '{print $1}' | grep -q "^${pid}$"
    if [[ $? == 0 ]]; then
        echo -n yes
    else
        echo -n no
    fi
}

is_mms_web_server_running() {
    local port=$1
    local portcmd;
    if command -v ss &> /dev/null; then
      portcmd="ss -tln"
    else
      portcmd="netstat -nl"
    fi
    ${portcmd} | grep :${port} > /dev/null 2>&1
    if [[ $? == 0 ]]; then
        echo -n yes
    else
        echo -n no
    fi
}

wait_for_start() {
    local idx="0"
    local process_start_timeout=$2

    local counter=0
    while [ $counter -lt $process_start_timeout ]; do
        local pid=$( mms_pid $idx )
        local process_running=$( is_mms_running $pid )

        if [[ $process_running == "yes" ]]; then
            break
        fi

        sleep 5
        echo -n .
        let counter=counter+5
    done

    local counter=0
    while true; do
        local port=$(($BASE_PORT + $idx))
        local port_ssl=$(($BASE_SSL_PORT + $idx))
        local process_running=$( is_mms_running $pid )
        local web_running=$( is_mms_web_server_running $port )
        local web_ssl_running=$( is_mms_web_server_running $port_ssl )

        if [[ $process_running == "no" ]]; then
            return 1
        fi

        if [[ $web_running == "yes" ]]; then
            return 0
        fi

        if [[ $web_ssl_running == "yes" ]]; then
            return 0
        fi

        sleep 5
        echo -n .
        let counter=counter+5
    done
}

# Backup Daemon Helper functions
get_daemon_root_directory() {
    echo `grep '^rootDirectory' ${APP_DIR}/conf/conf-mms.properties | cut -d = -f 2`
}

get_daemon_num_workers() {
    echo `grep '^numWorkers' ${APP_DIR}/conf/conf-mms.properties | cut -d = -f 2`
}

get_daemon_release_directory() {
    echo `grep '^mongodb.release.directory' ${APP_DIR}/conf/conf-mms.properties | cut -d = -f 2`
}

get_startup_log() {
    echo "${LOG_FILE_BASE}-startup.log"
}

get_pid() {
    local pid_file="${APP_DIR}/tmp/${DAEMON_APP_ID}-0.pid"
    if [[ ! -f ${pid_file} ]]; then
        echo "x"
    else
        cat ${pid_file}
    fi
}

is_instance_running() {
    local pid=$1
    ps -e -o pid,command | grep "$APP_NAME" | awk '{print $1}' | grep -q "^${pid}$"
    if [[ $? == 0 ]]; then
        echo -n yes
    else
        echo -n no
    fi
}

# Array contains function; based on https://stackoverflow.com/questions/3685970/check-if-a-bash-array-contains-a-value
contains() {
    local e match=$1
    shift
    for e; do [[ "$e" == "$match" ]] && return 0; done
    return 1
}

# Pre-generate the server key and ensure the appropriate permissions
if contains "--gen-key" "$@"; then
    ensureKey
fi

# Ensure that Ops Manager is correctly configured
if contains "--preflight" "$@"; then
    preFlightCheck
fi

# Prepare Ops Manager's AppDb: run migrations
if contains "--migrations" "$@"; then
    migrate
fi

# Start Ops Manager and the Backup Daemon
if contains "--start" "$@"; then
    start
fi
