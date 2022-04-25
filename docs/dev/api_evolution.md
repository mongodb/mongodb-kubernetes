# Ops Manager Kubernetes API Evolution #

## Abstract ##

This is a document describing approaches to API and schema evolution of Ops Manager Kubernetes Operator.
There seems to be two ways of handling changes in the system:
* through explicit API versioning
* without API versioning (internal support for types evolution and backward compatibility)

## TL; DR ##

*(This is a short conclusion based on the investigations below)*
 
It seems that API versioning is very hard to support and gives more troubles than benefits. We may consider API versioning 
for some major and full refactoring of schema when it becomes too hard to provide backward compatibility internally but for
now the easiest and most convenient (for both developers and users) will be gradual internal schema changing. 

*Based on this we decided to change `v1beta1` version for schema to `v1` as we don't want to change the version as soon as 
we release `v1` version of Operator. Operator versioning will not be affected with this though *

**Some caveats to consider:**
* If adding the new field it can be made mandatory through CRD validation - this will be applied only to user submitted 
configurations. But the Operator logic should make sure the old configs still work (as operator may "recreate" existing
objects on restart, check CLOUDP-30595). This may mean adding some default value for the attribute or just ignoring it
* Field renaming/deletion is possible (deserialization code will just ignore unknown attribute) so the only important moment
is to support backward compatibility in Operator code (may be some "migration" or "default" logic should be provided)
* Changing the type of field is not possible as deserialization for existing resources will fail. Seems adding a new field and
deprecating the old one should be the strategy to do this

## API versioning (creating different CustomResourceDefinition's) ##

Currently we create `CustomResourceDefinition`s (CRDs) for each of mongo db resources programmatically on the first 
start of Ops Manager Operator (Operator). CRD doesn't contain a schema for `CustomResource` (CR) - this schema is
 supported internally in the code of Operator. The things that are specified in CRD are: names (plural, single) or CR,
  its group, version and validation rules. 

After playing with CRDs the following became clear:
* only `singular, shortNames` fields are mutable - all others are immutable
* this means that the version cannot be changed for existing CRD
* validations can be changed any time though

But this doesn't mean that API versioning is not possible - there's a workaround in case it's really necessary to support
different versions. The gist is to create another CRD (which is formed as `plural name` + `.group`) and create another
 group for it. So the `kind` will stay the same (for example `MongodbStandalone`) but the group will be different.
 
The steps to support this (this is reproduced in the branch 
[CLOUDP-30452-api-versioning-dont-remove](https://github.com/10gen/ops-manager-kubernetes/tree/CLOUDP-30452-api-versioning-dont-remove)):

 1. copy package `pkg/apis/mongodb.com` to `pkg/apis/mongodb.com.v1`. Edit the content - change versions everywhere 
 from `v1beta1` to `v1`, group from `mongodb.com` to `mongodb.com.v1`", edit the `types.go` (change the schema as 
 needed, for example add `Auth        string json:"auth"` field everywhere)
 2. change `codegen.sh` - add new version:
 ```
 ./generate-groups.sh all github.com/10gen/ops-manager-kubernetes/pkg/client github.com/10gen/ops-manager-kubernetes/pkg/apis "mongodb.com:v1beta1, mongodb.com.v1:v1"
 ```
 3. run `codegen.sh` to generate all necessary resources
 4. Now we need to support new types in operator code. Of course the code would be refactored to enable maximum code 
 reuse but currently the developer will need to perform following actions:
     * copy-paste the whole `operator` package into `operatorv1`.
     * change all import statements in all classes to work with resources from `mongodb.v1/v1` package 
     * update `main.go` - add registration block for new CRDs, instantiate a controller from new package `mongodb.v1/v1`
      (so it will track changes to new resources)
     * change the code in `operatorv1/standalonekube.go` and other files to work with new type structure - add new 
     logic that should be run for new types
 5. New custom resources created will have a different `apiVersion` and new field and after creating it the new code will
 handle changes:
 
 ```
 apiVersion: mongodb.com.v1/v1
 kind: MongoDB
 metadata:
   name: liffey
   namespace: mongodb
 spec:
   type: ReplicaSet
   members: 2
   version: 3.6.4
   project: my-om-config
   credentials: alis-credentials
   persistent: false
   auth: strict
 ```
 
### Pros and Conses of API versioning ###

**Pros:**
* For some huge systems that cannot provide backward compatibility for changes supporting different versions is a key to 
convenient evolution

**Cons:**
* Supporting different versions leads to huge duplication and increased complexity of code especially when the number of
supported versions is bigger than 2. For Operator there's no way to completely avoid duplication as there are static
imports of new versions and the code working with them will be different
* For clients in fact API versioning also adds complexity as at some stage they will have to perform manual migrations
of already existing objects to new version and this cannot be done seamlessly (the only way is to create copies of old
objects with new versions and then remove old ones, which in general can be risky in production). As a client I'd rather
prefer to install new version of Operator and not drop-create anything in my system.
* In fact when using kubectl now the statement `kubectl get mongodbstandalones` will show only one of the types of CRD (the 
last CRD created?), which is quite misleading (I'd expect something like all objects but with different groups and versions).
As soon as you remove the last CRD created - the statement starts working as expected

## Internal Evolution

### Adding new (mandatory) attribute ###
Support for mandatory is done through CRD validation. So:
1. create the resource
1. change the schema - add new field to `MongoDbSpec` struct: `Auth string ``json:"auth"`` ` 
to `types.go`. Call `codegen.sh`
1. change validation rules, specify that the field is mandatory: 
```yaml
apiVersion: apiextensions.k8s.io/v1beta1
kind: CustomResourceDefinition
metadata:
  name: mongodbstandalones.mongodb.com
spec:
  group: mongodb.com
  names:
    kind: MongoDB
    listKind: MongoDBList
    plural: mongodb
    singular: mongodb
  scope: Namespaced
  version: v1beta1
  validation:
    openAPIV3Schema:
      oneOf: # remaining values omitted for brevity
      - properties:
         spec:
          required:
          - auth
``` 
1. apply the yaml configuration for standalone (change the version) and check that kubectl returned the error `.spec.auth in body is required`
1. change config - add `auth: foo` there and apply. Changes got applied and the object got updated:
```
2018-06-11T15:15:18.627Z	INFO	operator/standalonekube.go:33	Updating MongoDbStandalone	
{"standalone": "dublin", "oldConfig": {"version":"3.6.3","persistent":false,"project":"my-om-config","credentials":"alis-credentials","auth":""}, "newConfig": {"version":"3.6.3","persistent":false,"project":"my-om-config","credentials":"alis-credentials","auth":"foo"}}
```
**Conclusion** Validation works only for user requests - the existing data in etcd is not validated during update (so we
can easily add new fields and make them mandatory - this will only affect the configs applied by users) 
Interesting that skipping `omitempty` for the json field results in default value to be passed (so `auth` becomes "").

**Caveat** As of CLOUDP-30595 after restart operator receives "create" events for all deployed resources and tries to 
"create" them. So even after adding a mandatory field we need to provide code that will backward compatible and not fail
on old schemas. 

### Adding new (optional) attribute ###

For this scenario we will create a resource and then add the field `Auth string ``json:"auth,omitempty"`.
`omitempty` will result in that the attribute won't be deserialized at all. If no validation rules are specified - then
the Operator must provide the backward compatible logic as users may send configs without the new field

### Renaming attribute ###

1. Create a standalone
1. Change the schema - rename the json field `persistent` to `persistent1` in `types.go`. Call `codegen.sh`
1. Restart operator.
1. As of CLOUDP-30595 the operator will "create" the standalone again on restart and the schema will lack both new and old field now:
```
2018-06-11T20:12:24.608Z	INFO	operator/standalonekube.go:18	>> Creating MongoDbStandalone	
{"standalone": "dublin", "config": {"version":"3.6.4","podSpec":{},"project":"my-om-config","credentials":"alis-credentials"}}
```

And later the log has an error (because the statefulset cannot be updated to get the persistent volume claim if it 
didn't have it before)
```
2018-06-11T20:12:24.641Z	ERROR	operator/standalonekube.go:21	Failed to create statefulset: StatefulSet.apps "dublin" 
is invalid: spec: Forbidden: updates to statefulset spec for fields other than 'replicas', 'template', and 'updateStrategy' are forbidden.	{"standalone": "dublin"}
```

**Conclusion** Kubernetes serialization mechanisms are pretty flexible and when the request coming (both from user or
from etcd) has some attribute that is not recognized - then it is just ignored. So attribute rename is totally
possible physically (but of course requires attention from Operator to support backward compatibility)
   
### Changing the attribute type ###
1. Create a standalone with `auth string` field set to `foo`
1. Change the schema - change type to `auth int`. Call `codegen.sh`
1. Restart operator
1. As of CLOUDP-30595 the operator will "create" the standalone again on restart and will show the repeating error:
```
ERROR: logging before flag.Parse: E0612 08:59:38.199880       1 reflector.go:205] github.com/10gen/ops-manager-kubernetes/operator/crd/resource.go:67: 
Failed to list *v1beta1.MongoDbStandalone: v1beta1.MongoDbStandaloneList.Items: []v1beta1.MongoDbStandalone: 
v1beta1.MongoDbStandalone.Spec: v1beta1.MongoDbStandaloneSpec.Auth: readUint64: unexpected character: 
�, error found in #10 byte of ...|:{"auth":"ppppppp","|..., bigger context ...|6ea-6db0-11e8-a24b-12556e57ad48"},"spec":{"auth":"ppppppp","credentials":"alis-credentials","persist|.
```

**Conclusion** As expected changing a type of a field leads to errors deserializing existing data and must be avoided
in practice (usually not needed though)
 
### Removal of the attribute ###

Removal of an attribute from schema falls in the same category as "Renaming attribute" above - Go will ignore the attributes
it doesn't know. So if we decide to remove any of the fields and make sure that existing resources won't break because
of this - (de)serialization mechanisms in Operator won't make any problems.



