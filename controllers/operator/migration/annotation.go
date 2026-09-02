package migration

// AnnotationDryRun is the annotation key that triggers migration dry-run mode.
// When set to "true" on a MongoDB CR, the operator launches a connectivity validation
// Job instead of performing the normal reconciliation that would write to Ops Manager
// or modify StatefulSets.
//
// Example:
//
//	metadata:
//	  annotations:
//	    mongodb.com/migration-dry-run: "true"
const AnnotationDryRun = "mongodb.com/migration-dry-run"
