# Openshift 4.3

OpenShift 4.x clusters are installed using the
[openshift-installer](https://github.com/openshift/installer) tool provided by
RedHat. There's extensive documentation in their Github page that I won't
reproduce here, with the exception of what specific changes we did for the
cluster to be installed and any manual steps to perform after the cluster has
been installed.

## Required Tools

* [OpenShift-install](https://mirror.openshift.com/pub/openshift-v4/clients/ocp/latest/).
* [OpenShift-client](https://mirror.openshift.com/pub/openshift-v4/clients/ocp/latest/).

Get the packages containing the client and installer tools and copy the
`openshift-installer` and `oc` binaries to somewhere in your PATH.

## Required AWS Configuration

There's just one thing to do in AWS before you can install the Openshift
cluster, which is described in [this
document](https://github.com/openshift/installer/blob/master/docs/user/aws/iam.md).
This involves creating a user with `AdministratorAccess` and getting new AWS
credentials for it.

After creating the credentials (`access_key_id` and `secret_access_key`), add
them to your `~/.aws/credentials` file as a new profile, like in this example:

```
[default]
aws_access_key_id = XXX
aws_secret_access_key = YYY

[openshift-installer]
aws_access_key_id = AAA
aws_secret_access_key = BBB
```

In order to use this profile (`openshift-installer`) in particular, set the
profile AWS environment variable as:

    export AWS_PROFILE=openshift-installer

## Cluster Configuration

We'll first create a installation configuration by running:

```
openshift-install create install-config --dir=openshift4
```

During the creation of the install configuration you'll be asked a few
questions.

* ssh key: choose ssh key you want to use to access the cluster
* provider: aws
* domain: mongokubernetes.com
* subdomain: openshift4
* image pull secret: copy it from [here](https://cloud.redhat.com/openshift/install/aws/installer-provisioned)

Now we must configure our cluster a bit, open the
`openshift4/install-config.yaml` file and add the following to the root of the
yaml structure.

```yaml
compute:
- hyperthreading: Enabled
  name: worker
  platform:
    aws:
      rootVolume:
        iops: 4000
        size: 500
        type: io1
      type: c5.4xlarge
      zones:
      - us-east-1c
  replicas: 5
controlPlane:
  hyperthreading: Enabled
  name: master
  platform:
    aws:
      rootVolume:
        iops: 0
        size: 0
        type: ""
      type: m5.xlarge
      zones:
      - us-east-1a
      - us-east-1b
  replicas: 3
```

This will create a 3 master, 5 worker cluster distributed accross 3 availability
zones in region us-east-1. This can be changed if we need more resources in the
future.

In the example I'm using 3 availability zones, because the installer will by
default create nodes in every availability zone, but this will make it hit AWS
limits when creating elastic IPs. For now we can have our master and workers in
3 availability zones.

## Cluster Installation

Now that the installation has been configured, we can proceed to the
installation of the cluster. Just run:

    openshift-install create cluster --dir=openshift4

This process takes around 30 minutes to complete. When finished, it will output
the username and password that you have to use to connect to the cluster's web
ui. This web UI will be
[here](https://console-openshift-console.apps.openshift.mongokubernetes.com).

## Evergreen Configuration

The `ops-manager-kubernetes` Evergreen
[project](https://evergreen.mongodb.com/projects##ops-manager-kubernetes) needs
to be configured with a new kubeconfig. Login to the webui and click on "Copy
Login Command". Login localy using this command, like:

    oc login --token ...

This will generate a kubeconfig file, displayed in the output. Encode the
contents of the file and update the `openshift43_cluster_kubeconfig` Evergreen
variable.

    cat <location-of-kubeconfig> | base64 -w0 | pbcopy  # or xclip

## Removing Openshift 4

When installing Openshift4, there will be a file with name `metadata.json` that
you have to save. This file is used to remove the cluster and without this file,
the cluster won't be removed!!

Well, I think it can be removed, and I did it, but it takes a bit of work.

## Removing a cluster with missing `metadata.json`

* In case you only lost the `metadata.json` file, follow [these
instructions](https://access.redhat.com/solutions/3826921).

* If you removed the `metadata.json` file **and** your cluster is not
accessible, then the following instructions might help you:


* Find cluster_id from the tag of one of the resources. The tag has the format
  "kubernetes.io/cluster/<clusterID>" with `<clusterID>` looking something like
  a name and a random string of about 10 characters.
* Manually create a `metadata.json` with the following contents:

```json
{
    "aws": {
        "identifier": [
            {
                "kubernetes.io/cluster/<clusterID>": "owned"
            },
            {
                "openshiftClusterID": "<clusterID>"
            }
        ],
        "region": "us-east-1"
    },
    "infraID": "<clusterID>",
    "clusterID": "<clusterID>",
    "clusterName": "openshift"
}

```

* Run `openshift-install destroy cluster` and hope for the best :)
