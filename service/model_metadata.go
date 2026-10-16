package service

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/channelcost"
	"github.com/tidwall/gjson"
)

type MetadataOrigin struct {
	ChannelID     int    `json:"channel_id"`
	ChannelName   string `json:"channel_name"`
	SourceID      string `json:"source_id"`
	SourceName    string `json:"source_name"`
	UpstreamModel string `json:"upstream_model"`
}

type MetadataWarning struct {
	Message string `json:"message"`
	Name    string `json:"name,omitempty"`
}

type SupplementalMetadata struct {
	Models    map[string]model.MetadataValues
	Vendors   map[string]model.Vendor
	Origins   map[string][]MetadataOrigin
	Conflicts map[string][]string
	Warnings  []MetadataWarning
	Version   string
}

func NormalizeMetadataVendor(name string) string {
	name = strings.TrimSpace(name)
	switch strings.ToLower(name) {
	case "xai", "x.ai", "x ai":
		return "xAI"
	case "openai":
		return "OpenAI"
	case "anthropic":
		return "Anthropic"
	case "deepseek":
		return "DeepSeek"
	case "google":
		return "Google"
	case "meta":
		return "Meta"
	}
	return name
}

// ParseSourceMetadata reads descriptive fields using the bound source contract.
// Prices and upstream numeric vendor IDs never become local prices or IDs.
func ParseSourceMetadata(body []byte, source channelcost.Source) (SupplementalMetadata, error) {
	result := SupplementalMetadata{Models: map[string]model.MetadataValues{}, Vendors: map[string]model.Vendor{}}
	if !gjson.ValidBytes(body) {
		return result, errors.New("Invalid channel metadata response")
	}
	f := source.Fields
	if source.Format == "newapi" {
		defaults := map[*string]string{&f.Description: "description", &f.VendorID: "vendor_id", &f.Icon: "icon", &f.Tags: "tags", &f.Endpoints: "supported_endpoint_types", &f.Vendors: "vendors", &f.VendorKey: "id", &f.VendorName: "name", &f.VendorIcon: "icon"}
		for path, value := range defaults {
			if *path == "" {
				*path = value
			}
		}
	}
	root := gjson.ParseBytes(body)
	if f.Success != "" && !root.Get(f.Success).Bool() {
		return result, errors.New("Channel metadata response reports failure")
	}
	rows := root
	if f.Items != "" {
		rows = root.Get(f.Items)
	}
	if !rows.IsArray() || len(rows.Array()) > 20000 {
		return result, errors.New("Mapped metadata model list must be a JSON array")
	}
	byID := map[string]string{}
	if f.Vendors != "" {
		for _, v := range root.Get(f.Vendors).Array() {
			name := NormalizeMetadataVendor(v.Get(f.VendorName).String())
			id := v.Get(f.VendorKey).String()
			if name == "" || len(name) > 128 || id == "" {
				continue
			}
			if old, ok := byID[id]; ok && old != name {
				return result, errors.New("Conflicting upstream vendor IDs")
			}
			byID[id] = name
			vendor := model.Vendor{Name: name, Icon: v.Get(f.VendorIcon).String(), Status: 1}
			if old, exists := result.Vendors[name]; exists && old.Icon != "" {
				vendor.Icon = old.Icon
			}
			result.Vendors[name] = vendor
		}
	}
	seen := map[string]bool{}
	for _, row := range rows.Array() {
		name := strings.TrimSpace(row.Get(f.Model).String())
		if name == "" || len(name) > 200 {
			continue
		}
		if seen[name] {
			return result, errors.New("Duplicate channel metadata model")
		}
		seen[name] = true
		vendor := ""
		if f.Vendor != "" {
			vendor = NormalizeMetadataVendor(row.Get(f.Vendor).String())
		}
		if vendor == "" && f.VendorID != "" {
			vendor = byID[row.Get(f.VendorID).String()]
		}
		v := model.MetadataValues{Vendor: vendor, NameRule: model.NameRuleExact, Status: 1}
		if f.Description != "" {
			v.Description = row.Get(f.Description).String()
		}
		if f.Tags != "" {
			v.Tags = row.Get(f.Tags).String()
		}
		if f.Icon != "" {
			v.Icon = row.Get(f.Icon).String()
		}
		if v.Icon == "" {
			v.Icon = result.Vendors[vendor].Icon
		}
		if vendor != "" {
			if _, ok := result.Vendors[vendor]; !ok {
				result.Vendors[vendor] = model.Vendor{Name: vendor, Icon: v.Icon, Status: 1}
			}
		}
		if f.Endpoints != "" {
			raw := row.Get(f.Endpoints)
			if raw.Exists() && model.ValidateModelEndpoints(raw.Raw) == nil {
				v.Endpoints = raw.Raw
			}
		}
		if v.Description != "" || v.Icon != "" || v.Tags != "" || v.Vendor != "" || v.Endpoints != "" {
			result.Models[name] = v
		}
	}
	return result, nil
}

// FetchChannelMetadata fetches each bound source once, then selects only the
// upstream names configured on its own channels. Equal names do not share sources.
func FetchChannelMetadata(ctx context.Context) (SupplementalMetadata, error) {
	result := SupplementalMetadata{Models: map[string]model.MetadataValues{}, Vendors: map[string]model.Vendor{}, Origins: map[string][]MetadataOrigin{}, Conflicts: map[string][]string{}, Warnings: []MetadataWarning{}}
	var channels []model.Channel
	if err := model.DB.Select("id", "name", "type", "base_url", "models", "model_mapping", "settings").Order("id").Find(&channels).Error; err != nil {
		return result, err
	}
	fetched := map[string]SupplementalMetadata{}
	failures := map[string]bool{}
	bindings := []any{}
	for _, ch := range channels {
		cfg := ch.GetOtherSettings().CostBilling
		if cfg == nil || cfg.Source == "" {
			continue
		}
		source, err := model.GetChannelPriceSource(cfg.Source)
		if err != nil {
			result.Warnings = append(result.Warnings, MetadataWarning{Message: "Channel metadata source unavailable: {{name}}", Name: ch.Name})
			continue
		}
		bindings = append(bindings, []any{ch.Id, ch.Models, ch.ModelMapping, source})
		base := ch.GetBaseURL()
		if base == "" && ch.Type == 43 {
			base = "https://api.deepseek.com"
		}
		endpoint, _ := url.Parse(base)
		origin, _ := url.Parse(source.ChannelOrigin)
		if endpoint == nil || origin == nil || endpoint.Scheme != origin.Scheme || !strings.EqualFold(endpoint.Host, origin.Host) {
			result.Warnings = append(result.Warnings, MetadataWarning{Message: "Channel metadata source does not match endpoint: {{name}}", Name: ch.Name})
			continue
		}
		if source.Format == "deepseek_html" {
			continue
		}
		if failures[source.ID] {
			continue
		}
		data, ok := fetched[source.ID]
		if !ok {
			body, err := fetchChannelCostDocument(ctx, source.URL)
			if err == nil {
				data, err = ParseSourceMetadata(body, source)
			}
			if err != nil {
				failures[source.ID] = true
				result.Warnings = append(result.Warnings, MetadataWarning{Message: "Unable to read channel metadata source: {{name}}", Name: source.Name})
				continue
			}
			fetched[source.ID] = data
		}
		mapping := map[string]string{}
		if raw := ch.GetModelMapping(); raw != "" {
			if common.UnmarshalJsonStr(raw, &mapping) != nil {
				result.Warnings = append(result.Warnings, MetadataWarning{Message: "Invalid channel model mapping: {{name}}", Name: ch.Name})
				continue
			}
		}
		for name := range strings.SplitSeq(ch.Models, ",") {
			name = strings.TrimSpace(name)
			if name == "" {
				continue
			}
			upstream := name
			visited := map[string]bool{}
			for mapping[upstream] != "" && mapping[upstream] != upstream && !visited[upstream] {
				visited[upstream] = true
				upstream = mapping[upstream]
			}
			if visited[upstream] {
				continue
			}
			values, found := data.Models[upstream]
			if !found {
				continue
			}
			if values.Vendor != "" {
				result.Vendors[values.Vendor] = data.Vendors[values.Vendor]
			}
			origin := MetadataOrigin{ChannelID: ch.Id, ChannelName: ch.Name, SourceID: source.ID, SourceName: source.Name, UpstreamModel: upstream}
			result.Origins[name] = append(result.Origins[name], origin)
			if current, exists := result.Models[name]; exists {
				pairs := []struct {
					field    string
					value    *string
					incoming string
				}{{"description", &current.Description, values.Description}, {"icon", &current.Icon, values.Icon}, {"tags", &current.Tags, values.Tags}, {"vendor", &current.Vendor, values.Vendor}, {"endpoints", &current.Endpoints, values.Endpoints}}
				for _, p := range pairs {
					if slices.Contains(result.Conflicts[name], p.field) {
						continue
					}
					if *p.value == "" {
						*p.value = p.incoming
					} else if p.incoming != "" && *p.value != p.incoming {
						*p.value = ""
						result.Conflicts[name] = append(result.Conflicts[name], p.field)
					}
				}
				result.Models[name] = current
			} else {
				result.Models[name] = values
			}
		}
	}
	raw, _ := common.Marshal([]any{bindings, result.Models, result.Vendors, result.Origins, result.Conflicts, result.Warnings})
	result.Version = fmt.Sprintf("%x", sha256.Sum256(raw))
	return result, nil
}
