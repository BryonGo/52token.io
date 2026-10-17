// Package channelcost keeps upstream credit prices separate from wallet units.
package channelcost

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/QuantumNous/new-api/relaykit/dto"
)

const DeepSeekURL = "https://api-docs.deepseek.com/zh-cn/quick_start/pricing/"
const MaxAge = 24 * time.Hour

// Rates are upstream balance units per million tokens. Cache input is exclusive
// of ordinary input; it must never be added to an inclusive prompt count twice.
type Rates struct {
	Input        float64 `json:"input"`
	Output       float64 `json:"output"`
	CacheRead    float64 `json:"cache_read"`
	CacheWrite   float64 `json:"cache_write"`
	CacheWrite1h float64 `json:"cache_write_1h"`
}
type ChargeUnit struct {
	Unit  string  `json:"unit"`
	Units float64 `json:"units"`
	Price float64 `json:"price"`
}

type Price struct {
	RuleSource    string      `json:"rule_source,omitempty"`
	Dynamic       *Expression `json:"dynamic,omitempty"`
	ChargeUnit    *ChargeUnit `json:"charge_unit,omitempty"`
	RequestPrice  *float64    `json:"request_price,omitempty"`
	UpstreamModel string      `json:"upstream_model"`
	OffPeak       Rates       `json:"off_peak"`
	Peak          *Rates      `json:"peak,omitempty"`
	Tiers         []Tier      `json:"tiers,omitempty"`
}
type Snapshot struct {
	Fingerprint string            `json:"fingerprint"`
	SourceURL   string            `json:"source_url"`
	SyncedAt    int64             `json:"synced_at"`
	GroupRatio  float64           `json:"group_ratio"`
	Prices      map[string]Price  `json:"prices"`
	Errors      map[string]string `json:"errors,omitempty"`
}
type Receipt struct {
	RuleSource           string                     `json:"rule_source,omitempty"`
	UsageSchema          map[string]json.RawMessage `json:"usage_schema,omitempty"`
	Dynamic              bool                       `json:"dynamic,omitempty"`
	TaskUsageBilling     bool                       `json:"task_usage_billing,omitempty"`
	ChargeUnit           *ChargeUnit                `json:"charge_unit,omitempty"`
	UpstreamRequestPrice *float64                   `json:"upstream_request_price,omitempty"`
	CNYRequestPrice      *float64                   `json:"cny_request_price,omitempty"`
	ChannelID            int                        `json:"channel_id"`
	Model                string                     `json:"model"`
	SourceURL            string                     `json:"source_url"`
	SyncedAt             int64                      `json:"synced_at"`
	CreditsPerCNY        float64                    `json:"credits_per_cny"`
	UpstreamGroup        string                     `json:"upstream_group"`
	GroupRatio           float64                    `json:"upstream_group_ratio"`
	CNYPerWalletUnit     float64                    `json:"cny_per_wallet_unit"`
	Tier                 string                     `json:"tier"`
	Upstream             Rates                      `json:"upstream"`
	CNY                  Rates                      `json:"cny"`
	Tiers                []Tier                     `json:"tiers,omitempty"`
	Expression           string                     `json:"expression,omitempty"`
}

func Positive(v float64) bool { return v > 0 && !math.IsNaN(v) && !math.IsInf(v, 0) }
func (r Rates) Valid() bool {
	for _, v := range []float64{r.Input, r.Output, r.CacheRead, r.CacheWrite, r.CacheWrite1h} {
		if v < 0 || math.IsNaN(v) || math.IsInf(v, 0) {
			return false
		}
	}
	return true
}
func Validate(c *dto.ChannelCostSettings) error {
	if c == nil || !c.Enabled {
		return nil
	}
	if !Positive(c.CreditsPerCNY) || c.CreditsPerCNY < 0.000001 || c.CreditsPerCNY > 1000000 {
		return errors.New("Invalid channel recharge ratio")
	}
	if !SourceID.MatchString(c.Source) {
		return errors.New("Unsupported channel cost source")
	}
	if c.Source == "deepseek" && c.UpstreamGroup != "" {
		return errors.New("DeepSeek official does not use upstream groups")
	}
	if c.Source == "happyapi" && c.UpstreamGroup == "" || c.UpstreamGroup == "auto" {
		return errors.New("Select the upstream token group")
	}

	return nil
}
func (s *Snapshot) Resolve(c *dto.ChannelCostSettings, model string, fingerprint string, now time.Time, cnyPerUnit float64) (*Receipt, string, error) {
	if err := Validate(c); err != nil {
		return nil, "", err
	}
	if c == nil || !c.Enabled {
		return nil, "", errors.New("Channel cost billing is disabled")
	}
	if s == nil || s.Fingerprint != fingerprint || now.Unix() < s.SyncedAt || now.Sub(time.Unix(s.SyncedAt, 0)) > MaxAge {
		return nil, "", errors.New("Channel prices are missing, changed or expired; synchronize prices")
	}
	if !Positive(cnyPerUnit) || !Positive(s.GroupRatio) {
		return nil, "", errors.New("Invalid channel cost conversion")
	}
	price, ok := s.Prices[model]
	if !ok {
		if reason := s.Errors[model]; reason != "" {
			return nil, "", errors.New(reason)
		}
		return nil, "", errors.New("No upstream token price for this channel model")
	}
	factor := s.GroupRatio / c.CreditsPerCNY
	var meter *ChargeUnit
	if price.ChargeUnit != nil {
		copy := *price.ChargeUnit
		copy.Price *= factor
		meter = &copy
	}
	if price.Dynamic != nil {
		expression, err := price.Dynamic.Scale(factor)
		if err != nil {
			return nil, "", err
		}
		walletExpression, err := price.Dynamic.Scale(factor / cnyPerUnit)
		if err != nil {
			return nil, "", err
		}
		return &Receipt{RuleSource: price.RuleSource, Dynamic: true, UsageSchema: price.Dynamic.UsageSchema, TaskUsageBilling: price.Dynamic.Task, ChargeUnit: meter, Model: price.UpstreamModel, SourceURL: s.SourceURL, SyncedAt: s.SyncedAt, CreditsPerCNY: c.CreditsPerCNY, UpstreamGroup: c.UpstreamGroup, GroupRatio: s.GroupRatio, CNYPerWalletUnit: cnyPerUnit, Tier: "expression", Expression: expression}, walletExpression, nil
	}
	if price.RequestPrice != nil {
		amount := *price.RequestPrice * s.GroupRatio / c.CreditsPerCNY
		expression, err := RequestExpression(amount)
		if err != nil {
			return nil, "", err
		}
		walletExpression, err := RequestExpression(amount / cnyPerUnit)
		if err != nil {
			return nil, "", err
		}
		return &Receipt{Model: price.UpstreamModel, SourceURL: s.SourceURL, SyncedAt: s.SyncedAt, CreditsPerCNY: c.CreditsPerCNY, UpstreamGroup: c.UpstreamGroup, GroupRatio: s.GroupRatio, CNYPerWalletUnit: cnyPerUnit, Tier: "request", ChargeUnit: meter, UpstreamRequestPrice: price.RequestPrice, CNYRequestPrice: &amount, Expression: expression}, walletExpression, nil
	}
	rates, tier := price.OffPeak, "standard"
	if price.Peak != nil {
		peak, err := DeepSeekPeak(now)
		if err != nil {
			return nil, "", err
		}
		tier = "off_peak"
		if peak {
			rates, tier = *price.Peak, "peak"
		}
	}
	cny := Rates{rates.Input * factor, rates.Output * factor, rates.CacheRead * factor, rates.CacheWrite * factor, rates.CacheWrite1h * factor}
	if !rates.Valid() || !cny.Valid() {
		return nil, "", errors.New("Invalid upstream token prices")
	}
	// Keep existing wallet/quota semantics. CNY display applies this same
	// conversion once; the upstream's '$' symbol never supplies an FX rate.
	expr := fmt.Sprintf("p * %.17g + c * %.17g + cr * %.17g + cc * %.17g + cc1h * %.17g", cny.Input/cnyPerUnit, cny.Output/cnyPerUnit, cny.CacheRead/cnyPerUnit, cny.CacheWrite/cnyPerUnit, cny.CacheWrite1h/cnyPerUnit)
	receipt := &Receipt{Model: price.UpstreamModel, SourceURL: s.SourceURL, SyncedAt: s.SyncedAt, CreditsPerCNY: c.CreditsPerCNY, UpstreamGroup: c.UpstreamGroup, GroupRatio: s.GroupRatio, CNYPerWalletUnit: cnyPerUnit, Tier: tier, Upstream: rates, CNY: cny}
	if len(price.Tiers) > 0 {
		for _, tier := range price.Tiers {
			converted := tier
			converted.Rates = tier.Rates.Scale(factor)
			if !converted.Rates.Valid() {
				return nil, "", errors.New("Invalid upstream token prices")
			}
			receipt.Tiers = append(receipt.Tiers, converted)
		}
		receipt.Expression = TierExpression(price.Tiers, factor)
		expr = TierExpression(price.Tiers, factor/cnyPerUnit)
		receipt.Tier = "context"
	}
	return receipt, expr, nil
}

// DeepSeek's published rule excludes weekends even on make-up working days.
// Holiday ranges: State Council notice 国办发明电〔2025〕7号:
// https://www.gov.cn/zhengce/content/202511/content_7047090.htm
// Fail closed outside the verified calendar, rather than overcharge holidays.
func DeepSeekPeak(now time.Time) (bool, error) {
	t := now.In(time.FixedZone("Asia/Shanghai", 8*3600))
	if t.Year() != 2026 {
		return false, errors.New("DeepSeek holiday calendar needs updating")
	}
	day := t.Format("01-02")
	for _, r := range [][2]string{{"01-01", "01-03"}, {"02-15", "02-23"}, {"04-04", "04-06"}, {"05-01", "05-05"}, {"06-19", "06-21"}, {"09-25", "09-27"}, {"10-01", "10-07"}} {
		if day >= r[0] && day <= r[1] {
			return false, nil
		}
	}
	weekday, h := t.Weekday(), t.Hour()
	return weekday >= time.Monday && weekday <= time.Friday && ((h >= 9 && h < 12) || (h >= 14 && h < 18)), nil
}
