# Ops Manager preparation #

**Make sure you use the Ops Manager of version >= 4.0!**
 
After Ops Manager is started (possibly in mci) you need to perform some actions to get it ready for communication with
Kubernetes Mongodb Operator:

1. Generate public api key (click user name in top right corner and select "Public API Access" tab). Write down the 
key for later usage
1. Add Kubernetes cluster hosts to "API Whitelist" section. If you use `Minikube` then search for *myip* in google and
copy the ip address shown. If you have your cluster deployed in AWS or GCP then you need to add external ips of all 
non-master nodes
1. Write down the group id (you can take it directly from url) and host of Ops Manager - this will be necessary for 
configuring Operator later
