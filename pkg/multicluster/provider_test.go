package multicluster

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestProvider(t *testing.T) {
	ctx := context.Background()
	p := NewProvider()

	assert.Empty(t, p.Entries())

	p.Set(ctx, "a", Entry{ResourceName: "cr-a"})
	p.Set(ctx, "b", Entry{ResourceName: "cr-b"})
	assert.Equal(t, map[string]Entry{"a": {ResourceName: "cr-a"}, "b": {ResourceName: "cr-b"}}, p.Entries())

	// Entries returns a copy: mutating it must not affect the registry.
	entries := p.Entries()
	delete(entries, "a")
	assert.Len(t, p.Entries(), 2)

	p.Set(ctx, "a", Entry{ResourceName: "cr-a2"})
	assert.Equal(t, "cr-a2", p.Entries()["a"].ResourceName)

	p.Delete(ctx, "a")
	assert.Equal(t, map[string]Entry{"b": {ResourceName: "cr-b"}}, p.Entries())

	// Deleting an unknown cluster is a no-op.
	p.Delete(ctx, "a")
	assert.Len(t, p.Entries(), 1)
}

func TestProviderHooks(t *testing.T) {
	ctx := context.Background()
	p := NewProvider()

	var added, removed []string
	hooks := Hooks{
		OnAdd:    func(_ context.Context, clusterName string, _ Entry) { added = append(added, clusterName) },
		OnRemove: func(_ context.Context, clusterName string, _ Entry) { removed = append(removed, clusterName) },
	}

	p.Set(ctx, "a", Entry{})
	p.RegisterHooks(ctx, hooks)
	// Registration engages the hook for entries already present.
	assert.Equal(t, []string{"a"}, added)

	p.Set(ctx, "b", Entry{})
	assert.Equal(t, []string{"a", "b"}, added)

	p.Delete(ctx, "b")
	assert.Equal(t, []string{"b"}, removed)

	// OnRemove is not fired for unknown clusters.
	p.Delete(ctx, "b")
	assert.Equal(t, []string{"b"}, removed)
}
