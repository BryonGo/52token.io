package model

import (
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/pkg/channelcost"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPublicCatalogIdentitiesSeparateSameNameSameMultiplierRoutes(t *testing.T) {
	channels := []*Channel{
		{Id: 1, Status: 1, Type: constant.ChannelTypeOpenAI, Name: "HappyAPI A", Group: "route_a,route_b", Models: "deepseek-v4-pro,deepseek-v4-pro,seedream-5.0"},
		{Id: 2, Status: 1, Name: "HappyAPI B", Group: "route_a", Models: "deepseek-v4-pro"},
		{Id: 3, Status: 2, Name: "Disabled", Group: "route_a", Models: "secret-model"},
	}
	profiles := map[string]GroupProfile{"route_a": {Name: "HappyAPI DeepSeek"}, "route_b": {Name: "HappyAPI DeepSeek"}}
	ratios := map[string]float64{"route_a": 1, "route_b": 1}
	rows := buildPublicModelCatalog(channels, ratios, profiles, "")
	require.Len(t, rows, 5)
	ids := map[string]bool{}
	for _, row := range rows {
		assert.False(t, ids[row.PricingID])
		ids[row.PricingID] = true
	}
	first := publicCatalogModel(channels[0], "route_a", "deepseek-v4-pro", 1, profiles)
	filtered := buildPublicModelCatalog(channels, ratios, profiles, "route_b")
	require.Len(t, filtered, 2)
	for _, row := range filtered {
		assert.Equal(t, "route_b", row.GroupID)
		assert.Equal(t, 1, row.ChannelID)
	}
	assert.Empty(t, buildPublicModelCatalog(channels, ratios, profiles, "route"))
	assert.Empty(t, buildPublicModelCatalog(channels, ratios, profiles, "HappyAPI DeepSeek"))
	assert.Empty(t, buildPublicModelCatalog(channels, ratios, profiles, "route_%"))
	second := publicCatalogModel(channels[1], "route_a", "deepseek-v4-pro", 1, profiles)
	assert.Equal(t, first.ModelUID, second.ModelUID)
	assert.NotEqual(t, first.PricingID, second.PricingID)
	profiles["route_a"] = GroupProfile{Name: "Renamed"}
	channels[0].Name = "Renamed channel"
	renamed := publicCatalogModel(channels[0], "route_a", "deepseek-v4-pro", .08, profiles)
	assert.Equal(t, first.PricingID, renamed.PricingID)
	assert.Equal(t, first.ModelUID, renamed.ModelUID)
	assert.Equal(t, "Renamed", renamed.GroupName)
	assert.Empty(t, buildPublicModelCatalog(channels, map[string]float64{}, profiles, ""))
}

func TestPublicCatalogCostPricesMatchCreditsAndMultiplyLocalGroupOnce(t *testing.T) {
	oldOptions := common.OptionMap
	oldPrice, oldFX := operation_setting.Price, operation_setting.USDExchangeRate
	common.OptionMap = map[string]string{}
	operation_setting.Price, operation_setting.USDExchangeRate = 1, 1
	t.Cleanup(func() {
		common.OptionMap = oldOptions
		operation_setting.Price, operation_setting.USDExchangeRate = oldPrice, oldFX
	})
	now := time.Date(2026, 10, 19, 10, 30, 0, 0, time.FixedZone("Asia/Shanghai", 8*3600))
	source := channelcost.Source{ID: "catalog-reference", Enabled: true, Format: "deepseek_html", URL: channelcost.DeepSeekURL, ChannelOrigin: "https://example.test", Reference: &channelcost.ReferencePricing{LocalGroupBilling: true, GroupRatio: .08}}
	raw, err := common.Marshal(map[string]channelcost.Source{source.ID: source})
	require.NoError(t, err)
	common.OptionMap[priceSourcesOption] = string(raw)
	channel := &Channel{Id: 20, Status: 1, Group: "route_a", Models: "deepseek-v4-pro", Name: "POJIA", Key: "sk-fixture-never-public"}
	channel.SetOtherSettings(dto.ChannelOtherSettings{CostBilling: &dto.ChannelCostSettings{Enabled: true, Source: source.ID, CreditsPerCNY: 1, UpstreamGroup: "private-upstream-group"}})
	snapshot := &channelcost.Snapshot{Fingerprint: ChannelCostFingerprint(channel), SyncedAt: now.Unix(), GroupRatio: .08, Prices: map[string]channelcost.Price{
		"deepseek-v4-pro": {UpstreamModel: "deepseek-v4-pro", OffPeak: channelcost.Rates{Input: 4.5, Output: 13.5, CacheRead: .15}, Peak: &channelcost.Rates{Input: 9, Output: 27, CacheRead: .3}},
	}}
	raw, err = common.Marshal(snapshot)
	require.NoError(t, err)
	common.OptionMap["channel_cost.20"] = string(raw)
	row := publicCatalogModel(channel, "route_a", "deepseek-v4-pro", .08, nil)
	peak := resolvePublicModelPrice(row, channel, now)
	require.True(t, peak.Available, peak.Reason)
	require.NotNil(t, peak.Rates)
	assert.InDelta(t, .72, peak.Rates.Input, 1e-12)
	assert.InDelta(t, 2.16, peak.Rates.Output, 1e-12)
	assert.Equal(t, "peak", peak.CurrentTier)
	assert.Equal(t, "CNY", peak.Currency)
	assert.Equal(t, "reference", peak.Source)
	assert.InDelta(t, .72, peak.CostRates.Input, 1e-12)
	discountedRow := row
	discountedRow.GroupMultiplier = .04
	discounted := resolvePublicModelPrice(discountedRow, channel, now)
	require.True(t, discounted.Available)
	assert.InDelta(t, .36, discounted.Rates.Input, 1e-12)
	assert.InDelta(t, .72, discounted.CostRates.Input, 1e-12)
	assert.InDelta(t, .36, peak.OffPeakRates.Input, 1e-12)
	assert.Equal(t, time.Date(2026, 10, 19, 12, 0, 0, 0, now.Location()).Unix(), peak.ValidUntil)
	again := resolvePublicModelPrice(row, channel, now.Add(time.Minute))
	assert.Equal(t, peak.Version, again.Version)
	offPeak := resolvePublicModelPrice(row, channel, now.Add(2*time.Hour))
	assert.InDelta(t, .36, offPeak.Rates.Input, 1e-12)
	assert.NotEqual(t, peak.Version, offPeak.Version)
	encoded, err := common.Marshal(peak)
	require.NoError(t, err)
	assert.NotContains(t, string(encoded), channel.Key)
	assert.NotContains(t, string(encoded), "private-upstream-group")
	assert.NotContains(t, string(encoded), "example.test")
	row.GroupMultiplier = 1
	assert.Equal(t, "group_multiplier_exceeds_cost", resolvePublicModelPrice(row, channel, now).Reason)
	// Standard cost channels already contain the upstream group multiplier.
	source.Reference = nil
	raw, err = common.Marshal(map[string]channelcost.Source{source.ID: source})
	require.NoError(t, err)
	common.OptionMap[priceSourcesOption] = string(raw)
	snapshot.Fingerprint = ChannelCostFingerprint(channel)
	snapshot.GroupRatio = 5
	settings := channel.GetOtherSettings()
	settings.CostBilling.CreditsPerCNY = 5
	channel.SetOtherSettings(settings)
	snapshot.Fingerprint = ChannelCostFingerprint(channel)
	fixed := .025
	snapshot.Prices["deepseek-v4-pro"] = channelcost.Price{UpstreamModel: "deepseek-v4-pro", RequestPrice: &fixed}
	raw, err = common.Marshal(snapshot)
	require.NoError(t, err)
	common.OptionMap["channel_cost.20"] = string(raw)
	row.GroupMultiplier = .08
	request := resolvePublicModelPrice(row, channel, now)
	require.True(t, request.Available, request.Reason)
	assert.InDelta(t, .025, *request.RequestPrice, 1e-12)
	assert.Equal(t, 1.0, request.AppliedGroupMultiplier)
	assert.Equal(t, `tier("request", fixed(0.025))`, request.Expression)
	expired := resolvePublicModelPrice(row, channel, now.Add(channelcost.MaxAge+time.Second))
	assert.False(t, expired.Available)
	snapshot.Prices["deepseek-v4-pro"] = channelcost.Price{UpstreamModel: "deepseek-v4-pro", Tiers: []channelcost.Tier{
		{Label: "short", MaxContext: 32000, Rates: channelcost.Rates{Input: 1, Output: 4}},
		{Label: "long", Rates: channelcost.Rates{Input: 2, Output: 8}},
	}}
	raw, err = common.Marshal(snapshot)
	require.NoError(t, err)
	common.OptionMap["channel_cost.20"] = string(raw)
	contextPrice := resolvePublicModelPrice(row, channel, now)
	require.True(t, contextPrice.Available, contextPrice.Reason)
	require.Len(t, contextPrice.Tiers, 2)
	assert.Equal(t, 2.0, contextPrice.Tiers[1].Rates.Input)
	assert.Contains(t, contextPrice.Expression, "len <= 32000")
	// Missing and expired snapshots are unavailable, not a zero price.
	delete(common.OptionMap, "channel_cost.20")
	unavailable := resolvePublicModelPrice(row, channel, now)
	assert.False(t, unavailable.Available)
	assert.Nil(t, unavailable.Rates)
	assert.Nil(t, unavailable.RequestPrice)
}
