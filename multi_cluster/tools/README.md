### Installing Istio in e2e clusters

The script is intended to install Istio in the multi E2E clusters that we have currently deployed.

Steps to run the script and verify it:

* Install the istioctl binary:
  `curl -sL https://istio.io/downloadIstioctl | ISTIO_VERSION=1.27.1 sh -`
  `export PATH=$PATH:$HOME/.istioctl/bin`

* Export cluster variables:
  `export CTX_CLUSTER1=e2e.cluster1.mongokubernetes.com`

   `export CTX_CLUSTER2=e2e.cluster2.mongokubernetes.com `


*  Run the script : `sh ./install_istio.sh`

* [Verify the Istio installation](https://istio.io/latest/docs/setup/install/multicluster/verify/)
