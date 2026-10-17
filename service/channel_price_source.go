package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/pkg/jsplugin"
	"github.com/QuantumNous/new-api/setting/billing_setting"
	"math"
	"strconv"
	"strings"

	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/channelcost"
	"github.com/tidwall/gjson"
)

type SourcePrices struct {
	Prices     map[string]channelcost.Price `json:"prices"`
	Errors     map[string]string            `json:"errors"`
	GroupRatio float64                      `json:"group_ratio"`
}

func FetchPriceSource(ctx context.Context, source channelcost.Source, group string, credentials ...model.PriceSourceCredential) (*SourcePrices, error) {
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
		currency := source.PriceCurrency
		if currency == "" {
			currency = "CNY"
		}
		prices, err := parseDeepSeekCostCurrency(body, currency)
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
	var token string
	var userID int
	var err error
	if len(credentials) > 0 && credentials[0].Token != "" {
		credential := credentials[0]
		if err = model.ValidatePriceSourceCredential(source, credential); err != nil {
			return nil, err
		}
		token = credential.Token
		userID = credential.UserID
	} else {
		token, userID, err = model.GetPriceSourceCredential(source)
		if err != nil {
			return nil, err
		}
	}

	quota := 500000.0
	if source.StatusURL != "" {
		body, err := fetchChannelCostDocumentWithAuth(ctx, source.StatusURL, token, userID)
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
	body, err := fetchChannelCostDocumentWithAuth(ctx, source.URL, token, userID)
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
		if source.FilterField != "" {
			matches := row.Get(source.FilterField).String() == source.FilterValue
			if matches == source.FilterExclude {
				continue
			}
		}
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
				if !source.AdvancedPricing {
					result.Errors[name] = err.Error()
					continue
				}
				var schema map[string]json.RawMessage
				schemaPath := source.Fields.UsageSchema
				if schemaPath == "" && source.Format == "newapi" {
					schemaPath = "billing_usage_schema"
				}
				if raw := row.Get(schemaPath); schemaPath != "" && raw.Exists() {
					if e := common.UnmarshalJsonStr(raw.Raw, &schema); e != nil {
						result.Errors[name] = "Upstream expression usage schema is missing or invalid"
						continue
					}
				}
				price.Dynamic, err = channelcost.ParseExpression(row.Get(source.Fields.Expression).String(), schema)
				if err == nil {
					if price.Dynamic.Task {
						var typedSchema map[string]jsplugin.UsageFieldSchema
						encoded, e := common.Marshal(schema)
						if e == nil {
							e = common.Unmarshal(encoded, &typedSchema)
						}
						if e != nil {
							err = e
						} else {
							err = billing_setting.SmokeTestTaskExpr(price.Dynamic.Text, typedSchema)
						}
					} else {
						err = billing_setting.SmokeTestExpr(price.Dynamic.Text)
					}
				}
				if err != nil {
					result.Errors[name] = err.Error()
					continue
				}
				result.Prices[name] = price
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
				unitPath, unitsPath := source.Fields.RequestUnit, source.Fields.RequestUnits
				if source.Format == "newapi" {
					if unitPath == "" {
						unitPath = "request_price_display_unit"
					}
					if unitsPath == "" {
						unitsPath = "request_price_units"
					}
				}
				unit := "request"
				if unitPath != "" && row.Get(unitPath).String() != "" {
					unit = row.Get(unitPath).String()
				}
				units := 1.0
				if unitsPath != "" && row.Get(unitsPath).Exists() {
					value, valid := mappedNumber(row, unitsPath)
					if !valid || value < 1 || value > 1e6 || math.Trunc(value) != value {
						result.Errors[name] = "Invalid upstream request price unit count"
						continue
					}
					units = value
				}
				if unit != "request" && unit != "second" {
					result.Errors[name] = "Unsupported upstream billing unit"
					continue
				}
				if !source.AdvancedPricing && (unit != "request" || units != 1) {
					if unit != "request" {
						result.Errors[name] = "Upstream price is not per request; its billing unit requires a dedicated rule"
					} else {
						result.Errors[name] = "Upstream request price has a nonstandard unit count; confirm its billing rule"
					}
					continue
				}
				if !hasRequest {
					result.Errors[name] = "Invalid upstream request price"
					continue
				}
				if units != 1 || unit != "request" {
					price.ChargeUnit = &channelcost.ChargeUnit{Unit: unit, Units: units, Price: requestPrice * source.InputScale}
				}
				if unit == "second" {
					schema := map[string]json.RawMessage{"seconds": json.RawMessage(`{"type":"number","unit":"second","description":{"en":"Video generation unit price","zh":"视频生成单价"}}`)}
					expression := fmt.Sprintf("tier(\"second\", u(\"seconds\") * %.17g)", requestPrice*source.InputScale*units)
					dynamic, e := channelcost.ParseExpression(expression, schema)
					if e != nil {
						result.Errors[name] = e.Error()
						continue
					}
					price.Dynamic = dynamic
					result.Prices[name] = price
					continue
				}
				requestPrice *= units
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
			// Native video APIs expose completed output billing tokens, rather
			// than a chat prompt/completion pair. Only equal token rates can be
			// represented by that single documented meter.
			if source.AdvancedPricing && source.Format == "newapi" && row.Get("is_video_model").Int() == 1 {
				if rates.Input != rates.Output {
					result.Errors[name] = "Video token prices require distinct upstream usage meters"
					continue
				}
				schema := map[string]json.RawMessage{"tokens": json.RawMessage(`{"type":"number","unit":"token","description":{"en":"Billing token unit price","zh":"计费 Token 单价"}}`)}
				dynamic, e := channelcost.ParseExpression(fmt.Sprintf("tier(\"tokens\", u(\"tokens\") * %.17g / 1000000)", rates.Output), schema)
				if e != nil {
					result.Errors[name] = e.Error()
					continue
				}
				price.Dynamic = dynamic
			}
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

	for name, rule := range source.ModelRules {
		price, ok := result.Prices[name]
		if !ok {
			continue
		}
		expression, err := rule.Resolve(price)
		if err != nil {
			delete(result.Prices, name)
			result.Errors[name] = err.Error()
			continue
		}
		price.Dynamic = expression
		price.RequestPrice = nil
		price.ChargeUnit = nil
		price.RuleSource = rule.DocumentURL
		result.Prices[name] = price
	}
	if len(result.Prices) == 0 && len(result.Errors) == 0 {
		return nil, fmt.Errorf("No model names matched field path %s", source.Fields.Model)
	}
	return result, nil
}
