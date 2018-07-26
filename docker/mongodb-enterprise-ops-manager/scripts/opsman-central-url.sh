#!/bin/bash
readonly SCRIPT=$(basename "${BASH_SOURCE[0]}")
readonly PROP_NAME="mms.centralUrl"

usage() {
    echo "Usage: ${SCRIPT} --clean PROP_FILE"
    echo "Usage: ${SCRIPT} --set PROP_FILE URL"
    echo
    exit 1
}

# Remove the property, if defined in the specified file
clean() {
    local TEMP_FILE=$(mktemp)
    grep -v "^\\s*${PROP_NAME}\\s*=" "$1" > "${TEMP_FILE}"
    cat "${TEMP_FILE}" > "$1"
    rm -f "${TEMP_FILE}"
}

# Parameter check; print usage if no parameters were specified
if [[ "$#" -lt 1 ]]; then
    usage
fi

# Clean properties file and exit
if [[ "$1" == "--clean" ]]; then
    # Parameter check
    if [[ "$#" -lt 2 ]]; then
        echo "Properties file not specified: $SCRIPT $*"
        usage
    fi
    echo "Removing ${PROP_NAME} from $2..."
    clean "$2"
    exit 0

# Setting the property to a new value
elif [[ "$1" == "--set" ]]; then
    # Parameter check
    if [[ "$#" -lt 3 ]]; then
        echo "Properties file or URL not specified: $SCRIPT $*"
        usage
    fi
    echo "Updating ${PROP_NAME} to $3"
    echo "${PROP_NAME}=$3" >> "$2"
    exit 0
fi

# Usage
echo "Wrong arguments: $SCRIPT $*"
usage
exit 1
