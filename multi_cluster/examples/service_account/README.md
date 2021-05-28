

### This Operator Lists Pods in two Clusters.


* Build and push the operator image:

```bash
IMG=""
env GOOS=linux GOARCH=amd64 go build
docker build . -t ${IMG}
docker push ${IMG}
```

* Replace the image in `cluster1_resources/deployment` with the operator image.
* Change the namespace on every yaml file to your desired namespace.

* Before running the `convert_sa_to_kube_config.sh` script, change the values of `NAMESPACE` and `SA_NAME`
    * `NAMESPACE` will be the namespace in *both* clusters.
    * `SA_NAME` is the name of the ServiceAccount that will be created in `CLUSTER2`
        * If set to `can-read-pods` the operator that gets deployed will be able to list pods in both clusters
          as the `can-read-pods` ServiceAccount has the correct ClusterRole to read pods
        * If set to `cannot-read-pods` the operator will display errors as it cannot list pods due to lack of the
          correct ClusterRole.
          
* Run the `convert_sa_to_kube_config.sh` script

* Your context will be switched to `CLUSTER1` and you should see the operator deployment.

* How it works:
    * We create a ServiceAccount in `CLUSTER2`.
    * We create a KubeConfig from the associated secret.
    * We create a ConfigMap in `CLUSTER1`.
    * We create an operator deployment with that KubeConfig mounted inside.
    * We create both an incluster config, and a config from the KubeConfig.
    * We can list pods in both clusters with the RBAC rules in place for each individual cluster.
 