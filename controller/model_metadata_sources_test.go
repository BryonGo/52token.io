package controller

import (
	"testing"

	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/stretchr/testify/assert"
)

func TestMetadataResolutionPrefersBoundChannelAndNormalizesAliases(t *testing.T) {
	catalog := metadataCatalog{
		Models:       map[string]model.MetadataValues{"grok-4.2": {Description: "Generic", Vendor: "Catalog vendor"}, "claude-opus-4-7": {Description: "Canonical description"}},
		Vendors:      map[string]model.Vendor{"xAI": {Name: "xAI", Icon: "XAI"}},
		Supplemental: service.SupplementalMetadata{Models: map[string]model.MetadataValues{"grok-4.2": {Description: "Bound channel", Vendor: "xAI"}}},
		Config:       model.DefaultMetadataSources(),
	}
	r, found := catalog.resolve("grok-4.2")
	assert.True(t, found)
	assert.Equal(t, "Bound channel", r.Values.Description)
	assert.Equal(t, "xAI", r.Values.Vendor)
	assert.True(t, r.FillOnly)
	catalog.Config.Aliases = append(catalog.Config.Aliases, model.MetadataAlias{Match: "exact", Name: "internal-opus", MetadataModel: "claude-opus-4-7"})
	r, found = catalog.resolve("internal-opus")
	assert.True(t, found)
	assert.Equal(t, "Canonical description", r.Values.Description)
	assert.NotContains(t, metadataAvailableFields(r), "status")
	catalog.Config.Aliases = append(catalog.Config.Aliases, model.MetadataAlias{Match: "exact", Name: "grok-4.2", MetadataModel: "missing"})
	_, found = catalog.resolve("grok-4.2")
	assert.False(t, found, "missing explicit targets must not silently fall back")
	assert.Error(t, (model.MetadataSources{Aliases: []model.MetadataAlias{{Match: "prefix", Name: "grok-"}}}).Validate())
	assert.Error(t, (model.MetadataSources{Aliases: []model.MetadataAlias{{Match: "exact", Name: "m", Vendor: "xAI"}, {Match: "exact", Name: "m", Vendor: "Meta"}}}).Validate())
}
