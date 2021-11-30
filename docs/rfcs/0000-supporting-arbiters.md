- Feature Name: `community-supporting-arbiters`
- Start Date: 2021-11-24
- RFC PR: [2064](https://github.com/mongodb/mongodb-kubernetes-operator/pull/2064)
- RFC No: 1

# Summary
[summary]: #summary

Arbiters will be configured to a separate StatefulSet to allow for them being
scaled independently from the regular data bearing nodes of a Replica Set.

# Motivation
[motivation]: #motivation

There are multiple restrictions to how members can be added and removed from a
Replica Set; these restrictions arise from the agent and automation-config.

- Members can't be made `arbiterOnly` in one pass, they need to be removed and
  then readded back in order to modify this attribute.
- Members can't easily _start_ in the same Pod, because the `dbPath` is the same
  and `mongod` will refuse to start pointing at a `dbPath` from an already
  removed member.
- A member can't restart by itself. `mongod` is started when the container in
  the Pod starts. In order to _restart_ `mongod` with a different configuration,
  the operator will have to explicitly restart the Pod in question.

At the same time, we want the arbiters to be scaled _independently_ of the
members. If both members and arbiters shared the same ReplicaSet, scaling one of
them affects the StatefulSet in which all the members reside, forcing the
members, in the automation-config to be re-purposed; the constrains imposed have
been described already.


# Guide-level explanation
[guide-level-explanation]: #guide-level-explanation

When the count of arbiters is greater than 0, the Operator will start 2
StatefulSets, one holding the data-bearing nodes (or members) and one holding
the arbiters.

The goal of this new strategy is to *be able to scale members and arbiters
indepedently*, without having to sacrifice on simplicity in terms of automation
agent configuration and scaling constraints.

Example, lets start with this simple Replica Set:

```yaml
---
apiVersion: mongodb.com/v1
kind: MongoDB
metadata:
  name: my-rs
spec:
  members: 3
  version: 5.0.3-ent
  type: ReplicaSet
```

This resource will have all of its members in 1 StatefulSet. Now, if the
following change is applied:

```yaml
---
apiVersion: mongodb.com/v1
kind: MongoDB
metadata:
  name: my-rs
spec:
  members: 3
  arbiters: 1
  version: 5.0.3-ent
  type: ReplicaSet
```

The Operator will create a new StatfulSet with `replicas: 1` which _will hold
the arbiters_ configured. If `spec.arbiters` is changed back to 0, the
StatefulSet will be scaled to `replicas: 0` (not deleted).

The `status.MongoURI` will contain a _connection string_ that point only at the
members (not arbiters) of the Database.

Migrating from a previous version of the Operator will require manual
intervention, if the resource has `arbiters` > `0`, because the current model
won't let us _scale down_ or _scale up_ the amount of arbiters. Unfortunately
this was overlooked when the current design was put in place.

# Reference-level explanation
[reference-level-explanation]: #reference-level-explanation

Technically this solution is not difficult, but there are a few situations
around the edges that we must control in a more strict manner.

## Service

A new Service will be created for the arbiters, this service will be named
`mdb.Name + "-arb-svc"`, this is, the same name as the _regular_ Service, with
an `-arb` in it. The same naming rule will go for the arbiters StatefulSet.

There is always a posibility of naming collisions in Kubernetes, if there were 2
resources created, for instance, with names `mdbc/name` and `mdbc/name-arb`,
they will collide during creation time of the Service or StatefulSet of the
first one. Unfortunately, this error exists right now with any resource that
could exist previously to the MongoDB resources being created. We won't take
actions against its occurrence in this phase.

## Scaling of arbiters

The arbiters need to be scaled one by one, exactly the same as regular members;
the MongoDB automation constrains also apply in this case.

For this implementation we will scale both members and arbiters at _different
stages_, this is, the _members will always_ be scaled first, and then the
_arbiters_ will be scaled next. The scaling logic will wait for members to have
completed scaling before starting with arbiters.


## Automation Config IDs

We will use a non-contiguous space for IDs for members and arbiters. Members
will still receive IDs starting from `0`; Arbiters will receive IDs starting
from `100`. This way we will be able to scale both members and arbiters, without
this operation affecting or moving around existing IDs, because these IDs
_should not be repurposed_. This is a constraint that arises from the name of
the Pods in a StatefulSet, with stable identifiers, and constraints on the
automation config side.

* Example 1: A Replica Set with `members: 3` and `arbiters: 0`.

```json
{
  "replicaSets": [{
    [{"_id": 0, "host": "mdb-0"},
     {"_id": 1, "host": "mdb-1"},
     {"_id": 2, "host": "mdb-2"}
    ]
  }]
}
```

When this Replica Set's arbiters are scaled to 2, the resulting automation
config will be:

```json
{
  "replicaSets": [{
    [{"_id": 0,   "host": "mdb-0"},
     {"_id": 1,   "host": "mdb-1"},
     {"_id": 2,   "host": "mdb-2"},
     {"_id": 100, "host": "mdb-arb-0"},
     {"_id": 101, "host": "mdb-arb-1"}
    ]
  }]
}
```

As you can see, adding 2 arbiters didn't change the existing `"_id"`. Now the
solution becomes evident when _scaling_ the _existing members and arbiters_.
Lets' assume now that the members are scaled to 5, the resulting automation
config will be:

```json
{
  "replicaSets": [{
    [{"_id": 0,   "host": "mdb-0"},
     {"_id": 1,   "host": "mdb-1"},
     {"_id": 2,   "host": "mdb-2"},
     {"_id": 3,   "host": "mdb-3"},
     {"_id": 4,   "host": "mdb-4"},
     {"_id": 100, "host": "mdb-arb-0"},
     {"_id": 101, "host": "mdb-arb-1"}
    ]
  }]
}
```

In this case, the identifier of the `arbiters` was maintained, satisfying the
integrity requirements of the Automation agent.

Scaling up and down the `arbiters` will result in the same, `members` IDs will
be kept untouched.

The `automation_config_builder` package will be improved in order to take into
consideration the different types of Processes that will now exist:

- Arbiters will have a different `domainName` (different Service and name).
- Arbiters will have a different indexing strategy (start from 100).

## Labeling

The new resources will be labeled with `mongodb.com/role: arbiter`, and the
regular member resources will be labeled with `mongodb.com/role: member`. This
is not something we will be using now, but we think it is not a bad idea to
categorize the resources we create from the beginning.

For each member type (arbiter or member) there are Services, StatefulSets and
Pods to label.

## Reconcile Loop

There are not many changes to the reconcile loop, besided it creating an
additional Service (for arbiters) and StatefulSet, but this will be consolidated
in the functions that deal with the construction of StatefulSets.

There are no new `Pending` states to add to the reconciliation.

## Recommended Arbiter Settings

As per [MongoDB arbiter's
documentation](https://docs.mongodb.com/manual/tutorial/add-replica-set-arbiter/),
it is advised against having more than 1 Arbiter per Replica Set. We will add
this recommendation to the description/docs for the `Spec.Arbiters` attribute in
the `CRD`. The Operator will **not block, fail or produce a warning** if
`Spec.Arbiters` is greater than one.

# Drawbacks
[drawbacks]: #drawbacks

I don't see any compeling reason why this should not be done. It is going to be
clearer _which_ Pods are arbiters and members. Arbiters can be configured with
less resources in mind, thanks for them being in a different StatefulSet, and
their Node tolerations can be configured _independently_ of the data-bearing
nodes which will make it easier for them to be scheduled in the expected
locations, like in a different availability zone, as per Node labels.

With the current approach the _arbiters_ can't be _scaled up_ or _down_ and can
only be configured during the initialization phase. This is, the `spec.arbiters`
attribute is mutable, but it should be inmutable, because any changes to it will
make the resource go to Pending state.

# Rationale and alternatives
[rationale-and-alternatives]: #rationale-and-alternatives

Currently, the community operator _supports_ arbiters, but the amount of
arbiters can't be changed once the Replica Set has been deployed. This is
because of the current design that uses the same StatefulSet for both members
and arbiters, and the problems that have been explained above.

Unfortunately, the operator will not mark this attribute as inmutable and will
go ahead with any requested changes. Any change to the `spec.arbiters` attribute
will result in a resource in `Pending` state.

I approached this problem initially by trying to _change_ the _arbiterOnly_
attribute in the automation config. As described, this can't be done. I tried
multiple solutions, the most reasonable was: _If a member's attribute is
changed_, then start a new `mongod` on the same machine, with a different _ID_
and in a different port/location.

This resulted in many problems, specially related to having multiple `mongod`s
per machine (the previous one was not removed). Or having an `arbiter` on a Pod
meant for a data-bearing node (which requires much more resources).

This change is simple to do now, the use case for arbiters in Replica Set is
still manageable. The next stages will be to introduce arbiters in Sharded
Clusters, which will be more problematic. I see this solution, once it is well
tested in Replica Sets and Community Operator, to bring simplicity in a much
more complex scenario of Sharded clusters. The design seems simple enough and
easy to replica into more complex scenarios.

# Prior art
[prior-art]: #prior-art

I've tried 2 operators, besides ours ([^1] and [^2]) and both use StatefulSets
in order to deploy their resources, which seems to be the norm right now,

- [1] https://www.elastic.co/guide/en/cloud-on-k8s/current/k8s-deploy-eck.html
- [2] https://github.com/cockroachdb/cockroach-operator

In _Elastic_ the following Custom Resource will create 2 StatefulSets, with 1
and 2 members respectively:

```yaml
apiVersion: elasticsearch.k8s.elastic.co/v1
kind: Elasticsearch
metadata:
  name: my-elastic
spec:
  nodeSets:
  - name: default
    count: 1
  - name: othernode
    count: 2
```

This is similar to what we do with Sharded Clusters. However I think the
problems we are trying to solve are similar as well: "A non-homogeneous
collection of hosts deployed in Kubernetes". In this case, it can be 2 `nodes`
in Elastic or multiple `shards` and `config-servers` in a MongoDB Sharded
Cluster or `members` and `arbiters` in a Replica Set.

# Unresolved questions
[unresolved-questions]: #unresolved-questions

- None that I can think of.

# Future possibilities
[future-possibilities]: #future-possibilities

Specify a different `statefulset` template for the arbiters; and provide
reasonable defaults for it, for instance:

- No need to mount `PersistentVolumes`/`PVC`.
- Allocate less resources per container.
- Establish different tolerations for the Pods in this Statefulset, in order
  for better distribution of data-bearing/arbiters across the Kubernetes
  cluster regions and availability zones.

Ideally I would like to reach a level of abstraction of our Replica Set
deployment so it is easier to build Sharded Clusters, which are basically a
collection of Replica Sets, and each one of these Replica Sets _knows_ how to
handle its own state.

As we approach a decision about how to make both our Operators _work_ together,
we need to have a Goal of composability. Many of the primitives of MongoDB can
be composed in order to build elements of higher complexity; in the example we
just gave "A Sharded Cluster is a collection of Replica Sets working together".
This is a _simplification_ of what is required in order to have a Sharded
Cluster actually _working_ on Kubernetes, but this is the level of abstraction
we need to achieve in order for the medium-term goals to be technically
approachable.

We have also dicussed in the past about having members of the Replica Set with a
different set of properties. This approach might be useful in the future to have
a non-homogeneous Replica Set, in terms of their resources, internal
configuration, tagging for read-preferences, etc.
