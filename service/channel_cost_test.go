package service

import (
	"context"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/billingexpr"
	"github.com/QuantumNous/new-api/pkg/channelcost"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestChannelCostCreditsGroupsAndExclusiveCache(t *testing.T) {
	body := []byte(`{"success":true,"group_ratio":{"default":1,"five":5},"data":[{"model_name":"DeepSeek-V3","quota_type":0,"model_ratio":0.5,"completion_ratio":4,"cache_ratio":0.25,"enable_groups":["default","five"]}]}`)
	now := time.Date(2026, 10, 16, 5, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		group        string
		fx, expected float64
	}{{"default", 1, 0.85}, {"default", 7, 0.85}, {"five", 1, 4.25}} {
		t.Run(fmt.Sprintf("%s/fx_%g", tc.group, tc.fx), func(t *testing.T) {
			prices, group, err := parseHappyCost(body, tc.group, 500000)
			require.NoError(t, err)
			snapshot := &channelcost.Snapshot{Fingerprint: "one", SyncedAt: now.Unix(), GroupRatio: group, Prices: prices}
			cfg := &dto.ChannelCostSettings{Enabled: true, Source: "happyapi", CreditsPerCNY: 5, UpstreamGroup: tc.group}
			receipt, expr, err := snapshot.Resolve(cfg, "DeepSeek-V3", "one", now, tc.fx)
			require.NoError(t, err)
			usage := &dto.Usage{PromptTokens: 1000000, CompletionTokens: 1000000}
			usage.PromptTokensDetails.CachedTokens = 1000000
			params := BuildTieredTokenParams(usage, false, billingexpr.UsedVarsByHash(expr, billingexpr.ExprHashString(expr)))
			raw, _, err := billingexpr.RunExprByHashWithRequest(expr, billingexpr.ExprHashString(expr), params, billingexpr.RequestInput{})
			require.NoError(t, err)
			assert.InDelta(t, tc.expected, raw/1e6*tc.fx, 1e-10, "one million cached input plus output, display FX applied once")
			assert.Equal(t, group, receipt.GroupRatio)
			_, _, err = snapshot.Resolve(cfg, "DeepSeek-V3", "other-channel", now, tc.fx)
			require.Error(t, err)
			_, _, err = snapshot.Resolve(cfg, "missing", "one", now, tc.fx)
			require.Error(t, err)
			_, _, err = snapshot.Resolve(cfg, "DeepSeek-V3", "one", now.Add(25*time.Hour), tc.fx)
			require.Error(t, err)
			cfg.CreditsPerCNY = 0
			_, _, err = snapshot.Resolve(cfg, "DeepSeek-V3", "one", now, tc.fx)
			require.Error(t, err)
		})
	}
	_, _, err := parseHappyCost(body, "unknown", 500000)
	require.Error(t, err)
	_, _, err = parseHappyCost([]byte(`{"success":false}`), "default", 500000)
	require.Error(t, err)
}

func TestDeepSeekCostOfficialDocumentAndCalendar(t *testing.T) {
	body, err := os.ReadFile("testdata/deepseek_cny_pricing.html")
	require.NoError(t, err)
	prices, err := parseDeepSeekCost(body)
	require.NoError(t, err)
	assert.Equal(t, channelcost.Rates{Input: 1, Output: 4, CacheRead: 0.02, CacheWrite: 1, CacheWrite1h: 1}, prices["deepseek-flash"].OffPeak)
	assert.Equal(t, channelcost.Rates{Input: 9, Output: 27, CacheRead: 0.3, CacheWrite: 9, CacheWrite1h: 9}, *prices["deepseek-v4-pro"].Peak)
	_, err = parseDeepSeekCost([]byte("<table><tr><td>$0.15</td></tr></table>"))
	require.Error(t, err)
	usdHTML := []byte(`<table><tr><th>MODEL</th><th>deepseek-flash(1)</th><th>deepseek-v4-pro</th></tr><tr><td>1M INPUT TOKENS (CACHE HIT)</td><td>OFF-PEAK</td><td>$0.003</td><td>$0.022</td></tr><tr><td>PEAK</td><td>$0.006</td><td>$0.044</td></tr><tr><td>1M INPUT TOKENS (CACHE MISS)</td><td>OFF-PEAK</td><td>$0.15</td><td>$0.66</td></tr><tr><td>PEAK</td><td>$0.3</td><td>$1.32</td></tr><tr><td>1M OUTPUT TOKENS</td><td>OFF-PEAK</td><td>$0.6</td><td>$1.98</td></tr><tr><td>PEAK</td><td>$1.2</td><td>$3.96</td></tr></table><p>Peak hours are 01:00 - 04:00 and 06:00 - 10:00 UTC, Monday through Friday, excluding Chinese public holidays.</p>`)
	usdPrices, err := parseDeepSeekCostCurrency(usdHTML, "USD")
	require.NoError(t, err)
	assert.Equal(t, channelcost.Rates{Input: .15, Output: .6, CacheRead: .003, CacheWrite: .15, CacheWrite1h: .15}, usdPrices["deepseek-flash"].OffPeak)
	assert.Equal(t, channelcost.Rates{Input: 1.32, Output: 3.96, CacheRead: .044, CacheWrite: 1.32, CacheWrite1h: 1.32}, *usdPrices["deepseek-v4-pro"].Peak)
	_, err = parseDeepSeekCostCurrency(body, "USD")
	require.Error(t, err, "a CNY table cannot be treated as USD")
	_, err = parseDeepSeekCostCurrency(usdHTML, "CNY")
	require.Error(t, err, "a USD table cannot be treated as CNY")
	now := time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)
	usdSnapshot := channelcost.Snapshot{Fingerprint: "usd-official", SyncedAt: now.Unix(), GroupRatio: 1, Prices: usdPrices}
	receipt, _, err := usdSnapshot.Resolve(&dto.ChannelCostSettings{Enabled: true, Source: "deepseek-usd", CreditsPerCNY: 1 / 6.69}, "deepseek-v4-pro", "usd-official", now, 1)
	require.NoError(t, err)
	assert.InDelta(t, .66*6.69, receipt.CNY.Input, 1e-12)
	assert.InDelta(t, 1.98*6.69, receipt.CNY.Output, 1e-12)

	for _, tc := range []struct {
		at   string
		peak bool
	}{{"2026-10-16T08:59:00+08:00", false}, {"2026-10-16T09:00:00+08:00", true}, {"2026-10-16T12:00:00+08:00", false}, {"2026-10-16T14:00:00+08:00", true}, {"2026-10-16T18:00:00+08:00", false}, {"2026-10-01T10:00:00+08:00", false}, {"2026-10-10T10:00:00+08:00", false}} {
		at, err := time.Parse(time.RFC3339, tc.at)
		require.NoError(t, err)
		peak, err := channelcost.DeepSeekPeak(at)
		require.NoError(t, err)
		assert.Equal(t, tc.peak, peak, tc.at)
	}
	_, err = channelcost.DeepSeekPeak(time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC))
	require.Error(t, err)
}

func TestLiveChannelCostSources(t *testing.T) {
	if os.Getenv("TEST_CHANNEL_COST_LIVE") != "1" {
		t.Skip("live provider checks are opt-in")
	}
	body, err := fetchChannelCostDocument(context.Background(), channelcost.DeepSeekURL)
	require.NoError(t, err)
	prices, err := parseDeepSeekCost(body)
	require.NoError(t, err)
	require.Contains(t, prices, "deepseek-flash")
	body, err = fetchChannelCostDocument(context.Background(), "https://happyapi.org/api/pricing")
	require.NoError(t, err)
	prices, group, err := parseHappyCost(body, "default", 500000)
	require.NoError(t, err)
	require.Contains(t, prices, "deepseek-ai/DeepSeek-V3")
	assert.Equal(t, 1.0, group)
}

// costProviderTransport serves the verified provider contract without paid calls.
type costProviderTransport struct {
	calls     int
	onPricing func()
}

func (s *costProviderTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	s.calls++
	body := `{"success":true,"data":{"quota_per_unit":500000}}`
	if req.URL.Path == "/api/pricing" {
		if s.onPricing != nil {
			s.onPricing()
		}
		body = `{"success":true,"group_ratio":{"default":1},"data":[{"model_name":"DeepSeek-V3","quota_type":0,"model_ratio":0.5,"completion_ratio":4,"enable_groups":["default"]},{"model_name":"unconfigured-model","quota_type":0,"model_ratio":50,"completion_ratio":10,"enable_groups":["default"]}]}`
	}
	return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body)), Request: req}, nil
}

func TestSyncChannelCostScopesSourceModelsAndAdvancedRoutes(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "cost-sync.db")), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.Channel{}, &model.Option{}))
	previousDB, previousHTTP := model.DB, channelCostHTTP
	model.DB = db
	transport := &costProviderTransport{}
	channelCostHTTP = &http.Client{Transport: transport}
	common.OptionMapRWMutex.Lock()
	previousOptions := common.OptionMap
	common.OptionMap = map[string]string{"ModelRatio": "keep global prices", "channel_cost.8": "keep other channel"}
	common.OptionMapRWMutex.Unlock()
	t.Cleanup(func() {
		model.DB, channelCostHTTP = previousDB, previousHTTP
		common.OptionMapRWMutex.Lock()
		common.OptionMap = previousOptions
		common.OptionMapRWMutex.Unlock()
		sqlDB, _ := db.DB()
		require.NoError(t, sqlDB.Close())
	})
	channel := &model.Channel{Id: 7, Type: constant.ChannelTypeAdvancedCustom, Name: "Happy custom", BaseURL: common.GetPointer("https://happyapi.org"), Models: "DeepSeek-V3"}
	settings := dto.ChannelOtherSettings{
		CostBilling:    &dto.ChannelCostSettings{Enabled: true, Source: "happyapi", CreditsPerCNY: 5, UpstreamGroup: "default"},
		AdvancedCustom: &dto.AdvancedCustomConfig{Routes: []dto.AdvancedCustomRoute{{IncomingPath: "/v1/chat/completions", UpstreamPath: "https://happyapi.org/v1/chat/completions"}}},
	}
	channel.SetOtherSettings(settings)
	require.NoError(t, db.Create(channel).Error)
	snapshot, err := SyncChannelCost(context.Background(), channel.Id)
	require.NoError(t, err)
	require.Len(t, snapshot.Prices, 1, "source models not configured on this channel must not be imported")
	require.Contains(t, snapshot.Prices, "DeepSeek-V3")
	assert.Equal(t, "keep global prices", common.OptionMap["ModelRatio"])
	assert.Equal(t, "keep other channel", common.OptionMap["channel_cost.8"])
	receipt, _, err := snapshot.Resolve(settings.CostBilling, "DeepSeek-V3", model.ChannelCostFingerprint(channel), time.Now(), 1)
	require.NoError(t, err)
	assert.InDelta(t, 0.2, receipt.CNY.Input, 1e-9)
	assert.InDelta(t, 0.8, receipt.CNY.Output, 1e-9)

	priorSnapshot := model.GetChannelCostSnapshot(channel.Id)
	transport.onPricing = func() {
		sources, version, err := model.GetChannelPriceSources()
		require.NoError(t, err)
		source := sources["happyapi"]
		source.InputScale = 2
		require.NoError(t, model.SaveChannelPriceSource(source, version))
	}
	_, err = SyncChannelCost(context.Background(), channel.Id)
	require.EqualError(t, err, "Channel changed during price synchronization")
	assert.Equal(t, priorSnapshot, model.GetChannelCostSnapshot(channel.Id), "an in-flight source edit cannot stamp old prices with the new source fingerprint")
	transport.onPricing = nil
	_, version, err := model.GetChannelPriceSources()
	require.NoError(t, err)
	require.NoError(t, model.SaveChannelPriceSource(channelcost.DefaultSources()["happyapi"], version))
	settings.AdvancedCustom.Routes[0].UpstreamPath = "https://another-provider.test/v1/chat/completions"
	channel.SetOtherSettings(settings)
	require.NoError(t, db.Save(channel).Error)
	_, _, err = snapshot.Resolve(settings.CostBilling, "DeepSeek-V3", model.ChannelCostFingerprint(channel), time.Now(), 1)
	require.Error(t, err, "route changes must invalidate the prior price snapshot")
	beforeCalls := transport.calls
	_, err = SyncChannelCost(context.Background(), channel.Id)
	require.EqualError(t, err, "Advanced custom route does not match the channel price source")
	assert.Equal(t, beforeCalls, transport.calls, "mismatched sources must be rejected before any fetch or save")
	assert.Equal(t, "keep other channel", common.OptionMap["channel_cost.8"])

	settings.AdvancedCustom.Routes[0].UpstreamPath = "/v1/chat/completions"
	settings.CostBilling.Source = "deepseek"
	settings.CostBilling.UpstreamGroup = ""
	channel.SetOtherSettings(settings)
	require.NoError(t, db.Save(channel).Error)
	_, err = SyncChannelCost(context.Background(), channel.Id)
	require.EqualError(t, err, "Pricing source does not match the channel endpoint")
	assert.Equal(t, beforeCalls, transport.calls, "another provider cannot supply this channel's prices")
}

func TestMappedPriceSourceAndHappyContextTiers(t *testing.T) {
	source := channelcost.Source{ID: "custom", Name: "Custom JSON", Enabled: true, Format: "json", URL: "https://example.com/prices", ChannelOrigin: "https://example.com", UnitTokens: 1, InputScale: 1, Fields: channelcost.SourceFields{Items: "result.models", Model: "id", Input: "pricing.prompt", Output: "pricing.completion", CacheRead: "pricing.cached"}}
	require.NoError(t, source.Validate())
	parsed, err := ParsePriceSource([]byte(`{"result":{"models":[{"id":"same-name","pricing":{"prompt":"0.000002","completion":"0.000012","cached":"0.0000002"}}]}}`), source, "", 500000)
	require.NoError(t, err)
	assert.InDelta(t, 2, parsed.Prices["same-name"].OffPeak.Input, 1e-10)
	assert.InDelta(t, 12, parsed.Prices["same-name"].OffPeak.Output, 1e-10)
	assert.InDelta(t, .2, parsed.Prices["same-name"].OffPeak.CacheRead, 1e-10)

	body := []byte(`{"success":true,"group_ratio":{"default":1},"data":[{"model_name":"gpt-5.6-terra-xhigh","quota_type":0,"model_ratio":1.25,"completion_ratio":6,"enable_groups":["default"],"billing_mode":"tiered_expr","billing_expr":"len <= 272000 ? tier(\"Short context\", p * 2 + c * 12 + cr * 0.2 + cc * 2.499999) : tier(\"Long context\", p * 4 + c * 18 + cr * 0.4 + cc * 5)"}]}`)
	prices, group, err := parseHappyCost(body, "default", 500000)
	require.NoError(t, err)
	expressionOnlySource := channelcost.DefaultSources()["happyapi"]
	expressionOnlySource.Fields.BillingMode = ""
	expressionOnlySource.Fields.Input = ""
	expressionOnlySource.Fields.Output = ""
	require.NoError(t, expressionOnlySource.Validate())
	expressionOnly, err := ParsePriceSource(body, expressionOnlySource, "default", 500000)
	require.NoError(t, err)
	require.Len(t, expressionOnly.Prices["gpt-5.6-terra-xhigh"].Tiers, 2)
	price := prices["gpt-5.6-terra-xhigh"]
	require.Len(t, price.Tiers, 2)
	assert.EqualValues(t, 272000, price.Tiers[0].MaxContext)
	now := time.Now()
	snapshot := &channelcost.Snapshot{Fingerprint: "bound", SyncedAt: now.Unix(), GroupRatio: group, Prices: prices}
	cfg := &dto.ChannelCostSettings{Enabled: true, Source: "happyapi", CreditsPerCNY: 5, UpstreamGroup: "default"}
	for _, fx := range []float64{1, 7} {
		receipt, expression, err := snapshot.Resolve(cfg, "gpt-5.6-terra-xhigh", "bound", now, fx)
		require.NoError(t, err)
		assert.InDelta(t, .4, receipt.Tiers[0].Rates.Input, 1e-10)
		assert.InDelta(t, 2.4, receipt.Tiers[0].Rates.Output, 1e-10)
		assert.InDelta(t, .8, receipt.Tiers[1].Rates.Input, 1e-10)
		assert.InDelta(t, 3.6, receipt.Tiers[1].Rates.Output, 1e-10)
		for _, test := range []struct {
			prompt, cache int
			cost          float64
		}{{272000, 0, 2.5088}, {272001, 0, 3.8176008}, {300000, 200000, 3.696}} {
			usage := &dto.Usage{PromptTokens: test.prompt, CompletionTokens: 1000000}
			usage.PromptTokensDetails.CachedTokens = test.cache
			params := BuildTieredTokenParams(usage, false, billingexpr.UsedVars(expression))
			raw, _, err := billingexpr.RunExpr(expression, params)
			require.NoError(t, err)
			assert.InDelta(t, test.cost, raw/1e6*fx, 1e-9, "tiers use full context length even when most input is cached")
		}
	}
}
func TestPriceSourceRejectsUnsupportedOrInvalidPrices(t *testing.T) {
	source := channelcost.DefaultSources()["happyapi"]
	for _, expression := range []string{`tier("bad", p * -2 + c * 12)`, `tier("bad", p * 2 + c * 12) * header("factor")`, `len > 272000 ? tier("bad", p * 2 + c * 12) : tier("other", p / 0)`} {
		raw, err := common.Marshal(map[string]any{"success": true, "group_ratio": map[string]float64{"default": 1}, "data": []map[string]any{{"model_name": "m", "quota_type": 0, "enable_groups": []string{"default"}, "billing_mode": "tiered_expr", "billing_expr": expression}}})
		require.NoError(t, err)
		result, err := ParsePriceSource(raw, source, "default", 500000)
		require.NoError(t, err)
		assert.Empty(t, result.Prices)
		assert.NotEmpty(t, result.Errors["m"])
	}
	for _, ratio := range []float64{-1, math.NaN(), math.Inf(1), 0.0000001, 1000001} {
		source.DefaultCreditsPerCNY = ratio
		require.Error(t, source.Validate())
	}
	source.DefaultCreditsPerCNY = 1
	require.NoError(t, source.Validate())
	source.DefaultCreditsPerCNY = 0 // Existing sources do not supply a default.
	require.NoError(t, source.Validate())
	source.URL = ""
	require.Error(t, source.Validate())
	source = channelcost.DefaultSources()["happyapi"]
	source.Fields.Input = "data.#(secret)"
	require.Error(t, source.Validate())
}

func TestProtectedPriceSourceCredentialsStayOnBoundEndpoints(t *testing.T) {
	requests := 0
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		assert.Equal(t, "Bearer fixture-source-token", r.Header.Get("Authorization"))
		assert.Equal(t, "2752", r.Header.Get("New-Api-User"))
		if r.URL.Path == "/redirect" {
			w.Header().Set("Location", "/must-not-receive-token")
			w.WriteHeader(http.StatusFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"success":true,"data":[{"id":"m","input":2,"output":4}]}`))
	}))
	defer server.Close()
	source := channelcost.Source{ID: "protected-test", Name: "Protected", Enabled: true, Format: "json", URL: server.URL + "/pricing", ChannelOrigin: server.URL, UnitTokens: 1e6, InputScale: 1, Fields: channelcost.SourceFields{Items: "data", Model: "id", Input: "input", Output: "output", Success: "success"}}
	credential := model.PriceSourceCredential{Token: "fixture-source-token", UserID: 2752, PricingURL: source.URL}
	raw, err := common.Marshal(credential)
	require.NoError(t, err)
	key := "channel_price_source." + source.ID + ".Secret"
	common.OptionMapRWMutex.Lock()
	if common.OptionMap == nil {
		common.OptionMap = map[string]string{}
	}
	previous, existed := common.OptionMap[key]
	common.OptionMap[key] = string(raw)
	common.OptionMapRWMutex.Unlock()
	oldClient := channelCostHTTP
	client := *oldClient
	client.Transport = server.Client().Transport
	channelCostHTTP = &client
	defer func() {
		channelCostHTTP = oldClient
		common.OptionMapRWMutex.Lock()
		if existed {
			common.OptionMap[key] = previous
		} else {
			delete(common.OptionMap, key)
		}
		common.OptionMapRWMutex.Unlock()
	}()
	parsed, err := FetchPriceSource(t.Context(), source, "")
	require.NoError(t, err)
	assert.Equal(t, 2.0, parsed.Prices["m"].OffPeak.Input)
	assert.Equal(t, 1, requests)
	changed := source
	changed.URL = server.URL + "/other"
	_, err = FetchPriceSource(t.Context(), changed, "")
	require.Error(t, err)
	assert.Equal(t, 1, requests, "a preview URL edit must not forward a saved credential")
	redirected := source
	redirected.URL = server.URL + "/redirect"
	_, err = FetchPriceSource(t.Context(), redirected, "", credential)
	require.Error(t, err)
	assert.Equal(t, 2, requests, "redirects must stop before forwarding credentials")
	insecure := source
	insecure.URL = "http://example.com/pricing"
	_, err = FetchPriceSource(t.Context(), insecure, "", credential)
	require.Error(t, err)
	assert.Equal(t, 2, requests)
	crossOrigin := source
	crossOrigin.ChannelOrigin = "https://different-upstream.example"
	_, err = FetchPriceSource(t.Context(), crossOrigin, "", credential)
	require.ErrorContains(t, err, "bound origin")
	assert.Equal(t, 2, requests)
}

func TestFixedUpstreamPricesNormalizeEveryRequestModel(t *testing.T) {
	source := channelcost.DefaultSources()["happyapi"]
	raw := []byte(`{"success":true,"group_ratio":{"default":1,"five":5},"data":[{"model_name":"grok-4-fast-ci","quota_type":1,"model_price":0.025,"enable_groups":["default","five"]},{"model_name":"grok-4-ci","quota_type":1,"model_price":0.5,"enable_groups":["default","five"]},{"model_name":"some-other-request-model","quota_type":1,"model_price":0.125,"enable_groups":["default","five"]},{"model_name":"already-expression","quota_type":1,"model_price":99,"billing_mode":"tiered_expr","billing_expr":"tier(\"request\", fixed(0.025))","enable_groups":["default","five"]},{"model_name":"free-request","quota_type":1,"model_price":0,"enable_groups":["default","five"]},{"model_name":"missing-price","quota_type":1,"enable_groups":["default","five"]},{"model_name":"token-missing-fields","quota_type":0,"model_price":0,"enable_groups":["default","five"]}]}`)
	for _, group := range []string{"default", "five"} {
		parsed, err := ParsePriceSource(raw, source, group, 500000)
		require.NoError(t, err)
		require.Len(t, parsed.Prices, 5)
		assert.NotEmpty(t, parsed.Errors["missing-price"])
		assert.NotEmpty(t, parsed.Errors["token-missing-fields"], "a stale zero model_price cannot convert a token model into a free request")
		expected := map[string]float64{"grok-4-fast-ci": .005, "grok-4-ci": .1, "some-other-request-model": .025, "already-expression": .005, "free-request": 0}
		for name, cny := range expected {
			for _, fx := range []float64{1, 7} {
				now := time.Now()
				snapshot := &channelcost.Snapshot{Fingerprint: "bound", SyncedAt: now.Unix(), GroupRatio: parsed.GroupRatio, Prices: parsed.Prices}
				receipt, expression, err := snapshot.Resolve(&dto.ChannelCostSettings{Enabled: true, Source: "happyapi", CreditsPerCNY: 5, UpstreamGroup: group}, name, "bound", now, fx)
				require.NoError(t, err)
				require.NotNil(t, receipt.CNYRequestPrice)
				assert.InDelta(t, cny*parsed.GroupRatio, *receipt.CNYRequestPrice, 1e-12)
				for _, tokens := range []float64{0, 1000000} {
					result, trace, err := billingexpr.RunExpr(expression, billingexpr.TokenParams{P: tokens, C: tokens, Len: tokens})
					require.NoError(t, err)
					assert.Equal(t, billingexpr.BillingUnitRequest, trace.BillingUnit)
					assert.InDelta(t, cny*parsed.GroupRatio, result/1e6*fx, 1e-12, "fixed prices are per request and independent of token counts")
				}
			}
		}
	}
	generic := source
	generic.Format = "json"
	generic.StatusURL = ""
	generic.Fields = channelcost.SourceFields{Items: "models", Model: "id", RequestPrice: "pricing.call", QuotaType: "kind"}
	require.NoError(t, generic.Validate())
	parsed, err := ParsePriceSource([]byte(`{"models":[{"id":"third-provider","kind":"request","pricing":{"call":"0.03"}}]}`), generic, "", 500000)
	require.NoError(t, err)
	require.NotNil(t, parsed.Prices["third-provider"].RequestPrice)
	assert.Equal(t, .03, *parsed.Prices["third-provider"].RequestPrice)
}

func TestFixedUpstreamExpressionRejectsInvalidCharges(t *testing.T) {
	for _, expression := range []string{`tier("request", fixed(-1))`, `tier("request", fixed(1e308))`, `tier("request", fixed(header("price")))`} {
		_, err := channelcost.ParseRequestPrice(expression)
		require.Error(t, err)
	}
	amount, err := channelcost.ParseRequestPrice(`v1:tier("any label", fixed(0))`)
	require.NoError(t, err)
	require.NotNil(t, amount)
	assert.Zero(t, *amount)
}

func sub2APITestSource() channelcost.Source {
	return channelcost.Source{ID: "sub2api-test", Name: "Sub2API", Enabled: true, Format: "sub2api", URL: "https://upstream.example/api/v1/model-plaza", ChannelOrigin: "https://upstream.example", UnitTokens: 1, InputScale: 1, Fields: channelcost.SourceFields{Items: "data.groups", Groups: "models", GroupRatios: "rate_multiplier", Model: "name", Input: "pricing.input_price", Output: "pricing.output_price", CacheRead: "pricing.cache_read_price", CacheWrite: "pricing.cache_write_price", CacheWrite1h: "pricing.cache_write_1h_price", RequestPrice: "pricing.per_request_price", BillingMode: "pricing.billing_mode"}}
}

func TestSub2APIObservedDeepSeekCosts(t *testing.T) {
	// Model aggregates from an authenticated POJIA usage query on 2026-10-16.
	// Flash reference rates reproduce these receipts; aggregates do not prove
	// unique unit prices. Pro's full reference rates were confirmed by the user;
	// its observed receipts exercise only cache reads. These fixtures must not
	// become a live price source or imply verification of peak-time behavior.
	for _, tc := range []struct {
		model                    string
		input, output, cacheRead int
		standard, actual         float64
		rates                    channelcost.Rates
	}{
		{"deepseek-v4.1-flash", 170, 765, 20075, .0036315, .00029052, channelcost.Rates{Input: 1, Output: 4, CacheRead: .02}},
		{"deepseek-v4-flash", 346, 4, 6210, .0004862, .000038896, channelcost.Rates{Input: 1, Output: 4, CacheRead: .02}},
		{"deepseek-v4-pro", 0, 0, 5091, .00076365, .000061092, channelcost.Rates{Input: 4.5, Output: 13.5, CacheRead: .15}},
	} {
		t.Run(tc.model, func(t *testing.T) {
			assert.InDelta(t, tc.actual, tc.standard*.08, 1e-15)
			now := time.Date(2026, 10, 16, 11, 51, 0, 0, time.UTC)
			snapshot := &channelcost.Snapshot{
				Fingerprint: "receipt-only", SyncedAt: now.Unix(), GroupRatio: .08,
				Prices: map[string]channelcost.Price{tc.model: {UpstreamModel: tc.model, OffPeak: tc.rates}},
			}
			cfg := &dto.ChannelCostSettings{Enabled: true, Source: "pojia", CreditsPerCNY: 1, UpstreamGroup: "POJIAAI-国模破甲"}
			for _, fx := range []float64{1, 7} {
				receipt, expression, err := snapshot.Resolve(cfg, tc.model, "receipt-only", now, fx)
				require.NoError(t, err)
				assert.Equal(t, .08, receipt.GroupRatio)
				assert.Equal(t, 1.0, receipt.CreditsPerCNY)
				// Sub2API's usage report separates uncached input from cache reads.
				// OpenAI-format relay usage includes cache in prompt_tokens.
				usage := &dto.Usage{PromptTokens: tc.input + tc.cacheRead, CompletionTokens: tc.output}
				usage.PromptTokensDetails.CachedTokens = tc.cacheRead
				hash := billingexpr.ExprHashString(expression)
				params := BuildTieredTokenParams(usage, false, billingexpr.UsedVarsByHash(expression, hash))
				assert.EqualValues(t, tc.input, params.P, "cache reads must not be charged again as ordinary input")
				raw, _, err := billingexpr.RunExprByHashWithRequest(expression, hash, params, billingexpr.RequestInput{})
				require.NoError(t, err)
				assert.InDelta(t, tc.actual, raw/1e6*fx, 1e-15, "CNY cost must match the receipt without adding an FX or second group multiplier")
			}
		})
	}
}

func TestSub2APIPricesUseExactGroupAndActualChannelPrices(t *testing.T) {
	source := sub2APITestSource()
	require.NoError(t, source.Validate())
	body := []byte(`{"code":0,"data":{"groups":[{"name":"POJIAAI-国模破甲","rate_multiplier":0.08,"models":[
 {"name":"same-model","pricing":{"billing_mode":"token","input_price":0.000002,"output_price":0.000008,"cache_read_price":0.0000005,"cache_write_price":0.0000025,"cache_write_1h_price":0.000004},"official_pricing":{"input_price":99,"output_price":99}},
 {"name":"request-model","pricing":{"billing_mode":"per_request","per_request_price":0.025}},
 {"name":"free-request","pricing":{"billing_mode":"per_request","per_request_price":0}},
 {"name":"missing-cost","pricing":null,"official_pricing":{"input_price":0.000001,"output_price":0.000004}},
 {"name":"context-model","long_context_basis":"whole_request","pricing":{"billing_mode":"token","input_price":0.000002,"output_price":0.000008,"cache_write_price":0.0000025,"cache_write_1h_price":0.000004,"intervals":[{"min_tokens":0,"max_tokens":100000},{"min_tokens":100000,"max_tokens":null,"input_multiplier":2,"output_price":0.000012,"cache_write_price":0.000006}]}}
 ]},{"name":"other","rate_multiplier":1,"models":[{"name":"same-model","pricing":{"billing_mode":"token","input_price":1,"output_price":2}}]}]}}`)
	result, err := ParsePriceSource(body, source, "POJIAAI-国模破甲", 500000)
	require.NoError(t, err)
	assert.Equal(t, .08, result.GroupRatio)
	assert.Len(t, result.Prices, 4)
	assert.Contains(t, result.Errors, "missing-cost")
	assert.Equal(t, 2.0, result.Prices["same-model"].OffPeak.Input)
	for _, fx := range []float64{1, 7} {
		now := time.Now()
		snapshot := &channelcost.Snapshot{Fingerprint: "sub2api", SyncedAt: now.Unix(), GroupRatio: result.GroupRatio, Prices: result.Prices, Errors: result.Errors}
		cfg := &dto.ChannelCostSettings{Enabled: true, Source: source.ID, CreditsPerCNY: 1, UpstreamGroup: "POJIAAI-国模破甲"}
		receipt, expression, err := snapshot.Resolve(cfg, "same-model", "sub2api", now, fx)
		require.NoError(t, err)
		assert.InDelta(t, .16, receipt.CNY.Input, 1e-12)
		assert.InDelta(t, .64, receipt.CNY.Output, 1e-12)
		raw, _, err := billingexpr.RunExprByHashWithRequest(expression, billingexpr.ExprHashString(expression), billingexpr.TokenParams{P: 1000000, C: 1000000, Len: 1000000}, billingexpr.RequestInput{})
		require.NoError(t, err)
		assert.InDelta(t, .8, raw/1e6*fx, 1e-12)
		request, _, err := snapshot.Resolve(cfg, "request-model", "sub2api", now, fx)
		require.NoError(t, err)
		assert.InDelta(t, .002, *request.CNYRequestPrice, 1e-12)
		free, _, err := snapshot.Resolve(cfg, "free-request", "sub2api", now, fx)
		require.NoError(t, err)
		assert.Zero(t, *free.CNYRequestPrice)
		tiers, _, err := snapshot.Resolve(cfg, "context-model", "sub2api", now, fx)
		require.NoError(t, err)
		require.Len(t, tiers.Tiers, 2)
		assert.EqualValues(t, 100000, tiers.Tiers[0].MaxContext)
		assert.InDelta(t, .32, tiers.Tiers[1].Rates.Input, 1e-12)
		assert.InDelta(t, .96, tiers.Tiers[1].Rates.Output, 1e-12)
		assert.InDelta(t, .48, tiers.Tiers[1].Rates.CacheWrite1h, 1e-12)
	}
	_, err = ParsePriceSource(body, source, "default", 500000)
	require.Error(t, err)
}

func TestSub2APIRejectsAmbiguousAndUnsupportedPriceRules(t *testing.T) {
	source := sub2APITestSource()
	for _, tc := range []struct {
		name, body string
		global     bool
	}{
		{"duplicate group", `{"data":{"groups":[{"name":"g"},{"name":"g"}]}}`, true},
		{"negative rate", `{"data":{"groups":[{"name":"g","rate_multiplier":-0.08,"models":[]}]}}`, true},
		{"marginal tiers", `{"data":{"groups":[{"name":"g","rate_multiplier":0.08,"models":[{"name":"m","long_context_basis":"marginal","pricing":{"billing_mode":"token","input_price":1,"output_price":2}}]}]}}`, false},
		{"scheduled rate", `{"data":{"groups":[{"name":"g","rate_multiplier":0.08,"peak_rate_enabled":true,"models":[]}]}}`, true},
		{"missing channel price", `{"data":{"groups":[{"name":"g","rate_multiplier":0.08,"models":[{"name":"m","pricing":null,"official_pricing":{"input_price":1,"output_price":2}}]}]}}`, false},
		{"duplicate model", `{"data":{"groups":[{"name":"g","rate_multiplier":0.08,"models":[{"name":"m","pricing":{"billing_mode":"token","input_price":1,"output_price":2}},{"name":"m","pricing":{"billing_mode":"token","input_price":3,"output_price":4}}]}]}}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			prices, err := ParsePriceSource([]byte(tc.body), source, "g", 500000)
			if tc.global {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Empty(t, prices.Prices)
			assert.Contains(t, prices.Errors, "m")
		})
	}
}

func TestSub2APIEffectiveRateUsesOwnKeyAndRejectsRedirects(t *testing.T) {
	calls := 0
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls++ }))
	defer target.Close()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/v1/sub2api/billing", r.URL.Path)
		switch r.Header.Get("Authorization") {
		case "Bearer own-key":
			_, _ = w.Write([]byte(`{"object":"sub2api.key_billing","schema_version":1,"billing_scope":"token","group_rate_multiplier":0.08,"effective_rate_multiplier":0.04}`))
		case "Bearer redirect-key":
			http.Redirect(w, r, target.URL, http.StatusFound)
		default:
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte("private-key echo"))
		}
	}))
	defer server.Close()
	channel := &model.Channel{BaseURL: common.GetPointer(server.URL), Key: "own-key"}
	value, err := Sub2APIEffectiveRate(context.Background(), channel, .08)
	require.NoError(t, err)
	assert.Equal(t, .04, value)
	_, err = Sub2APIEffectiveRate(context.Background(), channel, 1)
	require.ErrorContains(t, err, "does not match")
	channel.Key = "redirect-key"
	_, err = Sub2APIEffectiveRate(context.Background(), channel, .08)
	require.Error(t, err)
	assert.Zero(t, calls)
	channel.Key = "private-key"
	_, err = Sub2APIEffectiveRate(context.Background(), channel, .08)
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "private-key")
	channel.BaseURL = common.GetPointer("http://upstream.invalid")
	_, err = Sub2APIEffectiveRate(context.Background(), channel, .08)
	require.ErrorContains(t, err, "HTTPS")
}

func TestSyncSub2APICostUsesAuthenticatedMultiplierAndPreservesLastSnapshot(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "sub2api.db")), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.Channel{}, &model.Option{}))
	oldDB, oldHTTP := model.DB, channelCostHTTP
	common.OptionMapRWMutex.Lock()
	oldOptions := common.OptionMap
	common.OptionMap = map[string]string{}
	common.OptionMapRWMutex.Unlock()
	model.DB = db
	channelCostHTTP = &http.Client{}
	t.Cleanup(func() {
		model.DB = db
		connection, e := db.DB()
		require.NoError(t, e)
		require.NoError(t, connection.Close())
		model.DB = oldDB
		channelCostHTTP = oldHTTP
		common.OptionMapRWMutex.Lock()
		common.OptionMap = oldOptions
		common.OptionMapRWMutex.Unlock()
	})
	enabled := true
	effectiveRate := .04
	referenceHTML, err := os.ReadFile("testdata/deepseek_cny_pricing.html")
	require.NoError(t, err)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/reference-price":
			assert.Empty(t, r.Header.Get("Authorization"), "official references never receive model keys")
			_, _ = w.Write(referenceHTML)
		case "/api/v1/model-plaza":
			assert.Empty(t, r.Header.Get("Authorization"), "a public source never receives the channel key")
			if !enabled {
				w.WriteHeader(http.StatusNotFound)
				_, _ = w.Write([]byte(`{"code":404,"message":"Model plaza is not enabled"}`))
				return
			}
			_, _ = w.Write([]byte(`{"code":0,"data":{"groups":[{"name":"g","rate_multiplier":0.08,"models":[{"name":"m","pricing":{"billing_mode":"token","input_price":0.000002,"output_price":0.000008}}]}]}}`))
		case "/v1/sub2api/billing":
			assert.Equal(t, "Bearer own-model-key", r.Header.Get("Authorization"))
			_, _ = fmt.Fprintf(w, `{"object":"sub2api.key_billing","schema_version":1,"billing_scope":"token","group_rate_multiplier":0.08,"effective_rate_multiplier":%g}`, effectiveRate)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	source := sub2APITestSource()
	source.ChannelOrigin = server.URL
	source.URL = server.URL + "/api/v1/model-plaza"
	_, version, err := model.GetChannelPriceSources()
	require.NoError(t, err)
	require.NoError(t, model.SaveChannelPriceSource(source, version))
	channel := &model.Channel{Type: constant.ChannelTypeSub2API, Name: "own source", BaseURL: common.GetPointer(server.URL), Key: "own-model-key", Models: "m"}
	channel.SetOtherSettings(dto.ChannelOtherSettings{CostBilling: &dto.ChannelCostSettings{Enabled: true, Source: source.ID, CreditsPerCNY: 1, UpstreamGroup: "g"}})
	require.NoError(t, db.Create(channel).Error)
	snapshot, err := SyncChannelCost(context.Background(), channel.Id)
	require.NoError(t, err)
	assert.Equal(t, .04, snapshot.GroupRatio)
	assert.Len(t, snapshot.Prices, 1)
	stored := model.GetChannelCostSnapshot(channel.Id)
	enabled = false
	_, err = SyncChannelCost(context.Background(), channel.Id)
	require.ErrorContains(t, err, "model plaza is unavailable")
	assert.Equal(t, stored, model.GetChannelCostSnapshot(channel.Id))

	// Explicit reference pricing is scoped to the selected channel and exact
	// mapped names. It is never an implicit fallback when model plaza fails.
	effectiveRate = .08
	reference := channelcost.Source{ID: "confirmed-reference", Name: "Confirmed DeepSeek agreement", Enabled: true, Format: "deepseek_html", URL: server.URL + "/reference-price", ChannelOrigin: server.URL, UnitTokens: 1e6, InputScale: 1, Reference: &channelcost.ReferencePricing{GroupRatio: .08, Models: map[string]string{"m": "deepseek-v4-pro"}, Schedule: "deepseek", LocalGroupBilling: true}}
	_, version, err = model.GetChannelPriceSources()
	require.NoError(t, err)
	require.NoError(t, model.SaveChannelPriceSource(reference, version))
	other := &model.Channel{Type: constant.ChannelTypeSub2API, Name: "other same model", BaseURL: common.GetPointer(server.URL), Key: "other-key", Models: "m"}
	require.NoError(t, db.Create(other).Error)
	require.NoError(t, model.SaveChannelCostSnapshot(other.Id, stored))
	otherBefore := model.GetChannelCostSnapshot(other.Id)
	channel.Models = "m,gpt-unconfigured"
	settings := channel.GetOtherSettings()
	settings.CostBilling.Source = reference.ID
	channel.SetOtherSettings(settings)
	require.NoError(t, db.Save(channel).Error)
	snapshot, err = SyncChannelCost(context.Background(), channel.Id)
	require.NoError(t, err)
	assert.Equal(t, .08, snapshot.GroupRatio)
	assert.Len(t, snapshot.Prices, 1)
	assert.NotContains(t, snapshot.Prices, "gpt-unconfigured")
	assert.Equal(t, otherBefore, model.GetChannelCostSnapshot(other.Id), "same model on another channel must remain unchanged")
	for _, tc := range []struct {
		at, tier             string
		input, output, cache float64
	}{
		{"2026-10-16T10:00:00+08:00", "peak", .72, 2.16, .024},
		{"2026-10-16T12:00:00+08:00", "off_peak", .36, 1.08, .012},
		{"2026-10-17T10:00:00+08:00", "off_peak", .36, 1.08, .012},
		{"2026-10-01T10:00:00+08:00", "off_peak", .36, 1.08, .012},
	} {
		at, e := time.Parse(time.RFC3339, tc.at)
		require.NoError(t, e)
		copy := *snapshot
		copy.SyncedAt = at.Unix()
		receipt, _, e := copy.Resolve(settings.CostBilling, "m", model.ChannelCostFingerprint(channel), at, 1)
		require.NoError(t, e)
		assert.Equal(t, tc.tier, receipt.Tier)
		assert.InDelta(t, tc.input, receipt.CNY.Input, 1e-12)
		assert.InDelta(t, tc.output, receipt.CNY.Output, 1e-12)
		assert.InDelta(t, tc.cache, receipt.CNY.CacheRead, 1e-12)
	}
	reference.Reference.Schedule = "off_peak"
	flat, err := FetchPriceSource(context.Background(), reference, "g")
	require.NoError(t, err)
	assert.Nil(t, flat.Prices["m"].Peak)
	reference.Reference.GroupRatio = 0
	require.Error(t, reference.Validate())
}

// A video unit or a multiple-request display must never become a flat request charge.
func TestPriceSourceFiltersKeepVideoUnitsDistinct(t *testing.T) {
	source := channelcost.DefaultSources()["happyapi"]
	source.AdvancedPricing = true
	raw := []byte(`{"success":true,"group_ratio":{"default":1},"data":[{"model_name":"image","quota_type":1,"model_price":0.3,"request_price_display_unit":"request","request_price_units":1,"enable_groups":["default"]},{"model_name":"video","is_video_model":1,"quota_type":1,"model_price":1,"request_price_display_unit":"second","request_price_units":1,"enable_groups":["default"]},{"model_name":"batch","quota_type":1,"model_price":0.02,"request_price_display_unit":"request","request_price_units":4,"enable_groups":["default"]}]}`)
	source.FilterField = "is_video_model"
	source.FilterValue = "1"
	source.FilterExclude = true
	require.NoError(t, source.Validate())
	parsed, err := ParsePriceSource(raw, source, "default", 500000)
	require.NoError(t, err)
	require.Len(t, parsed.Prices, 2)
	require.Equal(t, .3, *parsed.Prices["image"].RequestPrice)
	require.Equal(t, .08, *parsed.Prices["batch"].RequestPrice)
	require.Equal(t, 4.0, parsed.Prices["batch"].ChargeUnit.Units)
	require.Empty(t, parsed.Errors)
	source.FilterExclude = false
	parsed, err = ParsePriceSource(raw, source, "default", 500000)
	require.NoError(t, err)
	require.Len(t, parsed.Prices, 1)
	require.Empty(t, parsed.Errors)
	require.True(t, parsed.Prices["video"].Dynamic.Task)
	legacy := source
	legacy.AdvancedPricing = false
	old, err := ParsePriceSource(raw, legacy, "default", 500000)
	require.NoError(t, err)
	require.Empty(t, old.Prices)
	require.NotEmpty(t, old.Errors["video"], "unrelated existing sources keep their previous behavior")
	source.FilterField = "is_video_model.#"
	require.Error(t, source.Validate())
}

func TestComplexUpstreamExpressionsPreserveUnitsAndCNYCost(t *testing.T) {
	source := channelcost.DefaultSources()["happyapi"]
	source.AdvancedPricing = true
	schema := map[string]any{"seconds": map[string]any{"type": "number", "unit": "second"}, "resolution": map[string]any{"enum": []string{"720p", "1080p"}}}
	for _, tc := range []struct {
		name, expression string
		params           billingexpr.TokenParams
		request          billingexpr.RequestInput
		expected         float64
		task             bool
	}{
		{"weekday peak", `weekday("Asia/Shanghai") >= 1 && weekday("Asia/Shanghai") <= 5 && hour("Asia/Shanghai") >= 9 && hour("Asia/Shanghai") < 12 ? tier("peak", p * 9 + c * 27 + cr * 0.3) : tier("idle", p * 4.5 + c * 13.5 + cr * 0.15)`, billingexpr.TokenParams{P: 1e6, C: 1e6, CR: 1e6}, billingexpr.RequestInput{Now: time.Date(2026, 10, 20, 2, 0, 0, 0, time.UTC)}, 36.3, false},
		{"weekend idle", `weekday("Asia/Shanghai") >= 1 && weekday("Asia/Shanghai") <= 5 ? tier("peak",p*9+c*27) : tier("idle",p*4.5+c*13.5)`, billingexpr.TokenParams{P: 1e6, C: 1e6}, billingexpr.RequestInput{Now: time.Date(2026, 10, 17, 2, 0, 0, 0, time.UTC)}, 18, false},
		{"context and cache", `param("cache") == "explicit" ? (len <= 256000 ? tier("short",p*2+c*8+cr*0.2+cc*2.5) : tier("long",p*6+c*24+cr*0.6+cc*7.5)) : tier("implicit",p*2+c*8+cr*.4+cc*2)`, billingexpr.TokenParams{P: 100, C: 300, Len: 300000, CR: 299900, CC: 10}, billingexpr.RequestInput{Body: []byte(`{"cache":"explicit"}`)}, .187815, false},
		{"image quantities", `tier("image",fixed(0.2))*image_count`, billingexpr.TokenParams{}, billingexpr.RequestInput{ImageCount: new(3)}, .6, false},
		{"per-second resolution", `u("resolution") == "720p" ? tier("720p",u("seconds")*1) : tier("1080p",u("seconds")*2.48)`, billingexpr.TokenParams{}, billingexpr.RequestInput{Usage: map[string]any{"seconds": 5.0, "resolution": "1080p"}}, 12.4, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			row := map[string]any{"model_name": "m", "billing_mode": "tiered_expr", "billing_expr": tc.expression, "billing_usage_schema": schema, "enable_groups": []string{"discount"}}
			raw, err := common.Marshal(map[string]any{"success": true, "group_ratio": map[string]float64{"discount": .5}, "data": []any{row}})
			require.NoError(t, err)
			parsed, err := ParsePriceSource(raw, source, "discount", 500000)
			require.NoError(t, err)
			require.Empty(t, parsed.Errors)
			require.NotNil(t, parsed.Prices["m"].Dynamic)
			now := time.Now()
			snapshot := channelcost.Snapshot{Fingerprint: "f", SyncedAt: now.Unix(), GroupRatio: parsed.GroupRatio, Prices: parsed.Prices}
			receipt, expression, err := snapshot.Resolve(&dto.ChannelCostSettings{Enabled: true, Source: "happyapi", UpstreamGroup: "discount", CreditsPerCNY: 5}, "m", "f", now, 7)
			require.NoError(t, err)
			require.True(t, receipt.Dynamic)
			assert.Equal(t, tc.task, receipt.TaskUsageBilling)
			amount, _, err := billingexpr.RunExprWithRequest(expression, tc.params, tc.request)
			require.NoError(t, err)
			divisor := 1e6
			if tc.task {
				divisor = 1
			}
			assert.InDelta(t, tc.expected*.5/5, amount/divisor*7, 1e-10)
			settled, err := billingexpr.ComputeTieredQuotaWithRequest(&billingexpr.BillingSnapshot{ExprString: expression, ExprHash: billingexpr.ExprHashString(expression), GroupRatio: 1, QuotaPerUnit: 500000, TaskUsageBilling: tc.task, ProcurementCost: true}, tc.params, tc.request)
			require.NoError(t, err)
			assert.Equal(t, common.QuotaFromFloat(tc.expected*.5/5/7*500000), settled.ActualQuotaAfterGroup)
			if !tc.task && billingexpr.UsesFixedPricing(expression) {
				taskExpr, err := channelcost.TaskExpression(expression)
				require.NoError(t, err)
				r, _, err := billingexpr.RunExprWithRequest(taskExpr, billingexpr.TokenParams{}, billingexpr.RequestInput{Usage: map[string]any{"image_count": 3.0}})
				require.NoError(t, err)
				assert.InDelta(t, tc.expected*.5/5, r*7, 1e-10)
			}
		})
	}
	// Unsafe branches remain invalid even when a different branch would execute.
	for _, expression := range []string{`true ? tier("ok",p*2) : tier("bad",p*-1)`, `tier("bad",p/0)`, `tier("bad",p*header("coefficient"))`, `tier("bad",u(param("key"))*2)`, `tier("bad",sum(1..100000000))`, `tier("bad",u("seconds")*p)`} {
		raw, err := common.Marshal(map[string]any{"success": true, "group_ratio": map[string]float64{"default": 1}, "data": []any{map[string]any{"model_name": "m", "enable_groups": []string{"default"}, "billing_mode": "tiered_expr", "billing_expr": expression, "billing_usage_schema": schema}}})
		require.NoError(t, err)
		result, err := ParsePriceSource(raw, source, "default", 500000)
		require.NoError(t, err)
		assert.Empty(t, result.Prices, expression)
		assert.NotEmpty(t, result.Errors["m"], expression)
	}
}
