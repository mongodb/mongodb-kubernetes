# E2E Tests

## General

We have single test suite which is run in two different platforms: native Kubernetes (AWS created by Kops) and Openshift
Each test creates a new K8s namespace and the new group in Ops Manager with the same name (something like `a-330-r1at3ukxl5b2sk6f20mcz`)
The namespace is left unremoved if the test fails so it was easier to check problems

## FAQ
#### How to use Openshift cluster and check its state?

1. login here https://master.openshift-cluster.mongokubernetes.com:8443   
1. user is "admin", password is "asdqwe1"
1. get a login token from the "admin" panel top-right
1. paste it in command line and use `oc` CMD app to query Openshift cluster

#### If the test has failed - how to check what happened there?
* Check logs in Evergreen
* Check the state of existing objects in namespace using `kubectl`/`oc` (if they were not deleted)
* Check the state of project in Ops Manager (for kops cluster it's `http://54.160.170.171:30039`). To find out the external ip of Ops Manager pod run the following command:
```bash
k get nodes -o wide | grep "$(k get pods/mongodb-enterprise-ops-manager-0 -n operator-testing -o wide | awk '{print $NF}')" | awk '{print $6}'
``` 


Note, that you need to open ports for Ops Manager instance first time:
    * login to `https://console.aws.amazon.com` using account `2685-5815-7000` and 
    * in `Security Groups` find the relevant group starting with `nodes.` prefix (e.g. `nodes.dev02.mongokubernetes.com`) 
    for Kops cluster or `openshift-test-workersecgroup-` for OpenShift  
    * add the following 'inbound' rule (opens the port `30039` for any client): 
```
Custom TCP Rule     TCP     30039   0.0.0.0/0, ::/0 
```

#### Cleaning the old namespaces manually
```bash
for f in $(kubectl get ns -o name | grep a-); do kubectl delete $f --force; done

# removing Ops Manager (will be created automatically before next test run)
kubectl delete operator-testing --force

```
