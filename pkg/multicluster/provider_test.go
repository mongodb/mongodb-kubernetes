package multicluster

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestProvider(t *testing.T) {
	p := NewProvider()

	assert.Empty(t, p.Entries())

	p.Set("a", Entry{ResourceName: "cr-a"})
	p.Set("b", Entry{ResourceName: "cr-b"})
	assert.Equal(t, map[string]Entry{"a": {ResourceName: "cr-a"}, "b": {ResourceName: "cr-b"}}, p.Entries())

	// Entries returns a copy: mutating it must not affect the registry.
	entries := p.Entries()
	delete(entries, "a")
	assert.Len(t, p.Entries(), 2)

	p.Set("a", Entry{ResourceName: "cr-a2"})
	assert.Equal(t, "cr-a2", p.Entries()["a"].ResourceName)
}
