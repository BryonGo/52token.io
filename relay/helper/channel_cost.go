package helper

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/billingexpr"
	"github.com/QuantumNous/new-api/pkg/channelcost"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	hosttypes "github.com/QuantumNous/new-api/types"
	"github.com/gin-gonic/gin"
)

func channelCostPrice(c *gin.Context, info *relaycommon.RelayInfo, promptTokens int) (hosttypes.PriceData, bool, error) {
	id := common.GetContextKeyInt(c, constant.ContextKeyChannelId)
	if id == 0 {
		return hosttypes.PriceData{}, false, nil
	}
	ch, err := model.CacheGetChannel(id)
	if err != nil {
		return hosttypes.PriceData{}, true, err
	}
	cfg := ch.GetOtherSettings().CostBilling
	if cfg == nil || !cfg.Enabled {
		return hosttypes.PriceData{}, false, nil
	}
	if operation_setting.Price <= 0 || math.Abs(operation_setting.Price-operation_setting.USDExchangeRate) > 1e-9 {
		return hosttypes.PriceData{}, true, errors.New("CNY cost billing requires matching wallet recharge and display conversion rates")
	}
	if ch.ParamOverride != nil && strings.TrimSpace(*ch.ParamOverride) != "" && strings.TrimSpace(*ch.ParamOverride) != "{}" {
		return hosttypes.PriceData{}, true, errors.New("Channel cost billing does not support parameter overrides")
	}
	switch info.RelayFormat {
	case types.RelayFormatOpenAI, types.RelayFormatClaude, types.RelayFormatOpenAIResponses, types.RelayFormatEmbedding, types.RelayFormatOpenAIImage:
	default:
		return hosttypes.PriceData{}, true, errors.New("Channel cost billing does not support this request format")
	}
	input, err := ResolveIncomingBillingExprRequestInput(c, info)
	if err != nil {
		return hosttypes.PriceData{}, true, err
	}
	var body struct {
		Tools []struct {
			Type string `json:"type"`
		} `json:"tools"`
		WebSearchOptions any    `json:"web_search_options"`
		ServiceTier      string `json:"service_tier"`
		Speed            string `json:"speed"`
	}
	if len(input.Body) > 0 && common.Unmarshal(input.Body, &body) != nil {
		return hosttypes.PriceData{}, true, errors.New("Invalid request for channel cost billing")
	}
	for _, tool := range body.Tools {
		if tool.Type != "" && tool.Type != "function" && tool.Type != "custom" {
			return hosttypes.PriceData{}, true, errors.New("Upstream tool pricing is not synchronized")
		}
	}
	if body.WebSearchOptions != nil || body.ServiceTier != "" && body.ServiceTier != "default" || body.Speed != "" {
		return hosttypes.PriceData{}, true, errors.New("Upstream service tier pricing is not synchronized")
	}
	snap := model.GetChannelCostSnapshot(id)
	receipt, expr, err := snap.Resolve(cfg, info.OriginModelName, model.ChannelCostFingerprint(ch), time.Now(), operation_setting.USDExchangeRate)
	if err != nil {
		return hosttypes.PriceData{}, true, err
	}
	if receipt.TaskUsageBilling {
		return hosttypes.PriceData{}, true, errors.New("Channel cost expression requires a compatible task usage meter")
	}
	receipt.ChannelID = id
	info.BillingModelName = info.OriginModelName
	group := hosttypes.GroupRatioInfo{GroupRatio: 1, GroupSpecialRatio: -1}
	source, err := model.GetChannelPriceSource(cfg.Source)
	if err != nil {
		return hosttypes.PriceData{}, true, err
	}
	if source.UsesLocalGroupBilling() {
		group = HandleGroupRatio(c, info)
		if group.GroupRatio < 0 || math.IsNaN(group.GroupRatio) || math.IsInf(group.GroupRatio, 0) || group.GroupRatio > receipt.GroupRatio+1e-12 {
			return hosttypes.PriceData{}, true, errors.New("Local group multiplier exceeds the upstream cost multiplier")
		}
		// Resolve records the real purchase cost. Restore the base price here;
		// the local group multiplier is applied once by normal settlement.
		expr = "(" + expr + ") / " + fmt.Sprintf("%.17g", receipt.GroupRatio)
	}
	data, err := modelPriceHelperExpression(c, info, info.OriginModelName, promptTokens, group, expr)
	if err != nil {
		return hosttypes.PriceData{}, true, err
	}
	data.ChannelCost = receipt
	if info.TieredBillingSnapshot != nil {
		info.TieredBillingSnapshot.ProcurementCost = true
	}
	info.PriceData = data
	return data, true, nil
}

// RefreshChannelCostPricing runs after routing and before each upstream attempt.
// Clear the old expression when falling back to a channel using global pricing.
func RefreshChannelCostPricing(c *gin.Context, info *relaycommon.RelayInfo) (bool, error) {
	wasCost := info.PriceData.ChannelCost != nil
	if _, active, err := channelCostPrice(c, info, info.GetEstimatePromptTokens()); active {
		return true, err
	}
	if !wasCost {
		return false, nil
	}
	info.PriceData.ChannelCost = nil
	info.BillingModelName = ""
	info.TieredBillingSnapshot = nil
	_, err := ModelPriceHelper(c, info, info.GetEstimatePromptTokens(), info.Request.GetTokenCountMeta())
	return true, err
}

// ChannelCostTaskExpression pins the procurement contract to the selected
// channel before a task is submitted. A group discount is applied only once.
func ChannelCostTaskExpression(c *gin.Context, info *relaycommon.RelayInfo) (*channelcost.Receipt, string, hosttypes.GroupRatioInfo, error) {
	group := hosttypes.GroupRatioInfo{GroupRatio: 1, GroupSpecialRatio: -1}
	if info.ChannelMeta == nil || info.ChannelOtherSettings.CostBilling == nil || !info.ChannelOtherSettings.CostBilling.Enabled {
		return nil, "", group, nil
	}
	ch, err := model.CacheGetChannel(info.ChannelId)
	if err != nil {
		return nil, "", group, err
	}
	cfg := ch.GetOtherSettings().CostBilling
	if cfg == nil || !cfg.Enabled {
		return nil, "", group, nil
	}
	if !channelcost.Positive(operation_setting.Price) || math.Abs(operation_setting.Price-operation_setting.USDExchangeRate) > 1e-9 {
		return nil, "", group, errors.New("CNY cost billing requires matching wallet recharge and display conversion rates")
	}
	if ch.ParamOverride != nil && strings.TrimSpace(*ch.ParamOverride) != "" && strings.TrimSpace(*ch.ParamOverride) != "{}" {
		return nil, "", group, errors.New("Channel cost billing does not support parameter overrides")
	}
	receipt, expression, err := model.GetChannelCostSnapshot(ch.Id).Resolve(cfg, info.OriginModelName, model.ChannelCostFingerprint(ch), time.Now(), operation_setting.USDExchangeRate)
	if err != nil {
		return nil, "", group, err
	}
	if !receipt.TaskUsageBilling {
		expression, err = channelcost.TaskExpression(expression)
		if err != nil {
			return nil, "", group, err
		}
		if billingexpr.UsedUsageKeys(expression)["image_count"] {
			receipt.UsageSchema = map[string]json.RawMessage{"image_count": json.RawMessage(`{"type":"number","unit":"count"}`)}
		}
	}
	source, err := model.GetChannelPriceSource(cfg.Source)
	if err != nil {
		return nil, "", group, err
	}
	if source.UsesLocalGroupBilling() {
		group = HandleGroupRatio(c, info)
		if group.GroupRatio < 0 || math.IsNaN(group.GroupRatio) || math.IsInf(group.GroupRatio, 0) || group.GroupRatio > receipt.GroupRatio+1e-12 {
			return nil, "", group, errors.New("Local group multiplier exceeds the upstream cost multiplier")
		}
		expression = "(" + expression + ") / " + fmt.Sprintf("%.17g", receipt.GroupRatio)
	}
	receipt.ChannelID = ch.Id
	return receipt, expression, group, nil
}
