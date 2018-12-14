# Creation and Management of AWS Kubernetes cluster using `kops`

The easiest way of deploying and managing a Kubernetes cluster is using [kops](https://github.com/kubernetes/kops/blob/master/docs/aws.md#setup-iam-user) command line utility which is called a "kubectl for clusters"

## Prerequisites
1. Install `kops`:  `brew update && brew install kops`
2. Install `kubectl`
3. Install [aws tools](https://docs.aws.amazon.com/cli/latest/userguide/installing.html).
   * Make sure AWS api credentials are added to local aws config file (`aws configure`)
4. You can export the name of the cluster you plan to use most often, for example `export CLUSTER=dev.mongokubernetes.com`

### If you are a member of Private Cloud team

1. Check that you have access to the "MMS Engineering Test" AWS account for account (268558157000). Read the [AWS Accounts](https://wiki.corp.mongodb.com/display/DEVOPSP/How-To%3A+Access+AWS+Accounts) guide if you're not sure.
   * Your account must be a member of the group `Ops_Manager_Kubernetes`, otherwise ask anyone from Private Cloud team to create a user account.
2. Add `export KOPS_STATE_STORE=s3://kube-om-state-store` to your `~/.bashrc` file to tell kops the name of the S3 bucket containing our kops configuration.
   * This will allow to easily import kops cluster configurations that exist in the AWS account

## Using an existing Kubernetes cluster

1. To configure kubectl by existing cluster name:
```bash
kops export kubecfg $CLUSTER
kubectl get nodes
```
2. SSH access. The easy way is to just share the private key (don't forget to add it through `ssh-add`). Seems the hard way is to add the ssh public key for another user to all the EC2 instances individually using AWS UI/CLI. ()The trick with adding another kops secret for admin didn't work..)


## Building a new Kubernetes cluster using kops

### Prepare the AWS Account

To set up kops on a new AWS account, some extra [configuration](#aws_configuration) is required. This is not needed if you are using an AWS access that has already been configured by the team.

### Create a new Kubernetes Cluster

1. Generate a new pair of SSH keys, public key will be used when creating the cluster.
```
ssh-keygen -f ~/.ssh/id_aws_rsa && ssh-add ~/.ssh/id_aws_rsa
```
2. Create the cluster configuration for the cluster named `dev.kube.mmscloudteam.com` (note, that real cluster won't be created, use `--yes` flag if you want to create it right away. You can use `--dry-run --output=yaml` flags to just show the config generated)
```bash
kops create cluster --node-count 3 --zones us-east-1a,us-east-1b,us-east-1c --node-size t2.small --node-volume-size 16 --master-size=t2.small --master-volume-size 16  --kubernetes-version=v1.11.0 --ssh-public-key=~/.ssh/id_aws_rsa.pub --authorization RBAC $CLUSTER
```
3. (Optionally) Create kops secret with just generated public key (this allows to replace public keys easily for the cluster later)
```bash
kops create secret --name $CLUSTER sshpublickey admin -i ~/.ssh/id_aws_rsa.pub
```
4. Edit the config created. As the `kops` cluster is created with RBAC enabled - we need to make the admin a superuser:
```bash
kops edit cluster $CLUSTER
```
Add the following block to the `spec` element to yaml:
```yaml
  kubeAPIServer:
    authorizationRbacSuperUser: admin
```
5. Real creation of all the resources
```bash
kops update cluster $CLUSTER --yes
.... (skipping the output)
kops has set your kubectl context to <..>

Cluster is starting.  It should be ready in a few minutes.
```
Now you can use `kubectl` right away (as `kops` will add all necessary configuration to kube config) and SSH to the machines in the cluster under `admin` user (check the DNS names in AWS console beforehand) as private key was added to ssh config using `ssh-add` before.
```bash
kubectl get nodes
# some quick check for cluster availability - deploy two nginx servers
kubectl run my-nginx --image=nginx --replicas=2 --port=80
kubectl get pods
kubectl get deployment
kubectl expose deployment my-nginx --port=80 --type=LoadBalancer
kubectl get services # get the external endpoint
# wait for ~1 minute
open <LB external endpoint> # you will see the "Welcome to nginx!" page
```
6. Download the configuration for cluster as yaml
```bash
kops get --name $CLUSTER -o yaml > dev_cluster_config.yml
```
### Delete the Cluster

```bash

kops delete cluster $CLUSTER --yes
```

### Change public keys on existing cluster

```bash
kops delete secret --name $CLUSTER sshpublickey admin
kops create secret --name $CLUSTER sshpublickey admin -i ~/.ssh/newkey.pub
kops update cluster $CLUSTER --yes # to reconfigure the auto-scaling groups
kops rolling-update cluster --name $CLUSTER --yes # to immediately roll all the machines so they have the new key (optional)
```

### Install the Kubernetes Dashboard

```bash
kubectl apply -f https://raw.githubusercontent.com/kubernetes/dashboard/master/src/deploy/recommended/kubernetes-dashboard.yaml
# ... let it deploy
```

Open the link https://api.dev.mongokubernetes.com/api/v1/namespaces/kube-system/services/https:kubernetes-dashboard:/proxy/#!/overview?namespace=default in browser

Api server will ask for basic authentication - use `admin` as user name and `kops get secrets kube -oplaintext` for password
After that dashboard will ask you to authenticate as well - you need to upload `~/.kube/config` file but before this you need to update its credentials:

```bash
kubectl -n kube-system describe secret $(kubectl -n kube-system get secret | grep kubernetes-dashboard-token | awk '{print $1}') # (copy the token value)
kubectl config set-credentials $CLUSTER --token=<token_value>
```

After that you can upload your kube config to authenticate

### <a name="aws_configuration"></a> AWS Configuration (Must be done only once for new account)
#### Creating S3 bucket
The s3 bucket is needed to store kops configuration. This allows team members to easily share and manage clusters.

```bash
aws s3api create-bucket --bucket kube-om-state-store --region us-east-1
aws s3api put-bucket-versioning --bucket kube-om-state-store  --versioning-configuration Status=Enabled
```

**Proceed to the next section only if you plan to use a _subdomain_ of the main domain for the AWS account**
   
#### Configuring hosted zone in subdomain of the domain purchased in AWS

1. Create Hosted zone, outputting the NS servers
```bash
ID=$(uuidgen) && aws route53 create-hosted-zone --name kube.mmscloudteam.com --caller-reference $ID --hosted-zone-config Comment="Hosted zone used for Kubernetes clusters. Owned by OpsManager team"|    jq .DelegationSet.NameServers

[
  "ns-794.awsdns-35.net",
  "ns-203.awsdns-25.com",
  "ns-1571.awsdns-04.co.uk",
  "ns-1129.awsdns-13.org"
]
```
2. Find the parent domain id

```bash
aws route53 list-hosted-zones | jq '.HostedZones[] | select(.Name=="mmscloudteam.com.") | .Id'
"/hostedzone/Z2K45YHT6R24Z1"
```

3. Change the NS records for parent zone to point to new NS servers

**kube.json**:

```bash
{
  "Comment": "Create a subdomain NS record for kube. in the parent domain mmscloudteam.com",
  "Changes": [
    {
      "Action": "CREATE",
      "ResourceRecordSet": {
        "Name": "kube.mmscloudteam.com",
        "Type": "NS",
        "TTL": 300,
        "ResourceRecords": [
          {
            "Value": "ns-794.awsdns-35.net"
          },
          {
            "Value": "ns-203.awsdns-25.com"
          },
          {
            "Value": "ns-1571.awsdns-04.co.uk"
          },
          {
            "Value": "ns-1129.awsdns-13.org"
          }
        ]
      }
    }
  ]
}
```

```bash
aws route53 change-resource-record-sets --hosted-zone-id /hostedzone/Z2K45YHT6R24Z1 --change-batch file://kube.json
```

4. Testing

```bash
dig ns kube.mmscloudteam.com
;; ANSWER SECTION:
kube.mmscloudteam.com.	21599	IN	NS	ns-1129.awsdns-13.org.
kube.mmscloudteam.com.	21599	IN	NS	ns-1571.awsdns-04.co.uk.
kube.mmscloudteam.com.	21599	IN	NS	ns-203.awsdns-25.com.
kube.mmscloudteam.com.	21599	IN	NS	ns-794.awsdns-35.net.
```

