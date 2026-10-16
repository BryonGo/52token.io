package controller

import (
	"testing"

	"github.com/QuantumNous/new-api/model"
	"github.com/stretchr/testify/assert"
)

func TestMetadataModelMatchesExactBeforeRecognizedReasoningAlias(t *testing.T) {
	catalog := map[string]model.MetadataValues{
		"claude-opus-4-7":      {Description: "Base metadata"},
		"claude-opus-4-7-high": {Description: "Specific metadata"},
		"grok-4":               {Description: "Grok 4"},
		"grok-4.3":             {Description: "Grok 4.3"},
	}
	value, matched, found := resolveMetadataModel(catalog, "claude-opus-4-7-high")
	assert.True(t, found)
	assert.Equal(t, "claude-opus-4-7-high", matched)
	assert.Equal(t, "Specific metadata", value.Description)
	delete(catalog, "claude-opus-4-7-high")
	value, matched, found = resolveMetadataModel(catalog, "claude-opus-4-7-high")
	assert.True(t, found)
	assert.Equal(t, "claude-opus-4-7", matched)
	assert.Equal(t, "Base metadata", value.Description)
	for _, name := range []string{"grok-4-0709", "grok-4.2", "claude-opus-4-8-high", "unknown-high"} {
		_, _, found := resolveMetadataModel(catalog, name)
		assert.False(t, found, "must not guess metadata for %s", name)
	}
}
