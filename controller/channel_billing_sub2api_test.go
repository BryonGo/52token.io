package controller

import (
	"fmt"
	"maps"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/channelcost"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// setupSub2APIBalanceTest wires an in-memory SQLite DB into model.DB so
// Channel.UpdateBalance can persist, mirroring the pattern used by other
// controller tests.
func setupSub2APIBalanceTest(t *testing.T) {
	t.Helper()
	originalDB := model.DB
	database, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{
		Logger: logger.Discard,
	})
	require.NoError(t, err)
	sqlDB, err := database.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	require.NoError(t, database.AutoMigrate(&model.Channel{}))
	model.DB = database
	t.Cleanup(func() {
		model.DB = originalDB
	})
}

func newSub2APITestChannel(t *testing.T, serverURL string) *model.Channel {
	t.Helper()
	channel := &model.Channel{
		Type:    constant.ChannelTypeSub2API,
		BaseURL: &serverURL,
		Key:     "sk-test",
		Name:    "sub2api-test",
	}
	require.NoError(t, model.DB.Create(channel).Error)
	return channel
}

func TestUpdateChannelSub2APIBalanceRemainingPreferred(t *testing.T) {
	setupSub2APIBalanceTest(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/v1/usage", r.URL.Path)
		assert.Equal(t, "Bearer sk-test", r.Header.Get("Authorization"))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"balance": 100.0, "remaining": 986.17194888, "isValid": true, "unit": "USD"}`))
	}))
	defer server.Close()

	channel := newSub2APITestChannel(t, server.URL)
	balance, err := updateChannelSub2APIBalance(channel)
	require.NoError(t, err)
	assert.InDelta(t, 986.17194888, balance, 1e-9)
	assert.InDelta(t, 986.17194888, channel.Balance, 1e-9)
}

func TestUpdateChannelSub2APIBalanceWalletFallback(t *testing.T) {
	setupSub2APIBalanceTest(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"balance": 42.5, "isValid": true, "unit": "USD"}`))
	}))
	defer server.Close()

	channel := newSub2APITestChannel(t, server.URL)
	balance, err := updateChannelSub2APIBalance(channel)
	require.NoError(t, err)
	assert.InDelta(t, 42.5, balance, 1e-9)
}

func TestUpdateChannelSub2APIBalanceSubscriptionKeyMissingFields(t *testing.T) {
	setupSub2APIBalanceTest(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"balance": null, "remaining": null, "isValid": true, "mode": "unrestricted"}`))
	}))
	defer server.Close()

	channel := newSub2APITestChannel(t, server.URL)
	_, err := updateChannelSub2APIBalance(channel)
	require.Error(t, err)
}

func TestUpdateChannelSub2APIBalanceInvalidKey(t *testing.T) {
	setupSub2APIBalanceTest(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"isValid": false}`))
	}))
	defer server.Close()

	channel := newSub2APITestChannel(t, server.URL)
	_, err := updateChannelSub2APIBalance(channel)
	require.EqualError(t, err, "Sub2API key is invalid (isValid=false)")
}

func TestUpdateChannelSub2APIBalanceUnlimitedSubscription(t *testing.T) {
	setupSub2APIBalanceTest(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"remaining": -1, "isValid": true, "mode": "unrestricted", "subscription": {"daily_limit_usd": null, "weekly_limit_usd": null, "monthly_limit_usd": null}}`))
	}))
	defer server.Close()

	channel := newSub2APITestChannel(t, server.URL)
	require.NoError(t, model.DB.Model(channel).Update("balance", 12.5).Error)

	_, err := updateChannelSub2APIBalance(channel)
	require.EqualError(t, err, "Sub2API /v1/usage reports an unlimited subscription without a finite balance")

	stored := model.Channel{}
	require.NoError(t, model.DB.First(&stored, channel.Id).Error)
	assert.InDelta(t, 12.5, stored.Balance, 1e-9)
}

func TestUpdateChannelSub2APIBalanceNegativeWalletBalance(t *testing.T) {
	setupSub2APIBalanceTest(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"balance": -1, "remaining": -1, "isValid": true, "mode": "unrestricted", "unit": "USD"}`))
	}))
	defer server.Close()

	channel := newSub2APITestChannel(t, server.URL)
	balance, err := updateChannelSub2APIBalance(channel)
	require.NoError(t, err)
	assert.InDelta(t, -1, balance, 1e-9)
}

func TestUpdateChannelSub2APIBalanceMissingBaseURL(t *testing.T) {
	setupSub2APIBalanceTest(t)
	empty := ""
	channel := &model.Channel{BaseURL: &empty, Key: "sk-test"}
	_, err := updateChannelSub2APIBalance(channel)
	require.Error(t, err)
}

func TestUpdateStandardChannelBalanceRoutesSub2API(t *testing.T) {
	setupSub2APIBalanceTest(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"balance": 77.7, "isValid": true, "unit": "USD"}`))
	}))
	defer server.Close()

	channel := newSub2APITestChannel(t, server.URL)
	result, err := updateChannelBalance(channel)
	require.NoError(t, err)
	assert.InDelta(t, 77.7, result.Balance, 1e-9)
}

func TestSub2APIBalanceUsesRechargeRatioWithoutGroupMultiplier(t *testing.T) {
	setupSub2APIBalanceTest(t)
	testSub2APICostAndBalance(t)
}

func testSub2APICostAndBalance(t *testing.T) {
	t.Helper()
	previousFX := operation_setting.USDExchangeRate
	common.OptionMapRWMutex.RLock()
	previousOptions := maps.Clone(common.OptionMap)
	common.OptionMapRWMutex.RUnlock()
	if common.OptionMap == nil {
		common.OptionMap = map[string]string{}
	}
	ids := []int{}
	t.Cleanup(func() {
		operation_setting.USDExchangeRate = previousFX
		require.NoError(t, model.DB.Where("id IN ?", ids).Delete(&model.Channel{}).Error)
		for _, id := range ids {
			require.NoError(t, model.DB.Where(&model.Option{Key: fmt.Sprintf("channel_cost.%d", id)}).Delete(&model.Option{}).Error)
		}
		if raw, ok := previousOptions["channel_price_sources"]; ok {
			require.NoError(t, model.DB.Model(&model.Option{}).Where(&model.Option{Key: "channel_price_sources"}).Update("value", raw).Error)
		} else {
			require.NoError(t, model.DB.Where(&model.Option{Key: "channel_price_sources"}).Delete(&model.Option{}).Error)
		}
		common.OptionMapRWMutex.Lock()
		common.OptionMap = previousOptions
		common.OptionMapRWMutex.Unlock()
	})
	require.NoError(t, model.DB.AutoMigrate(&model.Option{}))
	source := channelcost.Source{ID: "sub2api-balance-test", Name: "Sub2API fractional group", Enabled: true, Format: "sub2api", URL: "https://upstream.example/api/v1/model-plaza", ChannelOrigin: "https://upstream.example", UnitTokens: 1, InputScale: 1, Fields: channelcost.SourceFields{Items: "data.groups", Groups: "models", GroupRatios: "rate_multiplier", Model: "name", Input: "pricing.input_price", Output: "pricing.output_price"}}
	_, version, err := model.GetChannelPriceSources()
	require.NoError(t, err)
	require.NoError(t, model.SaveChannelPriceSource(source, version))
	loaded, err := model.GetChannelPriceSource(source.ID)
	require.NoError(t, err)
	assert.Equal(t, source, loaded)
	var registry model.Option
	require.NoError(t, model.DB.Where(&model.Option{Key: "channel_price_sources"}).First(&registry).Error)
	var savedSources map[string]channelcost.Source
	require.NoError(t, common.UnmarshalJsonStr(registry.Value, &savedSources))
	assert.Equal(t, source, savedSources[source.ID])
	reference := channelcost.Source{ID: "sub2api-reference-test", Name: "Confirmed DeepSeek", Enabled: true, Format: "deepseek_html", URL: channelcost.DeepSeekURL, ChannelOrigin: "https://upstream.example", UnitTokens: 1e6, InputScale: 1, Reference: &channelcost.ReferencePricing{GroupRatio: .08, Models: map[string]string{"deepseek-v4-pro": "deepseek-v4-pro"}, Schedule: "deepseek", LocalGroupBilling: true}}
	_, version, err = model.GetChannelPriceSources()
	require.NoError(t, err)
	require.NoError(t, model.SaveChannelPriceSource(reference, version))
	loadedReference, err := model.GetChannelPriceSource(reference.ID)
	require.NoError(t, err)
	assert.Equal(t, reference, loadedReference)
	refChannel := &model.Channel{Type: constant.ChannelTypeSub2API, Models: "deepseek-v4-pro", Group: "POJIA", BaseURL: common.GetPointer(reference.ChannelOrigin), Key: "reference-key"}
	refChannel.SetOtherSettings(dto.ChannelOtherSettings{CostBilling: &dto.ChannelCostSettings{Enabled: true, Source: reference.ID, CreditsPerCNY: 1, UpstreamGroup: "upstream"}})
	require.NoError(t, model.DB.Create(refChannel).Error)
	ids = append(ids, refChannel.Id)
	referenceSnapshot := &channelcost.Snapshot{GroupRatio: .08, Fingerprint: model.ChannelCostFingerprint(refChannel), SyncedAt: time.Now().Unix(), Prices: map[string]channelcost.Price{"deepseek-v4-pro": {UpstreamModel: "deepseek-v4-pro", OffPeak: channelcost.Rates{Input: 4.5, Output: 13.5, CacheRead: .15}}}}
	require.NoError(t, model.SaveChannelCostSnapshot(refChannel.Id, referenceSnapshot))
	operation_setting.USDExchangeRate = 1
	catalog, err := model.GetChannelCostCatalog([]model.Pricing{{ModelName: "deepseek-v4-pro"}}, time.Now())
	require.NoError(t, err)
	var publicReference *model.ChannelCostPublicPrice
	for _, row := range catalog {
		if row.ChannelID == refChannel.Id {
			publicReference = row.ChannelCost
		}
	}
	require.NotNil(t, publicReference)
	require.NotNil(t, publicReference.CNY)
	assert.True(t, publicReference.LocalGroupBilling)
	assert.Equal(t, 4.5, publicReference.CNY.Input, "public reference prices stay at base; local group applies once")
	assert.Equal(t, .08, model.GetChannelCostSnapshot(refChannel.Id).GroupRatio, "procurement rate is retained separately")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "Bearer own-model-key", r.Header.Get("Authorization"))
		assert.Equal(t, "/v1/usage", r.URL.Path)
		_, _ = w.Write([]byte(`{"balance":100,"remaining":100,"isValid":true,"unit":"USD"}`))
	}))
	defer server.Close()
	for _, fx := range []float64{1, 7} {
		operation_setting.USDExchangeRate = fx
		for _, credits := range []float64{1, 5} {
			cfg := &dto.ChannelCostSettings{Enabled: true, Source: source.ID, CreditsPerCNY: credits, UpstreamGroup: "POJIAAI-国模破甲"}
			channel := &model.Channel{Type: constant.ChannelTypeSub2API, BaseURL: common.GetPointer(server.URL), Key: "own-model-key", Models: "m"}
			channel.SetOtherSettings(dto.ChannelOtherSettings{CostBilling: cfg})
			require.NoError(t, model.DB.Create(channel).Error)
			ids = append(ids, channel.Id)
			snapshot := &channelcost.Snapshot{GroupRatio: .08, Fingerprint: model.ChannelCostFingerprint(channel), SyncedAt: time.Now().Unix(), Prices: map[string]channelcost.Price{"m": {UpstreamModel: "m", OffPeak: channelcost.Rates{Input: 2, Output: 8}}}}
			require.NoError(t, model.SaveChannelCostSnapshot(channel.Id, snapshot))
			assert.Equal(t, snapshot, model.GetChannelCostSnapshot(channel.Id))
			var priceOption model.Option
			require.NoError(t, model.DB.Where(&model.Option{Key: fmt.Sprintf("channel_cost.%d", channel.Id)}).First(&priceOption).Error)
			var storedSnapshot channelcost.Snapshot
			require.NoError(t, common.UnmarshalJsonStr(priceOption.Value, &storedSnapshot))
			assert.Equal(t, .08, storedSnapshot.GroupRatio)
			result, err := updateChannelBalance(channel)
			require.NoError(t, err)
			assert.InDelta(t, 100/credits, result.Balance*fx, 1e-10, "wallet balance uses recharge cost, never the 0.08 consumption multiplier")
			var saved model.Channel
			require.NoError(t, model.DB.First(&saved, channel.Id).Error)
			assert.Equal(t, cfg, saved.GetOtherSettings().CostBilling)
			assert.InDelta(t, result.Balance, saved.Balance, 1e-10)
		}
	}
}
