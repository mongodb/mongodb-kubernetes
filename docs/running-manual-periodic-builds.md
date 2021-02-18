# Running Manual Periodic Builds

Periodic builds use a specific Evergreen project file,
`.evergreen-periodic-builds.yaml`. Evergreen knows how to run a periodic build
defined in a non-standard Evergreen file, but unfortunatelly, it does not know
how to run a `patch` using a custom file. This could be resolved in
[EVG-13935|https://jira.mongodb.org/browse/EVG-13935], but in the meantime, we
can force Evergreen to use our file.

```
cp .evergreen-periodic-builds.yaml .evergreen.yml
evergreen patch -p ops-manager-kubernetes -v periodic_build -t all -y -f -d "Running Periodic Builds" -u --browse
cp .evergreen.yml .evergreen-periodic-builds.yaml

# Make sure .evergreen.yml file goes back to normality.
git checkout -- .evergreen.yml
```

This will open a browser window with your Evergreen patch on it. It is important
to know that this process can be run many times; but if executed twice the same
day, runs will override the previous built images.
