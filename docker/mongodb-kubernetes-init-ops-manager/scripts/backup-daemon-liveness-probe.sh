#!/usr/bin/env bash

set -Eeou pipefail -o posix

check_backup_daemon_alive () {
    pgrep --exact 'mms-app' || pgrep -f '/mongodb-ops-manager/jdk/bin/mms-app'
}

check_backup_daemon_alive
