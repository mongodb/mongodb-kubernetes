#!/bin/bash

# if changes related to operator version, and CRDS version, test will fail, because dependency is not yet released
# we can change dependency to file to check the test. Those changes remain only for this job container.

# sample
# repository: "file://../atlas-operator-crds"

# required inputs:
CRD_DIRECTORY=$1
OPERATOR_DIRECTORY=$2

if [[ -z "${CRD_DIRECTORY}" ]] || [[ -z "${OPERATOR_DIRECTORY}" ]]; then
    echo "Please pass parameters:"
    echo "_1: CRD directory name"
    echo "_2: Operator directory name"
    echo ""
    echo "example: dependancy_as_file.sh atlas-operator-crds atlas-operator"
    exit 1
fi

OPERATOR_PATH="charts/${OPERATOR_DIRECTORY}/Chart.yaml"
CRD_VERSION=$(awk '/^appVersion:/{print $2}' "${OPERATOR_PATH}")
CRD_NAME=$(awk '/^name:/{print "mongodb/"$2}' "${OPERATOR_PATH}")
helm install crd "$CRD_NAME" --version "${CRD_VERSION}" --dry-run

ERR=$?
if [[ "${ERR}" != 0 ]]; then
    REPOSITORY=$(awk '/repository:/{print $2}' "${OPERATOR_PATH}" | sed 's/\//\\\//g')
    FILE_REPOSITORY=$( echo "file://../${CRD_DIRECTORY}" | sed 's/\//\\\//g')
    sed -i 's/'"${REPOSITORY}"'/'"${FILE_REPOSITORY}"'/' "${OPERATOR_PATH}"
fi
