# E2E Tests

## General

We have single test suite which is run in two different platforms: native Kubernetes (AWS created by Kops) and Openshift
Each test creates a new K8s namespace and the new group in Ops Manager with the same name (something like `a-330-r1at3ukxl5b2sk6f20mcz`)
The namespace is left unremoved if the test fails so it was easier to check problems

## FAQ
#### How to connect to Openshift e2e cluster?

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

### How to recreate e2e kops cluster?

```bash
CLUSTER=e2e.mongokubernetes.com

# wait until the cluster is removed
kops delete cluster $CLUSTER --yes

# make sure you run the version >= 1.11.0
kops version

# generate keys if necessary
ssh-keygen -f ~/.ssh/id_aws_rsa && ssh-add ~/.ssh/id_aws_rsa

kops create cluster --node-count 3 \
    --zones us-east-1a,us-east-1b,us-east-1c \
    --node-size t2.large \
    --node-volume-size 32 \
    --master-size=t2.medium \
    --master-volume-size 16  \
    --kubernetes-version=v1.11.6 \
    --ssh-public-key=~/.ssh/id_aws_rsa.pub \
    --authorization RBAC \
    $CLUSTER
    
kops create secret --name $CLUSTER sshpublickey admin -i ~/.ssh/id_aws_rsa.pub

# edit and add the following record to the spec:
#  kubeAPIServer:
#    authorizationRbacSuperUser: admin
kops edit cluster $CLUSTER

kops update cluster $CLUSTER --yes

```
Follow up:
* Add all team members public keys to `.ssh/authorized_keys` file on each node
* Configure firewall rules for Ops Manager (see below)

### How to recreate e2e Openshift cluster?

1. Install `ansible`: `sudo easy_install pip && sudo pip install ansible`
1. Create `scripts/evergreen/test_clusters/exports.do` following the instructions in `scripts/evergreen/test_clusters/README.md`
    * specify `export OPENSHIFT_ADMIN_USER=admin` and `export OPENSHIFT_ADMIN_PASSWORD='$apr1$qoY/N094$ohaRogbdoWWz.W1gFhfYk/'` to get `asdqwe1` password
1. Generate AWS key pair if necessary: https://console.aws.amazon.com/ec2/v2/home?region=us-east-1#KeyPairs:sort=keyName. 
Don't do it if you already have a private key in `~/.ssh` for some AWS key pair - then you can reuse it. Ideally both kops
and openshift clusters should be created with the same ssh keys
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
* Check logs in Evergreen
* Check the state of existing objects in namespace using `kubectl`/`oc` (if they were not deleted)
* Check the state of project in Ops Manager. 
    * To find out the external ip of Ops Manager pod run the following command:
```bash
kubectl get nodes -o wide | grep "$(k get pods/mongodb-enterprise-ops-manager-0 -n operator-testing -o wide | awk '{print $NF}')" | awk '{print $6}'
``` 
    * Use `admin/admin12345%` to login

Note, that you need to open ports for Ops Manager instance first time:
    * login to `https://console.aws.amazon.com` using account `2685-5815-7000` and 
    * in `Security Groups` find the relevant group ([e2e](https://console.aws.amazon.com/ec2/v2/home?region=us-east-1#SecurityGroups:search=nodes.e2e;sort=groupName) 
    or [openshift](https://console.aws.amazon.com/ec2/v2/home?region=us-east-1#SecurityGroups:search=openshift-test-workersecgroup-;sort=groupName))    
    * add the following 'inbound' rule (opens the port `30039` for any client): 
```
Custom TCP Rule     TCP     30039   0.0.0.0/0, ::/0 
```

#### Cleaning the old namespaces manually
```bash
./scripts/evergreen/prepare_test_env
```

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
 These volumes are removed manually in `scripts/evergreen/prepare_test_env` script
* One quite common and annoying thing is taint `NodeWithImpairedVolumes` that is sometimes added to the Kubernetes nodes.
It means that there are some stuck volumes. The fixes above try to fix all stuck volumes (though the taint is not removed automatically).
Also the taint is removed in `prepare_test_env`. This doesn't mean that the problem is solved completely (AWS is quite 
unpredictable) but may help sometimes avoid complete rebuilds of the cluster
 
 #### How to restart kops cluster (creating new nodes)
 
If the volumes hacks don't help and volumes keep getting stuck the best option is to rebuild kops cluster (cloudonly flag 
means do not validate the cluster):

```bash
kops rolling-update cluster e2e.mongokubernetes.com --yes  --cloudonly --force
```

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
