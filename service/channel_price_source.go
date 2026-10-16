package service

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/QuantumNous/new-api/pkg/channelcost"
	"github.com/tidwall/gjson"
)

type SourcePrices struct {
	Prices     map[string]channelcost.Price `json:"prices"`
	Errors     map[string]string            `json:"errors"`
	GroupRatio float64                      `json:"group_ratio"`
}

func FetchPriceSource(ctx context.Context, source channelcost.Source, group string) (*SourcePrices, error) {
	if err := source.Validate(); err != nil {
		return nil, err
	}
	if !source.Enabled {
		return nil, errors.New("Price source is missing or disabled")
	}
	if source.Format == "deepseek_html" {
		body, err := fetchChannelCostDocument(ctx, source.URL)
		if err != nil {
			return nil, err
		}
		prices, err := parseDeepSeekCost(body)
		if err != nil {
			return nil, err
		}
		if ref := source.Reference; ref != nil {
			result := &SourcePrices{Prices: map[string]channelcost.Price{}, Errors: map[string]string{}, GroupRatio: ref.GroupRatio}
			for name, target := range ref.Models {
				price, ok := prices[target]
				if !ok {
					result.Errors[name] = "Confirmed reference model price is unavailable"
					continue
				}
				price.UpstreamModel = name
				if ref.Schedule == "off_peak" {
					price.Peak = nil
				}
				result.Prices[name] = price
			}
			return result, nil
		}
		return &SourcePrices{Prices: prices, Errors: map[string]string{}, GroupRatio: 1}, err
	}
	quota := 500000.0
	if source.StatusURL != "" {
		body, err := fetchChannelCostDocument(ctx, source.StatusURL)
		if err != nil {
			return nil, err
		}
		if source.Fields.QuotaUnit != "" {
			quota = gjson.GetBytes(body, source.Fields.QuotaUnit).Float()
		}
		if !channelcost.Positive(quota) {
			return nil, errors.New("Upstream quota unit is unavailable")
		}
	}
	body, err := fetchChannelCostDocument(ctx, source.URL)
	if err != nil {
		if source.Format == "sub2api" {
			return nil, errors.New("Sub2API model plaza is unavailable; ask the upstream to enable its pricing endpoint")
		}
		return nil, err
	}
	return ParsePriceSource(body, source, group, quota)
}
func mappedNumber(row gjson.Result, path string) (float64, bool) {
	if path == "" {
		return 0, false
	}
	raw := row.Get(path)
	if !raw.Exists() || raw.Type == gjson.Null || raw.Type == gjson.True || raw.Type == gjson.False {
		return 0, false
	}
	value, err := strconv.ParseFloat(raw.String(), 64)
	return value, err == nil && value >= 0 && channelcost.Rates{Input: value}.Valid()
}
func ParsePriceSource(body []byte, source channelcost.Source, group string, quota float64) (*SourcePrices, error) {
	if source.Format == "sub2api" {
		return parseSub2APIPrices(body, source, group)
	}
	if !gjson.ValidBytes(body) {
		return nil, errors.New("Invalid upstream price response")
	}
	root := gjson.ParseBytes(body)
	if source.Fields.Success != "" && !root.Get(source.Fields.Success).Bool() {
		return nil, errors.New("Upstream price response reports failure")
	}
	result := &SourcePrices{Prices: map[string]channelcost.Price{}, Errors: map[string]string{}, GroupRatio: 1}
	if source.Fields.GroupRatios != "" {
		if group == "" || group == "auto" {
			return nil, errors.New("Select the upstream token group")
		}
		ratios := root.Get(source.Fields.GroupRatios).Map()
		raw, ok := ratios[group]
		if !ok || !channelcost.Positive(raw.Float()) {
			return nil, errors.New("Upstream group multiplier is unavailable")
		}
		result.GroupRatio = raw.Float()
	}
	rows := root
	if source.Fields.Items != "" {
		rows = root.Get(source.Fields.Items)
	}
	if !rows.IsArray() {
		return nil, errors.New("Mapped model list must be a JSON array")
	}
	if len(rows.Array()) > 20000 {
		return nil, errors.New("Too many upstream pricing models")
	}
	seen := map[string]bool{}
	for _, row := range rows.Array() {
		name := strings.TrimSpace(row.Get(source.Fields.Model).String())
		if name == "" {
			continue
		}
		if seen[name] {
			delete(result.Prices, name)
			result.Errors[name] = "Duplicate upstream model prices"
			continue
		}
		seen[name] = true
		if source.Fields.Groups != "" && group != "" {
			allowed := false
			for _, g := range row.Get(source.Fields.Groups).Array() {
				if g.String() == group {
					allowed = true
				}
			}
			if !allowed {
				result.Errors[name] = "Model is unavailable in the selected upstream group"
				continue
			}
		}
		mode := ""
		if source.Fields.BillingMode != "" {
			mode = row.Get(source.Fields.BillingMode).String()
		}
		if mode == "" && source.Fields.Expression != "" && strings.TrimSpace(row.Get(source.Fields.Expression).String()) != "" {
			mode = "tiered_expr"
		}
		price := channelcost.Price{UpstreamModel: name}
		if mode == "tiered_expr" {
			var err error
			price.RequestPrice, err = channelcost.ParseRequestPrice(row.Get(source.Fields.Expression).String())
			if err == nil && price.RequestPrice != nil {
				result.Prices[name] = price
				continue
			}
			if err == nil {
				price.Tiers, err = channelcost.ParseTokenTiers(row.Get(source.Fields.Expression).String())
			}
			if err != nil {
				result.Errors[name] = err.Error()
				continue
			}
			price.OffPeak = price.Tiers[0].Rates
		} else {
			requestPath, typePath := source.Fields.RequestPrice, source.Fields.QuotaType
			if source.Format == "newapi" {
				if requestPath == "" {
					requestPath = "model_price"
				}
				if typePath == "" {
					typePath = "quota_type"
				}
			}
			requestPrice, hasRequest := mappedNumber(row, requestPath)
			kind := row.Get(typePath)
			requestType := false
			if typePath != "" && kind.Exists() {
				switch kind.String() {
				case "1", "request", "fixed", "per_request":
					requestType = true
				case "0", "token", "":
				default:
					result.Errors[name] = "Unsupported upstream billing unit"
					continue
				}
			}
			if requestType || mode == "request" || mode == "fixed" || mode == "per_request" || hasRequest && !kind.Exists() && !row.Get(source.Fields.Input).Exists() && !row.Get(source.Fields.Output).Exists() {
				requestPrice *= source.InputScale
				_, err := channelcost.RequestExpression(requestPrice)
				if !hasRequest || err != nil {
					result.Errors[name] = "Invalid upstream request price"
					continue
				}
				price.RequestPrice = &requestPrice
				result.Prices[name] = price
				continue
			}
			if mode != "" && mode != "ratio" {
				result.Errors[name] = "Unsupported upstream billing mode"
				continue
			}
			input, hasInput := mappedNumber(row, source.Fields.Input)
			output, hasOutput := mappedNumber(row, source.Fields.Output)
			if !hasInput || !hasOutput {
				result.Errors[name] = "Mapped input or output price is missing or invalid"
				continue
			}
			scale := 1e6 / source.UnitTokens * source.InputScale
			if source.Format == "newapi" {
				if !channelcost.Positive(quota) {
					return nil, errors.New("Upstream quota unit is unavailable")
				}
				scale = 1e6 / quota * source.InputScale
			}
			input *= scale
			if source.OutputRatio {
				output *= input
			} else {
				output *= scale
			}
			rates := channelcost.Rates{Input: input, Output: output, CacheRead: input, CacheWrite: input, CacheWrite1h: input}
			valid := true
			for _, field := range []struct {
				path  string
				value *float64
			}{{source.Fields.CacheRead, &rates.CacheRead}, {source.Fields.CacheWrite, &rates.CacheWrite}, {source.Fields.CacheWrite1h, &rates.CacheWrite1h}} {
				if field.path == "" || !row.Get(field.path).Exists() {
					continue
				}
				value, ok := mappedNumber(row, field.path)
				if !ok {
					valid = false
					break
				}
				if source.CacheRatio {
					value *= input
				} else {
					value *= scale
				}
				*field.value = value
			}
			if source.Format == "newapi" && source.Fields.CacheWrite1h == "" {
				rates.CacheWrite1h = rates.CacheWrite * 1.6
			}
			if !valid || !rates.Valid() {
				result.Errors[name] = "Mapped input or output price is missing or invalid"
				continue
			}
			price.OffPeak = rates
		}
		if _, exists := result.Prices[name]; exists {
			delete(result.Prices, name)
			result.Errors[name] = "Duplicate upstream model prices"
			continue
		}
		if result.Errors[name] == "Duplicate upstream model prices" {
			continue
		}
		result.Prices[name] = price
	}
	if len(result.Prices) == 0 && len(result.Errors) == 0 {
		return nil, fmt.Errorf("No model names matched field path %s", source.Fields.Model)
	}
	return result, nil
}
