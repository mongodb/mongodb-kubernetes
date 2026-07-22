// Package helmchart embeds the MongoDB Kubernetes Operator Helm chart so it can
// be linked into Go binaries (notably the `kubectl mongodb` plugin) and rendered
// with the Helm SDK without shipping the chart separately or copying it at build
// time. Embedding the live on-disk chart means the plugin and the chart can never
// drift: they are the same source of truth.
//
// The package intentionally has no dependencies beyond the standard library — it
// exposes only the embedded filesystem. Callers that need a *chart.Chart build it
// from ChartFiles using the Helm SDK loader.
package helmchart

import "embed"

// ChartFiles holds the embedded Helm chart, rooted at the chart directory
// (Chart.yaml, values.yaml, templates/, crds/).
// The `all:` prefix on the directories is required so that files beginning with '_'
// (e.g. templates/_helpers.tpl) are included — a bare pattern would silently drop them.
//
//go:embed Chart.yaml values.yaml all:templates all:crds
var ChartFiles embed.FS
