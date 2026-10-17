package model

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/pkg/channelcost"
	"github.com/QuantumNous/new-api/pkg/jsplugin"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	"gorm.io/gorm"
)

// Catalog identities describe offerings, not credentials. The channel prefix
// permits a point lookup without scanning every group or keeping a second ID DB.
type PublicCatalogModel struct {
	ID                 string                  `json:"id"`
	Object             string                  `json:"object"`
	ModelUID           string                  `json:"model_uid"`
	PricingID          string                  `json:"pricing_id"`
	PricingURL         string                  `json:"pricing_url"`
	GroupID            string                  `json:"group_id"`
	GroupName          string                  `json:"group_name"`
	GroupMultiplier    float64                 `json:"group_multiplier"`
	ChannelID          int                     `json:"channel_id"`
	ChannelName        string                  `json:"channel_name"`
	ModelFamily        string                  `json:"model_family"`
	SupportedEndpoints []constant.EndpointType `json:"supported_endpoint_types"`
}

type PublicCatalogGroup struct {
	GroupID         string  `json:"group_id"`
	GroupName       string  `json:"group_name"`
	GroupMultiplier float64 `json:"group_multiplier"`
	ModelCount      int     `json:"model_count"`
}

type PublicModelPrice struct {
	CostExpression string                     `json:"cost_expression,omitempty"`
	UsageSchema    map[string]json.RawMessage `json:"usage_schema,omitempty"`
	ChargeUnit     *channelcost.ChargeUnit    `json:"charge_unit,omitempty"`
	PublicCatalogModel
	Currency               string             `json:"currency"`
	Available              bool               `json:"available"`
	Reason                 string             `json:"unavailable_reason,omitempty"`
	Source                 string             `json:"price_source,omitempty"`
	BillingType            string             `json:"billing_type,omitempty"`
	UnitTokens             int                `json:"unit_tokens,omitempty"`
	Rates                  *channelcost.Rates `json:"rates,omitempty"`
	CostRates              *channelcost.Rates `json:"cost_rates,omitempty"`
	CostRequestPrice       *float64           `json:"cost_request_price,omitempty"`
	RequestPrice           *float64           `json:"request_price,omitempty"`
	Expression             string             `json:"expression,omitempty"`
	Tiers                  []channelcost.Tier `json:"tiers,omitempty"`
	PeakRates              *channelcost.Rates `json:"peak_rates,omitempty"`
	OffPeakRates           *channelcost.Rates `json:"off_peak_rates,omitempty"`
	Schedule               string             `json:"schedule,omitempty"`
	Timezone               string             `json:"timezone,omitempty"`
	CurrentTier            string             `json:"current_tier,omitempty"`
	AppliedGroupMultiplier float64            `json:"applied_group_multiplier"`
	SyncedAt               int64              `json:"synced_at,omitempty"`
	QuotedAt               int64              `json:"quoted_at"`
	ValidUntil             int64              `json:"valid_until,omitempty"`
	Version                string             `json:"price_version,omitempty"`
}

func publicCatalogModel(channel *Channel, group, name string, ratio float64, profiles map[string]GroupProfile) PublicCatalogModel {
	groupName := profiles[group].Name
	if groupName == "" {
		groupName = group
	}
	identity, _ := common.Marshal([]string{group, name})
	pricingID := fmt.Sprintf("prc_%d_%x", channel.Id, sha256.Sum256(identity))
	endpoints := common.GetEndpointTypesByChannelType(channel.Type, name)
	if channel.Type == constant.ChannelTypeAdvancedCustom {
		if custom := channel.GetOtherSettings().AdvancedCustom; custom != nil {
			endpoints = custom.SupportedEndpointTypesForModel(name)
		}
	}
	return PublicCatalogModel{
		ID: name, Object: "model", ModelUID: fmt.Sprintf("mdl_%x", sha256.Sum256([]byte(name))),
		PricingID: pricingID, PricingURL: "/v1/model-prices/" + pricingID,
		GroupID: group, GroupName: groupName, GroupMultiplier: ratio,
		ChannelID: channel.Id, ChannelName: channel.Name, ModelFamily: projectModelFamily(name), SupportedEndpoints: endpoints,
	}
}

func GetPublicModelCatalog(groupID string) ([]PublicCatalogModel, error) {
	var channels []*Channel
	// Public catalog never selects model-call credentials or account credentials.
	if err := DB.Select("id", "name", "type", "status", "models", "group", "settings").Where("status = ?", common.ChannelStatusEnabled).Order("id").Find(&channels).Error; err != nil {
		return nil, err
	}
	return buildPublicModelCatalog(channels, ratio_setting.GetGroupRatioCopy(), GetGroupProfiles(), groupID), nil
}

func GetPublicModelGroups() ([]PublicCatalogGroup, error) {
	rows, err := GetPublicModelCatalog("")
	if err != nil {
		return nil, err
	}
	groups := []PublicCatalogGroup{}
	indices := map[string]int{}
	for _, row := range rows {
		index, exists := indices[row.GroupID]
		if !exists {
			index = len(groups)
			indices[row.GroupID] = index
			groups = append(groups, PublicCatalogGroup{
				GroupID: row.GroupID, GroupName: row.GroupName, GroupMultiplier: row.GroupMultiplier,
			})
		}
		groups[index].ModelCount++
	}
	slices.SortFunc(groups, func(a, b PublicCatalogGroup) int { return strings.Compare(a.GroupID, b.GroupID) })
	return groups, nil
}

func buildPublicModelCatalog(channels []*Channel, ratios map[string]float64, profiles map[string]GroupProfile, groupID string) []PublicCatalogModel {
	rows := []PublicCatalogModel{}
	seen := map[string]bool{}
	for _, channel := range channels {
		if channel.Status != common.ChannelStatusEnabled {
			continue
		}
		for group := range strings.SplitSeq(channel.Group, ",") {
			if groupID != "" && group != groupID {
				continue
			}
			ratio, ok := ratios[group]
			if !ok || ratio < 0 || math.IsNaN(ratio) || math.IsInf(ratio, 0) {
				continue
			}
			for name := range strings.SplitSeq(channel.Models, ",") {
				name = strings.TrimSpace(name)
				if name == "" {
					continue
				}
				row := publicCatalogModel(channel, group, name, ratio, profiles)
				if seen[row.PricingID] {
					continue
				}
				seen[row.PricingID] = true
				rows = append(rows, row)
			}
		}
	}
	slices.SortFunc(rows, func(a, b PublicCatalogModel) int { return strings.Compare(a.PricingID, b.PricingID) })
	return rows
}

// PublicModelPriceByID always loads current channel configuration and resolves
// the existing billing snapshot at request time (including peak boundaries).
func PublicModelPriceByID(id string, now time.Time) (*PublicModelPrice, error) {
	prefix, digest, ok := strings.Cut(strings.TrimPrefix(id, "prc_"), "_")
	channelID, err := strconv.Atoi(prefix)
	if !strings.HasPrefix(id, "prc_") || !ok || err != nil || channelID <= 0 || len(digest) != 64 {
		return nil, nil
	}
	channel, err := GetChannelById(channelID, true)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	rows := buildPublicModelCatalog([]*Channel{channel}, ratio_setting.GetGroupRatioCopy(), GetGroupProfiles(), "")
	for _, row := range rows {
		if row.PricingID == id {
			return resolvePublicModelPrice(row, channel, now), nil
		}
	}
	return nil, nil
}

func resolvePublicModelPrice(row PublicCatalogModel, channel *Channel, now time.Time) *PublicModelPrice {
	result := &PublicModelPrice{PublicCatalogModel: row, Currency: "CNY", QuotedAt: now.Unix(), AppliedGroupMultiplier: 1}
	cfg := channel.GetOtherSettings().CostBilling
	if cfg == nil || !cfg.Enabled {
		return publicConfiguredModelPrice(result, channel)
	}
	result.Source = "upstream"
	source, err := GetChannelPriceSource(cfg.Source)
	if err != nil {
		result.Reason = "price_source_unavailable"
		return result
	}
	if source.Reference != nil {
		result.Source = "reference"
	}
	if !channelcost.Positive(operation_setting.Price) || math.Abs(operation_setting.Price-operation_setting.USDExchangeRate) > 1e-9 {
		result.Reason = "wallet_conversion_mismatch"
		return result
	}
	if channel.ParamOverride != nil && strings.TrimSpace(*channel.ParamOverride) != "" && strings.TrimSpace(*channel.ParamOverride) != "{}" {
		result.Reason = "unsupported_channel_configuration"
		return result
	}
	snapshot := GetChannelCostSnapshot(channel.Id)
	receipt, _, err := snapshot.Resolve(cfg, row.ID, ChannelCostFingerprint(channel), now, operation_setting.USDExchangeRate)
	if err != nil {
		result.Reason = "channel_price_unavailable"
		return result
	}
	factor := 1.0
	if source.UsesLocalGroupBilling() {
		if row.GroupMultiplier > receipt.GroupRatio+1e-12 {
			result.Reason = "group_multiplier_exceeds_cost"
			return result
		}
		factor = row.GroupMultiplier / receipt.GroupRatio
		result.AppliedGroupMultiplier = row.GroupMultiplier
	}
	result.SyncedAt = receipt.SyncedAt
	result.CurrentTier = receipt.Tier
	result.ValidUntil = time.Unix(receipt.SyncedAt, 0).Add(channelcost.MaxAge).Unix()
	result.ChargeUnit = receipt.ChargeUnit
	if receipt.Dynamic {
		result.CostExpression = receipt.Expression
		result.Expression = receipt.Expression
		if factor != 1 {
			scaled, e := snapshot.Prices[row.ID].Dynamic.Scale(snapshot.GroupRatio / cfg.CreditsPerCNY * factor)
			if e != nil {
				result.Reason = "channel_price_unavailable"
				return result
			}
			result.Expression = scaled
		}
		result.BillingType = "expression"
		if receipt.TaskUsageBilling {
			result.BillingType = "task_usage"
			result.UsageSchema = snapshot.Prices[row.ID].Dynamic.UsageSchema
		}
		// Clock and request-dependent contracts cannot be replaced by one flat
		// token or request price. Keep the complete expression and a short lease.
		result.ValidUntil = min(result.ValidUntil, now.Truncate(time.Minute).Add(time.Minute).Unix())
		result.Available = true
		result.Version = publicPriceVersion(result)
		return result
	}
	if receipt.CNYRequestPrice != nil {
		result.CostRequestPrice = receipt.CNYRequestPrice
		value := *receipt.CNYRequestPrice * factor
		result.BillingType = "request"
		result.RequestPrice = &value
		result.Expression, _ = channelcost.RequestExpression(value)
	} else {
		result.CostRates = &receipt.CNY
		rates := receipt.CNY.Scale(factor)
		result.BillingType = "tokens"
		result.UnitTokens = 1000000
		result.Rates = &rates
		result.Expression = rates.Expression()
		if len(receipt.Tiers) > 0 {
			result.Tiers = make([]channelcost.Tier, len(receipt.Tiers))
			for i, tier := range receipt.Tiers {
				result.Tiers[i] = tier
				result.Tiers[i].Rates = tier.Rates.Scale(factor)
			}
			result.Expression = channelcost.TierExpression(receipt.Tiers, factor)
		}
		price := snapshot.Prices[row.ID]
		if price.Peak != nil {
			conversion := snapshot.GroupRatio / cfg.CreditsPerCNY * factor
			peak, offPeak := price.Peak.Scale(conversion), price.OffPeak.Scale(conversion)
			result.PeakRates = &peak
			result.OffPeakRates = &offPeak
			result.Timezone = "Asia/Shanghai"
			result.Schedule = "deepseek_2026"
			peakNow, _ := channelcost.DeepSeekPeak(now)
			for next := now.Truncate(time.Hour).Add(time.Hour); next.Unix() < result.ValidUntil; next = next.Add(time.Hour) {
				peakNext, e := channelcost.DeepSeekPeak(next)
				if e != nil || peakNext != peakNow {
					result.ValidUntil = next.Unix()
					break
				}
			}
		}
	}
	result.Available = true
	result.Version = publicPriceVersion(result)
	return result
}

// Legacy/manual pricing is published as configured billing, never presented as
// an independently synchronized upstream purchase cost.
func publicConfiguredModelPrice(result *PublicModelPrice, channel *Channel) *PublicModelPrice {
	result.Source = "configured"
	result.AppliedGroupMultiplier = result.GroupMultiplier
	for _, pricing := range GetPricing() {
		if pricing.ModelName != result.ID {
			continue
		}
		expression := pricing.BillingExpr
		if len(pricing.BillingPluginVariants) > 0 {
			plugin, ok := jsplugin.DefaultRegistry.GetByChannelType(channel.Type)
			if !ok {
				result.Reason = "plugin_pricing_unavailable"
				return result
			}
			expression = ""
			for _, variant := range pricing.BillingPluginVariants {
				if variant.PluginKey == plugin.Meta.Key {
					expression = variant.BillingExpr
					break
				}
			}
			if expression == "" {
				result.Reason = "plugin_pricing_unavailable"
				return result
			}
		}
		factor := operation_setting.USDExchangeRate * result.GroupMultiplier
		if math.IsInf(factor, 0) || math.IsNaN(factor) {
			result.Reason = "invalid_configured_price"
			return result
		}
		if !channelcost.Positive(operation_setting.USDExchangeRate) {
			result.Reason = "wallet_conversion_mismatch"
			return result
		}
		if expression != "" {
			result.BillingType = "expression"
			result.Expression = fmt.Sprintf("(%s) * %.17g", strings.TrimPrefix(expression, "v1:"), factor)
		} else if pricing.QuotaType == 1 {
			price := pricing.ModelPrice * factor
			result.BillingType = "request"
			result.RequestPrice = &price
			var err error
			result.Expression, err = channelcost.RequestExpression(price)
			if err != nil {
				result.RequestPrice = nil
				result.Reason = "invalid_configured_price"
				return result
			}
		} else {
			if _, configured, _ := ratio_setting.GetModelRatio(result.ID); !configured {
				result.Reason = "model_price_unavailable"
				return result
			}
			if pricing.ImageRatio != nil || pricing.AudioRatio != nil || pricing.AudioCompletionRatio != nil {
				result.Reason = "configured_modality_price_unavailable"
				return result
			}
			input := pricing.ModelRatio * 2 * factor
			rates := channelcost.Rates{Input: input, Output: input * pricing.CompletionRatio, CacheRead: input, CacheWrite: input, CacheWrite1h: input}
			if pricing.CacheRatio != nil {
				rates.CacheRead = input * *pricing.CacheRatio
			}
			if pricing.CreateCacheRatio != nil {
				rates.CacheWrite = input * *pricing.CreateCacheRatio
				rates.CacheWrite1h = rates.CacheWrite
			}
			if !rates.Valid() {
				result.Reason = "invalid_configured_price"
				return result
			}
			result.BillingType = "tokens"
			result.UnitTokens = 1000000
			result.Rates = &rates
			result.Expression = rates.Expression()
		}
		result.Available = true
		result.Version = publicPriceVersion(result)
		return result
	}
	result.Reason = "model_price_unavailable"
	return result
}
func publicPriceVersion(price *PublicModelPrice) string {
	copy := *price
	// Response time is not a price revision. Labels are not price revisions either.
	copy.QuotedAt = 0
	copy.GroupName = ""
	copy.ChannelName = ""
	copy.Version = ""
	raw, _ := common.Marshal(copy)
	return fmt.Sprintf("%x", sha256.Sum256(raw))
}
