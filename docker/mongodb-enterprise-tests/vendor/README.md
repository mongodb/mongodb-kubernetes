# Vendored Packages

In this directory you'll find vendored dependencies of the E2E tests.

## OpenLDAP

Originally this [Helm chart](https://github.com/helm/charts/tree/master/stable/openldap).

- This specifically vendors the 1.2.4 version at [this
  commit](https://github.com/helm/charts/tree/b2f720d33515d2308c558d927722e197163efc3e/stable/openldap).

### Changes to the original chart

- `image.repository` in `values.yaml` has been updated to point to our own vendored
  version in ECR.
- `spec.template.spec.serviceAccount` has been changed to point to
  `operator-tests-service-account`, for the Pod to be able to pull the new image
  from ECR.
