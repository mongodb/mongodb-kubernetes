
### CSV Changes for next release

None

#### examples:

* Added a new environment variable `NEW_ENV` with a value of `abc` to the operator deployment.

```yaml
- name: NEW_ENV
  value: abc
```

* Added deployments to the permissions list. 
```yaml
  resources:
    - configmaps
    - secrets
    - services
    - deployments
  verbs:
    - get
    - list
    - create
    - update
    - delete
    - watch
```

