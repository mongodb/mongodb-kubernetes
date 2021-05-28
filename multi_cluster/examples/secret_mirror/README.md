### Multi-cluster operator prototype

This operator is intended to be used as a protoype to test cross-cluster reconciliations. In a later stage we will extend these ideas/primitives to the enterprise-operator codebase.

This operator watches for secret objects in `cluster1` and creates them in `cluster2`. It reconciles on secret create events in cluster1.


### Running and Testing it locally

* <b> Create two Kind clusters</b>
    * `kind create cluster --name cl1`
    * `kind create cluster --name cl2`


* <b> Save the kubeconfigs corresponding to kind clusters</b>
    * `kind get kubeconfig --internal --name cl1 > configs/cluster1`
    * `kind get kubeconfig --internal --name cl2 > configs/cluster2`

* <b> Push the dockerfile to a repo </b>
    * `env GOOS=linux GOARCH=amd64 go build .`
    * `docker build -t $TAG .`
    * `docker push $IMG`

* Change the image name in `deployment.yaml`
* Create the operator pod in cluster1 `cl1`: `kubectl create -f deployment.yaml`

* <b> Verify cross cluster secret creation </b>
   * create the test namespace in both the clusters.
   * create a secret object in `cl1` in the namespace.
      * ` kubectl create secret generic empty-secret`
   * verify the same secret object is created in cluster2 `cl2`.
