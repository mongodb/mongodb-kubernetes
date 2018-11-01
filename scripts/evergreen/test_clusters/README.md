# Aulë

The purpose of Aulë is to build Kubernetes clusters in Amazon. It can
currently spawn OpenShift clusters on AWS with CloudFormation.

## Basic Usage

    ./aule.py create-cluster --name my-cloud-formation-stack-name --aws-key my-aws-key

This will create the needed infrastructure for OpenShift to run in
AWS, included DNS, EBS for PersistentVolumes etc. A `control` instance
will be also created, from where you can execute the rest of the
commands. These commands are Ansible playbooks (provided by RedHat) to
deploy a full OpenShift cluster.


## Configuration

There are a few configuration options you'll have to have ready for
this to work. They need to exist in a file called `exports.do` that
will be copied into the control host. An example of this file is:

``` bash
export AWS_ACCESS_KEY_ID=<your-aws-key-id>
export AWS_SECRET_ACCESS_KEY=<your-aws-access-key>
export OPENSHIFT_CONNECT_ADMIN_USER=<redhat-connect-user>
export OPENSHIFT_CONNECT_ADMIN_PASSWORD=<redhat-connect-password>
export ECR_PASSWORD=<your-ecr-password-for-docker-login>
export OPENSHIFT_ADMIN_PASSWORD=
export OPENSHIFT_ADMIN_USER=
```

* `AWS_ACCESS_KEY_ID` and `AWS_SECRET_ACCESS_KEY`: If you have
  configured AWS CLI tool, this value should be in
  `~/.aws/credentials`.
* `OPENSHIFT_CONNECT_ADMIN_PASSWORD` and
  `OPENSHIFT_CONNECT_ADMIN_USER` a valid user and password for RedHat
  connect.
* `ECR_PASSWORD` this password can be obtained by running `aws ecr
  get-login --no-include-email --region us-east-1`. This will be the
  value of the `-p` parameter.
* `OPENSHIFT_ADMIN_USER` and `OPENSHIFT_ADMIN_PASSWORD` the web
  credentials for the admin user of the OpenShift cluster to be
  created. To know how to generate the password please see
  [this](https://docs.openshift.com/container-platform/3.11/install_config/configuring_authentication.html#HTPasswdPasswordIdentityProvider).

## Evergreen

For Evergreen to be able to run test in your new cluster, the following variables need to be specified:

*  **openshift_cluster_url**: Location of your OpenShift installation
   (https://master.openshift-cluster.mongokubernetes.com:8443)
* **openshift_cluster_token**: Token for loging using the `oc`
  tool. This is a manual process for now, after creating your
  Openshift cluster, visit your cluster with your browser and
  login. In the upper right part of your window, click on the "Admin"
  menu, and then in "Copy Login Command". Just paste whatever you have
  in your clipboard, it should be something like `oc login
  <openshift_cluster_url> --token=<openshift_cluster_token>`. The
  value you need is the value of the `<openshift_cluster_token>`. A
  token will be something like
  `HAh_04jcJ5_9mmwIsxPj1b1becaa8KUNapExp5jHw8Gw`.

## Origin of the Name

Aulë is part of the Tolkien mythology: the Smith, the Creator,
concerned with rock, metal, nature of substances and works of craft.

See http://tolkiengateway.net/wiki/Aul%C3%AB
