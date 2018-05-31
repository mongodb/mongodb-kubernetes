# Projects And Credentials #

In Ops/Cloud Manager there's the concept of credentials: a pair of
username + public-api-key that is used to act, on behalf of the user,
to make changes to a Deployment. With the MongoDb operator we use this
to authenticate users to the API and have a fine grained control of
who is capable of doing what.

Any user that wants to make sure needs permissions to create/update
objects in given `Project`. The Ops Manager admin is responsible of
giving this access and also to create the `Project`s where the user
create/update their objects.

The idea behind this is to abstract away the details of the system
operating on your deployments, instead focusing on "who am I" and
"where do I want my deployments to reside".


## Projects ##

A `Project` object is a Kubernetes `ConfigMap` that points to a given
Ops/Cloud Manager installation and to a `Project`. This `ConfigMap`
will have the following structure:


``` yaml
---
apiVersion: v1
kind: ConfigMap
metadata:
  name: project-01
  namespace: mongodb
data:
  projectId: my-project-id
  baseUrl: https://my-ops-cloud-manager-url

```

## Credentials ##

For a user to be able to create or update objects in this Ops/Cloud
Manager Project she needs special credentials (usually supplied by an
Ops/Cloud Manager administrator). The secrets can be created easily
with the following command:

``` bash
kubectl -n mongodb create secret generic alice-credentials --from-literal=user=alice@example.com --from-literal=publicApiKey=my-public-api-key
```

In this example, a `Secret` object with the name `alice-credentials`
was created. The contents of this `Secret` object is the `user` and
`publicApiKey` attribute. You can see this secret with a command like:

``` bash
Name:         alice-credentials
Namespace:    mongodb
Labels:       <none>
Annotations:  <none>

Type:  Opaque

Data
====
publicApiKey:  41 bytes
user:          14 bytes
```

We can't see the contents of the `Secret`, because it is a secret!
This is good, it will allow us to maintain a separation between our
users.

## MongoDb Objects ##

After creating our own `credentials` object we can use it like in the
following example:

``` yaml
---
apiVersion: mongodb.com/v1beta1
kind: MongoDbReplicaSet
metadata:
  name: my-replica-set
  namespace: mongodb
spec:
  members: 3
  version: 3.6.5

  project: project-01
  credentials: alice-credentials

  persistent: false # Please note: this indicates there will be no Persistent
                    # Volumes attached to this deployment.
```

When applying the changes in this yaml file, Kubernetes will create a
new `MongoDbReplicaSet` in the `project` defined, using the
`credentials` provided in the configuration.
