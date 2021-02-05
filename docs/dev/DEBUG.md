# Debugging a running operator

It is easy to debug a running Operator; in order to do so, we need to make sure 2
things are in place:

* The Operator is built with debugging symbols enabled.
* The Operator deployment need to change its "CMD" to use `delve` in front of
  the Operator.

## Build an Operator image with Debug enabled

After the following command, all of your Operator images will include `delve`.

    ./pipeline.py --include operator --debug

The multi stage build for the operator always build the binary in release mode,
so after *enabling* debug mode, the operator should be built using the *patch*
build:

    ./pipeline.py --include operator-quick

## Enable Debugging mode in the Operator deployment

You should patch the operator deployment to use `delve` as the `CMD` of the
image instead of the Operator binary. `delve` will run the operator for us.

```
kubectl patch deployment/mongodb-enterprise-operator --type json \
    -p='[{"op": "replace", "path": "/spec/template/spec/containers/0/workingDir", "value":  "/tmp"},
         {"op": "replace", "path": "/spec/template/spec/containers/0/command", "value": ["/usr/local/bin/dlv", "--accept-multiclient", "--log", "--listen=0.0.0.0:40000", "--headless=true", "--api-version=2", "exec", "/usr/local/bin/mongodb-enterprise-operator"]},
         {"op": "remove", "path": "/spec/template/spec/containers/0/args"}]'
```

## Exposing the Operator debugging port locally

Use `kubectl port-forward` to be able to see this port from your workstation:

    kubectl port-forward deployment/mongodb-enterprise-operator :40000

This will open a random port locally that you can use to configure your local
environment to connect to a debugging session in the Operator Pod. You can
use a fixed port number with:

    kubectl port-forward deployment/mongodb-enterprise-operator 40000:40000

In this case, the localport will be `40000`.

With `kubectl port-forward` there will be a connection established between
localhost and the Kubernetes API. In contrast with a NodePort or Ingress, there
is no need to create a Service or to open ports in your firewall. `port-forward`
solution is preferred because it is easier to start and remove. As soon as
the `kubectl port-forward` command is killed, the connection disappears
completely and there will be no residual Kubernetes objects.

## Test

To try this solution, you can call delve locally like:

    $ dlv connect localhost:40000 # or any other port you are using
    Type 'help' for list of commands.
    (dlv)

## Disabling local Debug mode

You can build the operator again this time with no `--debug` mode with pipeline.

    ./pipeline.py --include operator

## Notes

* When calling `scripts/build/build_operator.sh` the binary is *always built with
debugging symbols enabled.* This is the default when running the operator locally.

* When building the operator using the multi-stage builds, the operator is
  *always built in release mode.* This is the default used in Evergreen.
