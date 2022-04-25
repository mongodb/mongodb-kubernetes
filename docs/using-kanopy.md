Using Kanopy
============

We have a namespace on the Kanopy staging cluster called `cloud-mdb-operator`.
Anyone in the `10gen-private-cloud-team` LDAP group should have access. You can
check this on [MANA](https://mana.corp.mongodb.com)—if you're not in that LDAP
group and you want to use this namespace go and bother someone (probably James
Broadhead).

In order to configure access to the namespace, you should follow the
instructions in the Kanopy docs:
https://github.com/10gen/kanopy-docs/blob/master/docs/kubeconfig.md#generating-a-configuration-file.

**Important:** you probably don't want to override the `kubectl` config file
completely. Instead you should manually merge the generated file with your
existing configuration. In addition, you may not want to set your global
`kubectl` context to use the Kanopy namespace by default. Instead you can use
the context and namespace flags like so:

```
kubectl --context api.staging.corp.mongodb.com --namespace cloud-mdb-operator get pods
```

In order to make this easier to use you may want to set up a command alias like so:
```
alias ky='kubectl --context api.staging.corp.mongodb.com --namespace cloud-mdb-operator'

```

Logs
----
Logs for the running operator can be viewed here:
https://mongodb.splunkcloud.com/en-US/app/search/search?q=search%20index%3D%22mongodb-operator-staging%22&sid=1560188577.3328211&display.page.search.mode=smart&dispatch.sample_ratio=1&earliest=-30m%40m&latest=now
