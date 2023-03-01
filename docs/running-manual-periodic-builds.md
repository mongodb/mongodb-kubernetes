# Running Manual Periodic Builds

Periodic builds use a specific Evergreen project file,
`.evergreen-periodic-builds.yaml`. In order to run it, you
must pass the `--path` parameter pointing at the periodic builds
Evergreen yaml file:

```
evergreen patch -p ops-manager-kubernetes -v periodic_build -t all -y -f -d "Running Periodic Builds" \
    --path .evergreen-periodic-builds.yaml -u --browse --param pin_tag_at=$(date +'%H:%M')
```

This will open a browser window with your Evergreen patch on it. It is important
to know that this process can be run many times; but if executed twice the same
day, runs will override the previous built images.

**Important**: always pass `--param pin_tag_at=HH:MM` with current hour and minute, to not override previously published images.
Digest pinning process is using images built in periodic built. If these images are overwritten (periodic run with the same `pin_tag_at`) it would break pinned certified bundle.  
