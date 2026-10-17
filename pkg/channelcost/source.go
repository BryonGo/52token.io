package channelcost

import (
	"errors"
	"net/url"
	"regexp"
	"strings"
)

// Source is an administrator-maintained public GET pricing contract.
// Field paths use dot notation (for example data.models, pricing.input).
type Source struct {
	LocalGroupBilling    bool                      `json:"local_group_billing,omitempty"`
	ModelRules           map[string]ModelPriceRule `json:"model_rules,omitempty"`
	AdvancedPricing      bool                      `json:"advanced_pricing,omitempty"`
	ID                   string                    `json:"id"`
	Name                 string                    `json:"name"`
	Enabled              bool                      `json:"enabled"`
	Format               string                    `json:"format"`
	URL                  string                    `json:"url"`
	ChannelOrigin        string                    `json:"channel_origin"`
	StatusURL            string                    `json:"status_url,omitempty"`
	FilterField          string                    `json:"filter_field,omitempty"`
	FilterValue          string                    `json:"filter_value,omitempty"`
	FilterExclude        bool                      `json:"filter_exclude,omitempty"`
	AuthUserID           int                       `json:"auth_user_id,omitempty"`
	AuthConfigured       bool                      `json:"auth_configured,omitempty"`
	PriceCurrency        string                    `json:"price_currency,omitempty"`
	DefaultCreditsPerCNY float64                   `json:"default_credits_per_cny,omitempty"`
	UnitTokens           float64                   `json:"unit_tokens"`
	InputScale           float64                   `json:"input_scale"`
	OutputRatio          bool                      `json:"output_ratio"`
	CacheRatio           bool                      `json:"cache_ratio"`
	Fields               SourceFields              `json:"fields"`
	Reference            *ReferencePricing         `json:"reference,omitempty"`
}

// ReferencePricing is an explicit administrator-confirmed price agreement.
// It never enables implicit model-name matching or a fallback for other models.
type ReferencePricing struct {
	GroupRatio        float64           `json:"group_ratio"`
	Models            map[string]string `json:"models"`
	Schedule          string            `json:"schedule"`
	LocalGroupBilling bool              `json:"local_group_billing"`
}
type SourceFields struct {
	Description  string `json:"description,omitempty"`
	Vendor       string `json:"vendor,omitempty"`
	VendorID     string `json:"vendor_id,omitempty"`
	Icon         string `json:"icon,omitempty"`
	Tags         string `json:"tags,omitempty"`
	Endpoints    string `json:"endpoints,omitempty"`
	Vendors      string `json:"vendors,omitempty"`
	VendorKey    string `json:"vendor_key,omitempty"`
	VendorName   string `json:"vendor_name,omitempty"`
	VendorIcon   string `json:"vendor_icon,omitempty"`
	RequestPrice string `json:"request_price,omitempty"`
	UsageSchema  string `json:"usage_schema,omitempty"`
	RequestUnit  string `json:"request_unit,omitempty"`
	RequestUnits string `json:"request_units,omitempty"`
	QuotaType    string `json:"quota_type,omitempty"`
	Items        string `json:"items"`
	Model        string `json:"model"`
	Input        string `json:"input"`
	Output       string `json:"output"`
	CacheRead    string `json:"cache_read,omitempty"`
	CacheWrite   string `json:"cache_write,omitempty"`
	CacheWrite1h string `json:"cache_write_1h,omitempty"`
	BillingMode  string `json:"billing_mode,omitempty"`
	Expression   string `json:"expression,omitempty"`
	Groups       string `json:"groups,omitempty"`
	GroupRatios  string `json:"group_ratios,omitempty"`
	QuotaUnit    string `json:"quota_unit,omitempty"`
	Success      string `json:"success,omitempty"`
}

var SourceID = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,63}$`)
var fieldPath = regexp.MustCompile(`^[A-Za-z0-9_-]+(\.[A-Za-z0-9_-]+)*$`)

func (s Source) UsesLocalGroupBilling() bool {
	return s.LocalGroupBilling || s.Reference != nil && s.Reference.LocalGroupBilling
}

func (s Source) Validate() error {
	if len(s.ModelRules) > 2000 || len(s.ModelRules) > 0 && !s.AdvancedPricing {
		return errors.New("Invalid documented model price rules")
	}
	for name, rule := range s.ModelRules {
		if name == "" || len(name) > 256 || strings.TrimSpace(name) != name {
			return errors.New("Invalid documented model price rules")
		}
		if _, err := rule.Resolve(Price{RequestPrice: new(1.0), OffPeak: Rates{Input: 1, Output: 1}}); err != nil {
			return errors.New("Invalid documented model price rules")
		}
	}
	if !SourceID.MatchString(s.ID) || len(s.Name) == 0 || len(s.Name) > 120 {
		return errors.New("Invalid price source name or ID")
	}
	if s.FilterField != "" && (s.Format != "json" && s.Format != "newapi" || !fieldPath.MatchString(s.FilterField) || len(s.FilterField) > 160 || len(s.FilterValue) > 128) {
		return errors.New("Invalid model filter")
	}
	if s.PriceCurrency != "" && s.PriceCurrency != "CNY" && s.PriceCurrency != "USD" && s.PriceCurrency != "CREDIT" {
		return errors.New("Invalid upstream price currency")
	}
	if s.Format == "deepseek_html" && s.PriceCurrency == "CREDIT" {
		return errors.New("Invalid upstream price currency")
	}
	if s.Format != "json" && s.Format != "newapi" && s.Format != "sub2api" && s.Format != "deepseek_html" {
		return errors.New("Unsupported price response format")
	}
	if s.Format == "sub2api" && (s.OutputRatio || s.CacheRatio) {
		return errors.New("Sub2API price fields must contain unit prices")
	}
	if ref := s.Reference; ref != nil {
		if s.Format != "deepseek_html" || !Positive(ref.GroupRatio) || ref.GroupRatio > 1e6 || len(ref.Models) == 0 || len(ref.Models) > 20000 || ref.Schedule != "deepseek" && ref.Schedule != "off_peak" {
			return errors.New("Invalid confirmed reference pricing")
		}
		for name, target := range ref.Models {
			if strings.TrimSpace(name) != name || strings.TrimSpace(target) != target || name == "" || target == "" || len(name) > 256 || len(target) > 256 || strings.ContainsAny(name+target, "\r\n\x00") {
				return errors.New("Invalid confirmed reference model mapping")
			}
		}
	}
	endpoints := []string{s.URL, s.ChannelOrigin}
	if s.StatusURL != "" {
		endpoints = append(endpoints, s.StatusURL)
	}
	for _, endpoint := range endpoints {
		u, err := url.Parse(endpoint)
		if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || u.User != nil || u.Fragment != "" || len(endpoint) > 2048 {
			return errors.New("Invalid price source URL")
		}
	}
	origin, _ := url.Parse(s.ChannelOrigin)
	if origin == nil || origin.Host == "" || origin.RawQuery != "" || (origin.Path != "" && origin.Path != "/") {
		return errors.New("Channel origin must contain only scheme and host")
	}
	if s.DefaultCreditsPerCNY != 0 && (!Positive(s.DefaultCreditsPerCNY) || s.DefaultCreditsPerCNY < 0.000001 || s.DefaultCreditsPerCNY > 1e6) {
		return errors.New("Invalid channel recharge ratio")
	}
	if !Positive(s.UnitTokens) || s.UnitTokens > 1e9 || !Positive(s.InputScale) || s.InputScale > 1e9 {
		return errors.New("Invalid price source units")
	}
	if s.Format != "deepseek_html" && (s.Fields.Model == "" || s.Fields.Expression == "" && s.Fields.RequestPrice == "" && (s.Fields.Input == "" || s.Fields.Output == "")) {
		return errors.New("Model and input/output or expression mappings are required")
	}
	for _, path := range []string{s.Fields.Description, s.Fields.Vendor, s.Fields.VendorID, s.Fields.Icon, s.Fields.Tags, s.Fields.Endpoints, s.Fields.Vendors, s.Fields.VendorKey, s.Fields.VendorName, s.Fields.VendorIcon, s.Fields.RequestPrice, s.Fields.UsageSchema, s.Fields.RequestUnit, s.Fields.RequestUnits, s.Fields.QuotaType, s.Fields.Items, s.Fields.Model, s.Fields.Input, s.Fields.Output, s.Fields.CacheRead, s.Fields.CacheWrite, s.Fields.CacheWrite1h, s.Fields.BillingMode, s.Fields.Expression, s.Fields.Groups, s.Fields.GroupRatios, s.Fields.QuotaUnit, s.Fields.Success} {
		if len(path) > 160 || path != "" && !fieldPath.MatchString(path) {
			return errors.New("Invalid price field path")
		}
	}
	return nil
}
func DefaultSources() map[string]Source {
	return map[string]Source{
		"deepseek": {ID: "deepseek", Name: "DeepSeek 官方", Enabled: true, Format: "deepseek_html", URL: DeepSeekURL, ChannelOrigin: "https://api.deepseek.com", UnitTokens: 1e6, InputScale: 1},
		"happyapi": {ID: "happyapi", Name: "HappyAPI", Enabled: true, Format: "newapi", URL: "https://happyapi.org/api/pricing", ChannelOrigin: "https://happyapi.org", StatusURL: "https://happyapi.org/api/status", UnitTokens: 1e6, InputScale: 1, OutputRatio: true, CacheRatio: true, Fields: SourceFields{RequestPrice: "model_price", QuotaType: "quota_type", Items: "data", Model: "model_name", Input: "model_ratio", Output: "completion_ratio", CacheRead: "cache_ratio", CacheWrite: "create_cache_ratio", BillingMode: "billing_mode", Expression: "billing_expr", Groups: "enable_groups", GroupRatios: "group_ratio", QuotaUnit: "data.quota_per_unit", Success: "success"}},
	}
}
