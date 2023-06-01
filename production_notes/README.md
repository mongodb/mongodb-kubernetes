## Running Load Tests

### Deploy Monitoring

The monitoring setup will install Prometheus and Grafana. Promethues is configured with persistant storage and retention. Feel free to configure them in the promethues configmap (`04-cm.yaml`) to suit your needs.

* `kubectl apply -f monitoring/`.

* Add the prometheus svc(http://prometheus-int:8080) as the [source](https://grafana.com/docs/grafana/latest/datasources/add-a-data-source/) for Grafana. 
* Create dasboards in Grafana following this [instruction](https://grafana.com/docs/grafana/latest/getting-started/getting-started/#step-3-create-a-dashboard).

* Following are some of the queries you can add to get the necessary metrics from the operator:
    * __CPU Usage(millicores)__: 
        ```bash
        sum(rate(container_cpu_usage_seconds_total{namespace="mongodb", pod=~"om-operator-.*"}[2m])) by (pod) * 1000
        ```
    * __Memory Usage(MB)__: 
        ```bash
        sum(container_memory_usage_bytes{namespace="mongodb",pod=~"om-operator-.*", container="mongodb-enterprise-operator"}) by (pod_name) / 1e6
        ```
    * __Reconcile Time(P90 seconds)__:
        ```bash
        histogram_quantile(0.90, sum by (controller, le) (rate(controller_runtime_reconcile_time_seconds_bucket{controller="mongodbreplicaset-controller"}[1m]))) * 1e3
        ```
    * __Reconcile Time(average seconds)__:
        ```bash
        (rate(controller_runtime_reconcile_time_seconds_sum{controller="mongodbreplicaset-controller"}[1m]) / rate(controller_runtime_reconcile_time_seconds_count{controller="mongodbreplicaset-controller"}[1m])) * 1e3
        ```
    * __File Descriptor Count__:
        ```bash
        container_file_descriptors{namespace="mongodb",pod=~"om-operator-.*",container=~"mongodb-.*"}
        ```
    _Note: You might need to change the pod_name in the queries based on your configuration_
### Deploy Operator and OpsManager

* Create a namespace for running the tests: `kubectl create ns mongodb`.
* Update the helm chart dependency: `helm dep update helm_charts/opsmanager/`.
_Note: Make sure you've sufficient CPU/Memory reources in your cluster to deploy or adjust the resources in the yaml files accordingly_.
* Deploy OpsManager + Operator + Cert-manager: `helm install om helm_charts/opsmanager/`.
  _Note: If you have an existing deployment of cert-manager, the above command may error out. In that case, delete the existing `cert-manager` deployment and its corresponding CRDs._
*  Wait for the ops-manager CR to reach `Running` state.

### Deploy MongoDB Replicasets - Loadtests

* Build the `runtest` binary in the path `cmd/runtest`.
* The `runtest` binary is used to deploy MongoDB Replicasets and loadtest the operator setup.
* Run `./runtest --help` to checkout the various settings possible to deploy mongodbs.
* Ex command: 
```bash
  ./runtest --prometheus-url $prometheus_url --time-to-wait 80m --ops-manager-release-name om --tls true --mongodb-rs-count 1
  ``` 
  _Note: `$prometheus_url` can be obtained from the `loadbalancer-url` of the `prometheus-ext` service. The `runtest` command needs the prometheus-url to query the prometheus server we deployed to compute the CPU/Mmeory average metrics._
* Checkout the Grafana dashboard (deployed while installing the monitoring) to get insights about the operator metrics you would like to test.
* Additionally, to run [YCSB](https://github.com/brianfrankcooper/YCSB) against the mongoDB deployments you can execute the command.
  ```bash
    helm install ycsb --set binding=$mongodb-rs-name-"binding" --set tls=true helm_charts/ycsb/
  ```
