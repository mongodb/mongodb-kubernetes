#!/bin/bash

#
# ©, 2018, the Docker Community. https://github.com/docker-library/mongo
#

#
# This is an entrypoint script to support starting MongoDB with minimal default configurations.
# Any extra flags can be passed as as parameters to build container.
#

# this allows to launch docker container with just specifying arguments: 'mongod' will be appended to the beginning
if [ "${1:0:1}" = '-' ]; then
	set -- mongod "$@"
fi

# we use numa as advised by the MongoDB production notes
# https://docs.mongodb.com/manual/administration/production-notes/#configuring-numa-on-linux
numa='numactl --interleave=all'
if $numa true &> /dev/null; then
    set -- $numa "$@"
fi

# check if the argument has already been specified
_argument_specified() {
	local checkArg="$1"; shift
	local arg
	for arg; do
		case "$arg" in
			"$checkArg"|"$checkArg"=*)
				return 0
				;;
		esac
	done
	return 1
}

if ! _argument_specified "--logpath" "$@"; then
    set -- "$@" "--logpath" "$LOGS_DIR/mongodb.log"
fi

if ! _argument_specified "--dbpath" "$@"; then
    set -- "$@" "--dbpath" "$DATA_DIR"
fi

if ! _argument_specified "--logappend" "$@"; then
    set -- "$@" "--logappend"
fi

if ! _argument_specified "--bind_ip" "$@" && ! _argument_specified "--bind_ip_all" "$@"; then
    set -- "$@" "--bind_ip_all"
fi

# Some thoughts for the future about authentication: we start the DB in auth mode and then the Operator will try to login, fail
# and will try to create an admin via localhost exception

#if ! _argument_specified "--auth" "$@"; then
#    set -- "$@" "--auth"
#fi

"$@"

