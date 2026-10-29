# IdP federation POC

> Disclaimer: this POC (this guide, the scripts and the Go code) was mostly generated with AI.

This POC shows that the operator can authenticate to member clusters with short-lived tokens from an IdP, instead of static service account tokens. No operator code was changed.

- A mock IdP (`navikt/mock-oauth2-server`) runs in a container on the Evergreen host.
- The member clusters' API servers are configured to trust it.
- In the operator's member kubeconfig, each static token is replaced by an `exec:` entry that runs `cmd/idp-credential`. This binary requests a token from the IdP and prints it for client-go.

## Files

- `cmd/idp-credential/main.go`: the exec credential binary.
- `scripts/dev/idp_poc_host_setup.sh`: runs on the Evergreen host. Starts the IdP, configures the member API servers, and creates the RBAC bindings.
- `scripts/dev/idp_poc_local_setup.sh`: runs on the Mac. Builds the binary and swaps the member kubeconfig users from `token:` to `exec:`.

## Setup

- An Evergreen host with the multi-cluster kind clusters (`kind-e2e-operator`, `kind-e2e-cluster-1/2/3`), on Kubernetes 1.30 or later.
- `yq` (mikefarah) on the Mac.
- The operator runs locally in the IDE (`LOCAL_OPERATOR=true`).
- Source your env.

## Steps

1. `scripts/dev/evg_host.sh tunnel`
2. In a second terminal: `scripts/dev/evg_host.sh ssh -N -L 4200:127.0.0.1:4200`
3. Prepare the clusters:
   ```bash
   .cripts/dev/prepare_local_e2e_run.sh
   scripts/dev/configure_container_auth.sh
   ```
4. `scripts/dev/evg_host.sh` and `scripts/dev/idp_poc_host_setup.sh`
5. `sudo sh -c 'grep -q central-idp /etc/hosts || echo "127.0.0.1 central-idp" >> /etc/hosts'`
6. `scripts/dev/idp_poc_local_setup.sh`
7. `scripts/dev/evg_host.sh cmd "KUBECONFIG=~/.operator-dev/evg-host.kubeconfig kubectl config use-context kind-e2e-operator"`
8. Run the operator in your IDE, and create a `MongoDBMultiCluster` in `mongodb-test` on `kind-e2e-operator`, with members `kind-e2e-cluster-1/2/3`.

The POC works if StatefulSets and Pods are created on the three members and the resource reaches `Running`.

## Shortcuts

These are deliberate and not meant for production:

- The mock IdP accepts any client secret, and the binary skips TLS verification of the IdP.
- The operator identity is bound to `cluster-admin` on the members.
- The host script patches the `kube-apiserver` static pods directly, and pins the IdP IP in each node's `/etc/hosts`. Run it again if the IdP container is recreated.
- Cleanup (`make reset`) does not undo the IdP setup on the members.
