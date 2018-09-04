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
apiVersion: v1
kind: ConfigMap
metadata:
  name: my-project
  namespace: mongodb
data:
  projectName: testProject
  orgId: 5b890e0feacf0b76ff3e7183 # this is an optional parameter
  baseUrl: https://my-ops-cloud-manager-url
  
```

Note, that we don't support the single `projectId` parameter any more and request two configuration parameters `projectName`
and `orgId` instead. The former is the name of the group to be created/used and latter is the id of organization in which
the project is to be created. If `orgId` is skipped then Ops Manager will create a new organization with name `projectName` 
and will create a `project` in it. Note that to be able to create new project in the organization the user must have 
`ORG_GROUP_CREATOR` role.

### Projects and Tags ###

All the groups in Ops Manager may have tags. They are usually quite 
internal (so group owners will not see them in the Ops Manager UI). When Operator creates the group it marks it with special
tag `EXTERNALLY_MANAGED_BY_KUBERNETES` that allows to perform some additional validation logic for group deployments in 
Ops Manager (for example to forbid Ops Manager users to change some parameters for existing mongodb deployments that were
created by Kubernetes Operator to avoid configuration diverge). 

Current Operator logic tries to fix the group tags if they are missing in current group (if for example the group
wasn't created by Kubernetes Operator but from Ops Manager directly) but this behavior may change in future and Operator
may throw the error instead. So it's recommended to always let Operator create the group instead of reusing the existing one
that wasn't created by it.

## Credentials ##

For a user to be able to create or update objects in this Ops/Cloud
Manager Project she needs special credentials (usually supplied by an
Ops/Cloud Manager administrator). The secrets can be created easily
with the following command:

``` bash
kubectl -n mongodb create secret generic my-credentials --from-literal=user=alice@example.com --from-literal=publicApiKey=my-public-api-key
```

In this example, a `Secret` object with the name `my-credentials`
was created. The contents of this `Secret` object is the `user` and
`publicApiKey` attribute. You can see this secret with a command like:

``` bash
Name:         my-credentials
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
apiVersion: mongodb.com/v1
kind: MongoDbReplicaSet
metadata:
  name: my-replica-set
  namespace: mongodb
spec:
  members: 3
  version: 3.6.5

  project: my-project
  credentials: my-credentials

  persistent: false # Please note: this indicates there will be no Persistent
                    # Volumes attached to this deployment.
```

When applying the changes in this yaml file, Kubernetes will create a
new `MongoDbReplicaSet` in the `project` defined, using the
`credentials` provided in the configuration.
