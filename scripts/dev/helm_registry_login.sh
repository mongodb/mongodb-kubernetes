#!/usr/bin/env bash

ECR_REGISTRY="268558157000.dkr.ecr.us-east-1.amazonaws.com"
AWS_REGION="us-east-1"

source scripts/dev/set_env_context.sh

export PATH=${PROJECT_DIR}/bin:$PATH

echo "Checking if helm CLI is installed..."
if ! command -v helm &> /dev/null
then
    echo "Error: helm CLI could not be found."
    echo "Please ensure helm is installed and in your system's PATH."
    exit 1
fi

echo "Checking if aws CLI is installed..."
if ! command -v aws &> /dev/null
then
    echo "Error: aws CLI could not be found."
    echo "Please ensure aws CLI is installed and configured."
    exit 1
fi

echo "Logging into OCI Registry: ${ECR_REGISTRY} in region ${AWS_REGION}..."

if aws ecr get-login-password --region "$AWS_REGION" | helm registry login \
    --username AWS \
    --password-stdin \
    "$ECR_REGISTRY"
then
    echo "Helm successfully logged into the OCI registry."
    exit 0
else
    echo "ERROR: Helm login to ECR registry failed."
    echo "Please ensure your AWS credentials have permission to access ECR in the specified region."
    exit 1
fi
