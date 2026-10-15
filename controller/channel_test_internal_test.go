package controller

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/billingexpr"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/dto"
	relaytypes "github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/QuantumNous/new-api/types"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

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

	belowLimit := sweepChannelModels(context.Background(), newChannel(map[string]any{"beta": float64(maxModelFailuresBeforeParking - 1)}), probeFailingBeta, maxModelProbesPerSweep)
	assert.Equal(t, []string{"alpha", "beta"}, belowLimit.Alive)
	assert.Equal(t, map[string]int{"beta": maxModelFailuresBeforeParking}, belowLimit.Failures)

	exceeded := sweepChannelModels(context.Background(), newChannel(map[string]any{"beta": float64(maxModelFailuresBeforeParking)}), probeFailingBeta, maxModelProbesPerSweep)
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

func TestSweepChannelModelsNeverProbesGenerationModels(t *testing.T) {
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
	assert.Equal(t, []string{"chat-model"}, probed, "probing a generation model would produce a charged output")
	assert.Equal(t, []string{"gpt-image-1", "chat-model"}, sweep.Alive)
	assert.Equal(t, []string{"seedream-4-0"}, sweep.Disabled)
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

func TestChannelHealthCheckTargetNeverPicksAGenerationModel(t *testing.T) {
	chat := &model.Channel{Name: "chat-first"}
	chat.Models = "gpt-image-1,chat-model"
	target, allowed := channelHealthCheckTarget(chat)
	assert.Equal(t, "chat-model", target)
	assert.True(t, allowed)

	generationOnly := &model.Channel{Name: "images-only"}
	generationOnly.Models = "seedream-4-0,sora-2"
	target, allowed = channelHealthCheckTarget(generationOnly)
	assert.Empty(t, target)
	assert.False(t, allowed, "a channel that only serves generation models is not probed")

	pinnedGeneration := &model.Channel{Name: "pinned"}
	pinnedGeneration.Models = "chat-model"
	pinnedGeneration.TestModel = common.GetPointer("sora-2")
	_, allowed = channelHealthCheckTarget(pinnedGeneration)
	assert.False(t, allowed, "even a pinned test model is not probed when it bills for generation")

	pinnedChat := &model.Channel{Name: "pinned-chat"}
	pinnedChat.Models = "chat-model"
	pinnedChat.TestModel = common.GetPointer("gpt-5-medium")
	target, allowed = channelHealthCheckTarget(pinnedChat)
	assert.Equal(t, "gpt-5-medium", target)
	assert.True(t, allowed)
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
