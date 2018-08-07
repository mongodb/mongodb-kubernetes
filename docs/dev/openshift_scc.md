# Volume Mount Permissions and Openshift Security Context Constraints 

## Abstract ##
This is some dump of thoughts and knowledge which were received during work on [CLOUDP-31604](CLOUDP-31604)

## Volume Mount Problem ##
Persistent volumes created in AWS (EBS) were mounted to containers in pod with read permissions for the `mongodb` user 
used to run container process. So `/data` directory was readonly for him. This didn't happen locally in Openshift or
Minikube though. The reason was that physical directory in the volumes was created by root user and of course couldn't be
used by any other user. There can be a couple of solutions:
    * add the `initContainers` section as described [here](https://github.com/kubernetes/kubernetes/issues/2630#issuecomment-375504696) 
which will run before main container is started and will add `g+w` permission for `/data` (as the `mongodb` user is in 
the same `root` group as `root` user so he will be able to write)
    * specify `FSGroup` attribute of pod `SecurityContext`. Kubernetes then will automatically change the group ownership of 
mounted directory to the new group with specified id before mounting. In practice it will also ensure that the container
user is in this group, so specifying just `FSGRoup: 100500` without modifying `mongodb` user groups will result in Kubernetes
adding `mongodb` user to group with id `100500` automatically.

Second option is much better as allows to avoid running any containers under `root` user and relies on proper Kubernetes
behavior. Setting `FSGroup: 0` to give `root` group write permissions worked for bare Kubernetes in cloud (AWS) but 
didn't work in local OpenShift as the creation of statefulset failed with error:

```
create Pod dublin-0 in StatefulSet dublin failed error: pods "dublin-0" is forbidden: 
unable to validate against any security context constraint: [fsGroup: Invalid value: []int64{0}: 0 is not an allowed group] 
```

## Openshift Security Context Constraints ##
Openshift provides some additional security mechanisms atop standard Kubernetes. One of them is **Security Context Constraints**
which are additional objects that control the actions that a pod can perform and what it has the ability to access. These
objects can be created like other objects in Kubernetes and they describe whether the user/system account can run privileged
containers, which user/group ids the user running container can have. There are default SCCs (it's not recommended to
change them)

```bash
$ oc get scc
NAME               PRIV      CAPS      SELINUX     RUNASUSER          FSGROUP     SUPGROUP    PRIORITY   READONLYROOTFS   VOLUMES
anyuid             false     []        MustRunAs   RunAsAny           RunAsAny    RunAsAny    10         false            [configMap downwardAPI emptyDir persistentVolumeClaim secret]
hostaccess         false     []        MustRunAs   MustRunAsRange     MustRunAs   RunAsAny    <none>     false            [configMap downwardAPI emptyDir hostPath persistentVolumeClaim secret]
hostmount-anyuid   false     []        MustRunAs   RunAsAny           RunAsAny    RunAsAny    <none>     false            [configMap downwardAPI emptyDir hostPath nfs persistentVolumeClaim secret]
hostnetwork        false     []        MustRunAs   MustRunAsRange     MustRunAs   MustRunAs   <none>     false            [configMap downwardAPI emptyDir persistentVolumeClaim secret]
nonroot            false     []        MustRunAs   MustRunAsNonRoot   RunAsAny    RunAsAny    <none>     false            [configMap downwardAPI emptyDir persistentVolumeClaim secret]
privileged         true      [*]       RunAsAny    RunAsAny           RunAsAny    RunAsAny    <none>     false            [*]
restricted         false     []        MustRunAs   MustRunAsRange     MustRunAs   RunAsAny    <none>     false            [configMap downwardAPI emptyDir persistentVolumeClaim secret]

``` 

So the most permissive SCC is `privileged` which allows any fs groups, user ids and allows to run containers in privileged
mode (should be assigned only to other admin users) and the most restrictive is `restricted` which allows to use some ranges
for user ids and group ids (`RUNASUSER=MustRunAsRange` and `FSGROUP=MustRunAs`). SCCs are mapped on users/service accounts
using command:

```bash
oadm policy add-scc-to-user anyuid system:serviceaccount:mongodb:default
```

By default all new pods are run under `restricted` SCC if the user or serviceAccount is not mapped to other SCC. So when 
statefulset is created by Operator its pods are run under serviceAccount `default` and are authorized against `restricted`
SCC. 

## SCC and Volume Mount permissions Problems ##

SCC results in some validations for `SecurityContext` of the pod during its creation. So if it has `Privileged: true` 
or `RunAsUser: 0` and SCC for serviceAccount/user doesn't allow this - pod won't be created. That's what happened with 
our pod when we specified `FSGroup: 0` - SCC has some FSGroup range that doesn't include `0`. So what works in standard
Kubernetes (AWS) doesn't work in OpenShift.

Let's check what uids and gids are allowed to be used for `restricted` SCC:

```bash
$ oc get scc restricted -o yaml
...
fsGroup:
  type: MustRunAs
groups:
- system:authenticated
...
runAsUser:
  type: MustRunAsRange
seLinuxContext:
  type: MustRunAs
...
``` 

So the `restricted` SCC doesn't have ranges specified but somehow validation still works. This is because if the SCC
doesn't have ranges specified the default for the project will be used:

```bash
$ oc get project mongodb -o yaml
apiVersion: project.openshift.io/v1
kind: Project
metadata:
  annotations:
    openshift.io/sa.scc.mcs: s0:c9,c4
    openshift.io/sa.scc.supplemental-groups: 1000080000/10000
    openshift.io/sa.scc.uid-range: 1000080000/10000
...
```

So we see that for the groups and users the id range is between `1000080000` and `1000090000`. So `FSGroup` used in 
`SecurityContext` must conform to this range.

## Solution ##

The best solution would be to change the project settings and specify some custom range of ids if Operator is
installed to OpenShift. Then this group id could be used for user running container. But seems this would 
require admins to do this manually for the project they use for creation of mongodb resources which is not good.

Other solution is to just use hardcoded id `1000080000` as id of the group for `mongodb` user. That's how it is implemented
in `mongodb-enterprise-database/Dockerfile` now where the `mongodb` group with id `1000080000` is created. It's not perfect
as uses hardcoded value but seems this id was specified in OpenShift since creation and is not going to be changed.

## Caveat of OpenShift Authentication Model ##

In `mongodb-enterprise-database/Dockerfile` we specify that the id of `mongodb` user is `1007`. But if we check the user
id in running container in OpenShift we'll see that it's different:

```bash
$ k exec -it dublin-0 bash
mongodb@dublin-0:/$ echo $(id)
uid=1000080000(mongodb) gid=0(root) groups=0(root),1000080000(mongodb)
```

The trick is that OpenShift overrides the user ids specified in DockerFile according to the default value specified for
SCC. So if in normal Kubernetes pod user id is `1007` in OpenShift it is `1000080000`
