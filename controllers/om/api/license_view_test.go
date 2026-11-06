package api

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestLicenseView_HoldsArtifact(t *testing.T) {
	view := LicenseView{Licenses: []LicenseArtifactView{
		{ArtifactID: "held", Status: "ACTIVE"},
		{ArtifactID: "unstated"},
		{ArtifactID: "released", Status: LicenseArtifactReleased},
	}}

	assert.True(t, view.HoldsArtifact("held"))
	assert.True(t, view.HoldsArtifact("unstated"))
	assert.False(t, view.HoldsArtifact("released"))
	assert.False(t, view.HoldsArtifact("missing"))
}

func TestLicenseView_ArtifactLists(t *testing.T) {
	view := LicenseView{Licenses: []LicenseArtifactView{
		{ArtifactID: "held", Status: "ACTIVE"},
		{ArtifactID: "released", Status: LicenseArtifactReleased},
	}}

	artifact, listed := view.Artifact("released")
	assert.True(t, listed)
	assert.Equal(t, LicenseArtifactReleased, artifact.Status)
	_, listed = view.Artifact("missing")
	assert.False(t, listed)

	held, ok := view.HeldArtifact()
	assert.True(t, ok)
	assert.Equal(t, "held", held.ArtifactID)

	empty, ok := (&LicenseView{}).HeldArtifact()
	assert.False(t, ok)
	assert.Empty(t, empty.ArtifactID)
	nilHeld, ok := (*LicenseView)(nil).HeldArtifact()
	assert.False(t, ok)
	assert.Empty(t, nilHeld.ArtifactID)
	assert.False(t, (*LicenseView)(nil).HoldsArtifact("held"))
}

func TestLicenseView_HeldArtifactIsTheDeniedRowWhenItIsTheOnlyOneNotReleased(t *testing.T) {
	view := LicenseView{Licenses: []LicenseArtifactView{
		{ArtifactID: "old-1", Status: LicenseArtifactReleased},
		{ArtifactID: "denied", Status: "DENIED"},
		{ArtifactID: "old-2", Status: LicenseArtifactReleased},
	}}

	held, ok := view.HeldArtifact()
	assert.True(t, ok)
	assert.Equal(t, "denied", held.ArtifactID)
}
