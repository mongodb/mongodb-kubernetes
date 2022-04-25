# Summary

*Enter your issue summary here.*

## Documentation changes

* [ ] Add an entry to [release notes](.../RELEASE_NOTES.md).
* [ ] When needed, make sure you create a new [DOCSP ticket](https://jira.mongodb.org/projects/DOCSP) that documents your change.

## Changes to CRDs

* [ ] Add `@jamesbroadhead` (James) and `@jasonmimick` (Jason) as reviewers.
* [ ] Make sure any changes are reflected on `/public/samples` directory.

## If this PR introduces any change to the Kubernetes or Ops Manager APIs or core reconciliation logic.

* [ ] Make sure you add extensive E2E test specially to the creation and updates of the new resources.
* [ ] Your object definitions should adhere to [Kubernetes Object Names and IDs](https://kubernetes.io/docs/concepts/overview/working-with-objects/names/) conventions.

## If this PR touches existing functions whose docstrings need modifications
* [ ] Make sure to correct the docstring as per [The Go Blog](https://blog.golang.org/godoc).
* [ ] Ensure there are no stale comments.

## If this PR includes anything that will require modifying the CSVs.
E.g. changes to env vars, images or operator permissions.

* [ ] Make sure you have updated [csv changes](../docs/dev/release/csv-changes.md)

