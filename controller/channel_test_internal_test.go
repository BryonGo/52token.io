package controller

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/billingexpr"
	"github.com/QuantumNous/new-api/pkg/channelcost"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/dto"
	relaytypes "github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting/model_setting"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	"github.com/QuantumNous/new-api/types"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestChannelTestCostGroupAndProtocol(t *testing.T) {
	for _, dialect := range []struct{ kind, env string }{{"sqlite", ""}, {"mysql", "TEST_MYSQL_DSN"}, {"postgres", "TEST_POSTGRES_DSN"}} {
		t.Run(dialect.kind, func(t *testing.T) {
			if dialect.env != "" && os.Getenv(dialect.env) == "" {
				t.Skip("set " + dialect.env)
			}
			db := modelManagementDB(t, dialect.kind, os.Getenv(dialect.env))
			require.NoError(t, db.AutoMigrate(&model.Log{}))
			oldGroups := ratio_setting.GroupRatio2JSONString()
			oldSpecial := ratio_setting.GroupGroupRatio2JSONString()
			oldPrice, oldFX, oldQuota := operation_setting.Price, operation_setting.USDExchangeRate, common.QuotaPerUnit
			oldTimeout := constant.StreamingTimeout
			constant.StreamingTimeout = 30
			settings := model_setting.GetGlobalSettings()
			oldPolicy, oldPass := settings.ChatCompletionsToResponsesPolicy, settings.PassThroughRequestEnabled
			t.Cleanup(func() {
				require.NoError(t, ratio_setting.UpdateGroupRatioByJSONString(oldGroups))
				require.NoError(t, ratio_setting.UpdateGroupGroupRatioByJSONString(oldSpecial))
				operation_setting.Price, operation_setting.USDExchangeRate, common.QuotaPerUnit = oldPrice, oldFX, oldQuota
				constant.StreamingTimeout = oldTimeout
				settings.ChatCompletionsToResponsesPolicy, settings.PassThroughRequestEnabled = oldPolicy, oldPass
			})
			operation_setting.Price, operation_setting.USDExchangeRate, common.QuotaPerUnit = 1, 1, 500000
			settings.PassThroughRequestEnabled = false
			require.NoError(t, ratio_setting.UpdateGroupGroupRatioByJSONString(`{}`))
			user := model.User{Username: "channel-test-admin", Group: "default", Role: common.RoleRootUser, Status: common.UserStatusEnabled, Quota: 1000000}
			require.NoError(t, db.Create(&user).Error)
			for _, tc := range []struct {
				name, endpoint, group  string
				policy, stream, reject bool
				ratio                  float64
				credits                float64
				channelType, quota     int
			}{
				{name: "chat uses channel group", ratio: .08},
				{name: "automatic uses configured Responses policy", policy: true, ratio: .08},
				{name: "chat streaming uses configured Responses policy", endpoint: string(constant.EndpointTypeOpenAI), policy: true, stream: true, ratio: .08},
				{name: "invalid channel group still rejects markup", ratio: 1, reject: true},
				{name: "HappyAPI recharge discount applies once", group: "x1 HappyAPI", ratio: 1, credits: 5, channelType: constant.ChannelTypeNewAPILegacy, quota: 4075},
				{name: "Aizhong recharge discount applies once", group: "x1 爱中转 Claude", ratio: 1, credits: 7, channelType: constant.ChannelTypeNewAPILegacy, quota: 2911},
				{name: "official DeepSeek remains one to one", group: "x1 DeepSeek", ratio: 1, credits: 1, channelType: constant.ChannelTypeDeepSeek, quota: 20379},
			} {
				t.Run(tc.name, func(t *testing.T) {
					group, channelType, credits, upstreamRatio, expectedQuota := "POJIA-test", constant.ChannelTypeSub2API, 1.0, .08, 1630
					if tc.credits > 0 {
						group, channelType, credits, upstreamRatio, expectedQuota = tc.group, tc.channelType, tc.credits, 1, tc.quota
					}
					var calls atomic.Int32
					server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						calls.Add(1)
						expectedPath := "/v1/chat/completions"
						if tc.policy {
							expectedPath = "/v1/responses"
						}
						assert.Equal(t, expectedPath, r.URL.Path)
						var request map[string]any
						require.NoError(t, common.DecodeJson(r.Body, &request))
						assert.Equal(t, "deepseek-v4-pro", request["model"])
						if tc.policy {
							assert.Equal(t, float64(16), request["max_output_tokens"])
							response := `{"id":"resp-test","object":"response","model":"deepseek-v4-pro","status":"completed","output":[{"id":"msg-test","type":"message","role":"assistant","content":[{"type":"output_text","text":"OK"}]}],"usage":{"input_tokens":10000,"output_tokens":8,"total_tokens":10008,"input_tokens_details":{"cached_tokens":1000}}}`
							if tc.stream {
								w.Header().Set("Content-Type", "text/event-stream")
								fmt.Fprintf(w, "event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":%s}\n\n", response)
							} else {
								w.Header().Set("Content-Type", "application/json")
								_, _ = w.Write([]byte(response))
							}
						} else {
							w.Header().Set("Content-Type", "application/json")
							_, _ = w.Write([]byte(`{"id":"chat-test","object":"chat.completion","model":"deepseek-v4-pro","choices":[{"index":0,"message":{"role":"assistant","content":"OK"},"finish_reason":"stop"}],"usage":{"prompt_tokens":10000,"completion_tokens":8,"total_tokens":10008,"prompt_tokens_details":{"cached_tokens":1000}}}`))
						}
					}))
					defer server.Close()
					channel := model.Channel{Type: channelType, Status: common.ChannelStatusEnabled, Name: tc.name, Key: "test-only", BaseURL: &server.URL, Group: group, Models: "deepseek-v4-pro"}
					channel.SetOtherSettings(dto.ChannelOtherSettings{CostBilling: &dto.ChannelCostSettings{Enabled: true, Source: "pojia-test", CreditsPerCNY: credits, UpstreamGroup: "upstream"}})
					require.NoError(t, db.Create(&channel).Error)
					groups, err := common.Marshal(map[string]float64{"default": 1, group: tc.ratio})
					require.NoError(t, err)
					require.NoError(t, ratio_setting.UpdateGroupRatioByJSONString(string(groups)))
					source := channelcost.Source{ID: "pojia-test", Name: "Confirmed reference", Enabled: true, Format: "deepseek_html", URL: channelcost.DeepSeekURL, ChannelOrigin: "https://upstream.example", UnitTokens: 1e6, InputScale: 1, Reference: &channelcost.ReferencePricing{LocalGroupBilling: true, GroupRatio: .08, Models: map[string]string{"deepseek-v4-pro": "deepseek-v4-pro"}, Schedule: "off_peak"}}
					if tc.credits > 0 {
						source.Reference = nil
					}
					encoded, err := common.Marshal(map[string]channelcost.Source{source.ID: source})
					require.NoError(t, err)
					common.OptionMap["channel_price_sources"] = string(encoded)
					snapshot := channelcost.Snapshot{Fingerprint: model.ChannelCostFingerprint(&channel), SyncedAt: time.Now().Unix(), GroupRatio: upstreamRatio, Prices: map[string]channelcost.Price{"deepseek-v4-pro": {UpstreamModel: "deepseek-v4-pro", OffPeak: channelcost.Rates{Input: 4.5, Output: 13.5, CacheRead: .15}}}}
					encoded, err = common.Marshal(snapshot)
					require.NoError(t, err)
					common.OptionMap[fmt.Sprintf("channel_cost.%d", channel.Id)] = string(encoded)
					settings.ChatCompletionsToResponsesPolicy = model_setting.ChatCompletionsToResponsesPolicy{Enabled: tc.policy, ChannelIDs: []int{channel.Id}, ModelPatterns: []string{"^deepseek-v4-pro$"}}
					result := testChannel(t.Context(), &channel, user.Id, "deepseek-v4-pro", tc.endpoint, tc.stream)
					if tc.reject {
						require.ErrorContains(t, result.localErr, "exceeds the upstream cost")
						assert.Zero(t, calls.Load())
						return
					}
					require.NoError(t, result.localErr)
					assert.Equal(t, int32(1), calls.Load())
					assert.Equal(t, group, common.GetContextKeyString(result.context, constant.ContextKeyUsingGroup))
					assert.Equal(t, "default", common.GetContextKeyString(result.context, constant.ContextKeyUserGroup))
					var log model.Log
					require.NoError(t, db.Where("channel_id = ?", channel.Id).Last(&log).Error)
					assert.Equal(t, group, log.Group)
					assert.Equal(t, expectedQuota, log.Quota, "recharge discount and upstream group multiplier each apply once, including cached tokens")
				})
			}
		})
	}
}

func TestGetChannelDefaultBaseURLsUsesBuiltInDefaults(t *testing.T) {
	originalBaseURLs := constant.ChannelBaseURLs
	constant.ChannelBaseURLs = append([]string(nil), originalBaseURLs...)
	constant.ChannelBaseURLs[constant.ChannelTypeDeepSeek] = "https://deepseek.server.example"
	t.Cleanup(func() {
		constant.ChannelBaseURLs = originalBaseURLs
	})

	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/channel/default_base_urls", nil)
	GetChannelDefaultBaseURLs(c)

	require.Equal(t, http.StatusOK, recorder.Code)
	var response struct {
		Success bool           `json:"success"`
		Data    map[int]string `json:"data"`
	}
	require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &response))
	require.True(t, response.Success)
	assert.Equal(t, "https://deepseek.server.example", response.Data[constant.ChannelTypeDeepSeek])
	assert.Equal(t, "https://api.openai.com", response.Data[constant.ChannelTypeOpenAI])
	assert.NotContains(t, response.Data, constant.ChannelTypeAzure)
	assert.NotContains(t, response.Data, constant.ChannelTypeNewAPI)
	assert.NotContains(t, response.Data, constant.ChannelTypeTaskPlugin)
}

func TestValidateChannelProxy(t *testing.T) {
	tests := []struct {
		name    string
		proxy   string
		wantErr bool
	}{
		{name: "empty"},
		{name: "http", proxy: "http://proxy.example:8080"},
		{name: "https", proxy: "https://proxy.example:8443"},
		{name: "socks5", proxy: "socks5://proxy.example"},
		{name: "socks5h", proxy: "socks5h://proxy.example:1080/"},
		{name: "unsupported", proxy: "ftp://proxy.example", wantErr: true},
		{name: "path", proxy: "socks5://proxy.example:1080/path", wantErr: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			setting, err := common.Marshal(dto.ChannelSettings{Proxy: test.proxy})
			require.NoError(t, err)
			channel := &model.Channel{
				Type:    constant.ChannelTypeOpenAI,
				Setting: common.GetPointer(string(setting)),
			}

			err = validateChannel(channel, false)

			if test.wantErr {
				require.ErrorContains(t, err, "invalid channel proxy")
				return
			}
			require.NoError(t, err)
		})
	}
}

func TestValidateChannelRequiresNewAPIBaseURL(t *testing.T) {
	tests := []struct {
		name    string
		baseURL *string
		wantErr bool
	}{
		{name: "missing", wantErr: true},
		{name: "blank", baseURL: common.GetPointer("  "), wantErr: true},
		{name: "configured", baseURL: common.GetPointer("https://new-api.example")},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			channel := &model.Channel{
				Type:    constant.ChannelTypeNewAPI,
				BaseURL: test.baseURL,
			}

			err := validateChannel(channel, false)

			if test.wantErr {
				require.ErrorContains(t, err, "New API channel base URL cannot be empty")
				return
			}
			require.NoError(t, err)
		})
	}
}

func TestNewAPIChannelRegistration(t *testing.T) {
	apiType, ok := common.ChannelType2APIType(constant.ChannelTypeNewAPI)

	require.True(t, ok)
	assert.Equal(t, constant.APITypeNewAPI, apiType)
	assert.Equal(t, "New API", constant.GetChannelTypeName(constant.ChannelTypeNewAPI))
	require.Greater(t, len(constant.ChannelBaseURLs), constant.ChannelTypeNewAPI)
	assert.Empty(t, constant.ChannelBaseURLs[constant.ChannelTypeNewAPI])
}

func TestResponsesCompactChannelSupport(t *testing.T) {
	tests := []struct {
		name        string
		channelType int
		apiType     int
		want        bool
	}{
		{name: "OpenAI", channelType: constant.ChannelTypeOpenAI, apiType: constant.APITypeOpenAI, want: true},
		{name: "Azure", channelType: constant.ChannelTypeAzure, apiType: constant.APITypeOpenAI, want: true},
		{name: "Codex", channelType: constant.ChannelTypeCodex, apiType: constant.APITypeCodex, want: true},
		{name: "Advanced Custom", channelType: constant.ChannelTypeAdvancedCustom, apiType: constant.APITypeAdvancedCustom, want: true},
		{name: "Sub2API", channelType: constant.ChannelTypeSub2API, apiType: constant.APITypeSub2API, want: true},
		{name: "New API", channelType: constant.ChannelTypeNewAPI, apiType: constant.APITypeNewAPI, want: true},
		{name: "Anthropic", channelType: constant.ChannelTypeAnthropic, apiType: constant.APITypeAnthropic, want: false},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			assert.Equal(t, test.want, common.SupportsResponsesCompact(test.channelType, test.apiType))
		})
	}
}

func TestMultiprotocolGatewayEndpointTypes(t *testing.T) {
	want := []constant.EndpointType{
		constant.EndpointTypeOpenAI,
		constant.EndpointTypeOpenAIResponse,
		constant.EndpointTypeOpenAIResponseCompact,
		constant.EndpointTypeAnthropic,
		constant.EndpointTypeGemini,
		constant.EndpointTypeOpenAIAlphaSearch,
	}

	assert.Equal(t, want, common.GetEndpointTypesByChannelType(constant.ChannelTypeNewAPI, "gpt-5"))
	assert.Equal(t, want, common.GetEndpointTypesByChannelType(constant.ChannelTypeSub2API, "gpt-5"))
}

func TestCopyChannelRejectsInvalidLegacyProxySettings(t *testing.T) {
	db := setupModelListControllerTestDB(t)
	settingBytes, err := common.Marshal(dto.ChannelSettings{
		Proxy: "socks5://proxy.example/legacy-path",
	})
	require.NoError(t, err)
	setting := string(settingBytes)
	origin := &model.Channel{
		Type:    constant.ChannelTypeOpenAI,
		Name:    "legacy proxy channel",
		Key:     "test-key",
		Models:  "gpt-test",
		Group:   "default",
		Setting: &setting,
	}
	require.NoError(t, db.Create(origin).Error)

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Params = gin.Params{{Key: "id", Value: fmt.Sprintf("%d", origin.Id)}}
	ctx.Request = httptest.NewRequest(http.MethodPost, "/api/channel/copy", nil)

	CopyChannel(ctx)

	assert.Contains(t, recorder.Body.String(), "invalid channel settings")
	var channelCount int64
	require.NoError(t, db.Model(&model.Channel{}).Count(&channelCount).Error)
	assert.Equal(t, int64(1), channelCount)
}

func TestDeleteChannelResetsProxyCacheWhenPreReadFails(t *testing.T) {
	db := setupModelListControllerTestDB(t)
	require.NoError(t, db.AutoMigrate(&model.Log{}, &model.AuditLog{}))
	service.ResetProxyClientCache()
	t.Cleanup(service.ResetProxyClientCache)

	proxyURL := "http://proxy.example:8080"
	beforeDelete, err := service.GetHttpClientWithProxy(proxyURL)
	require.NoError(t, err)

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Params = gin.Params{{Key: "id", Value: "999999"}}
	ctx.Request = httptest.NewRequest(http.MethodDelete, "/api/channel/999999", nil)

	DeleteChannel(ctx)

	assert.Contains(t, recorder.Body.String(), `"success":true`)
	afterDelete, err := service.GetHttpClientWithProxy(proxyURL)
	require.NoError(t, err)
	assert.NotSame(t, beforeDelete, afterDelete)
}

func TestDeleteChannelBatchReportsAndAuditsActualDeletedCount(t *testing.T) {
	db := setupModelListControllerTestDB(t)
	require.NoError(t, db.AutoMigrate(&model.Log{}, &model.AuditLog{}))
	channel := &model.Channel{Name: "existing", Key: "test-key"}
	require.NoError(t, db.Create(channel).Error)

	requestBody, err := common.Marshal(ChannelBatch{Ids: []int{channel.Id, 999999}})
	require.NoError(t, err)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodDelete, "/api/channel/batch", bytes.NewReader(requestBody))
	ctx.Request.Header.Set("Content-Type", "application/json")

	DeleteChannelBatch(ctx)

	var response struct {
		Success bool  `json:"success"`
		Data    int64 `json:"data"`
	}
	require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &response))
	assert.True(t, response.Success)
	assert.Equal(t, int64(1), response.Data)

	var auditLog model.AuditLog
	require.NoError(t, db.Order("id desc").First(&auditLog).Error)
	var auditData struct {
		Operation struct {
			Params map[string]any `json:"params"`
		} `json:"op"`
	}
	encodedAudit, err := common.Marshal(auditLog.Other)
	require.NoError(t, err)
	require.NoError(t, common.Unmarshal(encodedAudit, &auditData))
	assert.Equal(t, float64(1), auditData.Operation.Params["count"])
}

func TestSettleTestQuotaUsesTieredBilling(t *testing.T) {
	info := &relaycommon.RelayInfo{
		TieredBillingSnapshot: &billingexpr.BillingSnapshot{
			BillingMode:   "tiered_expr",
			ExprString:    `param("stream") == true ? tier("stream", p * 3) : tier("base", p * 2)`,
			ExprHash:      billingexpr.ExprHashString(`param("stream") == true ? tier("stream", p * 3) : tier("base", p * 2)`),
			GroupRatio:    1,
			EstimatedTier: "stream",
			QuotaPerUnit:  common.QuotaPerUnit,
			ExprVersion:   1,
		},
		BillingRequestInput: &billingexpr.RequestInput{
			Body: []byte(`{"stream":true}`),
		},
	}

	quota, result := settleTestQuota(info, types.PriceData{
		ModelRatio:      1,
		CompletionRatio: 2,
	}, &dto.Usage{
		PromptTokens: 1000,
	})

	require.Equal(t, 1500, quota)
	require.NotNil(t, result)
	require.Equal(t, "stream", result.MatchedTier)
}

func TestBuildTestLogOtherInjectsTieredInfo(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())

	info := &relaycommon.RelayInfo{
		TieredBillingSnapshot: &billingexpr.BillingSnapshot{
			BillingMode: "tiered_expr",
			ExprString:  `tier("base", p * 2)`,
		},
		ChannelMeta: &relaycommon.ChannelMeta{},
	}
	priceData := types.PriceData{
		GroupRatioInfo: types.GroupRatioInfo{GroupRatio: 1},
	}
	usage := &dto.Usage{
		PromptTokensDetails: dto.InputTokenDetails{
			CachedTokens: 12,
		},
	}

	requestRules := []billingexpr.RequestRuleTrace{{
		Cond:       `param("service_tier") == "fast"`,
		Multiplier: 2,
		Matched:    true,
	}}
	other := buildTestLogOther(ctx, info, priceData, usage, &billingexpr.TieredResult{
		MatchedTier:  "base",
		RequestRules: requestRules,
	})

	fields := other.Snapshot()
	require.Equal(t, "tiered_expr", fields["billing_mode"])
	require.Equal(t, "base", fields["matched_tier"])
	require.Equal(t, requestRules, fields["request_rules"])
	require.NotEmpty(t, fields["expr_b64"])
}

func TestResolveChannelTestUserIDUsesRequestUser(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ctx.Set("id", 2)

	userID, err := resolveChannelTestUserID(ctx)

	require.NoError(t, err)
	require.Equal(t, 2, userID)
}

func TestSelectChannelsForAutomaticTestPassiveRecoveryOnlyUsesAutoDisabled(t *testing.T) {
	channels := []*model.Channel{
		{Id: 1, Status: common.ChannelStatusEnabled},
		{Id: 2, Status: common.ChannelStatusAutoDisabled},
		{Id: 3, Status: common.ChannelStatusManuallyDisabled},
	}

	selected := selectChannelsForAutomaticTest(channels, operation_setting.ChannelTestModePassiveRecovery)

	require.Len(t, selected, 1)
	require.Equal(t, 2, selected[0].Id)
}

func TestSelectChannelsForAutomaticTestScheduledSkipsManualDisabled(t *testing.T) {
	channels := []*model.Channel{
		{Id: 1, Status: common.ChannelStatusEnabled},
		{Id: 2, Status: common.ChannelStatusAutoDisabled},
		{Id: 3, Status: common.ChannelStatusManuallyDisabled},
	}

	selected := selectChannelsForAutomaticTest(channels, operation_setting.ChannelTestModeScheduledAll)

	require.Len(t, selected, 2)
	require.Equal(t, 1, selected[0].Id)
	require.Equal(t, 2, selected[1].Id)
}

func TestSelectChannelsForAutomaticTestAutoBanOnlyUsesEligibleChannels(t *testing.T) {
	autoBanEnabled := 1
	autoBanDisabled := 0
	channels := []*model.Channel{
		{Id: 1, Status: common.ChannelStatusEnabled, AutoBan: &autoBanEnabled},
		{Id: 2, Status: common.ChannelStatusEnabled, AutoBan: &autoBanDisabled},
		{Id: 3, Status: common.ChannelStatusAutoDisabled, AutoBan: &autoBanEnabled},
		{Id: 4, Status: common.ChannelStatusManuallyDisabled, AutoBan: &autoBanEnabled},
		{Id: 5, Status: common.ChannelStatusEnabled},
	}

	selected := selectChannelsForAutomaticTest(channels, operation_setting.ChannelTestModeAutoBanOnly)

	require.Len(t, selected, 2)
	require.Equal(t, 1, selected[0].Id)
	require.Equal(t, 3, selected[1].Id)
}

func TestRunChannelTestWorkersHonorsConfiguredConcurrency(t *testing.T) {
	originalInterval := common.RequestInterval
	common.RequestInterval = 0
	t.Cleanup(func() { common.RequestInterval = originalInterval })

	channels := []*model.Channel{
		{Id: 1, Status: common.ChannelStatusEnabled},
		{Id: 2, Status: common.ChannelStatusEnabled},
		{Id: 3, Status: common.ChannelStatusEnabled},
		{Id: 4, Status: common.ChannelStatusEnabled},
	}
	started := make(chan struct{}, len(channels))
	release := make(chan struct{})
	var active atomic.Int32
	var maxActive atomic.Int32
	progress := make([]int, 0, len(channels)+1)
	summaryResult := make(chan channelTestSummary, 1)

	go func() {
		summaryResult <- runChannelTestWorkers(
			context.Background(),
			channels,
			2,
			func(_ context.Context, _ *model.Channel) channelTestSummary {
				current := active.Add(1)
				defer active.Add(-1)
				for {
					observed := maxActive.Load()
					if current <= observed || maxActive.CompareAndSwap(observed, current) {
						break
					}
				}
				started <- struct{}{}
				<-release
				return channelTestSummary{Tested: 1, Succeeded: 1}
			},
			func(processed, _ int) {
				progress = append(progress, processed)
			},
		)
	}()

	<-started
	<-started
	select {
	case <-started:
		t.Fatal("started more channel tests than the configured concurrency")
	default:
	}
	close(release)

	summary := <-summaryResult

	assert.Equal(t, int32(2), maxActive.Load())
	assert.Equal(t, channelTestSummary{Tested: 4, Succeeded: 4}, summary)
	assert.Equal(t, []int{0, 1, 2, 3, 4}, progress)
}

func TestRunChannelTestWorkersStopsAfterCancellation(t *testing.T) {
	originalInterval := common.RequestInterval
	common.RequestInterval = 0
	t.Cleanup(func() { common.RequestInterval = originalInterval })

	ctx, cancel := context.WithCancel(context.Background())
	channels := []*model.Channel{
		{Id: 1, Status: common.ChannelStatusEnabled},
		{Id: 2, Status: common.ChannelStatusEnabled},
		{Id: 3, Status: common.ChannelStatusEnabled},
		{Id: 4, Status: common.ChannelStatusEnabled},
	}
	started := make(chan struct{}, len(channels))
	progress := make([]int, 0, 1)
	summaryResult := make(chan channelTestSummary, 1)

	go func() {
		summaryResult <- runChannelTestWorkers(
			ctx,
			channels,
			2,
			func(ctx context.Context, _ *model.Channel) channelTestSummary {
				started <- struct{}{}
				<-ctx.Done()
				return channelTestSummary{Tested: 1, Succeeded: 1}
			},
			func(processed, _ int) {
				progress = append(progress, processed)
			},
		)
	}()

	<-started
	<-started
	cancel()

	summary := <-summaryResult

	select {
	case <-started:
		t.Fatal("started another channel test after cancellation")
	default:
	}
	assert.Equal(t, channelTestSummary{Tested: 2, Succeeded: 2}, summary)
	assert.Equal(t, []int{0}, progress)
}

func TestTestAllChannelsRejectsExistingActiveTask(t *testing.T) {
	db := setupModelListControllerTestDB(t)
	require.NoError(t, db.AutoMigrate(&model.SystemTask{}, &model.SystemTaskLock{}))

	existing, err := model.CreateSystemTask(model.SystemTaskTypeChannelTest, nil, nil)
	require.NoError(t, err)

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/api/channel/test", nil)

	TestAllChannels(ctx)

	require.Equal(t, http.StatusConflict, recorder.Code)
	require.Contains(t, recorder.Body.String(), existing.TaskID)
	require.Contains(t, recorder.Body.String(), "A channel test task is already running or queued")
}

func TestClearChannelInfoHidesTheSuperAdminKey(t *testing.T) {
	channel := &model.Channel{Id: 1, Name: "legacy-new-api"}
	channel.SetSetting(dto.ChannelSettings{
		Proxy:          "http://proxy.example",
		SuperKey:       "super-admin-token",
		SuperKeyUserID: 11566,
	})
	require.Contains(t, *channel.Setting, "super-admin-token")

	clearChannelInfo(channel)

	setting := channel.GetSetting()
	assert.Empty(t, setting.SuperKey, "the super admin key never leaves the management API")
	assert.Equal(t, 11566, setting.SuperKeyUserID)
	assert.Equal(t, "http://proxy.example", setting.Proxy, "other settings stay untouched")
	assert.NotContains(t, *channel.Setting, "super-admin-token")
}

func TestSweepChannelModelsKeepsAFailingModelUntilItExceedsTheFailureLimit(t *testing.T) {
	probeFailingBeta := func(_ context.Context, target string) *relaytypes.NewAPIError {
		if target == "beta" {
			return relaytypes.NewOpenAIError(errors.New("no available channel for model"), relaytypes.ErrorCodeBadResponseStatusCode, http.StatusNotFound)
		}
		return nil
	}
	newChannel := func(priorFailures map[string]any) *model.Channel {
		channel := &model.Channel{Id: 1, Name: "sweep"}
		channel.Models = "alpha,beta"
		if priorFailures != nil {
			channel.SetOtherInfo(map[string]any{"model_failures": priorFailures})
		}
		return channel
	}

	first := sweepChannelModels(context.Background(), newChannel(nil), probeFailingBeta, maxModelProbesPerSweep)
	assert.Equal(t, []string{"alpha", "beta"}, first.Alive, "one failure alone must not remove a model from routing")
	assert.Empty(t, first.Disabled)
	assert.Equal(t, map[string]int{"beta": 1}, first.Failures)

	belowLimit := sweepChannelModels(context.Background(), newChannel(map[string]any{"beta": float64(maxModelFailuresBeforeParking - 2)}), probeFailingBeta, maxModelProbesPerSweep)
	assert.Equal(t, []string{"alpha", "beta"}, belowLimit.Alive)
	assert.Equal(t, map[string]int{"beta": maxModelFailuresBeforeParking - 1}, belowLimit.Failures)

	exceeded := sweepChannelModels(context.Background(), newChannel(map[string]any{"beta": float64(maxModelFailuresBeforeParking - 1)}), probeFailingBeta, maxModelProbesPerSweep)
	assert.Equal(t, []string{"alpha"}, exceeded.Alive)
	assert.Equal(t, []string{"beta"}, exceeded.Disabled, "the model is parked once it failed more than the allowed number of times")
	assert.Empty(t, exceeded.Failures, "a parked model no longer carries a failure count")
}

func TestSweepChannelModelsClearsTheFailureCountAfterSuccess(t *testing.T) {
	channel := &model.Channel{Id: 2, Name: "recovered-sweep"}
	channel.Models = "alpha,beta"
	channel.SetOtherInfo(map[string]any{"model_failures": map[string]any{"beta": float64(2)}})

	sweep := sweepChannelModels(context.Background(), channel, func(_ context.Context, _ string) *relaytypes.NewAPIError { return nil }, maxModelProbesPerSweep)

	assert.Equal(t, []string{"alpha", "beta"}, sweep.Alive)
	assert.Empty(t, sweep.Failures, "a model that answers again starts over")
}

func TestSweepChannelModelsRestoresARecoveredModel(t *testing.T) {
	channel := &model.Channel{Id: 3, Name: "parked-sweep"}
	channel.Models = "alpha"
	channel.SetOtherInfo(map[string]any{"disabled_models": []any{"delta", "epsilon"}})

	sweep := sweepChannelModels(context.Background(), channel, func(_ context.Context, target string) *relaytypes.NewAPIError {
		if target == "epsilon" {
			return relaytypes.NewOpenAIError(errors.New("still down"), relaytypes.ErrorCodeBadResponseStatusCode, http.StatusBadGateway)
		}
		return nil
	}, maxModelProbesPerSweep)

	assert.Equal(t, []string{"alpha", "delta"}, sweep.Alive)
	assert.Equal(t, []string{"epsilon"}, sweep.Disabled)
}

func TestSweepChannelModelsStopsAtItsProbeBudget(t *testing.T) {
	channel := &model.Channel{Id: 4, Name: "large-sweep"}
	channel.Models = "m1,m2,m3,m4,m5,m6"
	channel.SetOtherInfo(map[string]any{"disabled_models": []any{"parked1", "parked2"}})

	probed := map[string]int{}
	probe := func(_ context.Context, target string) *relaytypes.NewAPIError {
		probed[target]++
		return nil
	}

	sweep := sweepChannelModels(context.Background(), channel, probe, 3)

	require.True(t, sweep.Complete)
	assert.Len(t, probed, 3, "one sweep never spends more probes than its budget")
	assert.ElementsMatch(t,
		[]string{"m1", "m2", "m3", "m4", "m5", "m6", "parked1", "parked2"},
		append(append([]string{}, sweep.Alive...), sweep.Disabled...),
		"every model keeps a routing state")
	for _, parked := range []string{"parked1", "parked2"} {
		if probed[parked] == 0 {
			assert.Contains(t, sweep.Disabled, parked, "a model the sweep did not reach stays parked")
		}
	}
}

func TestSweepChannelModelsDelegatesMediaToItsSafeProbe(t *testing.T) {
	channel := &model.Channel{Id: 5, Name: "generation-sweep"}
	channel.Models = "gpt-image-1,chat-model"
	channel.SetOtherInfo(map[string]any{"disabled_models": []any{"seedream-4-0"}})

	var probed []string
	probe := func(_ context.Context, target string) *relaytypes.NewAPIError {
		probed = append(probed, target)
		return nil
	}

	sweep := sweepChannelModels(context.Background(), channel, probe, maxModelProbesPerSweep)

	require.True(t, sweep.Complete)
	assert.ElementsMatch(t, []string{"seedream-4-0", "gpt-image-1", "chat-model"}, probed, "the supplied health probe handles media using read-only discovery")
	assert.Equal(t, []string{"gpt-image-1", "chat-model", "seedream-4-0"}, sweep.Alive)
	assert.Empty(t, sweep.Disabled)
}

func TestSweepChannelModelsSpendsNothingAfterCancellation(t *testing.T) {
	channel := &model.Channel{Id: 6, Name: "cancelled-sweep"}
	channel.Models = "alpha,beta"

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	probed := 0
	probe := func(_ context.Context, _ string) *relaytypes.NewAPIError {
		probed++
		return nil
	}

	sweep := sweepChannelModels(ctx, channel, probe, maxModelProbesPerSweep)

	assert.False(t, sweep.Complete, "a cancelled sweep must not be persisted")
	assert.Nil(t, sweep.Alive)
	assert.Nil(t, sweep.Disabled)
	assert.Zero(t, probed)
}

func TestSweepChannelModelsKeepsWhatItLearnedWhenTheBudgetRunsOut(t *testing.T) {
	channel := &model.Channel{Id: 7, Name: "budget-sweep"}
	channel.Models = "alpha,beta"
	channel.SetOtherInfo(map[string]any{"disabled_models": []any{"parked"}})

	ctx, cancel := context.WithTimeout(context.Background(), 0)
	defer cancel()

	probed := 0
	probe := func(_ context.Context, _ string) *relaytypes.NewAPIError {
		probed++
		return nil
	}

	sweep := sweepChannelModels(ctx, channel, probe, maxModelProbesPerSweep)

	assert.True(t, sweep.Complete, "a spent budget still stores what the sweep learned")
	assert.Zero(t, probed, "no probe runs once the budget is spent")
	assert.Equal(t, []string{"alpha", "beta"}, sweep.Alive)
	assert.Equal(t, []string{"parked"}, sweep.Disabled)
}

func TestBillableGenerationModelCoversImageVideoAndAudioFamilies(t *testing.T) {
	for _, model := range []string{
		"gemini-3-pro-image", "gpt-image-1", "nano-banana", "doubao-seedream-4-0",
		"flux-1.1-pro", "imagen-3.0", "seedance-2-5", "sora-2", "veo-3.1", "kling-v2",
		"wan2.7-image", "hailuo-02", "text-embedding-3-large-tts", "whisper-1", "suno-v4",
	} {
		assert.True(t, billableGenerationModel(model), model)
	}
	for _, model := range []string{
		"claude-sonnet-5", "gpt-5-medium", "deepseek-ai/DeepSeek-V3", "qwen38-abl",
		"gemini-1.5-flash", "grok-4.1-thinking",
	} {
		assert.False(t, billableGenerationModel(model), model)
	}
}

func testModelHealthFreeDiscoveryAndRouting(t *testing.T) {
	t.Helper()
	var gets, posts int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			posts++
			w.WriteHeader(500)
			return
		}
		gets++
		require.Equal(t, "/v1/models", r.URL.Path)
		_, _ = w.Write([]byte(`{"object":"list","data":[{"id":"gpt-image-2"}]}`))
	}))
	defer server.Close()
	channel := &model.Channel{Type: constant.ChannelTypeTaskPlugin, Status: common.ChannelStatusEnabled, Name: "Free media health", Key: "fixture-private-key", BaseURL: &server.URL, Models: "gpt-image-2,nana-banana-2_sync", Group: "default"}
	channel.SetSetting(dto.ChannelSettings{TaskPluginKey: "newtoken"})
	require.NoError(t, channel.Insert())
	cfg := &dto.ChannelCostSettings{Enabled: true, Source: "newtoken", CreditsPerCNY: 1, UpstreamGroup: "default"}
	channel.SetOtherSettings(dto.ChannelOtherSettings{CostBilling: cfg})
	require.NoError(t, model.DB.Model(channel).Update("settings", channel.OtherSettings).Error)
	purchased := &channelcost.Snapshot{Fingerprint: model.ChannelCostFingerprint(channel), SourceURL: "https://newtoken.club/api/pricing", SyncedAt: time.Now().Unix(), GroupRatio: 1, Prices: map[string]channelcost.Price{"gpt-image-2": {RequestPrice: new(.5)}, "nana-banana-2_sync": {RequestPrice: new(.25)}}}
	require.NoError(t, model.SaveChannelCostSnapshot(channel.Id, purchased))
	sibling := &model.Channel{Type: constant.ChannelTypeOpenAI, Status: common.ChannelStatusEnabled, Name: "Other supplier", Group: "other", Models: "nana-banana-2_sync"}
	require.NoError(t, sibling.Insert())
	t.Cleanup(func() {
		require.NoError(t, channel.Delete())
		require.NoError(t, sibling.Delete())
		key := fmt.Sprintf("channel_cost.%d", channel.Id)
		require.NoError(t, model.DB.Where(&model.Option{Key: key}).Delete(&model.Option{}).Error)
		common.OptionMapRWMutex.Lock()
		delete(common.OptionMap, key)
		common.OptionMapRWMutex.Unlock()
	})

	manualRecorder := httptest.NewRecorder()
	manualCtx, _ := gin.CreateTestContext(manualRecorder)
	manualCtx.Request = httptest.NewRequest(http.MethodGet, "/api/channel/test/"+fmt.Sprint(channel.Id)+"?model=gpt-image-2", nil)
	manualCtx.Params = gin.Params{{Key: "id", Value: fmt.Sprint(channel.Id)}}
	manualCtx.Set("id", 1)
	TestChannel(manualCtx)
	assert.Contains(t, manualRecorder.Body.String(), "directory")
	assert.Equal(t, 1, gets)
	assert.Zero(t, posts)
	require.NoError(t, model.DB.First(channel, channel.Id).Error)
	for round := range 3 {
		if round > 0 {
			health := readModelHealth(channel)
			for name, item := range health {
				item.CheckedAt = time.Now().Add(-modelHealthInterval - time.Minute).Unix()
				health[name] = item
			}
			info := channel.GetOtherInfo()
			info["model_health"] = health
			channel.SetOtherInfo(info)
			require.NoError(t, model.DB.Model(channel).Update("other_info", channel.OtherInfo).Error)
		}
		testChannelForHealthCheck(context.Background(), channel, 0, true)
	}
	assert.Equal(t, 4, gets)
	assert.Zero(t, posts, "media health must never submit a paid generation")

	snapshot := model.GetChannelCostSnapshot(channel.Id)
	require.NotNil(t, snapshot)
	assert.Equal(t, purchased.Prices, snapshot.Prices, "parking must not change synchronized purchase prices")
	assert.Equal(t, purchased.SyncedAt, snapshot.SyncedAt, "health checks never refresh price validity")
	assert.Equal(t, model.ChannelCostFingerprint(channel), snapshot.Fingerprint, "surviving model pricing stays valid after routing changes")
	receipt, _, err := snapshot.Resolve(cfg, "gpt-image-2", snapshot.Fingerprint, time.Now(), 1)
	require.NoError(t, err)
	assert.Equal(t, .5, *receipt.CNYRequestPrice)
	var kept model.Channel
	require.NoError(t, model.DB.First(&kept, sibling.Id).Error)
	assert.Equal(t, "nana-banana-2_sync", kept.Models)
	assert.Equal(t, common.ChannelStatusEnabled, kept.Status)
	assert.Equal(t, common.ChannelStatusEnabled, channel.Status)
	assert.Equal(t, "gpt-image-2", channel.Models)
	assert.Equal(t, []string{"nana-banana-2_sync"}, readChannelDisabledModels(channel))
	assert.Equal(t, []string{"gpt-image-2"}, model.GetGroupEnabledModels("default"))
	previousSelfUse := operation_setting.SelfUseModeEnabled
	operation_setting.SelfUseModeEnabled = true
	t.Cleanup(func() { operation_setting.SelfUseModeEnabled = previousSelfUse })
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	common.SetContextKey(ctx, constant.ContextKeyTokenGroup, "default")
	common.SetContextKey(ctx, constant.ContextKeyTokenModelLimitEnabled, true)
	common.SetContextKey(ctx, constant.ContextKeyTokenModelLimit, map[string]bool{"gpt-image-2": true, "nana-banana-2_sync": true})
	ListModels(ctx, constant.ChannelTypeOpenAI)
	ids := decodeListModelsResponse(t, recorder)
	assert.Contains(t, ids, "gpt-image-2")
	assert.NotContains(t, ids, "nana-banana-2_sync", "a key allowlist cannot revive a parked model")
	recorder = httptest.NewRecorder()
	ctx, _ = gin.CreateTestContext(recorder)
	GetModelHealth(ctx)
	assert.Contains(t, recorder.Body.String(), "interface_only")
	assert.Contains(t, recorder.Body.String(), "disabled")
	assert.NotContains(t, recorder.Body.String(), "fixture-private-key")
	// A second automatic pass within thirty minutes does no work.
	testChannelForHealthCheck(context.Background(), channel, 0, true)
	assert.Equal(t, 4, gets)
	assert.Zero(t, posts)
	stale := model.GetChannelCostSnapshot(channel.Id)
	stale.Fingerprint = "stale-source"
	require.NoError(t, model.SaveChannelCostSnapshot(channel.Id, stale))
	require.NoError(t, model.SaveChannelHealthState(channel, "", channel.OtherInfo))
	assert.Equal(t, "stale-source", model.GetChannelCostSnapshot(channel.Id).Fingerprint, "health routing cannot bless an already invalid price snapshot")

}

func TestModelHealthChatCooldownAndGenerationAliases(t *testing.T) {
	ch := &model.Channel{Models: "gpt-5.6-terra", Type: constant.ChannelTypeOpenAI}
	ch.SetOtherInfo(map[string]any{"model_health": map[string]modelHealthSample{"gpt-5.6-terra": {CheckedAt: time.Now().Unix(), Checks: 1, Successes: 1, Success: true, Method: "chat"}}})
	calls := 0
	sweep := sweepChannelModels(context.Background(), ch, func(context.Context, string) *relaytypes.NewAPIError { calls++; return nil }, 20)
	assert.Zero(t, calls)
	assert.Equal(t, []string{"gpt-5.6-terra"}, sweep.Alive)
	mapping := `{"gpt-5.6-terra":"nana-banana-2_sync"}`
	ch.ModelMapping = &mapping
	assert.False(t, chatHealthProbeAllowed(ch, "gpt-5.6-terra"), "an image behind a chat-looking alias must stay read-only")
	assert.False(t, chatHealthProbeAllowed(&model.Channel{Type: constant.ChannelTypeTaskPlugin}, "gpt-5.6-terra"))
	assert.False(t, chatHealthProbeAllowed(&model.Channel{Type: constant.ChannelTypeOpenAI}, "unknown-alias"))
	assert.False(t, chatHealthProbeAllowed(&model.Channel{Type: constant.ChannelTypeOpenAI}, "grok-imagine"))
}
