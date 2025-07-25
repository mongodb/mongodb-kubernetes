## Quick Start

### Build mdbdebug

```bash
$ tools/mdbdebug/build.sh
```

### Run mdbdebug
Run mdbdebug controller to watch deployments in the current cluster:
```bash
bin/mdbdebug --context kind-kind --watch --deployPods
```

Do not use `--deployPods` if you have large deployment in your cluster. Debugging pods are inefficient and eat ~0.5cpu each idle.

### Attach to debugging pods

```bash
$ /bin/attach.sh
```





