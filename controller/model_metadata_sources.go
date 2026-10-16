package controller

import (
	"slices"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
)

var fetchChannelMetadata = service.FetchChannelMetadata

type metadataCatalog struct {
	Models       map[string]model.MetadataValues
	Vendors      map[string]model.Vendor
	Supplemental service.SupplementalMetadata
	Config       model.MetadataSources
}

type resolvedMetadata struct {
	Values   model.MetadataValues
	Model    string
	Source   string
	FillOnly bool
}

func (catalog *metadataCatalog) resolve(name string) (resolvedMetadata, bool) {
	channel, channelFound := catalog.Supplemental.Models[name]
	var selected *model.MetadataAlias
	for i := range catalog.Config.Aliases {
		a := &catalog.Config.Aliases[i]
		if a.Match == "exact" && a.Name == name {
			selected = a
			break
		}
		if a.Match == "prefix" && strings.HasPrefix(name, a.Name) && (selected == nil || len(a.Name) > len(selected.Name)) {
			selected = a
		}
	}
	target := name
	if selected != nil && selected.MetadataModel != "" {
		target = selected.MetadataModel
	}
	canonical, matched, canonicalFound := resolveMetadataModel(catalog.Models, target)
	if selected != nil && selected.MetadataModel != "" && !canonicalFound {
		// Explicit aliases may also target a model already read from its bound channel.
		canonical, canonicalFound = catalog.Supplemental.Models[target]
		matched = target
		if !canonicalFound {
			return resolvedMetadata{}, false
		}
	}
	if !channelFound && !canonicalFound && selected == nil {
		return resolvedMetadata{}, false
	}
	value := channel
	source := "channel"
	if !channelFound {
		value = canonical
		source = "catalog"
	} else if canonicalFound {
		pairs := []struct {
			field    string
			current  *string
			fallback string
		}{{"description", &value.Description, canonical.Description}, {"icon", &value.Icon, canonical.Icon}, {"tags", &value.Tags, canonical.Tags}, {"vendor", &value.Vendor, canonical.Vendor}, {"endpoints", &value.Endpoints, canonical.Endpoints}}
		for _, p := range pairs {
			if *p.current == "" && !slices.Contains(catalog.Supplemental.Conflicts[name], p.field) {
				*p.current = p.fallback
			}
		}
	}
	if selected != nil {
		source = "alias"
		if selected.Vendor != "" {
			value.Vendor = service.NormalizeMetadataVendor(selected.Vendor)
			if vendor, ok := catalog.Vendors[value.Vendor]; ok && vendor.Icon != "" {
				value.Icon = vendor.Icon
			}
		}
	}
	fillOnly := channelFound || selected != nil
	if fillOnly {
		value.NameRule, value.Status = model.NameRuleExact, 1
	}
	if matched == "" {
		matched = name
	}
	resolved := resolvedMetadata{Values: value, Model: matched, Source: source, FillOnly: fillOnly}
	if fillOnly && len(metadataAvailableFields(resolved)) == 0 {
		return resolvedMetadata{}, false
	}
	return resolved, true
}

func metadataAvailableFields(resolved resolvedMetadata) []string {
	if !resolved.FillOnly {
		return model.MetadataSyncFields
	}
	fields := []string{}
	v := resolved.Values
	for _, f := range []struct{ name, value string }{{"description", v.Description}, {"icon", v.Icon}, {"tags", v.Tags}, {"vendor", v.Vendor}, {"endpoints", v.Endpoints}} {
		if strings.TrimSpace(f.value) != "" {
			fields = append(fields, f.name)
		}
	}
	return fields
}

func GetMetadataSources(c *gin.Context) {
	cfg, version, err := model.GetMetadataSources()
	if err != nil {
		common.ApiError(c, err)
		return
	}
	common.ApiSuccess(c, gin.H{"config": cfg, "version": version})
}

func SaveMetadataSources(c *gin.Context) {
	var request struct {
		Config          model.MetadataSources `json:"config"`
		ExpectedVersion string                `json:"expected_version"`
	}
	if err := common.DecodeJson(c.Request.Body, &request); err != nil {
		common.ApiError(c, err)
		return
	}
	if err := model.SaveMetadataSources(request.Config, request.ExpectedVersion); err != nil {
		common.ApiError(c, err)
		return
	}
	recordManageAudit(c, "model.metadata.sources", map[string]any{"channel_fallback": request.Config.ChannelFallback, "aliases": len(request.Config.Aliases)})
	common.ApiSuccess(c, nil)
}

// Keep this check shared by preview and apply: sparse supplemental data can only
// fill fields actually supplied by that source, never clear unrelated metadata.
func metadataFieldAvailable(resolved resolvedMetadata, field string) bool {
	return slices.Contains(metadataAvailableFields(resolved), field)
}
