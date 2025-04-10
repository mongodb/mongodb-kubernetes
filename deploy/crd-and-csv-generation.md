# Generating CRDs with the operator-sdk tool

# prequisites
* The latest version of the [operator-sdk](https://github.com/operator-framework/operator-sdk/releases)

We can generate crds with the following command

```bash
operator-sdk generate crds
```

This defaults to creating the crds in `deploy/crds/`


# Generating the CSV

Generating the CSV file can be done with the following command

```bash
operator-sdk generate csv --operator-name mongodb-enterprise --csv-version 1.5.4 --apis-dir api --from-version 1.5.3
```

The default directory the CR examples comes from is `deploy/crds`, but it can be specified
with `--crd-dir <dir>`. Note: the CRDs need to exist in this directory for the examples to be shown.

In order for the deployments, roles and service accounts to be shown, they need to appear
as as yaml files in the `deploy` directory.


# Annotation Tips

* For embedded structs, using `json:",inline"`. This will inherit all of the json tags on the embedded struct.
* Exclude fields from the CRD with `json:"-"`
* Any comments above a field or type will appear as the description of that field or object in the CRD.
* Add printer columns
```golang
// +kubebuilder:printcolumn:name="Phase",type="string",JSONPath=".status.phase",description="The current state of the MongoDB User."
```
* Add a subresource
```golang
// +kubebuilder:subresource:status
```
* Make a field `// +optional` or `// +required`

# Leaving dev comments

Dev comments can be left by having a space between the description and the comment.
```golang

// Description of MongodbSpec
type MongoDbSpec struct {

	// DEV COMMENT, won't show up in CRD

	// This is the description of Service
	Service string `json:"service,omitempty"`

```
