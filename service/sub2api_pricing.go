package service

import (
	"context"
	"errors"
	"io"
	"math"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/channelcost"
	"github.com/tidwall/gjson"
)

// FetchSub2APIDocument sends a channel's model key only to its fixed read-only
// usage and billing endpoints, never to an administrator-configured price URL.
func FetchSub2APIDocument(ctx context.Context, channel *model.Channel, path string) ([]byte, error) {
	if path != "/v1/usage" && path != "/v1/sub2api/billing" {
		return nil, errors.New("Invalid Sub2API account endpoint")
	}
	base := strings.TrimRight(channel.GetBaseURL(), "/")
	endpoint, err := url.Parse(base)
	if err != nil || endpoint.Host == "" || endpoint.User != nil || endpoint.RawQuery != "" || endpoint.Fragment != "" {
		return nil, errors.New("Invalid Sub2API account endpoint")
	}
	ip := net.ParseIP(endpoint.Hostname())
	loopback := strings.EqualFold(endpoint.Hostname(), "localhost") || ip != nil && ip.IsLoopback()
	if endpoint.Scheme != "https" && !(endpoint.Scheme == "http" && loopback) {
		return nil, errors.New("Sub2API credentials require HTTPS")
	}
	setting := channel.GetSetting()
	if setting.TLSInsecureSkipVerify || common.TLSInsecureSkipVerify {
		return nil, errors.New("Sub2API credentials require certificate verification")
	}
	key := strings.TrimSpace(channel.Key)
	if key == "" || len(key) > 8192 || strings.ContainsAny(key, "\r\n") {
		return nil, errors.New("Invalid Sub2API credentials")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+path, nil)
	if err != nil {
		return nil, errors.New("Invalid Sub2API account endpoint")
	}
	req.Header.Set("Authorization", "Bearer "+key)
	client, err := GetHttpClientWithProxySettings(setting.Proxy, setting)
	if err != nil {
		return nil, errors.New("Unable to query Sub2API account")
	}
	isolated := *client
	isolated.Timeout = 20 * time.Second
	isolated.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	response, err := isolated.Do(req)
	if err != nil {
		return nil, errors.New("Unable to query Sub2API account")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, common.NewMessage("Sub2API account request returned HTTP {{status}}", map[string]any{"status": response.StatusCode})
	}
	const limit = 256 << 10
	body, err := io.ReadAll(io.LimitReader(response.Body, limit+1))
	if err != nil || len(body) > limit {
		return nil, errors.New("Invalid Sub2API account response")
	}
	return body, nil
}

// Sub2APIEffectiveRate uses the API key's authenticated rate, including any
// user-specific override. Scheduled rates require explicit expression support.
func Sub2APIEffectiveRate(ctx context.Context, channel *model.Channel, published float64) (float64, error) {
	body, err := FetchSub2APIDocument(ctx, channel, "/v1/sub2api/billing")
	if err != nil {
		return 0, err
	}
	var info struct {
		Object    string   `json:"object"`
		Version   int      `json:"schema_version"`
		Scope     string   `json:"billing_scope"`
		Group     *float64 `json:"group_rate_multiplier"`
		Effective *float64 `json:"effective_rate_multiplier"`
		Peak      bool     `json:"peak_rate_enabled"`
	}
	if common.Unmarshal(body, &info) != nil || info.Object != "sub2api.key_billing" || info.Version != 1 || info.Scope != "token" || info.Group == nil || info.Effective == nil || !channelcost.Positive(*info.Group) || !channelcost.Positive(*info.Effective) {
		return 0, errors.New("Invalid Sub2API billing multiplier response")
	}
	if info.Peak {
		return 0, errors.New("Sub2API scheduled group rates are not supported")
	}
	if math.Abs(*info.Group-published) > 1e-10*math.Max(1, math.Abs(published)) {
		return 0, errors.New("Sub2API key group does not match the selected price group")
	}
	return *info.Effective, nil
}

// The public model plaza contains actual channel prices under pricing; the
// official_pricing object is reference data and must never supply missing costs.
func parseSub2APIPrices(body []byte, source channelcost.Source, group string) (*SourcePrices, error) {
	if !gjson.ValidBytes(body) {
		return nil, errors.New("Invalid upstream price response")
	}
	root := gjson.ParseBytes(body)
	if code := root.Get("code"); code.Exists() && code.Float() != 0 {
		return nil, errors.New("Upstream price response reports failure")
	}
	if group == "" || group == "auto" {
		return nil, errors.New("Select the upstream token group")
	}
	groups := root.Get(source.Fields.Items)
	if !groups.IsArray() || len(groups.Array()) > 1000 {
		return nil, errors.New("Invalid Sub2API price group list")
	}
	var selected gjson.Result
	for _, row := range groups.Array() {
		if row.Get("name").String() == group {
			if selected.Exists() {
				return nil, errors.New("Duplicate Sub2API price group")
			}
			selected = row
		}
	}
	if !selected.Exists() {
		return nil, errors.New("Upstream group multiplier is unavailable")
	}
	rate, ok := mappedNumber(selected, source.Fields.GroupRatios)
	if !ok || !channelcost.Positive(rate) {
		return nil, errors.New("Upstream group multiplier is unavailable")
	}
	if selected.Get("peak_rate_enabled").Bool() {
		return nil, errors.New("Sub2API scheduled group rates are not supported")
	}
	rows := selected.Get(source.Fields.Groups)
	if !rows.IsArray() || len(rows.Array()) > 20000 {
		return nil, errors.New("Mapped model list must be a JSON array")
	}
	result := &SourcePrices{Prices: map[string]channelcost.Price{}, Errors: map[string]string{}, GroupRatio: rate}
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
		price, err := sub2APIModelPrice(row, source)
		if err != nil {
			result.Errors[name] = err.Error()
			continue
		}
		price.UpstreamModel = name
		result.Prices[name] = price
	}
	if len(result.Prices) == 0 && len(result.Errors) == 0 {
		return nil, errors.New("No Sub2API model prices found")
	}
	return result, nil
}

func sub2APIModelPrice(row gjson.Result, source channelcost.Source) (channelcost.Price, error) {
	price := channelcost.Price{}
	if row.Get("time_pricing.periods.#").Int() > 0 || row.Get("long_context_basis").String() == "marginal" {
		return price, errors.New("Unsupported Sub2API model pricing rules")
	}
	for _, value := range row.Get("pricing.reasoning_effort_multipliers").Map() {
		if value.Float() != 1 {
			return price, errors.New("Unsupported Sub2API model pricing rules")
		}
	}
	mode := row.Get(source.Fields.BillingMode).String()
	intervals := row.Get("pricing.intervals")
	if mode == "per_request" {
		value, ok := mappedNumber(row, source.Fields.RequestPrice)
		if !ok || len(intervals.Array()) > 0 {
			return price, errors.New("Invalid upstream request price")
		}
		value *= source.InputScale
		if _, err := channelcost.RequestExpression(value); err != nil {
			return price, err
		}
		price.RequestPrice = &value
		return price, nil
	}
	if mode != "token" {
		return price, errors.New("Unsupported upstream billing mode")
	}
	rates, err := sub2APIModelRates(row, source)
	if err != nil {
		return price, err
	}
	price.OffPeak = rates
	if len(intervals.Array()) == 0 {
		return price, nil
	}
	if len(intervals.Array()) > 32 {
		return price, errors.New("Too many upstream price tiers")
	}
	var previous int64
	for i, interval := range intervals.Array() {
		min := interval.Get("min_tokens")
		max := interval.Get("max_tokens")
		if min.Float() != math.Trunc(min.Float()) || min.Int() < 0 || i == 0 && min.Int() != 0 || i > 0 && min.Int() != previous && min.Int() != previous+1 {
			return price, errors.New("Invalid Sub2API context tiers")
		}
		limit := int64(0)
		if max.Exists() && max.Type != gjson.Null {
			if max.Float() != math.Trunc(max.Float()) || max.Int() <= previous || max.Int() > 1e9 {
				return price, errors.New("Invalid Sub2API context tiers")
			}
			limit = max.Int()
		}
		if limit == 0 && i != len(intervals.Array())-1 || limit != 0 && i == len(intervals.Array())-1 {
			return price, errors.New("Invalid Sub2API context tiers")
		}
		// Relative interval prices are resolved against this model's own channel
		// base rates, never the official reference catalog.
		resolved := rates
		values := []struct {
			field, multiplier string
			target            *float64
			base              float64
		}{
			{"input_price", "input_multiplier", &resolved.Input, rates.Input}, {"output_price", "output_multiplier", &resolved.Output, rates.Output},
			{"cache_read_price", "cache_read_multiplier", &resolved.CacheRead, rates.CacheRead}, {"cache_write_price", "cache_write_multiplier", &resolved.CacheWrite, rates.CacheWrite}, {"cache_write_1h_price", "cache_write_multiplier", &resolved.CacheWrite1h, rates.CacheWrite1h},
		}
		for _, v := range values {
			raw := interval.Get(v.field)
			if raw.Exists() && raw.Type != gjson.Null {
				n, ok := mappedNumber(interval, v.field)
				if !ok {
					return price, errors.New("Invalid Sub2API context tiers")
				}
				*v.target = n * (1e6 / source.UnitTokens * source.InputScale)
			} else if m := interval.Get(v.multiplier); m.Exists() && m.Type != gjson.Null {
				n, ok := mappedNumber(interval, v.multiplier)
				if !ok {
					return price, errors.New("Invalid Sub2API context tiers")
				}
				*v.target = v.base * n
			}
		}
		if value := interval.Get("cache_write_1h_price"); !value.Exists() || value.Type == gjson.Null {
			if value := interval.Get("cache_write_price"); value.Exists() && value.Type != gjson.Null {
				resolved.CacheWrite1h = resolved.CacheWrite
			}
		}
		if !resolved.Valid() {
			return price, errors.New("Invalid Sub2API context tiers")
		}
		label := interval.Get("tier_label").String()
		if label == "" {
			label = "context"
		}
		price.Tiers = append(price.Tiers, channelcost.Tier{Label: label, MaxContext: limit, Rates: resolved})
		previous = limit
	}
	price.OffPeak = price.Tiers[0].Rates
	return price, nil
}

func sub2APIModelRates(row gjson.Result, source channelcost.Source) (channelcost.Rates, error) {
	input, a := mappedNumber(row, source.Fields.Input)
	output, b := mappedNumber(row, source.Fields.Output)
	if !a || !b {
		return channelcost.Rates{}, errors.New("Mapped input or output price is missing or invalid")
	}
	scale := 1e6 / source.UnitTokens * source.InputScale
	rates := channelcost.Rates{Input: input * scale, Output: output * scale, CacheRead: input * scale, CacheWrite: input * scale, CacheWrite1h: input * scale}
	for _, v := range []struct {
		path   string
		target *float64
	}{{source.Fields.CacheRead, &rates.CacheRead}, {source.Fields.CacheWrite, &rates.CacheWrite}, {source.Fields.CacheWrite1h, &rates.CacheWrite1h}} {
		raw := row.Get(v.path)
		if v.path == "" || !raw.Exists() || raw.Type == gjson.Null {
			continue
		}
		n, ok := mappedNumber(row, v.path)
		if !ok {
			return rates, errors.New("Mapped input or output price is missing or invalid")
		}
		*v.target = n * scale
	}
	if value := row.Get(source.Fields.CacheWrite1h); source.Fields.CacheWrite1h == "" || !value.Exists() || value.Type == gjson.Null {
		rates.CacheWrite1h = rates.CacheWrite
	}
	if !rates.Valid() {
		return rates, errors.New("Mapped input or output price is missing or invalid")
	}
	return rates, nil
}
