package controller

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/billingexpr"
	"github.com/QuantumNous/new-api/pkg/channelcost"
	pluginruntime "github.com/QuantumNous/new-api/pkg/jsplugin"
	"github.com/QuantumNous/new-api/relay"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	kitdto "github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

type imageStreamRecorder struct {
	*httptest.ResponseRecorder
	onWrite func([]byte)
}

func (w *imageStreamRecorder) Write(data []byte) (int, error) {
	if w.onWrite != nil {
		w.onWrite(data)
	}
	return w.ResponseRecorder.Write(data)
}

func TestBytePlusNativeImageDelivery(t *testing.T) {
	gin.SetMode(gin.TestMode)
	service.InitHttpClient()
	db, dialect := openTaskDialectDatabase(t, &model.User{}, &model.Token{}, &model.Channel{}, &model.Task{}, &model.Log{})
	oldDB, oldLogDB := model.DB, model.LOG_DB
	oldMain, oldLog := common.MainDatabaseType(), common.LogDatabaseType()
	oldRedis, oldMemory, oldBatch, oldConsume := common.RedisEnabled, common.MemoryCacheEnabled, common.BatchUpdateEnabled, common.LogConsumeEnabled
	model.DB, model.LOG_DB = db, db
	common.SetDatabaseTypes(dialect, dialect)
	common.RedisEnabled, common.MemoryCacheEnabled, common.BatchUpdateEnabled, common.LogConsumeEnabled = false, false, false, true
	t.Cleanup(func() {
		model.DB, model.LOG_DB = oldDB, oldLogDB
		common.SetDatabaseTypes(oldMain, oldLog)
		common.RedisEnabled, common.MemoryCacheEnabled, common.BatchUpdateEnabled, common.LogConsumeEnabled = oldRedis, oldMemory, oldBatch, oldConsume
	})
	const intl = "seedream-4-5-251128"
	const china = "doubao-seedream-4-5-251128"
	const newtokenImage = "dola-seedream-5-0-pro-260628-2k"
	withTieredBillingConfig(t, map[string]string{intl: "tiered_expr", china: "tiered_expr", newtokenImage: "tiered_expr"}, map[string]string{
		intl:          `tier("image", u("images_above_1_5k") * 0.01)`,
		china:         `tier("image", u("images_above_1_5k") * 0.01)`,
		newtokenImage: `tier("image", u("images_above_1_5k") * 0.01)`,
	})
	for _, tc := range []struct {
		name, model, baseSuffix, format string
		stream, disconnect, openai      bool
		plugin                          string
	}{
		{"international URL", intl, "", "url", false, false, false, "doubao"},
		{"international full base and large Base64", intl, "/api/v3/", "b64_json", false, false, false, "doubao"},
		{"international live SSE with partial failure", intl, "/api/v3", "url", true, false, false, "doubao"},
		{"international live SSE Base64", intl, "", "b64_json", true, false, false, "doubao"},
		{"client disconnect still settles delivered image", intl, "", "url", true, true, false, "doubao"},
		{"domestic URL regression", china, "", "url", false, false, false, "doubao"},
		{"OpenAI Images live SSE", intl, "/api/v3/", "url", true, false, true, "doubao"},
		{"OpenAI Images SSE Base64", intl, "", "b64_json", true, false, true, "doubao"},
		{"OpenAI Images SSE disconnect", intl, "", "url", true, true, true, "doubao"},
		{"BytePlus plugin native SSE", intl, "/api/v3/", "b64_json", true, false, false, "byteplus"},
		{"BytePlus plugin OpenAI SSE", intl, "", "url", true, false, true, "byteplus"},
		{"NewToken plugin native image", newtokenImage, "", "url", false, false, false, "newtoken"},
		{"NewToken plugin OpenAI image", newtokenImage, "", "url", false, false, true, "newtoken"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			user := model.User{Username: common.GetRandomString(12), AffCode: common.GetRandomString(12), Quota: 1_000_000, Group: "default"}
			require.NoError(t, db.Create(&user).Error)
			token := model.Token{UserId: user.Id, Key: common.GetRandomString(48), Status: common.TokenStatusEnabled, UnlimitedQuota: true, Group: "default"}
			require.NoError(t, db.Create(&token).Error)
			image := map[string]any{"size": "2048x2048"}
			if tc.format == "b64_json" {
				image["b64_json"] = strings.Repeat("A", 2<<20)
			} else {
				image["url"] = "https://cdn.example/image.png"
			}
			partialSeen := make(chan struct{}, 1)
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				imagePath := "/api/v3/images/generations"
				if tc.plugin == "newtoken" {
					imagePath = "/v1/images/generations"
				}
				assert.Equal(t, imagePath, r.URL.Path)
				assert.Equal(t, "Bearer test-byteplus-credential", r.Header.Get("Authorization"))
				var request map[string]any
				require.NoError(t, common.DecodeJson(r.Body, &request))
				assert.Equal(t, tc.model, request["model"])
				assert.Equal(t, tc.format, request["response_format"])
				if !tc.stream {
					w.Header().Set("Content-Type", "application/json")
					data, marshalErr := common.Marshal(map[string]any{"model": tc.model, "data": []any{image}, "usage": map[string]any{"generated_images": 1}})
					require.NoError(t, marshalErr)
					_, _ = w.Write(data)
					return
				}
				w.Header().Set("Content-Type", "text/event-stream")
				partial := map[string]any{"type": "image_generation.partial_succeeded", "image_index": 0}
				for key, value := range image {
					partial[key] = value
				}
				encoded, marshalErr := common.Marshal(partial)
				require.NoError(t, marshalErr)
				_, _ = w.Write(append(append([]byte("data: "), encoded...), []byte("\n\n")...))
				w.(http.Flusher).Flush()
				select {
				case <-partialSeen:
				case <-time.After(5 * time.Second):
					t.Error("the first image must reach the client before generation completes")
				}
				_, _ = io.WriteString(w, "data: {\"type\":\"image_generation.partial_failed\",\"image_index\":1,\"error\":{\"code\":\"filtered\"}}\n\ndata: {\"type\":\"image_generation.completed\",\"usage\":{\"generated_images\":1}}\n\n")
			}))
			defer upstream.Close()
			base := upstream.URL + tc.baseSuffix
			channel := model.Channel{Type: constant.ChannelTypeVolcEngine, Name: tc.name, Key: "test-byteplus-credential", BaseURL: &base, Status: common.ChannelStatusEnabled, Models: tc.model, Group: "default"}
			if tc.plugin != "doubao" {
				channel.Type = constant.ChannelTypeTaskPlugin
				setting := `{"task_plugin_key":"` + tc.plugin + `"}`
				channel.Setting = &setting
			}
			require.NoError(t, db.Create(&channel).Error)
			request := map[string]any{"model": tc.model, "prompt": "a landscape", "response_format": tc.format}
			if tc.stream {
				request["stream"] = true
				request["sequential_image_generation"] = "auto"
				request["sequential_image_generation_options"] = map[string]any{"max_images": 3}
			}
			body, marshalErr := common.Marshal(request)
			require.NoError(t, marshalErr)
			recorder := httptest.NewRecorder()
			writer := &imageStreamRecorder{ResponseRecorder: recorder}
			c, _ := gin.CreateTestContext(writer)
			nativePath := "/" + tc.plugin + "/api/v3/images/generations"
			if tc.plugin == "newtoken" {
				nativePath = "/newtoken/v1/images/generations"
			}
			path := nativePath
			if tc.openai {
				path = "/v1/images/generations"
			}
			c.Request = httptest.NewRequest(http.MethodPost, path, bytes.NewReader(body))
			clientContext, cancel := context.WithCancel(c.Request.Context())
			defer cancel()
			c.Request = c.Request.WithContext(clientContext)
			c.Request.Header.Set("Content-Type", "application/json")
			writer.onWrite = func(data []byte) {
				if bytes.Contains(data, []byte("partial_succeeded")) {
					select {
					case partialSeen <- struct{}{}:
					default:
					}
					if tc.disconnect {
						cancel()
					}
				}
				if bytes.Contains(data, []byte("image_generation.completed")) {
					var persisted model.Task
					require.NoError(t, db.Where("user_id = ?", user.Id).First(&persisted).Error)
					assert.Equal(t, common.QuotaRound(.01*common.QuotaPerUnit), persisted.Quota)
					var settled model.User
					require.NoError(t, db.First(&settled, user.Id).Error)
					assert.Equal(t, 1_000_000-persisted.Quota, settled.Quota, "completion follows billing settlement")
				}
			}
			generation := pluginruntime.DefaultRegistry.Generation()
			binding, found := generation.LookupDeclaredRoute(http.MethodPost, nativePath)
			require.True(t, found)
			c.Set(pluginruntime.ContextKeyPinnedRoute, pluginruntime.PinnedRoute{Generation: generation, Plugin: binding.Plugin, Route: binding.Route})
			for key, value := range map[constant.ContextKey]int{constant.ContextKeyUserId: user.Id, constant.ContextKeyTokenId: token.Id, constant.ContextKeyUserQuota: user.Quota} {
				common.SetContextKey(c, key, value)
			}
			common.SetContextKey(c, constant.ContextKeyUserGroup, "default")
			common.SetContextKey(c, constant.ContextKeyUsingGroup, "default")
			common.SetContextKey(c, constant.ContextKeyTokenGroup, "default")
			common.SetContextKey(c, constant.ContextKeyTokenUnlimited, true)
			common.SetContextKey(c, constant.ContextKeyUserSetting, kitdto.UserSetting{BillingPreference: "wallet_only"})
			if tc.openai {
				c.Keys[pluginruntime.ContextKeyPinnedRoute] = nil
				var endpoint pluginruntime.ProtocolBinding
				for _, candidate := range generation.LookupEndpointCandidates(http.MethodPost, path, tc.model) {
					if candidate.Plugin.Meta.Key == tc.plugin {
						endpoint = candidate
						break
					}
				}
				require.NotNil(t, endpoint.Plugin)
				c.Set(pluginruntime.ContextKeyPinnedEndpoint, pluginruntime.PinnedEndpoint{Generation: generation, Plugin: endpoint.Plugin, Protocol: endpoint.Protocol, Operation: endpoint.Operation, Model: tc.model})
				middleware.PrepareTaskPluginEndpoint()(c)
			} else {
				middleware.PrepareTaskPluginRoute()(c)
			}
			require.False(t, c.IsAborted(), recorder.Body.String())
			require.Nil(t, middleware.SetupContextForSelectedChannel(c, &channel, tc.model))
			if tc.openai {
				RelayTaskPluginEndpoint(c, func(c *gin.Context) { t.Error("unexpected protocol fallback") })
			} else {
				RelayTask(c)
			}
			require.Equal(t, http.StatusOK, recorder.Code)
			if tc.stream {
				assert.Contains(t, recorder.Header().Get("Content-Type"), "text/event-stream")
				assert.Contains(t, recorder.Body.String(), "partial_succeeded")
				assert.Equal(t, !tc.disconnect, strings.Contains(recorder.Body.String(), "image_generation.completed"))
			} else {
				var response map[string]any
				require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &response))
				if tc.openai {
					assert.NotEmpty(t, response["data"])
				} else {
					assert.Equal(t, tc.model, response["model"])
				}
				if tc.format == "b64_json" {
					assert.Contains(t, recorder.Body.String(), image["b64_json"])
				}
			}
			var stored model.Task
			require.NoError(t, db.Where("user_id = ?", user.Id).First(&stored).Error)
			assert.Empty(t, stored.Data, "inline media must never be persisted")
			assert.True(t, stored.PrivateData.ResultDiscarded)
			assert.Equal(t, constant.TaskPlatform(tc.plugin), stored.Platform)
			assert.Equal(t, common.QuotaRound(.01*common.QuotaPerUnit), stored.Quota)
		})
	}
}

type nativeRouteBilling struct {
	events      []string
	preConsumed int
	userID      int
	settled     bool
}

func (b *nativeRouteBilling) Settle(int) error {
	b.events = append(b.events, "settle")
	b.settled = true
	return nil
}

func (b *nativeRouteBilling) Refund(*gin.Context) {
	b.events = append(b.events, "refund")
	if !b.settled && b.preConsumed > 0 {
		_ = model.IncreaseUserQuota(b.userID, b.preConsumed, true)
		b.preConsumed = 0
	}
}

func (b *nativeRouteBilling) NeedsRefund() bool {
	return !b.settled && b.preConsumed > 0
}

func (b *nativeRouteBilling) GetPreConsumedQuota() int {
	return b.preConsumed
}

func (b *nativeRouteBilling) Reserve(quota int) error {
	b.events = append(b.events, "reserve")
	if err := model.DecreaseUserQuota(b.userID, quota, true); err != nil {
		return err
	}
	b.preConsumed = quota
	return nil
}

func TestKlingNativeRouteSubmitPollSettleAndQuery(t *testing.T) {
	gin.SetMode(gin.TestMode)
	service.InitHttpClient()

	previousDB := model.DB
	previousLogDB := model.LOG_DB
	previousMemoryCache := common.MemoryCacheEnabled
	previousBatchUpdate := common.BatchUpdateEnabled
	previousLogConsume := common.LogConsumeEnabled
	previousRedisEnabled := common.RedisEnabled
	database, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, database.AutoMigrate(&model.User{}, &model.Channel{}, &model.Task{}, &model.Log{}))
	model.DB = database
	model.LOG_DB = database
	common.MemoryCacheEnabled = false
	common.BatchUpdateEnabled = false
	common.LogConsumeEnabled = false
	common.RedisEnabled = false
	previousModelRatios := ratio_setting.ModelRatio2JSONString()
	require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(`{"kling-v1":1}`))
	t.Cleanup(func() {
		model.DB = previousDB
		model.LOG_DB = previousLogDB
		common.MemoryCacheEnabled = previousMemoryCache
		common.BatchUpdateEnabled = previousBatchUpdate
		common.LogConsumeEnabled = previousLogConsume
		common.RedisEnabled = previousRedisEnabled
		require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(previousModelRatios))
	})
	require.NoError(t, database.Create(&model.User{
		Id:       7,
		Username: "native-route-user",
		Group:    "default",
		Quota:    1_000_000,
	}).Error)

	var submitCalls atomic.Int32
	var queryCalls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/kling/v1/videos/text2video":
			submitCalls.Add(1)
			body, readErr := io.ReadAll(r.Body)
			if !assert.NoError(t, readErr) {
				http.Error(w, "read request", http.StatusInternalServerError)
				return
			}
			assert.Contains(t, string(body), `"model_name":"kling-v1"`)
			_, _ = io.WriteString(w, `{"code":0,"message":"","data":{"task_id":"kling-private-1","task_status":"submitted"}}`)
		case r.Method == http.MethodGet && r.URL.Path == "/kling/v1/videos/text2video/kling-private-1":
			queryCalls.Add(1)
			_, _ = io.WriteString(w, `{"code":0,"message":"","data":{"task_id":"kling-private-1","task_status":"succeed","task_status_msg":"","task_result":{"videos":[{"id":"video-private","url":"https://cdn.example/video.mp4","duration":"5"}]},"final_unit_deduction":"1"}}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer upstream.Close()

	channel := model.Channel{
		Type:    constant.ChannelTypeKling,
		Name:    "kling-native-e2e",
		Key:     "sk-test",
		BaseURL: &upstream.URL,
		Status:  common.ChannelStatusEnabled,
		Models:  "kling-v1",
		Group:   "default",
	}
	require.NoError(t, database.Create(&channel).Error)

	generation := pluginruntime.DefaultRegistry.Generation()
	require.NotNil(t, generation)
	submitBinding, found := generation.LookupDeclaredRoute(http.MethodPost, "/kling/v1/videos/text2video")
	require.True(t, found)
	require.Equal(t, "kling", submitBinding.Plugin.Meta.Key)

	submitRecorder := httptest.NewRecorder()
	submitContext, _ := gin.CreateTestContext(submitRecorder)
	submitContext.Request = httptest.NewRequest(
		http.MethodPost,
		"/kling/v1/videos/text2video",
		bytes.NewBufferString(`{"model_name":"kling-v1","prompt":"a lighthouse"}`),
	)
	submitContext.Request.Header.Set("Content-Type", "application/json")
	submitContext.Set(pluginruntime.ContextKeyPinnedRoute, pluginruntime.PinnedRoute{
		Generation: generation,
		Plugin:     submitBinding.Plugin,
		Route:      submitBinding.Route,
	})
	common.SetContextKey(submitContext, constant.ContextKeyUserId, 7)
	common.SetContextKey(submitContext, constant.ContextKeyUserGroup, "default")
	common.SetContextKey(submitContext, constant.ContextKeyUsingGroup, "default")
	common.SetContextKey(submitContext, constant.ContextKeyTokenGroup, "default")
	common.SetContextKey(submitContext, constant.ContextKeyUserQuota, 1_000_000)

	middleware.PrepareTaskPluginRoute()(submitContext)
	require.False(t, submitContext.IsAborted(), submitRecorder.Body.String())
	require.Equal(t, "kling-v1", submitContext.GetString("resolved_task_model"))
	require.Equal(t, "text_to_video", submitContext.GetString("task_action"))
	require.Nil(t, middleware.SetupContextForSelectedChannel(submitContext, &channel, "kling-v1"))

	billing := &nativeRouteBilling{userID: 7}
	relayInfo := &relaycommon.RelayInfo{
		UserId:          7,
		UserGroup:       "default",
		UsingGroup:      "default",
		UserQuota:       1_000_000,
		TokenGroup:      "default",
		OriginModelName: "kling-v1",
		Billing:         billing,
		TaskRelayInfo: &relaycommon.TaskRelayInfo{
			Action:        submitContext.GetString("task_action"),
			PublicTaskID:  "task_kling_public",
			LockedChannel: &channel,
		},
	}

	outcome, taskErr := executeTaskSubmissionWith(submitContext, relayInfo, relay.RelayTaskSubmit)
	require.Nil(t, taskErr)
	require.NotNil(t, outcome)
	require.Equal(t, []string{"reserve", "settle"}, billing.events)
	require.False(t, submitContext.Writer.Written())

	presentTaskSubmission(submitContext, outcome)
	require.Equal(t, http.StatusOK, submitRecorder.Code)
	assert.Contains(t, submitRecorder.Body.String(), `"task_id":"task_kling_public"`)
	assert.NotContains(t, submitRecorder.Body.String(), "kling-private-1")
	assert.Equal(t, int32(1), submitCalls.Load())

	var persisted model.Task
	require.NoError(t, database.Where("task_id = ?", "task_kling_public").First(&persisted).Error)
	assert.Equal(t, constant.TaskPlatform("kling"), persisted.Platform)
	assert.Equal(t, "kling-private-1", persisted.PrivateData.UpstreamTaskID)
	assert.Equal(t, model.TaskStatus(model.TaskStatusNotStart), persisted.Status)

	previousAdaptorFactory := service.GetTaskAdaptorFunc
	service.GetTaskAdaptorFunc = func(platform constant.TaskPlatform) service.TaskPollingAdaptor {
		return relay.GetTaskAdaptor(platform)
	}
	t.Cleanup(func() { service.GetTaskAdaptorFunc = previousAdaptorFactory })
	service.DispatchPlatformUpdate(
		context.Background(),
		persisted.Platform,
		map[int][]string{channel.Id: {"kling-private-1"}},
		map[string]*model.Task{"kling-private-1": &persisted},
	)

	require.NoError(t, database.Where("task_id = ?", "task_kling_public").First(&persisted).Error)
	assert.Equal(t, model.TaskStatus(model.TaskStatusSuccess), persisted.Status)
	assert.Equal(t, "100%", persisted.Progress)
	assert.Equal(t, 1, persisted.Quota)
	assert.Equal(t, int32(1), queryCalls.Load())
	var settledUser model.User
	require.NoError(t, database.First(&settledUser, 7).Error)
	assert.Equal(t, 999_999, settledUser.Quota)

	queryBinding, found := generation.LookupDeclaredRoute(http.MethodGet, "/kling/v1/videos/text2video/:task_id")
	require.True(t, found)
	queryRecorder := httptest.NewRecorder()
	queryContext, _ := gin.CreateTestContext(queryRecorder)
	queryContext.Request = httptest.NewRequest(http.MethodGet, "/kling/v1/videos/text2video/task_kling_public", nil)
	queryContext.Params = gin.Params{{Key: "task_id", Value: "task_kling_public"}}
	queryContext.Set(pluginruntime.ContextKeyPinnedRoute, pluginruntime.PinnedRoute{
		Generation: generation,
		Plugin:     queryBinding.Plugin,
		Route:      queryBinding.Route,
	})
	common.SetContextKey(queryContext, constant.ContextKeyUserId, 7)

	middleware.PrepareTaskPluginRoute()(queryContext)

	require.True(t, queryContext.IsAborted())
	require.Equal(t, http.StatusOK, queryRecorder.Code)
	assert.Contains(t, queryRecorder.Body.String(), `"task_id":"task_kling_public"`)
	assert.Contains(t, queryRecorder.Body.String(), `"task_status":"succeed"`)
	assert.NotContains(t, queryRecorder.Body.String(), "kling-private-1")
	assert.NotContains(t, queryRecorder.Body.String(), upstream.URL)
}

// Exercise the public quote, reservation, upstream task, poll, wallet/key debit
// and persisted procurement receipt together; upstream responses are local fixtures.
func TestNewTokenProcurementTaskBilling(t *testing.T) {
	gin.SetMode(gin.TestMode)
	service.InitHttpClient()
	db, dialect := openTaskDialectDatabase(t, &model.User{}, &model.Token{}, &model.Channel{}, &model.Task{}, &model.Log{}, &model.Option{})
	oldDB, oldLogDB := model.DB, model.LOG_DB
	oldMain, oldLog := common.MainDatabaseType(), common.LogDatabaseType()
	oldRedis, oldMemory, oldBatch, oldConsume, oldExport := common.RedisEnabled, common.MemoryCacheEnabled, common.BatchUpdateEnabled, common.LogConsumeEnabled, common.DataExportEnabled
	oldOptions := common.OptionMap
	oldPrice, oldFX := operation_setting.Price, operation_setting.USDExchangeRate
	model.DB, model.LOG_DB = db, db
	common.SetDatabaseTypes(dialect, dialect)
	common.RedisEnabled, common.MemoryCacheEnabled, common.BatchUpdateEnabled, common.LogConsumeEnabled, common.DataExportEnabled = false, false, false, true, false
	operation_setting.Price, operation_setting.USDExchangeRate = 1, 1
	oldRatio := ratio_setting.GroupRatio2JSONString()
	require.NoError(t, ratio_setting.UpdateGroupRatioByJSONString(`{"default":1}`))
	oldFactory := service.GetTaskAdaptorFunc
	service.GetTaskAdaptorFunc = func(platform constant.TaskPlatform) service.TaskPollingAdaptor { return relay.GetTaskAdaptor(platform) }
	t.Cleanup(func() {
		model.DB, model.LOG_DB = oldDB, oldLogDB
		common.SetDatabaseTypes(oldMain, oldLog)
		common.RedisEnabled, common.MemoryCacheEnabled, common.BatchUpdateEnabled, common.LogConsumeEnabled, common.DataExportEnabled = oldRedis, oldMemory, oldBatch, oldConsume, oldExport
		common.OptionMap = oldOptions
		operation_setting.Price, operation_setting.USDExchangeRate = oldPrice, oldFX
		require.NoError(t, ratio_setting.UpdateGroupRatioByJSONString(oldRatio))
		service.GetTaskAdaptorFunc = oldFactory
	})
	for _, tc := range []struct {
		name, model, unit, expression           string
		price, units, group, recharge, expected float64
		fail, reference                         bool
	}{
		{"second duration reconciles", "video-ultra-720p", "second", "", .8, 1, .5, 1, 1.6, false, false},
		{"four units billed once", "nana-banana-pro", "request", "", .025, 4, 1, 1, .1, false, false},
		{"conditional seconds and recharge", "video-ultra-720p", "", `u("seconds") > 10 ? tier("long",u("seconds")*.7) : tier("short",u("seconds")*.8)`, 0, 1, .5, 5, .32, false, false},
		{"thirty second model", "video-ultra2.5-720p", "second", "", .8, 1, .5, 1, 1.6, false, false},
		{"reference video missing conditional price", "sd2.0-720p-official", "second", "", .8, 1, .5, 1, 1.6, false, true},
		{"synchronous GPT base64", "gpt-image-2", "request", "", .14, 1, 1, 1, .14, false, false},
		{"synchronous Nana", "nana-banana-pro_sync", "request", "", .1, 1, 1, 1, .1, false, false},
		{"Seedream reference image surcharge", "dola-seedream-5-0-pro-260628-1k", "request", "", .306, 1, .55, 1, .1914, false, false},
		{"failed generation refunds", "video-ultra-720p", "second", "", .8, 1, .5, 1, 0, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ratioRaw, e := common.Marshal(map[string]float64{"default": tc.group})
			require.NoError(t, e)
			require.NoError(t, ratio_setting.UpdateGroupRatioByJSONString(string(ratioRaw)))
			synchronous := tc.model == "gpt-image-2" || tc.model == "nana-banana-pro_sync" || strings.HasPrefix(tc.model, "dola-seedream")
			seedream := strings.HasPrefix(tc.model, "dola-seedream")
			var submits, queries atomic.Int32
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if r.Method == http.MethodPost {
					submits.Add(1)
					if synchronous {
						assert.Equal(t, "/v1/images/generations", r.URL.Path)
					} else {
						assert.Equal(t, "/v1/videos", r.URL.Path)
					}
					var body map[string]any
					require.NoError(t, common.DecodeJson(r.Body, &body))
					assert.Equal(t, tc.model, body["model"])
					if tc.model == "video-ultra2.5-720p" {
						assert.EqualValues(t, 30, body["seconds"])
						assert.EqualValues(t, 30, body["duration"])
					}
					if synchronous {
						if tc.model == "gpt-image-2" {
							_, _ = io.WriteString(w, `{"data":[{"b64_json":"aGVsbG8="}]}`)
						} else {
							_, _ = io.WriteString(w, `{"data":[{"url":"https://cdn.example/image.png","size":"1024x1024"}],"usage":{"input_images":3,"generated_images":1}}`)
						}
						return
					}
					_, _ = io.WriteString(w, `{"id":"private-task","status":"queued"}`)
					return
				}
				queries.Add(1)
				assert.Equal(t, "/v1/videos/private-task", r.URL.Path)
				if tc.fail {
					_, _ = io.WriteString(w, `{"id":"private-task","status":"failed","error":{"message":"fixture failure"}}`)
					return
				}
				_, _ = io.WriteString(w, `{"id":"private-task","status":"completed","duration":"4s","url":"https://cdn.example/output.png"}`)
			}))
			defer upstream.Close()
			user := model.User{Username: common.GetRandomString(12), AffCode: common.GetRandomString(12), Quota: 10000000, Group: "default"}
			require.NoError(t, db.Create(&user).Error)
			token := model.Token{UserId: user.Id, Key: common.GetRandomString(48), Group: "default", Status: common.TokenStatusEnabled, RemainQuota: 10000000}
			require.NoError(t, db.Create(&token).Error)
			source := channelcost.DefaultSources()["happyapi"]
			source.AdvancedPricing = true
			source.LocalGroupBilling = true
			if seedream {
				source.ModelRules = map[string]channelcost.ModelPriceRule{tc.model: {Expression: `tier("image", {{request_price}} * u("image_count") + .021 * u("extra_reference_images"))`, UsageSchema: map[string]json.RawMessage{"image_count": json.RawMessage(`{"type":"number","unit":"count"}`), "extra_reference_images": json.RawMessage(`{"type":"number","unit":"count"}`)}, DocumentURL: "https://doc.newtoken.club/api-docs/seedream-5-0-pro.html"}}
			}
			source.ID = "fixture-meter"
			source.Name = "Fixture"
			source.ChannelOrigin = upstream.URL
			source.URL = upstream.URL + "/api/pricing"
			source.StatusURL = ""
			source.PriceCurrency = "CREDIT"
			sources, err := common.Marshal(map[string]channelcost.Source{source.ID: source})
			require.NoError(t, err)
			common.OptionMap = map[string]string{"channel_price_sources": string(sources)}
			row := map[string]any{"model_name": tc.model, "quota_type": 1, "model_price": tc.price, "request_price_units": tc.units, "request_price_display_unit": tc.unit, "enable_groups": []string{"meter"}}
			if tc.expression != "" {
				row["billing_mode"] = "tiered_expr"
				row["billing_expr"] = tc.expression
				row["billing_usage_schema"] = map[string]any{"seconds": map[string]any{"type": "number", "unit": "second"}}
			}
			raw, err := common.Marshal(map[string]any{"success": true, "group_ratio": map[string]float64{"meter": tc.group}, "data": []any{row}})
			require.NoError(t, err)
			parsed, err := service.ParsePriceSource(raw, source, "meter", 500000)
			require.NoError(t, err)
			require.Empty(t, parsed.Errors)
			base := upstream.URL + "/v1/"
			setting := `{"task_plugin_key":"newtoken"}`
			settings, err := common.Marshal(map[string]any{"cost_billing": map[string]any{"enabled": true, "source": source.ID, "credits_per_cny": tc.recharge, "upstream_group": "meter"}})
			require.NoError(t, err)
			ch := model.Channel{Name: tc.name, Type: constant.ChannelTypeTaskPlugin, Status: 1, Models: tc.model, Group: "default", Key: "fixture-model-call", BaseURL: &base, Setting: &setting, OtherSettings: string(settings)}
			require.NoError(t, db.Create(&ch).Error)
			require.NoError(t, model.SaveChannelCostSnapshot(ch.Id, &channelcost.Snapshot{Fingerprint: model.ChannelCostFingerprint(&ch), SyncedAt: time.Now().Unix(), SourceURL: source.URL, GroupRatio: tc.group, Prices: parsed.Prices}))
			catalog, err := model.GetPublicModelCatalog("default")
			require.NoError(t, err)
			var pricingID string
			for _, row := range catalog {
				if row.ChannelID == ch.Id {
					pricingID = row.PricingID
				}
			}
			require.NotEmpty(t, pricingID)
			quote, err := model.PublicModelPriceByID(pricingID, time.Now())
			require.NoError(t, err)
			require.True(t, quote.Available, quote.Reason)
			assert.Equal(t, "CNY", quote.Currency)
			assert.InDelta(t, tc.group, quote.AppliedGroupMultiplier, 1e-12)
			quoteExpr := quote.Expression
			quoteCost, _, err := billingexpr.RunExprWithRequest(quoteExpr, billingexpr.TokenParams{}, billingexpr.RequestInput{Usage: map[string]any{"seconds": 4.0, "image_count": 1.0, "extra_reference_images": 2.0}})
			require.NoError(t, err)
			if quote.BillingType == "request" {
				quoteCost /= 1e6
			} else {
				assert.Equal(t, "task_usage", quote.BillingType)
				assert.Nil(t, quote.Rates)
				assert.Nil(t, quote.RequestPrice)
			}
			if !tc.fail {
				assert.InDelta(t, tc.expected, quoteCost, 1e-10)
			}
			request := map[string]any{"model": tc.model, "prompt": "a landscape", "seconds": "5s"}
			if tc.unit == "request" {
				delete(request, "seconds")
			}
			if tc.model == "video-ultra2.5-720p" {
				request["seconds"] = "30s"
			}
			if seedream {
				request["image"] = []string{"https://cdn.example/a.png", "https://cdn.example/b.png", "https://cdn.example/c.png"}
			}
			if tc.reference {
				request["extra_videos"] = []string{"https://cdn.example/reference.mp4"}
			}
			body, err := common.Marshal(request)
			require.NoError(t, err)
			recorder := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(recorder)
			path := "/newtoken/v1/videos"
			if synchronous {
				path = "/newtoken/v1/images/generations"
			}
			c.Request = httptest.NewRequest(http.MethodPost, path, bytes.NewReader(body))
			c.Request.Header.Set("Content-Type", "application/json")
			generation := pluginruntime.DefaultRegistry.Generation()
			binding, found := generation.LookupDeclaredRoute(http.MethodPost, path)
			require.True(t, found)
			c.Set(pluginruntime.ContextKeyPinnedRoute, pluginruntime.PinnedRoute{Generation: generation, Plugin: binding.Plugin, Route: binding.Route})
			for key, value := range map[constant.ContextKey]int{constant.ContextKeyUserId: user.Id, constant.ContextKeyTokenId: token.Id, constant.ContextKeyUserQuota: user.Quota} {
				common.SetContextKey(c, key, value)
			}
			common.SetContextKey(c, constant.ContextKeyUserGroup, "default")
			common.SetContextKey(c, constant.ContextKeyUsingGroup, "default")
			common.SetContextKey(c, constant.ContextKeyTokenGroup, "default")
			common.SetContextKey(c, constant.ContextKeyUserSetting, kitdto.UserSetting{BillingPreference: "wallet_only"})
			middleware.PrepareTaskPluginRoute()(c)
			require.False(t, c.IsAborted(), recorder.Body.String())
			require.Nil(t, middleware.SetupContextForSelectedChannel(c, &ch, tc.model))
			info := &relaycommon.RelayInfo{UserId: user.Id, UserGroup: "default", UsingGroup: "default", UserQuota: user.Quota, TokenGroup: "default", TokenId: token.Id, TokenKey: token.Key, OriginModelName: tc.model, ForcePreConsume: true, TaskRelayInfo: &relaycommon.TaskRelayInfo{PublicTaskID: model.GenerateTaskID(), LockedChannel: &ch}}
			info.UserSetting.BillingPreference = "wallet_only"
			outcome, taskErr := executeTaskSubmissionWith(c, info, relay.RelayTaskSubmit)
			if tc.reference {
				require.NotNil(t, taskErr)
				assert.Equal(t, "model_price_error", taskErr.Code)
				assert.Contains(t, taskErr.Message, "reference-video pricing")
				assert.Nil(t, outcome)
				var remaining model.User
				require.NoError(t, db.First(&remaining, user.Id).Error)
				assert.Equal(t, 10000000, remaining.Quota)
				var unchanged model.Token
				require.NoError(t, db.First(&unchanged, token.Id).Error)
				assert.Equal(t, 10000000, unchanged.RemainQuota)
				assert.Zero(t, submits.Load())
				assert.Zero(t, queries.Load())
				return
			}
			require.Nil(t, taskErr)
			require.NotNil(t, outcome)
			var task model.Task
			require.NoError(t, db.Where("user_id = ?", user.Id).First(&task).Error)
			require.NotNil(t, task.PrivateData.BillingContext.ChannelCost)
			require.True(t, task.PrivateData.BillingContext.TieredSnapshot.ProcurementCost)
			if !synchronous {
				service.DispatchPlatformUpdate(context.Background(), task.Platform, map[int][]string{ch.Id: {"private-task"}}, map[string]*model.Task{"private-task": &task})
			}
			require.NoError(t, db.First(&task, task.ID).Error)
			expected := common.QuotaFromFloat(tc.expected * common.QuotaPerUnit)
			assert.Equal(t, expected, task.Quota)
			if tc.fail {
				assert.Equal(t, model.TaskStatus(model.TaskStatusFailure), task.Status)
			} else {
				assert.Equal(t, model.TaskStatus(model.TaskStatusSuccess), task.Status)
			}
			var settled model.User
			require.NoError(t, db.First(&settled, user.Id).Error)
			assert.Equal(t, 10000000-expected, settled.Quota)
			var billedToken model.Token
			require.NoError(t, db.First(&billedToken, token.Id).Error)
			assert.Equal(t, 10000000-expected, billedToken.RemainQuota)
			assert.EqualValues(t, 1, submits.Load())
			if synchronous {
				assert.Zero(t, queries.Load())
			} else {
				assert.EqualValues(t, 1, queries.Load())
			}
		})
	}
}
