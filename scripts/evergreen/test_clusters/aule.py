#!/usr/bin/env python

"""
Creates Kubernetes Clusters in AWS.

Usage:
  {filename} create-cluster --name <cluster_name> --aws-key <aws_key> [--save-manual-steps]
  {filename} delete-cluster --name <cluster_name> [--no-wait]

{filename} can currently create OpenShift clusters on an automated way to be used by the E2E testing Framework we built.

"""


import os
import socket
import time

import boto3
import docopt

s3client = boto3.resource("s3")
cfclient = boto3.client("cloudformation")
ec2client = boto3.client("ec2")
r53client = boto3.client("route53")
CLOUD_FORMATION_TEMPLATE = "https://s3.amazonaws.com/om-kubernetes-conf/openshift.yaml"


def upload_cloud_formation_definition(filename):
    return s3client.Bucket("om-kubernetes-conf").upload_file(filename, filename)


def create_stack(name, aws_key, availability_zone="us-east-1a"):
    return cfclient.create_stack(
        StackName=name,
        TemplateURL=CLOUD_FORMATION_TEMPLATE,
        Parameters=[
            {"ParameterKey": "AvailabilityZone", "ParameterValue": availability_zone},
            {"ParameterKey": "KeyName", "ParameterValue": aws_key},
        ],
        Capabilities=["CAPABILITY_IAM"],
    )


def delete_stack(name):
    return cfclient.delete_stack(StackName=name)


def wait_stack_is_ready(stack_name):
    while True:
        response = cfclient.describe_stacks(StackName=stack_name)

        if response["Stacks"][0]["StackStatus"] == "CREATE_IN_PROGRESS":
            print(".", end="", flush=True)
            time.sleep(10)
            continue

        print("")
        break

    return response["Stacks"][0]["StackStatus"] == "CREATE_COMPLETE"


def wait_stack_is_deleted(stack_name):
    while True:
        try:
            response = cfclient.describe_stacks(StackName=stack_name)
        except Exception as e:
            # this means the stack is not found, we assume it to be deleted.
            return True

        if response["Stacks"][0]["StackStatus"] == "DELETE_IN_PROGRESS":
            print(".", end="", flush=True)
            time.sleep(10)
            continue

        print("")
        break

    return response["Stacks"][0]["StackStatus"] == "DELETE_COMPLETE"


def get_stack_resources_by_name(stack_name, name):
    resources = cfclient.list_stack_resources(StackName=stack_name)
    for res in resources["StackResourceSummaries"]:
        if res["LogicalResourceId"] == name:
            return res


def get_instance_from_ec2(instance_id):
    response = ec2client.describe_instances(InstanceIds=[instance_id])

    return response["Reservations"][0]["Instances"][0]


def get_update_dns_change_entry(hostname, public_ip):
    return {
        "Action": "UPSERT",
        "ResourceRecordSet": {
            "Type": "A",
            "Name": hostname,
            "TTL": 60,
            "ResourceRecords": [{"Value": public_ip}],
        },
    }


def update_dns(public_ip):
    dns_update_records = (
        "\\052.openshift-cluster.mongokubernetes.com.",
        "openshift-cluster.mongokubernetes.com.",
    )
    changes = [
        get_update_dns_change_entry(name, public_ip) for name in dns_update_records
    ]

    response = r53client.change_resource_record_sets(
        HostedZoneId="/hostedzone/Z1BNZ7MGFF9M06", ChangeBatch={"Changes": changes}
    )

    return response


def wait_for_dns_propagation(names):
    while True:
        for dns, ip in names:
            res = socket.gethostbyname(dns)
            if res != ip:
                print(".", end="")
                time.sleep(10)
                continue

        print("")
        break


def write_inventory_file(instances):
    with open("hosts.certification_prelude") as fd:
        prelude = fd.read()
    with open("hosts.certification_template") as fd:
        hosts = fd.read()

    with open("hosts.certification", "w") as fd:
        fd.write(
            prelude.format(
                master=instances["master"]["PublicDnsName"],
                node1=instances["node1"]["PublicDnsName"],
                node2=instances["node2"]["PublicDnsName"],
                control=instances["control"]["PublicDnsName"],
            )
        )
        fd.write(hosts)


def config_kubectl_context(name):
    "Gets the kubectl context configuration for the new cluster"
    pass


def create_exports_file(exports, filename="exports.do"):
    """Writes a bunch of key=value pair to a file meant to be sourced at the control host."""
    with open(filename, "w") as fd:
        for key, value in exports.keys():
            fd.write(f"export {key}={value}\n")


def create_stack_full(args):
    stack_name = args["<cluster_name>"]
    print("Uploading Cloud Formation Cluster definition.")
    upload_cloud_formation_definition("openshift.yaml")

    stack = create_stack(stack_name, args["<aws_key>"])
    print("Created stack with Id: {}".format(stack["StackId"]))

    print("Waiting for stack to be ready")
    wait_stack_is_ready(stack_name)

    resources = dict(
        master=get_stack_resources_by_name(stack_name, "openshiftmaster"),
        node1=get_stack_resources_by_name(stack_name, "openshiftworker1"),
        node2=get_stack_resources_by_name(stack_name, "openshiftworker2"),
        control=get_stack_resources_by_name(stack_name, "openshiftcontrol"),
    )

    print("master: {}".format(resources["master"]["PhysicalResourceId"]))
    print("node1: {}".format(resources["node1"]["PhysicalResourceId"]))
    print("node2: {}".format(resources["node2"]["PhysicalResourceId"]))
    print("control: {}".format(resources["control"]["PhysicalResourceId"]))

    instances = dict(
        master=get_instance_from_ec2(resources["master"]["PhysicalResourceId"]),
        node1=get_instance_from_ec2(resources["node1"]["PhysicalResourceId"]),
        node2=get_instance_from_ec2(resources["node2"]["PhysicalResourceId"]),
        control=get_instance_from_ec2(resources["control"]["PhysicalResourceId"]),
    )

    update_dns(instances["master"]["PublicIpAddress"])

    names_to_wait_for = (
        (
            "openshift-cluster.mongokubernetes.com.",
            instances["master"]["PublicIpAddress"],
        ),
        (
            "some.openshift-cluster.mongokubernetes.com.",
            instances["master"]["PublicIpAddress"],
        ),
    )

    print("Waiting for DNS propagation")
    wait_for_dns_propagation(names_to_wait_for)
    print("DNS has propagated")

    write_inventory_file(instances)

    playbooks = ("prerequisites.yml", "deploy_cluster.yml")
    playbooks_dir = "openshift-ansible/playbooks"
    ansible_path = ".local/bin/ansible-playbook"
    source_exports = "source exports.do"

    further_actions = ["ansible-playbook -i hosts.certification prepare.yml"]
    for playbook in playbooks:
        ansible_cmd = "ssh centos@{} '{} && {}'".format(
            instances["control"]["PublicDnsName"],
            source_exports,
            get_ansible_execution_path(
                ansible_path, "hosts.certification", playbooks_dir, playbook
            ),
        )
        further_actions.append(ansible_cmd)

    further_actions.append("ansible-playbook -i hosts.certification postinstall.yml")

    further_actions = "\n".join(further_actions)

    if args["--save-manual-steps"]:
        message = (
            "Plase execute 'sh complete_installation.sh' to finish your installation"
        )
        with open("complete_installation.sh", "w") as fd:
            fd.write("#!/usr/bin/env bash\n")
            fd.write("set -e\n\n")  # fail on errors
            fd.write(further_actions)
            fd.write("\n")
    else:
        message = f"Done, now run ansible commands\n{further_actions}"

    print(message)

    print("""# TODO:
# The authorization tokens will expire after a day, to fix this, change the `accessTokenMaxAgeSeconds`
# in the /etc/origin/master/master-config.yaml file to 8640000 (100 days) and then run:
#
# `/usr/local/bin/master-restart api`
#
""")


def get_ansible_execution_path(ansible_path, inventory, playbooks_dir, playbook):
    return f"{ansible_path} -i {inventory} {playbooks_dir}/{playbook}"


def main(args):
    stack_name = args["<cluster_name>"]

    if args.get("delete-cluster", False):
        delete_stack(stack_name)

        if args["--no-wait"] is False:
            print("Stack is being deleted")
            wait_stack_is_deleted(stack_name)
        print("Cluster deleted")

    elif "create-cluster" in args:
        create_stack_full(args)


if __name__ == "__main__":
    main(docopt.docopt(__doc__.format(filename=os.path.basename(__file__))))
