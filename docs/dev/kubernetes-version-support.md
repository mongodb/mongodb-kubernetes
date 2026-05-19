# Kubernetes and OpenShift version support

**`kubernetes-versions.json`** at the repo root is the engineering source of truth for declared support:

- **`kubernetes.min`** / **`kubernetes.max`**: version range we support the current version as it was tested with such version range.
- **`openshift`**: OpenShift **minor** is the version we expect for our shared test cluster to be at. This is **not** the same as **`release.json`**’s OLM minimum, as explained later.

```json
{
  "kubernetes": { "min": "1.33.7", "max": "1.35.0" },
  "openshift": "4.20"
}
```

## Kubernetes

**Today:** `./scripts/check-kube-versions.sh` reads the file and compares the Kubernetes range to [endoflife.date](https://endoflife.date/) APIs, with a configurable release-date buffer, to flag when to **bump**.

**Direction:** Pins (Kind node image, `kubectl` in scripts/Dockerfiles, CI cluster selectors) should **eventually** be driven from this file or generated from it.

**Run:** `./scripts/check-kube-versions.sh` from repo root. 

Optional env:
- **`CONFIG_FILE`**
- **`OPERATOR_RELEASE_DATE`** (`YYYY-MM-DD`, default today).

## OpenShift

We do not enforce the cluster version from the repo. **`openshift`** records what we **intend** to run against; automation should **fail** if the live cluster does not match so someone either **updates the cluster** or **the file**.

## `release.json` vs this file

**`openshift.minimumSupportedVersion`** in **`release.json`** feeds OLM bundle metadata (e.g. `com.redhat.openshift.versions`)—the **customer certification** floor. **`kubernetes-versions.json` → `openshift`** is the **tracked** line for our test infrastructure. They are independent.

