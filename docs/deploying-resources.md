# Deploying resources

## Standalone

```bash
# Deploy
oc apply -f ../samples/minimal/om-standalone.yaml

# Remove
oc delete -f ../samples/minimal/om-standalone.yaml
```

## Replica set

```bash
# Deploy
oc apply -f ../samples/minimal/om-replica-set.yaml

# Remove
oc del -f ../samples/minimal/om-replica-set.yaml
```

## Sharded cluster

```bash
# Deploy
oc apply -f ../samples/minimal/om-sharded-cluster.yaml

# Remove
oc del -f ../samples/minimal/om-sharded-cluster.yaml
```

# Other topics and useful commands

## Check the operator's logs

```bash
oc logs -f deployment/mongodb-enterprise-operator
```

## See running pods

```bash
oc get pods -n mongodb
```

## Get pod info for all mongodb-enterprise-operator pods

```bash
for pod in $(oc get pods -n mongodb | grep mongodb-enterprise-operator | awk '{ print $1 }'); do kubectl describe -n mongodb pod ${pod}; done
```
