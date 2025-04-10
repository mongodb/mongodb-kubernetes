#!/usr/bin/env bash

aws_region="eu-west-2"
region="${aws_region}a"
cluster_one_name="e2e.operator.mongokubernetes.com"
cluster_two_name="e2e.cluster1.mongokubernetes.com"
cluster_three_name="e2e.cluster2.mongokubernetes.com"
master_size="m5.large"
node_size="t2.medium"

# forward slash needs to be escaped for sed
# these are all non overlapping ranges.
cluster_one_cidr="172.20.32.0\/19"
cluster_two_cidr="172.20.64.0\/19"
cluster_three_cidr="172.20.0.0\/19"

if ! kops validate cluster "${cluster_one_name}"; then
    echo "Kops cluster \"${cluster_one_name}\" doesn't exist"
    sed -e  "s/<CLUSTER_NAME>/${cluster_one_name}/g" -e "s/<REGION>/${region}/g" -e "s/<CIDR>/${cluster_one_cidr}/g" -e '/<VPC_ID>/d' -e "s/<MASTER_SIZE>/${master_size}/g" -e "s/<NODE_SIZE>/${node_size}/g" < cluster.yaml | kops create -f -
    kops create secret --name ${cluster_one_name} sshpublickey admin -i ~/.ssh/id_rsa.pub
    kops update cluster --name ${cluster_one_name} --yes
    echo "Waiting until kops cluster gets ready..."
    kops export kubecfg "${cluster_one_name}" --admin=87600h
    kops validate cluster "${cluster_one_name}" --wait 20m
fi
kops export kubecfg "${cluster_one_name}" --admin=87600h


VPC_ID="$(aws ec2 describe-vpcs --region ${aws_region} --filters Name=tag:Name,Values=${cluster_one_name} | jq -r .Vpcs[].VpcId)"
echo "VPC ID is ${VPC_ID}"

if ! kops validate cluster "${cluster_two_name}"; then
    echo "Kops cluster \"${cluster_two_name}\" doesn't exist"
    sed -e  "s/<CLUSTER_NAME>/${cluster_two_name}/g" -e "s/<REGION>/${region}/g" -e "s/<CIDR>/${cluster_two_cidr}/g" -e "s/<VPC_ID>/${VPC_ID}/g" -e "s/<MASTER_SIZE>/${master_size}/g" -e "s/<NODE_SIZE>/${node_size}/g" < cluster.yaml | kops create -f -
    kops create secret --name ${cluster_two_name} sshpublickey admin -i ~/.ssh/id_rsa.pub
    kops update cluster --name ${cluster_two_name} --yes
    echo "Waiting until kops cluster ${cluster_two_name} gets ready..."

    kops export kubecfg "${cluster_two_name}" --admin=87600h
    kops validate cluster "${cluster_two_name}" --wait 20m
fi
kops export kubecfg "${cluster_two_name}" --admin=87600h


if ! kops validate cluster "${cluster_three_name}"; then
    echo "Kops cluster \"${cluster_three_name}\" doesn't exist"
    sed -e  "s/<CLUSTER_NAME>/${cluster_three_name}/g" -e "s/<REGION>/${region}/g" -e "s/<CIDR>/${cluster_three_cidr}/g" -e "s/<VPC_ID>/${VPC_ID}/g" -e "s/<MASTER_SIZE>/${master_size}/g" -e "s/<NODE_SIZE>/${node_size}/g" < cluster.yaml | kops create -f -
    kops create secret --name ${cluster_three_name} sshpublickey admin -i ~/.ssh/id_rsa.pub
    kops update cluster --name ${cluster_three_name} --yes
    echo "Waiting until kops cluster ${cluster_three_name} gets ready..."

    kops export kubecfg "${cluster_three_name}" --admin=87600h
    kops validate cluster "${cluster_three_name}" --wait 20m
fi
kops export kubecfg "${cluster_three_name}" --admin=87600h


./create_security_groups.py "${aws_region}" "${VPC_ID}" "${cluster_one_name},${cluster_two_name},${cluster_three_name}"
