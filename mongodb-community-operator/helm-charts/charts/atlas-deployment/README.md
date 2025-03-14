# MongoDB Atlas Cluster Helm Chart

The MongoDB Atlas Operator provides a native integration between the Kubernetes
orchestration platform and MongoDB Atlas — the only multi-cloud document
database service that gives you the versatility you need to build sophisticated
and resilient applications that can adapt to changing customer demands and
market trends.

The Atlas Cluster Helm Chart knows how to manager Atlas resources bound to
Custom Resources in your Kubernetes Cluster. These resources are:

- Atlas Projects: An Atlas Project is a place to create your MongoDB deployments,
  think of it as a _Folder_ for your deployments.
- Atlas Deployments: A MongoDB Database hosted in Atlas. An Atlas Cluster lives
  inside an Atlas Project.
- Atlas Database User: An Atlas Database User is a User you can authenticate as
  and login into an Atlas Cluster.

By default the `atlas-deployment` Helm Chart will create a user to connect to the
newly deployed Atlas Cluster, avoiding having to do this from the Atlas UI.

## Prerequisites

In order to use this chart, the [Atlas Operator Helm Chart](../atlas-operator)
needs to be installed already.

## Usage

1. Register or login to [Atlas](https://cloud.mongodb.com).

2. Create API Keys for your organization. You can find more information in
   [here](https://docs.atlas.mongodb.com/configure-api-access). Make sure you
   write down your:

   - Public API Key: `publicApiKey`,
   - Private API Key: `privateApiKey` and
   - Organization ID: `orgId`.

3. Deploy MongoDB Atlas Cluster

In the following example you have to set the correct `<orgId>`, `publicKey` and `privateKey`.

```shell
helm install atlas-deployment mongodb/atlas-deployment\
    --namespace=my-deployment \
    --create-namespace  \
    --set project.atlasProjectName='My Project' \
    --set atlas.secret.orgId='<orgid>' \
    --set atlas.secret.publicApiKey='<publicKey>' \
    --set atlas.secret.privateApiKey='<privateApiKey>'
```
Note, by default a random password will be generated. You can optionally also pass in a random username, however since this value is shared across templates this must be passed in, for example:

```shell
helm template --set "users[0].username=$(mktemp | cut -f2 -d.)" my-deployment mongodb/atlas-deployment 
```

## Connecting to MongoDB Atlas Cluster

The current state of your new Atlas deployment can be found in the
`status.conditions` array from the `AtlasCluster` resource:

```shell
kubectl get atlasdatabaseusers atlas-deployment-admin-user -o=jsonpath='{.status.conditions[?(@.type=="Ready")].status}'
```

Default HELM Chart values will create single Atlas Admin user with name
`atlas-deployment-admin-user`. Check the status of `AtlasDatabaseUser` resource for
Ready state.

You can test that the configuration is correct with the following command:

```shell
mongo $(kubectl -n my-deployment get secrets/my-project-atlas-deployment-admin-user -o jsonpath='{.data.connectionString\.standardSrv}' | base64 -d)
```

And Mongo Shell (`mongo`) should be able to connect and output something like:

```shell
MongoDB shell version v4.4.3
connecting to: mongodb://connection-string
Implicit session: session { "id" : UUID("xxx") }
MongoDB server version: 5.0.1
MongoDB Enterprise atlas-test-shard-0:PRIMARY> _
```

You have successfully connected to your Atlas instance!

## Example: Mounting Connection String to a Pod

You could use this secret to mount to an application, for example, the
_Connection String_ could be added as an environmental variable, that can be
easily consumed by your application.

```
containers:
 - name: test-app
   env:
     - name: "CONNECTION_STRING"
       valueFrom:
         secretKeyRef:
           name: my-project-atlas-deployment-admin-user
           key: connectionString.standardSrv
```

## Upgrade Notes

Atlas-operator version 0.6.1+ has to delete finalizers - this change requires additional steps.

Manual workaround for the update from Atlas-deployment-0.1.7:
1. Need to remove manually the "helm.sh/hook" from Atlasproject

```bash
kubectl annotate atlasproject helm.sh/hook- --selector app.kubernetes.io/instance=<release-name>
```

2. Need to add helm ownership annotation "meta.helm.sh/release-name" and "meta.helm.sh/release-namespace"

```bash
kubectl annotate atlasproject meta.helm.sh/release-name=<release-name> --selector app.kubernetes.io/instance=<release-name>
kubectl annotate atlasproject meta.helm.sh/release-namespace=<namespace> --selector app.kubernetes.io/instance=<release-name>
```

3. Run update

```bash
helm upgrade <release-name> mongodb/atlas-deployment <set variables>
```
