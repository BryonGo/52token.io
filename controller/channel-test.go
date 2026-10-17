package controller

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/i18n"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/billingexpr"
	"github.com/QuantumNous/new-api/relay"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/QuantumNous/new-api/relay/helper"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting/model_setting"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	hosttypes "github.com/QuantumNous/new-api/types"

	"github.com/samber/lo"
	"github.com/tidwall/gjson"

	"github.com/gin-gonic/gin"
)

type testResult struct {
	context     *gin.Context
	localErr    error
	newAPIError *types.NewAPIError
}

func normalizeChannelTestEndpoint(channel *model.Channel, endpointType string) string {
	normalized := strings.TrimSpace(endpointType)
	if normalized != "" {
		return normalized
	}
	if channel != nil && channel.Type == constant.ChannelTypeCodex {
		return string(constant.EndpointTypeOpenAIResponse)
	}
	return normalized
}

func resolveChannelTestUserID(c *gin.Context) (int, error) {
	if c != nil {
		if userID := c.GetInt("id"); userID > 0 {
			return userID, nil
		}
	}

	var rootUser model.User
	if err := model.DB.Select("id").Where("role = ?", common.RoleRootUser).First(&rootUser).Error; err != nil {
		return 0, fmt.Errorf("failed to resolve channel test user: %w", err)
	}
	if rootUser.Id == 0 {
		return 0, errors.New("failed to resolve channel test user")
	}
	return rootUser.Id, nil
}

func testChannel(ctx context.Context, channel *model.Channel, testUserID int, testModel string, endpointType string, isStream bool) testResult {
	if ctx == nil {
		ctx = context.Background()
	}
	tik := time.Now()
	var unsupportedTestChannelTypes = []int{
		constant.ChannelTypeMidjourney,
		constant.ChannelTypeMidjourneyPlus,
		constant.ChannelTypeSunoAPI,
		constant.ChannelTypeKling,
		constant.ChannelTypeJimeng,
		constant.ChannelTypeDoubaoVideo,
		constant.ChannelTypeVidu,
		constant.ChannelTypeTaskPlugin,
	}
	if lo.Contains(unsupportedTestChannelTypes, channel.Type) {
		channelTypeName := constant.GetChannelTypeName(channel.Type)
		return testResult{
			localErr: fmt.Errorf("%s channel test is not supported", channelTypeName),
		}
	}
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)

	testModel = strings.TrimSpace(testModel)
	if testModel == "" {
		if channel.TestModel != nil && *channel.TestModel != "" {
			testModel = strings.TrimSpace(*channel.TestModel)
		} else {
			models := channel.GetModels()
			if len(models) > 0 {
				testModel = strings.TrimSpace(models[0])
			}
			if testModel == "" {
				testModel = "gpt-4o-mini"
			}
		}
	}

	endpointType = normalizeChannelTestEndpoint(channel, endpointType)

	requestPath := "/v1/chat/completions"

	// 如果指定了端点类型，使用指定的端点类型
	if endpointType != "" {
		if endpointInfo, ok := common.GetDefaultEndpointInfo(constant.EndpointType(endpointType)); ok {
			requestPath = endpointInfo.Path
		}
	} else {
		// 如果没有指定端点类型，使用原有的自动检测逻辑

		if strings.Contains(strings.ToLower(testModel), "rerank") {
			requestPath = "/v1/rerank"
		}

		// 先判断是否为 Embedding 模型
		if strings.Contains(strings.ToLower(testModel), "embedding") ||
			strings.HasPrefix(testModel, "m3e") || // m3e 系列模型
			strings.Contains(testModel, "bge-") || // bge 系列模型
			strings.Contains(testModel, "embed") ||
			channel.Type == constant.ChannelTypeMokaAI { // 其他 embedding 模型
			requestPath = "/v1/embeddings" // 修改请求路径
		}

		// VolcEngine 图像生成模型
		if channel.Type == constant.ChannelTypeVolcEngine && strings.Contains(testModel, "seedream") {
			requestPath = "/v1/images/generations"
		}

		// responses-only models
		if strings.Contains(strings.ToLower(testModel), "codex") {
			requestPath = "/v1/responses"
		}

	}
	// Gemini 原生流式通过 URL action（:streamGenerateContent）表达而非请求体字段，
	// GeminiChatRequest.IsStream 依据请求 URL 判定，合成请求路径需与生产入口保持一致
	if isStream && constant.EndpointType(endpointType) == constant.EndpointTypeGemini {
		requestPath = strings.Replace(requestPath, ":generateContent", ":streamGenerateContent", 1)
	}
	c.Request = httptest.NewRequestWithContext(ctx, http.MethodPost, requestPath, nil)

	cache, err := model.GetUserCache(testUserID)
	if err != nil {
		return testResult{
			localErr:    err,
			newAPIError: nil,
		}
	}
	cache.WriteContext(c)
	c.Set("id", testUserID)

	//c.Request.Header.Set("Authorization", "Bearer "+channel.Key)
	c.Request.Header.Set("Content-Type", "application/json")
	c.Set("channel", channel.Type)
	c.Set("base_url", channel.GetBaseURL())
	group, _ := model.GetUserGroup(testUserID, false)
	// A pinned channel probe uses one of that channel's routing groups, rather
	// than pricing it through an unrelated administrator account group.
	groups := channel.GetGroups()
	if !slices.Contains(groups, group) {
		for _, candidate := range groups {
			if candidate != "" {
				group = candidate
				break
			}
		}
	}
	common.SetContextKey(c, constant.ContextKeyUsingGroup, group)
	common.SetContextKey(c, constant.ContextKeyTokenGroup, group)

	newAPIError := middleware.SetupContextForSelectedChannel(c, channel, testModel)
	if newAPIError != nil {
		return testResult{
			context:     c,
			localErr:    newAPIError,
			newAPIError: newAPIError,
		}
	}

	// Determine relay format based on endpoint type or request path
	var relayFormat types.RelayFormat
	if endpointType != "" {
		// 根据指定的端点类型设置 relayFormat
		switch constant.EndpointType(endpointType) {
		case constant.EndpointTypeOpenAI:
			relayFormat = types.RelayFormatOpenAI
		case constant.EndpointTypeOpenAIResponse:
			relayFormat = types.RelayFormatOpenAIResponses
		case constant.EndpointTypeOpenAIResponseCompact:
			relayFormat = types.RelayFormatOpenAIResponsesCompaction
		case constant.EndpointTypeAnthropic:
			relayFormat = types.RelayFormatClaude
		case constant.EndpointTypeGemini:
			relayFormat = types.RelayFormatGemini
		case constant.EndpointTypeJinaRerank:
			relayFormat = types.RelayFormatRerank
		case constant.EndpointTypeImageGeneration:
			relayFormat = types.RelayFormatOpenAIImage
		case constant.EndpointTypeEmbeddings:
			relayFormat = types.RelayFormatEmbedding
		default:
			relayFormat = types.RelayFormatOpenAI
		}
	} else {
		// 根据请求路径自动检测
		relayFormat = types.RelayFormatOpenAI
		if c.Request.URL.Path == "/v1/embeddings" {
			relayFormat = types.RelayFormatEmbedding
		}
		if c.Request.URL.Path == "/v1/images/generations" {
			relayFormat = types.RelayFormatOpenAIImage
		}
		if c.Request.URL.Path == "/v1/messages" {
			relayFormat = types.RelayFormatClaude
		}
		if strings.Contains(c.Request.URL.Path, "/v1beta/models") {
			relayFormat = types.RelayFormatGemini
		}
		if c.Request.URL.Path == "/v1/rerank" || c.Request.URL.Path == "/rerank" {
			relayFormat = types.RelayFormatRerank
		}
		if c.Request.URL.Path == "/v1/responses" {
			relayFormat = types.RelayFormatOpenAIResponses
		}
		if strings.HasPrefix(c.Request.URL.Path, "/v1/responses/compact") {
			relayFormat = types.RelayFormatOpenAIResponsesCompaction
		}
	}

	// Probe the configured upstream protocol when production chat requests are
	// upgraded to Responses. Raw passthrough deliberately bypasses that policy.
	if relayFormat == types.RelayFormatOpenAI &&
		!model_setting.GetGlobalSettings().PassThroughRequestEnabled &&
		!channel.GetSetting().PassThroughBodyEnabled &&
		service.ShouldChatCompletionsUseResponsesGlobal(channel.Id, channel.Type, testModel) {
		endpointType = string(constant.EndpointTypeOpenAIResponse)
		relayFormat = types.RelayFormatOpenAIResponses
		c.Request.URL.Path = "/v1/responses"
	}
	request := buildTestRequest(testModel, endpointType, channel, isStream)

	info, err := relaycommon.GenRelayInfo(c, relayFormat, request, nil)

	if err != nil {
		return testResult{
			context:     c,
			localErr:    err,
			newAPIError: types.NewError(err, types.ErrorCodeGenRelayInfoFailed),
		}
	}

	info.IsChannelTest = true
	info.InitChannelMeta(c)

	err = attachTestBillingRequestInput(info, request)
	if err != nil {
		return testResult{
			context:     c,
			localErr:    err,
			newAPIError: types.NewError(err, types.ErrorCodeJsonMarshalFailed),
		}
	}

	err = helper.ModelMappedHelper(c, info, request)
	if err != nil {
		return testResult{
			context:     c,
			localErr:    err,
			newAPIError: types.NewError(err, types.ErrorCodeChannelModelMappedError),
		}
	}
	if err := helper.ApplyReasoningModelSuffix(c, info, request); err != nil {
		return testResult{
			context:     c,
			localErr:    err,
			newAPIError: types.NewErrorWithStatusCode(err, types.ErrorCodeConvertRequestFailed, http.StatusBadRequest, types.ErrOptionWithSkipRetry()),
		}
	}

	testModel = info.UpstreamModelName
	// 更新请求中的模型名称
	request.SetModelName(testModel)

	apiType, _ := common.ChannelType2APIType(channel.Type)
	if info.RelayMode == relayconstant.RelayModeResponsesCompact &&
		!common.SupportsResponsesCompact(channel.Type, apiType) {
		return testResult{
			context:     c,
			localErr:    fmt.Errorf("responses compaction test is not supported for api type %d", apiType),
			newAPIError: types.NewError(fmt.Errorf("unsupported api type: %d", apiType), types.ErrorCodeInvalidApiType),
		}
	}
	adaptor := relay.GetAdaptor(apiType)
	if adaptor == nil {
		return testResult{
			context:     c,
			localErr:    fmt.Errorf("invalid api type: %d, adaptor is nil", apiType),
			newAPIError: types.NewError(fmt.Errorf("invalid api type: %d, adaptor is nil", apiType), types.ErrorCodeInvalidApiType),
		}
	}

	//// 创建一个用于日志的 info 副本，移除 ApiKey
	//logInfo := info
	//logInfo.ApiKey = ""
	common.SysLog(common.LogText("testing channel %d with model %s , info %+v ", channel.Id, testModel, info.ToString()))

	priceData, err := helper.ModelPriceHelper(c, info, 0, request.GetTokenCountMeta())
	if err != nil {
		return testResult{
			context:     c,
			localErr:    err,
			newAPIError: types.NewError(err, types.ErrorCodeModelPriceError, types.ErrOptionWithStatusCode(http.StatusBadRequest)),
		}
	}

	adaptor.Init(info)

	var convertedRequest any
	// 根据 RelayMode 选择正确的转换函数
	switch info.RelayMode {
	case relayconstant.RelayModeEmbeddings:
		// Embedding 请求 - request 已经是正确的类型
		if embeddingReq, ok := request.(*dto.EmbeddingRequest); ok {
			convertedRequest, err = adaptor.ConvertEmbeddingRequest(c, info, *embeddingReq)
		} else {
			return testResult{
				context:     c,
				localErr:    errors.New("invalid embedding request type"),
				newAPIError: types.NewError(errors.New("invalid embedding request type"), types.ErrorCodeConvertRequestFailed),
			}
		}
	case relayconstant.RelayModeImagesGenerations:
		// 图像生成请求 - request 已经是正确的类型
		if imageReq, ok := request.(*dto.ImageRequest); ok {
			convertedRequest, err = adaptor.ConvertImageRequest(c, info, *imageReq)
		} else {
			return testResult{
				context:     c,
				localErr:    errors.New("invalid image request type"),
				newAPIError: types.NewError(errors.New("invalid image request type"), types.ErrorCodeConvertRequestFailed),
			}
		}
	case relayconstant.RelayModeRerank:
		// Rerank 请求 - request 已经是正确的类型
		if rerankReq, ok := request.(*dto.RerankRequest); ok {
			convertedRequest, err = adaptor.ConvertRerankRequest(c, info.RelayMode, *rerankReq)
		} else {
			return testResult{
				context:     c,
				localErr:    errors.New("invalid rerank request type"),
				newAPIError: types.NewError(errors.New("invalid rerank request type"), types.ErrorCodeConvertRequestFailed),
			}
		}
	case relayconstant.RelayModeResponses:
		// Response 请求 - request 已经是正确的类型
		if responseReq, ok := request.(*dto.OpenAIResponsesRequest); ok {
			convertedRequest, err = adaptor.ConvertOpenAIResponsesRequest(c, info, *responseReq)
		} else {
			return testResult{
				context:     c,
				localErr:    errors.New("invalid response request type"),
				newAPIError: types.NewError(errors.New("invalid response request type"), types.ErrorCodeConvertRequestFailed),
			}
		}
	case relayconstant.RelayModeResponsesCompact:
		// Response compaction request - convert to OpenAIResponsesRequest before adapting
		switch req := request.(type) {
		case *dto.OpenAIResponsesCompactionRequest:
			convertedRequest, err = adaptor.ConvertOpenAIResponsesRequest(c, info, dto.OpenAIResponsesRequest{
				Model:              req.Model,
				Input:              req.Input,
				Instructions:       req.Instructions,
				PreviousResponseID: req.PreviousResponseID,
			})
		case *dto.OpenAIResponsesRequest:
			convertedRequest, err = adaptor.ConvertOpenAIResponsesRequest(c, info, *req)
		default:
			return testResult{
				context:     c,
				localErr:    errors.New("invalid response compaction request type"),
				newAPIError: types.NewError(errors.New("invalid response compaction request type"), types.ErrorCodeConvertRequestFailed),
			}
		}
	default:
		switch req := request.(type) {
		case *dto.GeneralOpenAIRequest:
			convertedRequest, err = adaptor.ConvertOpenAIRequest(c, info, req)
		case *dto.ClaudeRequest:
			convertedRequest, err = adaptor.ConvertClaudeRequest(c, info, req)
		case *dto.GeminiChatRequest:
			convertedRequest, err = adaptor.ConvertGeminiRequest(c, info, req)
		default:
			return testResult{
				context:     c,
				localErr:    errors.New("invalid chat request type"),
				newAPIError: types.NewError(errors.New("invalid chat request type"), types.ErrorCodeConvertRequestFailed),
			}
		}
	}

	if err != nil {
		return testResult{
			context:     c,
			localErr:    err,
			newAPIError: types.NewError(err, types.ErrorCodeConvertRequestFailed),
		}
	}
	jsonData, err := common.Marshal(convertedRequest)
	if err != nil {
		return testResult{
			context:     c,
			localErr:    err,
			newAPIError: types.NewError(err, types.ErrorCodeJsonMarshalFailed),
		}
	}

	//jsonData, err = relaycommon.RemoveDisabledFields(jsonData, info.ChannelOtherSettings)
	//if err != nil {
	//	return testResult{
	//		context:     c,
	//		localErr:    err,
	//		newAPIError: types.NewError(err, types.ErrorCodeConvertRequestFailed),
	//	}
	//}

	if len(info.ParamOverride) > 0 {
		jsonData, err = relaycommon.ApplyParamOverrideWithRelayInfo(jsonData, info)
		if err != nil {
			if fixedErr, ok := relaycommon.AsParamOverrideReturnError(err); ok {
				return testResult{
					context:     c,
					localErr:    fixedErr,
					newAPIError: relaycommon.NewAPIErrorFromParamOverride(fixedErr),
				}
			}
			return testResult{
				context:     c,
				localErr:    err,
				newAPIError: types.NewError(err, types.ErrorCodeChannelParamOverrideInvalid),
			}
		}
	}

	requestBody := bytes.NewBuffer(jsonData)
	c.Request.Body = io.NopCloser(bytes.NewBuffer(jsonData))
	resp, err := adaptor.DoRequest(c, info, requestBody)
	if err != nil {
		return testResult{
			context:     c,
			localErr:    err,
			newAPIError: types.NewOpenAIError(err, types.ErrorCodeDoRequestFailed, http.StatusInternalServerError),
		}
	}
	var httpResp *http.Response
	if resp != nil {
		httpResp = resp.(*http.Response)
		if httpResp.StatusCode != http.StatusOK {
			err := service.RelayErrorHandler(c.Request.Context(), httpResp, true)
			common.SysError(common.LogText(
				"channel test bad response: channel_id=%d name=%s type=%d model=%s endpoint_type=%s status=%d err=%v",
				channel.Id,
				channel.Name,
				channel.Type,
				testModel,
				endpointType,
				httpResp.StatusCode,
				err,
			))
			return testResult{
				context:     c,
				localErr:    err,
				newAPIError: types.NewOpenAIError(err, types.ErrorCodeBadResponse, http.StatusInternalServerError),
			}
		}
	}
	usageA, respErr := adaptor.DoResponse(c, httpResp, info)
	if respErr != nil {
		return testResult{
			context:     c,
			localErr:    respErr,
			newAPIError: respErr,
		}
	}
	usage, usageErr := coerceTestUsage(usageA, isStream, info.GetEstimatePromptTokens())
	if usageErr != nil {
		return testResult{
			context:     c,
			localErr:    usageErr,
			newAPIError: types.NewOpenAIError(usageErr, types.ErrorCodeBadResponseBody, http.StatusInternalServerError),
		}
	}
	result := w.Result()
	respBody, err := readTestResponseBody(result.Body, isStream)
	if err != nil {
		return testResult{
			context:     c,
			localErr:    err,
			newAPIError: types.NewOpenAIError(err, types.ErrorCodeReadResponseBodyFailed, http.StatusInternalServerError),
		}
	}
	if bodyErr := validateTestResponseBody(respBody, isStream); bodyErr != nil {
		return testResult{
			context:     c,
			localErr:    bodyErr,
			newAPIError: types.NewOpenAIError(bodyErr, types.ErrorCodeBadResponseBody, http.StatusInternalServerError),
		}
	}
	info.SetEstimatePromptTokens(usage.PromptTokens)

	quota, tieredResult := settleTestQuota(info, priceData, usage)
	tok := time.Now()
	milliseconds := tok.Sub(tik).Milliseconds()
	consumedTime := float64(milliseconds) / 1000.0
	other := buildTestLogOther(c, info, priceData, usage, tieredResult)
	model.RecordConsumeLog(c, testUserID, model.RecordConsumeLogParams{
		ChannelId:        channel.Id,
		PromptTokens:     usage.PromptTokens,
		CompletionTokens: usage.CompletionTokens,
		ModelName:        info.OriginModelName,
		TokenName:        "Model test",
		Quota:            quota,
		Content:          []*common.Message{common.NewMessage("Model test")},
		UseTimeSeconds:   int(consumedTime),
		IsStream:         info.IsStream,
		Group:            info.UsingGroup,
		Other:            other,
	})
	common.SysLog(common.LogText("testing channel #%d, response: \n%s", channel.Id, string(respBody)))
	return testResult{
		context:     c,
		localErr:    nil,
		newAPIError: nil,
	}
}

func attachTestBillingRequestInput(info *relaycommon.RelayInfo, request dto.Request) error {
	if info == nil {
		return nil
	}

	input, err := helper.BuildBillingExprRequestInputFromRequest(request, info.RequestHeaders)
	if err != nil {
		return err
	}
	info.BillingRequestInput = &input
	return nil
}

func settleTestQuota(info *relaycommon.RelayInfo, priceData hosttypes.PriceData, usage *dto.Usage) (int, *billingexpr.TieredResult) {
	if usage != nil && info != nil && info.TieredBillingSnapshot != nil {
		isClaudeUsageSemantic := usage.UsageSemantic == "anthropic" || info.GetFinalRequestRelayFormat() == types.RelayFormatClaude
		usedVars := billingexpr.UsedVars(info.TieredBillingSnapshot.ExprString)
		if ok, quota, result := service.TryTieredSettle(info, service.BuildTieredTokenParams(usage, isClaudeUsageSemantic, usedVars)); ok {
			return quota, result
		}
	}

	quota := 0
	if !priceData.UsePrice {
		completionQuota := common.QuotaRound(float64(usage.CompletionTokens) * priceData.CompletionRatio)
		quota = common.QuotaRound(float64(usage.PromptTokens) + float64(completionQuota))
		quota = common.QuotaRound(float64(quota) * priceData.ModelRatio)
		if priceData.ModelRatio != 0 && quota <= 0 {
			quota = 1
		}
		return quota, nil
	}

	return common.QuotaFromFloat(priceData.ModelPrice * common.QuotaPerUnit), nil
}

func buildTestLogOther(c *gin.Context, info *relaycommon.RelayInfo, priceData hosttypes.PriceData, usage *dto.Usage, tieredResult *billingexpr.TieredResult) *model.LogOther {
	other := service.GenerateTextOtherInfo(c, info, priceData.ModelRatio, priceData.GroupRatioInfo.GroupRatio, priceData.CompletionRatio,
		usage.PromptTokensDetails.CachedTokens, priceData.CacheRatio, priceData.ModelPrice, priceData.GroupRatioInfo.GroupSpecialRatio)
	if tieredResult != nil {
		service.InjectTieredBillingInfo(other, info, tieredResult)
	}
	return other
}

func coerceTestUsage(usageAny any, isStream bool, estimatePromptTokens int) (*dto.Usage, error) {
	switch u := usageAny.(type) {
	case *dto.Usage:
		return u, nil
	case dto.Usage:
		return &u, nil
	case nil:
		if !isStream {
			return nil, errors.New("usage is nil")
		}
		usage := &dto.Usage{
			PromptTokens: estimatePromptTokens,
		}
		usage.TotalTokens = usage.PromptTokens
		return usage, nil
	default:
		if !isStream {
			return nil, fmt.Errorf("invalid usage type: %T", usageAny)
		}
		usage := &dto.Usage{
			PromptTokens: estimatePromptTokens,
		}
		usage.TotalTokens = usage.PromptTokens
		return usage, nil
	}
}

func readTestResponseBody(body io.ReadCloser, isStream bool) ([]byte, error) {
	defer func() { _ = body.Close() }()
	const maxStreamLogBytes = 8 << 10
	if isStream {
		return io.ReadAll(io.LimitReader(body, maxStreamLogBytes))
	}
	return io.ReadAll(body)
}

func detectErrorFromTestResponseBody(respBody []byte) error {
	b := bytes.TrimSpace(respBody)
	if len(b) == 0 {
		return nil
	}
	if message := detectErrorMessageFromJSONBytes(b); message != "" {
		return fmt.Errorf("upstream error: %s", message)
	}

	for line := range bytes.SplitSeq(b, []byte{'\n'}) {
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			continue
		}
		if !bytes.HasPrefix(line, []byte("data:")) {
			continue
		}
		payload := bytes.TrimSpace(bytes.TrimPrefix(line, []byte("data:")))
		if len(payload) == 0 || bytes.Equal(payload, []byte("[DONE]")) {
			continue
		}
		if message := detectErrorMessageFromJSONBytes(payload); message != "" {
			return fmt.Errorf("upstream error: %s", message)
		}
	}

	return nil
}

func validateStreamTestResponseBody(respBody []byte) error {
	b := bytes.TrimSpace(respBody)
	if len(b) == 0 {
		return errors.New("stream response body is empty")
	}

	for line := range bytes.SplitSeq(b, []byte{'\n'}) {
		line = bytes.TrimSpace(line)
		if len(line) == 0 || !bytes.HasPrefix(line, []byte("data:")) {
			continue
		}
		payload := bytes.TrimSpace(bytes.TrimPrefix(line, []byte("data:")))
		if len(payload) == 0 || bytes.Equal(payload, []byte("[DONE]")) {
			continue
		}

		return nil
	}

	return errors.New("stream response body does not contain a valid stream event")
}

func validateTestResponseBody(respBody []byte, isStream bool) error {
	if bodyErr := detectErrorFromTestResponseBody(respBody); bodyErr != nil {
		return bodyErr
	}
	if isStream {
		return validateStreamTestResponseBody(respBody)
	}
	return nil
}

func shouldUseStreamForAutomaticChannelTest(channel *model.Channel) bool {
	return channel != nil && channel.Type == constant.ChannelTypeCodex
}

func detectErrorMessageFromJSONBytes(jsonBytes []byte) string {
	if len(jsonBytes) == 0 {
		return ""
	}
	if jsonBytes[0] != '{' && jsonBytes[0] != '[' {
		return ""
	}
	errVal := gjson.GetBytes(jsonBytes, "error")
	if !errVal.Exists() || errVal.Type == gjson.Null {
		return ""
	}

	message := gjson.GetBytes(jsonBytes, "error.message").String()
	if message == "" {
		message = gjson.GetBytes(jsonBytes, "error.error.message").String()
	}
	if message == "" && errVal.Type == gjson.String {
		message = errVal.String()
	}
	if message == "" {
		message = errVal.Raw
	}
	message = strings.TrimSpace(message)
	if message == "" {
		return "upstream returned error payload"
	}
	return message
}

func buildTestRequest(model string, endpointType string, channel *model.Channel, isStream bool) dto.Request {
	testResponsesInput := json.RawMessage(`[{"role":"user","content":"hi"}]`)

	// 根据端点类型构建不同的测试请求
	if endpointType != "" {
		switch constant.EndpointType(endpointType) {
		case constant.EndpointTypeEmbeddings:
			// 返回 EmbeddingRequest
			return &dto.EmbeddingRequest{
				Model: model,
				Input: []any{"hello world"},
			}
		case constant.EndpointTypeImageGeneration:
			// 返回 ImageRequest
			return &dto.ImageRequest{
				Model:  model,
				Prompt: "a cute cat",
				N:      lo.ToPtr(uint(1)),
				Size:   "1024x1024",
			}
		case constant.EndpointTypeJinaRerank:
			// 返回 RerankRequest
			return &dto.RerankRequest{
				Model:     model,
				Query:     "What is Deep Learning?",
				Documents: []any{"Deep Learning is a subset of machine learning.", "Machine learning is a field of artificial intelligence."},
				TopN:      lo.ToPtr(2),
			}
		case constant.EndpointTypeOpenAIResponse:
			// 返回 OpenAIResponsesRequest
			return &dto.OpenAIResponsesRequest{
				Model:           model,
				Input:           json.RawMessage(`[{"role":"user","content":"hi"}]`),
				Stream:          lo.ToPtr(isStream),
				MaxOutputTokens: lo.ToPtr(uint(16)),
			}
		case constant.EndpointTypeOpenAIResponseCompact:
			// 返回 OpenAIResponsesCompactionRequest
			return &dto.OpenAIResponsesCompactionRequest{
				Model: model,
				Input: testResponsesInput,
			}
		case constant.EndpointTypeAnthropic:
			return &dto.ClaudeRequest{
				Model:     model,
				Stream:    lo.ToPtr(isStream),
				MaxTokens: lo.ToPtr(uint(16)),
				Messages: []dto.ClaudeMessage{
					{
						Role:    "user",
						Content: "hi",
					},
				},
			}
		case constant.EndpointTypeGemini:
			return &dto.GeminiChatRequest{
				Contents: []dto.GeminiChatContent{
					{
						Role:  "user",
						Parts: []dto.GeminiPart{{Text: "hi"}},
					},
				},
				GenerationConfig: dto.GeminiChatGenerationConfig{
					MaxOutputTokens: lo.ToPtr(uint(3000)),
				},
			}
		case constant.EndpointTypeOpenAI:
			req := &dto.GeneralOpenAIRequest{
				Model:  model,
				Stream: lo.ToPtr(isStream),
				Messages: []dto.Message{
					{
						Role:    "user",
						Content: "hi",
					},
				},
				MaxTokens: lo.ToPtr(uint(16)),
			}
			if isStream {
				req.StreamOptions = &dto.StreamOptions{IncludeUsage: true}
			}
			return req
		}
	}

	// 自动检测逻辑（保持原有行为）
	if strings.Contains(strings.ToLower(model), "rerank") {
		return &dto.RerankRequest{
			Model:     model,
			Query:     "What is Deep Learning?",
			Documents: []any{"Deep Learning is a subset of machine learning.", "Machine learning is a field of artificial intelligence."},
			TopN:      lo.ToPtr(2),
		}
	}

	// 先判断是否为 Embedding 模型
	if strings.Contains(strings.ToLower(model), "embedding") ||
		strings.HasPrefix(model, "m3e") ||
		strings.Contains(model, "bge-") {
		// 返回 EmbeddingRequest
		return &dto.EmbeddingRequest{
			Model: model,
			Input: []any{"hello world"},
		}
	}

	// Responses-only models (e.g. codex series)
	if strings.Contains(strings.ToLower(model), "codex") {
		return &dto.OpenAIResponsesRequest{
			Model:           model,
			Input:           json.RawMessage(`[{"role":"user","content":"hi"}]`),
			Stream:          lo.ToPtr(isStream),
			MaxOutputTokens: lo.ToPtr(uint(16)),
		}
	}

	// Chat/Completion 请求 - 返回 GeneralOpenAIRequest
	testRequest := &dto.GeneralOpenAIRequest{
		Model:  model,
		Stream: lo.ToPtr(isStream),
		Messages: []dto.Message{
			{
				Role:    "user",
				Content: "hi",
			},
		},
	}
	if isStream {
		testRequest.StreamOptions = &dto.StreamOptions{IncludeUsage: true}
	}

	if dto.IsOpenAIReasoningOModel(model) {
		testRequest.MaxCompletionTokens = lo.ToPtr(uint(16))
	} else if strings.Contains(model, "thinking") {
		if !strings.Contains(model, "claude") {
			testRequest.MaxTokens = lo.ToPtr(uint(50))
		}
	} else if strings.Contains(model, "gemini") {
		testRequest.MaxTokens = lo.ToPtr(uint(3000))
	} else {
		testRequest.MaxTokens = lo.ToPtr(uint(16))
	}

	return testRequest
}

func TestChannel(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		common.ApiError(c, err)
		return
	}
	channel, err := model.GetChannelById(id, true)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	if _, running := modelHealthRunning.LoadOrStore(channel.Id, true); running {
		common.ApiErrorT(c, "A model health check is already running for this channel")
		return
	}
	defer modelHealthRunning.Delete(channel.Id)
	userID, err := resolveChannelTestUserID(c)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	name := strings.TrimSpace(c.Query("model"))
	if name == "" && channel.TestModel != nil {
		name = strings.TrimSpace(*channel.TestModel)
	}
	if name == "" {
		names := channel.GetModels()
		if len(names) > 0 {
			name = names[0]
		}
	}
	if name == "" {
		common.ApiErrorT(c, "No models configured for this channel")
		return
	}
	ctx := context.Background()
	if c.Request != nil {
		ctx = c.Request.Context()
	}
	health := readModelHealth(channel)
	sample := health[name]
	cached := sample.CheckedAt > 0 && time.Now().Unix()-sample.CheckedAt < int64(modelHealthInterval/time.Second)
	var probeErr *types.NewAPIError
	if !cached {
		stream, _ := strconv.ParseBool(c.Query("stream"))
		probeErr = newSafeModelHealthProbe(channel, userID, health, c.Query("endpoint_type"), stream)(ctx, name)
		if ctx.Err() == nil {
			persistModelSweep(channel, modelProbeSweep{Alive: channel.GetModels(), Disabled: readChannelDisabledModels(channel), Failures: readChannelModelFailures(channel), Health: health, Complete: true})
		}
		sample = health[name]
	}
	message := ""
	if sample.Method != "chat" {
		message = "Only the free model directory was checked; generation was not requested"
	}
	if probeErr == modelProbeUnverified || sample.Method == "unverified" {
		message = "The upstream does not provide a free model directory check; no generation request was sent"
	}
	if probeErr != nil && probeErr != modelProbeUnverified {
		message = sanitizeFetchModelsError(probeErr, channel.Key).Error()
	}
	c.JSON(http.StatusOK, gin.H{"success": sample.Success && probeErr == nil, "message": message, "time": float64(sample.LatencyMS) / 1000, "cached": cached, "probe_kind": sample.Method})
}

// channelTestSummary records the outcome of one channel test cycle so the
// system task can persist a per-run result for history.
type channelTestSummary struct {
	Tested          int `json:"tested"`
	Succeeded       int `json:"succeeded"`
	Failed          int `json:"failed"`
	Disabled        int `json:"disabled"`
	Enabled         int `json:"enabled"`
	ModelsDisabled  int `json:"models_disabled"`
	ModelsTested    int `json:"models_tested,omitempty"`
	ModelsSucceeded int `json:"models_succeeded,omitempty"`
	ModelsFailed    int `json:"models_failed,omitempty"`
}

// testChannelForHealthCheck shares the same safe per-model probes with Test All.
func testChannelForHealthCheck(ctx context.Context, channel *model.Channel, testUserID int, _ bool) channelTestSummary {
	// Both the scheduled runner and Test All use this per-model path. A failed
	// model is parked without disabling the channel or its healthy siblings.
	before := readModelHealth(channel)
	_, parked := refreshChannelModels(ctx, channel, testUserID, maxModelProbesPerSweep)
	summary := channelTestSummary{ModelsDisabled: parked}
	for name, sample := range readModelHealth(channel) {
		if sample.Checks <= before[name].Checks {
			continue
		}
		summary.ModelsTested++
		if sample.Success {
			summary.ModelsSucceeded++
		} else {
			summary.ModelsFailed++
		}
	}
	if summary.ModelsSucceeded > 0 && service.ShouldEnableChannel(nil, channel.Status) {
		service.EnableChannel(channel.Id, "", channel.Name)
		summary.Enabled++
	}
	if summary.ModelsTested > 0 {
		summary.Tested = 1
		if summary.ModelsFailed > 0 {
			summary.Failed = 1
		} else {
			summary.Succeeded = 1
		}
	}
	return summary
}

// billableGenerationModel reports whether a model name routes to an endpoint
// that spends real money on generation (image / video / speech). Those are never
// exercised by the automatic model health check, because generating an output
// would incur upstream cost; only their free discovery endpoint is checked.
func billableGenerationModel(model string) bool {
	lower := strings.ToLower(model)
	for _, marker := range [...]string{
		// Image generation.
		"image", "dall-e", "dall_e", "seedream", "flux", "stable-diffusion",
		"stable_diffusion", "midjourney", "mj-", "banana", "nana", "imagine", "cogview", "recraft", "ideogram", "picture", "photo",
		// Video generation.
		"video", "sora", "veo", "kling", "vidu", "jimeng", "seedance",
		"sd2.", "wan2", "wanx", "hailuo", "pika", "runway", "luma", "pixverse", "t2v", "i2v",
		// Audio and speech.
		"tts", "whisper", "speech", "voice", "audio", "music", "suno",
	} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

// maxModelProbesPerSweep bounds how many upstream probes an automatic sweep
// spends per channel. A channel may list thousands of models, so an unbounded
// sweep would send one request per model on every cycle. A bounded sweep starts
// at a rotating offset, so successive cycles still cover the whole list.
const maxModelProbesPerSweep = 20

// maxModelFailuresBeforeParking is how many consecutive probe failures a routed
// model may accumulate before it is parked. Transient failures below this
// threshold keep serving.
const maxModelFailuresBeforeParking = 3

// modelProbeFunc probes one model of a channel and returns the upstream error,
// if any.
type modelProbeFunc func(ctx context.Context, model string) *types.NewAPIError

// modelProbeSweep is the outcome of probing a channel's models.
type modelProbeSweep struct {
	// Alive lists the models that keep serving.
	Alive []string
	// Disabled lists the models that are parked.
	Disabled []string
	// Failures counts consecutive probe failures per model that still serves.
	Failures map[string]int
	// Complete reports whether the sweep ran to the end; a cancelled sweep must
	// not be persisted.
	Complete bool
	Health   map[string]modelHealthSample
}

// readChannelDisabledModels returns the models parked by an earlier sweep.
func readChannelDisabledModels(channel *model.Channel) []string {
	raw, ok := channel.GetOtherInfo()["disabled_models"].([]any)
	if !ok {
		return nil
	}
	disabled := make([]string, 0, len(raw))
	for _, value := range raw {
		if parked, ok := value.(string); ok && parked != "" {
			disabled = append(disabled, parked)
		}
	}
	return disabled
}

// readChannelModelFailures returns the consecutive probe failures recorded for
// the models that are still routed.
func readChannelModelFailures(channel *model.Channel) map[string]int {
	raw, ok := channel.GetOtherInfo()["model_failures"].(map[string]any)
	if !ok {
		return map[string]int{}
	}
	failures := make(map[string]int, len(raw))
	for model, value := range raw {
		if count, ok := value.(float64); ok && count > 0 {
			failures[model] = int(count)
		}
	}
	return failures
}

// sweepChannelModels probes the channel's models and reports which ones still
// answer. A routed model is parked once it failed more than
// maxModelFailuresBeforeParking consecutive probes, and a parked model that
// answers is routed again. Models the sweep does not reach keep their current
// state. Media models are checked through the safe free-directory probe.
func sweepChannelModels(ctx context.Context, channel *model.Channel, probe modelProbeFunc, limit int) modelProbeSweep {
	current := channel.GetModels()
	parked := readChannelDisabledModels(channel)
	failures := readChannelModelFailures(channel)
	if len(current) == 0 && len(parked) == 0 {
		return modelProbeSweep{Complete: true}
	}

	// Probe order: models that already failed first, so a failure count keeps
	// moving, then parked models so a recovery is noticed early, then the rest.
	// A bounded sweep starts the remainder at a rotating offset so successive
	// cycles still cover the whole list.
	ordered := make([]string, 0, len(current)+len(parked))
	for _, model := range current {
		if failures[model] > 0 {
			ordered = append(ordered, model)
		}
	}
	for _, model := range parked {
		if !slices.Contains(ordered, model) {
			ordered = append(ordered, model)
		}
	}
	rest := make([]string, 0, len(current)+len(parked))
	for _, model := range append(append([]string{}, parked...), current...) {
		if model != "" && !slices.Contains(ordered, model) && !slices.Contains(rest, model) {
			rest = append(rest, model)
		}
	}
	ordered = append(ordered, rest...)

	routed := make(map[string]bool, len(current))
	for _, model := range current {
		routed[model] = true
	}

	health := readModelHealth(channel)
	slices.SortStableFunc(ordered, func(a, b string) int { return cmp.Compare(health[a].CheckedAt, health[b].CheckedAt) })
	answered := make(map[string]bool, limit)
	for _, model := range ordered {
		if len(answered) >= limit {
			break
		}
		if ctx != nil {
			if err := ctx.Err(); err != nil {
				if errors.Is(err, context.DeadlineExceeded) {
					// The sweep ran out of its budget: keep what it learned.
					break
				}
				// A cancelled runner must not persist a partial sweep.
				return modelProbeSweep{}
			}
		}
		if sample := health[model]; sample.CheckedAt > 0 && time.Now().Unix()-sample.CheckedAt < int64(modelHealthInterval/time.Second) {
			continue
		}
		probeErr := probe(ctx, model)
		if probeErr == modelProbeUnverified {
			continue
		}
		ok := probeErr == nil
		answered[model] = ok
		if ok {
			delete(failures, model)
			continue
		}
		if routed[model] {
			failures[model]++
		}
	}

	// Rebuild the routing list in its stored order so a routine sweep that only
	// drops or restores models does not churn the string.
	sweep := modelProbeSweep{Complete: true}
	for _, model := range current {
		if ok, reached := answered[model]; reached && !ok && failures[model] >= maxModelFailuresBeforeParking {
			sweep.Disabled = append(sweep.Disabled, model)
			continue
		}
		sweep.Alive = append(sweep.Alive, model)
	}
	for _, model := range parked {
		ok, reached := answered[model]
		if reached {
			if ok && !slices.Contains(sweep.Alive, model) {
				sweep.Alive = append(sweep.Alive, model)
			} else if !ok {
				sweep.Disabled = append(sweep.Disabled, model)
			}
			continue
		}
		if !slices.Contains(sweep.Alive, model) {
			sweep.Disabled = append(sweep.Disabled, model)
		}
	}

	// Only models that still serve keep a failure count; parked or removed models
	// drop theirs.
	sweep.Failures = make(map[string]int, len(failures))
	for model, count := range failures {
		if count > 0 && slices.Contains(sweep.Alive, model) {
			sweep.Failures[model] = count
		}
	}
	return sweep
}

// persistModelSweep stores the routing state a sweep produced. A sweep that
// changes nothing does not touch the row.
func persistModelSweep(channel *model.Channel, sweep modelProbeSweep) {
	updates := map[string]any{}
	if newModels := strings.Join(sweep.Alive, ","); newModels != channel.Models {
		updates["models"] = newModels
	}

	newInfo := channel.GetOtherInfo()
	infoChanged := false
	if sweep.Health != nil && !maps.Equal(readModelHealth(channel), sweep.Health) {
		newInfo["model_health"] = sweep.Health
		infoChanged = true
	}
	if len(sweep.Disabled) == 0 {
		if _, present := newInfo["disabled_models"]; present {
			delete(newInfo, "disabled_models")
			infoChanged = true
		}
	} else if !slices.Equal(readChannelDisabledModels(channel), sweep.Disabled) {
		newInfo["disabled_models"] = sweep.Disabled
		infoChanged = true
	}
	if len(sweep.Failures) == 0 {
		if _, present := newInfo["model_failures"]; present {
			delete(newInfo, "model_failures")
			infoChanged = true
		}
	} else if !maps.Equal(readChannelModelFailures(channel), sweep.Failures) {
		newInfo["model_failures"] = sweep.Failures
		infoChanged = true
	}
	next := *channel
	if infoChanged {
		next.SetOtherInfo(newInfo)
		updates["other_info"] = next.OtherInfo
	}

	if len(updates) == 0 {
		return
	}
	modelsChanged := next.Models != strings.Join(sweep.Alive, ",")
	next.Models = strings.Join(sweep.Alive, ",")
	if err := model.SaveChannelHealthState(channel, next.Models, next.OtherInfo); err != nil {
		common.SysError(common.LogText("model health check: failed to persist channel %d: %v", channel.Id, err))
		return
	}
	if modelsChanged {
		model.InitChannelCache()
	}

}

// refreshChannelModels sweeps the channel's models with real upstream probes and
// persists the result, so routing only offers models that answered. It returns
// how many models stay usable and how many are parked.
func refreshChannelModels(ctx context.Context, channel *model.Channel, testUserID int, limit int) (usable, parked int) {
	if _, running := modelHealthRunning.LoadOrStore(channel.Id, true); running {
		return len(channel.GetModels()), len(readChannelDisabledModels(channel))
	}
	defer modelHealthRunning.Delete(channel.Id)
	latest, err := model.GetChannelById(channel.Id, true)
	if err != nil {
		return len(channel.GetModels()), len(readChannelDisabledModels(channel))
	}
	*channel = *latest
	health := readModelHealth(channel)
	probe := newSafeModelHealthProbe(channel, testUserID, health, "", shouldUseStreamForAutomaticChannelTest(channel))
	sweep := sweepChannelModels(ctx, channel, probe, limit)
	sweep.Health = health
	if !sweep.Complete {
		// Never persist a partial sweep during shutdown.
		return len(channel.GetModels()), len(readChannelDisabledModels(channel))
	}
	persistModelSweep(channel, sweep)
	if len(sweep.Disabled) > 0 || len(sweep.Failures) > 0 {
		common.SysLog(common.LogText("model health check: channel %d kept %d models, failing %d, disabled %d", channel.Id, len(sweep.Alive), len(sweep.Failures), len(sweep.Disabled)))
	}
	return len(sweep.Alive), len(sweep.Disabled)
}

// runChannelTestWorkers executes independent channel tests with bounded
// concurrency. Results and progress are reduced by the caller goroutine, so
// summary counts and the progress reporter remain serialized.
func runChannelTestWorkers(
	ctx context.Context,
	channels []*model.Channel,
	concurrency int,
	run func(context.Context, *model.Channel) channelTestSummary,
	report func(processed, total int),
) channelTestSummary {
	if ctx == nil {
		ctx = context.Background()
	}
	total := len(channels)
	if report != nil {
		report(0, total)
	}
	if total == 0 {
		return channelTestSummary{}
	}

	workerCount := min(operation_setting.NormalizeChannelTestConcurrency(concurrency), total)
	jobs := make(chan *model.Channel)
	results := make(chan channelTestSummary)

	var workers sync.WaitGroup
	workers.Add(workerCount)
	for range workerCount {
		go func() {
			defer workers.Done()
			for {
				select {
				case <-ctx.Done():
					return
				case channel, ok := <-jobs:
					if !ok {
						return
					}
					if ctx.Err() != nil {
						return
					}

					result := channelTestSummary{}
					if channel != nil && channel.Status != common.ChannelStatusManuallyDisabled {
						result = run(ctx, channel)
					}

					results <- result

					if common.RequestInterval > 0 {
						select {
						case <-ctx.Done():
							return
						case <-time.After(common.RequestInterval):
						}
					}
				}
			}
		}()
	}

	go func() {
		defer close(jobs)
		for _, channel := range channels {
			select {
			case <-ctx.Done():
				return
			case jobs <- channel:
			}
		}
	}()

	go func() {
		workers.Wait()
		close(results)
	}()

	summary := channelTestSummary{}
	processed := 0
	for result := range results {
		summary.Tested += result.Tested
		summary.Succeeded += result.Succeeded
		summary.Failed += result.Failed
		summary.Disabled += result.Disabled
		summary.Enabled += result.Enabled
		summary.ModelsDisabled += result.ModelsDisabled
		summary.ModelsTested += result.ModelsTested
		summary.ModelsSucceeded += result.ModelsSucceeded
		summary.ModelsFailed += result.ModelsFailed
		processed++
		if report != nil && ctx.Err() == nil {
			report(processed, total)
		}
	}
	return summary
}

// performChannelTests runs channel health checks with the configured bounded
// concurrency and honors cancellation when a system-task runner loses its
// lease.
func performChannelTests(ctx context.Context, channels []*model.Channel, testUserID int, allowDisable bool, concurrency int, report func(processed, total int)) channelTestSummary {
	if ctx == nil {
		ctx = context.Background()
	}
	return runChannelTestWorkers(
		ctx,
		channels,
		concurrency,
		func(ctx context.Context, channel *model.Channel) channelTestSummary {
			return testChannelForHealthCheck(ctx, channel, testUserID, allowDisable)
		},
		report,
	)
}

// runChannelTestTask runs one synchronous channel test cycle for the system task
// runner (both the scheduled job and the manual "test all channels" trigger go
// through here). It honors ctx cancellation so a runner that loses its lease
// stops promptly. mode selects the channel set: an empty mode falls back to the
// configured monitor ChannelTestMode (scheduled behavior), while a manual
// trigger passes ChannelTestModeScheduledAll to test every channel. When notify
// is set the root user is notified on completion. Cross-instance execution is
// guarded by the system task per-type lock, so no process-local guard is needed.
func runChannelTestTask(ctx context.Context, mode string, notify bool, report func(processed, total int)) (channelTestSummary, error) {
	testUserID, err := resolveChannelTestUserID(nil)
	if err != nil {
		return channelTestSummary{}, err
	}
	channels, err := model.GetAllChannels(0, 0, true, false)
	if err != nil {
		return channelTestSummary{}, err
	}
	if strings.TrimSpace(mode) == "" {
		mode = operation_setting.GetMonitorSetting().ChannelTestMode
	}
	selected := selectChannelsForAutomaticTest(channels, mode)
	allowDisable := mode != operation_setting.ChannelTestModePassiveRecovery
	concurrency := operation_setting.GetMonitorSetting().ChannelTestConcurrency
	summary := performChannelTests(ctx, selected, testUserID, allowDisable, concurrency, report)
	if notify && (ctx == nil || ctx.Err() == nil) {
		lang := service.RootUserLanguage()
		service.NotifyRootUser(dto.NotifyTypeChannelTest, i18n.Translate(lang, i18n.MsgChannelTestCompletedSubject), i18n.Translate(lang, i18n.MsgChannelTestCompletedContent))
	}
	return summary, nil
}

func selectChannelsForAutomaticTest(channels []*model.Channel, mode string) []*model.Channel {
	selected := make([]*model.Channel, 0, len(channels))
	for _, channel := range channels {
		if channel.Status == common.ChannelStatusManuallyDisabled {
			continue
		}
		if mode == operation_setting.ChannelTestModeAutoBanOnly && !channel.GetAutoBan() {
			continue
		}
		if mode == operation_setting.ChannelTestModePassiveRecovery && channel.Status != common.ChannelStatusAutoDisabled && len(readChannelDisabledModels(channel)) == 0 && len(readChannelModelFailures(channel)) == 0 {
			continue
		}
		selected = append(selected, channel)
	}
	return selected
}

// TestAllChannels enqueues a channel_test system task instead of running the
// test loop inline. If any channel_test task is already active, the manual run is
// rejected so the caller does not mistake a scheduled run for this manual one.
func TestAllChannels(c *gin.Context) {
	task, created, err := service.EnqueueSystemTask(model.SystemTaskTypeChannelTest, channelTestTaskPayload{
		Mode:   operation_setting.ChannelTestModeScheduledAll,
		Notify: true,
	})
	if err != nil {
		common.ApiError(c, err)
		return
	}
	if !created {
		msg := common.NewMessage("A channel test task is already running or queued. Cannot start this manual task")
		common.ApiErrorStatus(c, http.StatusConflict, msg, gin.H{"data": gin.H{
			"task_id": task.TaskID,
			"status":  task.Status,
			"type":    task.Type,
		}})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": "",
		"data": gin.H{
			"task_id": task.TaskID,
			"status":  task.Status,
		},
	})
}
