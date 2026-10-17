package model

import (
	"cmp"
	"crypto/sha256"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/pkg/channelcost"

	"github.com/QuantumNous/new-api/setting/operation_setting"
	"gorm.io/gorm"
)

// SaveChannelHealthState changes routing without changing purchase prices or
// extending their freshness. A stale price snapshot is never made valid here.
func SaveChannelHealthState(channel *Channel, nextModels, nextInfo string) error {
	expectedFingerprint := ChannelCostFingerprint(channel)
	optionKey := fmt.Sprintf("channel_cost.%d", channel.Id)
	optionValue := ""
	err := DB.Transaction(func(tx *gorm.DB) error {
		var current Channel
		if err := lockForUpdate(tx).First(&current, channel.Id).Error; err != nil {
			return err
		}
		if current.Models != channel.Models || current.OtherInfo != channel.OtherInfo || ChannelCostFingerprint(&current) != expectedFingerprint {
			return errors.New("channel changed while checking model health")
		}
		next := current
		next.Models, next.OtherInfo = nextModels, nextInfo
		modelsChanged := current.Models != nextModels
		if err := tx.Model(&current).Select("models", "other_info").Updates(&next).Error; err != nil {
			return err
		}
		if modelsChanged {
			if err := next.UpdateAbilities(tx); err != nil {
				return err
			}
			var option Option
			if err := tx.Where(commonKeyCol+" = ?", optionKey).First(&option).Error; err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
				return err
			}
			var snapshot channelcost.Snapshot
			if option.Value != "" && common.UnmarshalJsonStr(option.Value, &snapshot) == nil && snapshot.Fingerprint == expectedFingerprint {
				snapshot.Fingerprint = ChannelCostFingerprint(&next)
				if ChannelCostFingerprint(channel) != expectedFingerprint {
					return errors.New("purchase settings changed while checking model health")
				}
				data, err := common.Marshal(snapshot)
				if err != nil {
					return err
				}
				option.Value = string(data)
				if err := tx.Save(&option).Error; err != nil {
					return err
				}
				optionValue = option.Value
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	channel.Models, channel.OtherInfo = nextModels, nextInfo
	if optionValue != "" {
		common.OptionMapRWMutex.Lock()
		common.OptionMap[optionKey] = optionValue
		common.OptionMapRWMutex.Unlock()
	}
	return nil
}

func ChannelCostFingerprint(c *Channel) string {
	// Include routing and credentials so a changed token cannot silently inherit
	// the previous token's group cost. Only the digest is persisted.
	settings := c.GetOtherSettings()
	identity := []any{c.Id, c.Type, c.BaseURL, c.Models, c.ModelMapping, c.Key, settings.CostBilling}
	if settings.AdvancedCustom != nil {
		identity = append(identity, settings.AdvancedCustom)
	}
	if cfg := settings.CostBilling; cfg != nil && cfg.Enabled {
		if source, err := GetChannelPriceSource(cfg.Source); err == nil {
			if token, _, err := GetPriceSourceCredential(source); err == nil && token != "" {
				identity = append(identity, fmt.Sprintf("%x", sha256.Sum256([]byte(token))))
			}
			current, _ := common.Marshal(source)
			original, _ := common.Marshal(channelcost.DefaultSources()[source.ID])
			if string(current) != string(original) {
				identity = append(identity, source)
			}
		} else {
			identity = append(identity, "source-unavailable")
		}
	}
	b, _ := common.Marshal(identity)
	return fmt.Sprintf("%x", sha256.Sum256(b))
}
func GetChannelCostSnapshot(id int) *channelcost.Snapshot {
	common.OptionMapRWMutex.RLock()
	value := common.OptionMap[fmt.Sprintf("channel_cost.%d", id)]
	common.OptionMapRWMutex.RUnlock()
	var snapshot channelcost.Snapshot
	if common.UnmarshalJsonStr(value, &snapshot) != nil {
		return nil
	}
	return &snapshot
}
func SaveChannelCostSnapshot(id int, snapshot *channelcost.Snapshot) error {
	value, err := common.Marshal(snapshot)
	if err != nil {
		return err
	}
	return UpdateOptionsBulk(map[string]string{fmt.Sprintf("channel_cost.%d", id): string(value)})
}

// ChannelCostPublicPrice contains only information needed for the public price
// catalog. The receipt with purchase configuration remains in admin logs.
type ChannelCostPublicPrice struct {
	LocalGroupBilling bool               `json:"local_group_billing,omitempty"`
	CNYRequestPrice   *float64           `json:"cny_request_price,omitempty"`
	CNY               *channelcost.Rates `json:"cny,omitempty"`
	Tier              string             `json:"tier,omitempty"`
	SyncedAt          int64              `json:"synced_at,omitempty"`
	Error             string             `json:"error,omitempty"`
	Expression        string             `json:"expression,omitempty"`
	Tiers             []channelcost.Tier `json:"tiers,omitempty"`
}

// GetChannelCostCatalog expands models with cost billing into channel variants.
// Non-cost channels sharing a name retain their own legacy pricing variant.
func GetChannelCostCatalog(base []Pricing, now time.Time) ([]Pricing, error) {
	var channels []*Channel
	if common.MemoryCacheEnabled {
		channelSyncLock.RLock()
		for _, ch := range channelsIDM {
			channels = append(channels, ch)
		}
		channelSyncLock.RUnlock()
	} else if err := DB.Find(&channels).Error; err != nil {
		return nil, err
	}
	slices.SortFunc(channels, func(a, b *Channel) int { return cmp.Compare(a.Id, b.Id) })
	return channelCostCatalog(base, channels, now), nil
}
func channelCostCatalog(base []Pricing, channels []*Channel, now time.Time) []Pricing {
	owners := map[string][]*Channel{}
	costModels := map[string]bool{}
	for _, ch := range channels {
		cfg := ch.GetOtherSettings().CostBilling
		for name := range strings.SplitSeq(ch.Models, ",") {
			name = strings.TrimSpace(name)
			owners[name] = append(owners[name], ch)
			if cfg != nil && cfg.Enabled {
				costModels[name] = true
			}
		}
	}
	result := make([]Pricing, 0, len(base))
	for _, item := range base {
		if !costModels[item.ModelName] {
			result = append(result, item)
			continue
		}
		for _, ch := range owners[item.ModelName] {
			row := item
			row.ChannelID = ch.Id
			row.ChannelName = ch.Name
			row.ChannelEnabled = ch.Status == common.ChannelStatusEnabled
			row.EnableGroup = strings.Split(ch.Group, ",")
			cfg := ch.GetOtherSettings().CostBilling
			if cfg != nil && cfg.Enabled {
				row.SupportedEndpointTypes = nil
				for _, endpoint := range item.SupportedEndpointTypes {
					switch endpoint {
					case constant.EndpointTypeOpenAI, constant.EndpointTypeOpenAIResponse, constant.EndpointTypeAnthropic, constant.EndpointTypeEmbeddings, constant.EndpointTypeImageGeneration, constant.EndpointTypeOpenAIVideo:
						row.SupportedEndpointTypes = append(row.SupportedEndpointTypes, endpoint)
					}
				}
				row.BillingMode = "ratio"
				row.BillingExpr = ""
				row.BillingPluginVariants = nil
				row.BillingUsageSchema = nil
				row.QuotaType = 0
				row.ModelPrice = 0
				row.ModelRatio = 0
				row.CompletionRatio = 0
				row.CacheRatio = nil
				row.CreateCacheRatio = nil
				row.ImageRatio = nil
				row.AudioRatio = nil
				row.AudioCompletionRatio = nil
				row.ChannelCost = &ChannelCostPublicPrice{}
				receipt, _, err := GetChannelCostSnapshot(ch.Id).Resolve(cfg, item.ModelName, ChannelCostFingerprint(ch), now, operation_setting.USDExchangeRate)
				if err != nil {
					row.ChannelCost.Error = err.Error()
				} else {
					source, _ := GetChannelPriceSource(cfg.Source)
					if source.UsesLocalGroupBilling() {
						row.ChannelCost.LocalGroupBilling = true
						// Publish the base price for normal local group pricing. The
						// actual purchase receipt remains separate in admin logs.
						receipt.CNY = receipt.Upstream.Scale(1 / cfg.CreditsPerCNY)
						receipt.Expression = "(" + receipt.Expression + ") / " + fmt.Sprintf("%.17g", receipt.GroupRatio)
						if receipt.CNYRequestPrice != nil {
							v := *receipt.CNYRequestPrice / receipt.GroupRatio
							receipt.CNYRequestPrice = &v
						}
						for i := range receipt.Tiers {
							receipt.Tiers[i].Rates = receipt.Tiers[i].Rates.Scale(1 / receipt.GroupRatio)
						}
					}
					row.ChannelCost.CNY = &receipt.CNY
					if receipt.Dynamic {
						row.ChannelCost.CNY = nil
						if dynamic := GetChannelCostSnapshot(ch.Id).Prices[item.ModelName].Dynamic; dynamic != nil && dynamic.Task {
							encoded, _ := common.Marshal(dynamic.UsageSchema)
							_ = common.Unmarshal(encoded, &row.BillingUsageSchema)
						}
					}
					if receipt.CNYRequestPrice != nil {
						row.ChannelCost.CNY = nil
						row.ChannelCost.CNYRequestPrice = receipt.CNYRequestPrice
						row.QuotaType = 1
						row.ModelPrice = *receipt.CNYRequestPrice / operation_setting.USDExchangeRate
					}
					row.ChannelCost.Tier = receipt.Tier
					row.ChannelCost.SyncedAt = receipt.SyncedAt
					row.ChannelCost.Expression = receipt.Expression
					row.ChannelCost.Tiers = receipt.Tiers
					if receipt.Expression != "" {
						row.BillingMode = "tiered_expr"
						row.BillingExpr = receipt.Expression
					}
					row.ModelRatio = receipt.CNY.Input / operation_setting.USDExchangeRate / 2
					if receipt.CNY.Input > 0 {
						row.CompletionRatio = receipt.CNY.Output / receipt.CNY.Input
						row.CacheRatio = common.GetPointer(receipt.CNY.CacheRead / receipt.CNY.Input)
						row.CreateCacheRatio = common.GetPointer(receipt.CNY.CacheWrite / receipt.CNY.Input)
					}
				}
			}
			result = append(result, row)
		}
	}
	return result
}
