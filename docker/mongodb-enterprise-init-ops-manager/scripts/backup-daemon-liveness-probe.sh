#!/usr/bin/env bash

set -Eeou pipefail

check_backup_daemon_alive () {
    pgrep --exact 'mms-app'
}

check_backup_daemon_alive
