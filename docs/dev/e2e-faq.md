# E2E Tests

## General

We have single test suite which is run in two different platforms:
native Kubernetes (AWS created by Kops) and Openshift.

Each test creates a new K8s namespace and a new group in Ops Manager
with the same name as the namespace (something like
`a-330-r1at3ukxl5b2sk6f20mcz`). After the test finish, and in case of
a success run, the namespace will be removed. If the test fail, we
guarantee that the test namespace will remain for at least one hour
for you to investigate what is needed.

The tests are run against a combination of Ops Manager and Cloud
Manager releases and Kubernetes clusters (kops/Openshift)
 
# How to choose the taskGroup/buildVariant for an e2e test
1. Choose the task group (or create a new one) where the e2e test should fit into:
* `e2e_kube_only_task_group`: the task group for tests which focus on **Kubernetes features**
instead of Ops Manager ones. Examples: PersistentVolumes, CRD validation, Operator recovery 
from Kubernetes errors. This task group won't be run on all OM versions - only the
latest ones (1 per kops and Openshift cluster)
* `e2e_core_task_group`: the general task group which is run in both kops and Openshift
clusters for **all** the tested versions of OM and CloudManager QA. The tests are focused
on testing the OM + Automation Agents behavior
* `e2e_tls_task_group`: any tests focused on TLS. As they focus on OM more than on Kubernetes
they don't run on OM 4.2 version in both kops and Openshift clusters - only in kops one
* `e2e_scram_sha_task_group`: scram-sha related tests. Run on kops OM 4.2 and on Openshift CM QA.
* `e2e_tls_custom_ca_task_group`: the same as for scram-sha
* `e2e_x509_task_group`: the same as for scram-sha
* `e2e_om_4_2_plus_only_task_group`: includes tests for functionality which works in only OM 4.2.
Run in both kops OM 4.2 and Openshift OM 4.2
* `e2e_ops_manager_task_group`: the task group for all OM Custom Resource related tests

# Choose the correct tag
 
The tag assigned to e2e test affects which build variant will be chosen for the test for Evergreen
builds run in Github PRs. For example
```
- name: e2e_replica_set_tls_prefer
  tags: ["openshift-om-qa"]
  exec_timeout_secs: 1200
  commands:
  - func: "e2e_test"
```
means that in the PRs the test will be run in the build variant `e2e_openshift_cloud_qa`
**It's important to make sure that the task group which the test belongs to is run by this build 
variant!** Otherwise the test may be skipped from PR run totally and may get red only after merge 
to master.

## FAQ
#### How to connect to Openshift e2e cluster?

1. `brew install openshift-cli`
1. login here https://master.openshift-cluster.mongokubernetes.com:8443
1. user is "admin", password is "asdqwe1"
1. get a login token from the "admin" panel top-right
1. paste it in command line and use `oc` CMD app to query Openshift cluster

#### How to connect to kops e2e cluster?
```bash
export KOPS_STATE_STORE=s3://kube-om-state-store
kops export kubecfg e2e.mongokubernetes.com
#(later when you need to switch to this cluster)
kubectl config use-context e2e.mongokubernetes.com
```

#### How to recreate e2e kops cluster?

```bash
make recreate-e2e-kops
```

Follow up:
* Add all team members public keys to `.ssh/authorized_keys` file on each node
* Configure firewall rules for Ops Manager (see below)

#### How to recreate e2e Openshift cluster?

* Requirements
  + `openshift-test-cluster` RSA key. Ask your team mates for it. Do not use a personal key!
  + Make sure you have configured ssh client (`~/.ssh/config`) like:

```
Host *.compute-1.amazonaws.com
   ForwardAgent yes
   StrictHostKeyChecking no
   IdentityFile ~/.ssh/id_aws_rsa
```

``` bash
make recreate-e2e-openshift
```

#### Old instructions for Openshift cluster (might still be relevant)

1. Create `scripts/evergreen/test_clusters/exports.do` following the instructions in `scripts/evergreen/test_clusters/README.md`
    * specify `export OPENSHIFT_ADMIN_USER=admin` and `export OPENSHIFT_ADMIN_PASSWORD='$apr1$qoY/N094$ohaRogbdoWWz.W1gFhfYk/'` to get `asdqwe1` password
1. Delete the cluster: `python3 scripts/evergreen/test_clusters/aule.py delete-cluster --name openshift-test`
1. Create a new cluster: `cd scripts/evergreen/test_clusters/; python3 aule.py create-cluster --name openshift-test --aws-key <your_aws_key_pair_name>`
   * `--aws-key` is the name of ssh key pair
1. The script will output the sequence of commands to call - invoke them
   * caveat 1: you'll have to accept the identity for the hosts manually to allow ssh-ing there
   * caveat 2: you need to enable the `ForwardAgent` for the control host where you'll run ansible scripts:
   ```
   # ~/.ssh/config
   Host ec2-54-164-91-238.compute-1.amazonaws.com
       ForwardAgent yes
   ```
   * if some of the parameters in `exports.do` have changed - you need to copy the file to the remote host manually
   before running `ssh ... 'source exports.do ...' ` again


#### If the test has failed - how to check what happened there?
* **Check logs in Evergreen** (they show the output from testing application)
* Check the state of existing objects in the namespace - check the **files attached to the Evergreen job**:
    * `diagnostics.txt` - contains the output from `kubectl get.. -o yaml` for the most interesting objects in the namespace
    (if they exist): Persistent Volume Claims, Mongodb resources, pods
    * `operator.log` - contains the log from the Operator
    * `*` - set of files containing logs from Mongodb resource pods (for sharded clusters this includes only shards)
* If the files didn't provide enough information you can always use `kubectl` to query **more information for the Kubernetes cluster**
(`kubectl config use-context e2e.mongokubernetes.com/default/master-openshift-cluster-mongokubernetes-com:8443/admin`)
* Check the state of project in **Ops Manager**.
    * To find out the external ip of Ops Manager check the bottom of the output of a failed task in Evergreen - it will contain
    the following phrase:
    "Use the following address to access Ops Manager from the browser: http://3.87.239.164:30039 (namespace: a-042-y6y9c31v8j5kt9vumeptz)"
    * Use `admin/admin12345%` to login and search for the namespace
    * Note, that the external ports are opened automatically by the setup script so you don't need to do this
* If Ops Manager container is not running - the best way is to check its state using **Kubernetes Dashboard**:
    * switch kubectl context to the necessary cluster (for `e2e.mongokubernetes.com` the dashboard is installed automatically,
    for the openshift one you need to do this manually once)
    * call `make dashboard` and enter the token that is copied to the clipboard

#### Cleaning old namespaces via CronJob

The `docker/cluster-cleaner/job.yaml` should be applied on each
cluster. This file is a `CronJob` that runs every 10 minutes. Every
time this Job runs, it will remove all the testing namespaces in the
cluster that are older than 1 hour and that have "failed". Remember
that the tests that Succeed are removed immediately.

#### Getting Credentials for Cloud-QA

The different Ops Manager instances that run inside the Kubernetes
Cluster will use a `GLOBAL_OWNER` user. This is different as to how we
connect "Cloud-qa". In this case, the user is created manually and the
credentials are configured in the `ops-manager-kubernetes` Evergreen
project. These credentials will work for 30 days (until the trial
expires). To create a new user:

* Visit [Cloud QA Registration
  Page](https://cloud-qa.mongodb.com/user#/cloud/register/accountProfile)
* Register a new user with the following data:

| attribute | value | notes |
|-----------|-------|-------|
| Email Address | ops-manager-team+cloud-qa-kube-operator-e2e-<index>@mongodb.com | Make sure you increment the `<index>` |
| Password | *Ask someone on the Kubernetes team about this password.*| |
| First Name | Kubernetes | |
| Last Name | E2E Tests | |
| Phone Number | +353 (01) 901 4654 | This is the Dublin Office Phone Number |
| Company Name | Ireland | |
| Job Function | DBA | |
| Country | Ireland | |

* After logging-in, click on "Kubernetes" (our First Name) on the
  top-right part of the UI.
* Click on Account and then in "Public API Access"
* Create a new API Key, don't forget to write it down
* Whitelist all the IPs of the Kubernetes cluster that will be
  connecting to Cloud Manager. An easy way of getting all the external
  IPs that a cluster is using is to do:

``` bash
kubectl get nodes -o jsonpath='{$.items[*].status.addresses[?(@.type=="ExternalIP")].address}'
```

* Get the name of the [default
  organization](https://cloud-qa.mongodb.com/v2#/account/organizations). Find
  the organization named *MongoDB* (this is the "Company Name" you set
  during registration. Click on the Organization name and get the
  Organization ID from the URL.

* Finally, update this information into [Evergreen
  project](https://evergreen.mongodb.com/projects##ops-manager-kubernetes).

* The attributes to fill up are:
  - `e2e_cloud_qa_apikey`: The new Public API Key
  - `e2e_cloud_qa_baseurl`: This is always
    `https://cloud-qa.mongodb.com`
  - `e2e_cloud_qa_orgid` : Organization ID
  - `e2e_cloud_qa_user` : Email used for registration

#### How to avoid my test Namespaces being deleted?

You can label your namespaces to flag them so the `cluster-cleaner`
script won't kill them. Use the `label` command from `kubectl` like:

``` bash
kubectl label namespace/my-namespace "evg/keep=true"
```

This will skip the cleaner for as long as you need from removing your
testing namespace. To remove the label so the cluster cleaner can
deallocate the resources, remove the label from it with:

``` bash
kubectl patch namespace/my-namespace --type=json -p='[{"op": "remove", "path": "/metadata/labels/evg~1keep"}]'
```

* note: the `~1` in the `jsonpath` is how `/` (slash) is escaped.

#### Problems with EBS volumes
These are some facts that we gathered while fighting with EBS problems for e2e tests:
* Backing EBS volume (see `Volumes` in https://console.aws.amazon.com/ec2) are removed as soon as PVs are removed. We
use dynamic PVs in our tests, so to get them removed their PVCs must be removed (this happens when the namespace is removed
which happens after successful test or during namespaces cleanup). Dynamic removal happens because the `StorageClass` we use
(default one - `gp2`) declares the `Delete` reclaim policy.
* Usually this works fine, but sometimes the EBS volumes can get stuck in attaching and not removed even if PVs are removed:
 ![stuck-volumes](stuck-volumes.png)
 Such PVs get the status `Failed` and must be removed manually. This is done in `prepare_test_env`. It's still unclear
 if AWS removes the corresponding volumes eventually (seems no) so it's necessary to go the the UI and "force detach" them
 and delete then
* Seems there are problems cleaning volumes for Openshift (sometimes?). Volumes tend to stay in AWS but get status `available`:
 ![available-volumes](available-volumes.png)
 These volumes are removed automatically in `scripts/evergreen/prepare_test_env` script
* One quite common and annoying thing is taint `NodeWithImpairedVolumes` that is sometimes added to the Kubernetes nodes.
It means that there are some stuck volumes. The fixes above try to fix all stuck volumes (though the taint is not removed automatically).
Also the taint is removed in `prepare_test_env`. This doesn't mean that the problem is solved completely (AWS is quite
unpredictable) but may help sometimes avoid complete rebuilds of the cluster
* Sometimes deleting of the PVC/PV may get stuck. Even more - "Force detach" for the Volume in AWS console may get stuck as well.
Seems there are no well-knows ways of solving this except for recreating kops cluster...


#### Error terminating Ops Manager instance

This is something new and very rare. The first symptom of big problems is not being able to connect to the container:
```bash
kubectl -n "operator-testing" exec mongodb-enterprise-ops-manager-0 bash
rpc error: code = 14 desc = grpc: the connection is unavailable
command terminated with exit code 126
```

(Although the pod is in healthy state and `kubectl logs` return new logs all the time)

Trying to remove the namespace leaves the pod in `Terminating` state:
```bash
kubectl get all -n operator-testing -o wide
NAME                                  READY     STATUS        RESTARTS   AGE       IP             NODE
po/mongodb-enterprise-ops-manager-0   0/1       Terminating   0          3h        100.96.2.107   ip-172-20-96-41.ec2.internal
```

One possible inspection is to check the `kubelet` logs on the node but this requires `ssh` access to the node
Trying to detach/remove OM volumes in AWS console didn't succeed so only complete recreation of kops cluster helped

#### SSH access to nodes

* If there's someone who has the ssh access to the node then you should ask to add your public key to the
`.ssh/authorized_keys` on the node
* If the access is lost then it makes sense to use a new key pair (from https://github.com/kubernetes/kops/blob/master/docs/security.md#ssh-access)
```bash
kops delete secret --name e2e.mongokubernetes.com sshpublickey admin
kops create secret --name e2e.mongokubernetes.com sshpublickey admin -i ~/.ssh/newkey.pub
kops update cluster --yes
kops rolling-update cluster --yes
```

#### Runnings tests against a specific (perpetual) Ops Manager instance

The Evergreen project can be configured to either deploy an OM instance in Kubernetes for each test run, OR always direct traffic to a specific instance (named Ops Manager Kubernetes Perpetual, or `omkp` for short).

The feature can be controlled via the [Evergreen project settings](https://evergreen.mongodb.com/projects##ops-manager-kubernetes), by the `omkp_enabled` variable.

If the instance has been reaped, you will need to deploy a new one (see below) and perform minimal configurations.


##### Install Ops Manager in AWS

See the [Ops Manager deployment guide](./aws_ops_manager_deployment.md).

##### Configure Evergreen
- first, register a global owner on the Ops Manager instance
- generate a `public API key` and set a whitelist of `0.0.0.0/0`
- edit the following parameters in [EVG](https://evergreen.mongodb.com/projects##ops-manager-kubernetes):
  - `omkp_host`
  - `omkp_user`
  - `omkp_api_key`

##### Decide to use the perpetual instance, or deploy a new OM image in Kubernetes every time

go to [EVG](https://evergreen.mongodb.com/projects##ops-manager-kubernetes) and set:
  - `omkp_enabled=true` # Use the Perpetual instance
  - `omkp_enabled=false` # any value != `true` will have the effect of NOT using the perpetual instance

#### Testing a change against a specific (perpetual) Ops Manager instance

If you want to test a local change or a PR against the perpetual instance, simply change the equality comparison in `.evergreen.yml`/`&omkp_setup`:
```bash
# from
if [[ "${omkp_enabled}" == "true" ]]; then

# to
if [[ "${omkp_enabled}" != "true" ]]; then
```

Then submit an EVG patch (`evergreen patch -t ... -v ... -f -y -d ''`) or simply open a PR.

**DO NOT forget to revert the change to the `omkp_setup` EVG function!**
