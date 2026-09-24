package opencode_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/relay"
	"github.com/QuantumNous/new-api/relay/channel/opencode"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newRelayInfo(channelType int, model string) *relaycommon.RelayInfo {
	return &relaycommon.RelayInfo{
		OriginModelName: model,
		ChannelMeta: &relaycommon.ChannelMeta{
			ChannelType:       channelType,
			ChannelBaseUrl:    "https://opencode.ai/zen",
			ApiKey:            "test-key",
			UpstreamModelName: model,
		},
	}
}

func newGinContext(t *testing.T, target string, headers map[string]string) (*gin.Context, *httptest.ResponseRecorder) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, target, nil)
	for name, value := range headers {
		c.Request.Header.Set(name, value)
	}
	return c, recorder
}

func TestOpenCodeProtocolForModel(t *testing.T) {
	tests := []struct {
		channelType int
		model       string
		want        common.OpenCodeProtocol
	}{
		{constant.ChannelTypeOpenCodeGo, "glm-5.3", common.OpenCodeProtocolChat},
		{constant.ChannelTypeOpenCodeGo, "deepseek-v4.1-flash", common.OpenCodeProtocolChat},
		{constant.ChannelTypeOpenCodeGo, "kimi-k3", common.OpenCodeProtocolChat},
		{constant.ChannelTypeOpenCodeGo, "gpt-6-luna", common.OpenCodeProtocolResponses},
		{constant.ChannelTypeOpenCodeGo, "grok-4.7", common.OpenCodeProtocolResponses},
		{constant.ChannelTypeOpenCodeGo, "muse-spark-1.3-contributor", common.OpenCodeProtocolResponses},
		{constant.ChannelTypeOpenCodeGo, "qwen3.7-max", common.OpenCodeProtocolMessages},
		{constant.ChannelTypeOpenCodeGo, "minimax-m3", common.OpenCodeProtocolMessages},
		{constant.ChannelTypeOpenCodeGo, "gemini-3.8-flash", common.OpenCodeProtocolUnsupported},
		{constant.ChannelTypeOpenCodeGo, "jev-1.13", common.OpenCodeProtocolUnsupported},
		{constant.ChannelTypeOpenCodeZen, "gemini-3.8-flash", common.OpenCodeProtocolGemini},
		{constant.ChannelTypeOpenCodeZen, "claude-sonnet-4-5", common.OpenCodeProtocolMessages},
		{constant.ChannelTypeOpenCodeZen, "gpt-5.5", common.OpenCodeProtocolResponses},
		{constant.ChannelTypeOpenCodeZen, "some-new-model", common.OpenCodeProtocolChat},
		{constant.ChannelTypeOpenAI, "gpt-5.5", common.OpenCodeProtocolUnsupported},
	}
	for _, test := range tests {
		t.Run(test.model, func(t *testing.T) {
			assert.Equal(t, test.want, common.OpenCodeModelProtocolForChannel(test.channelType, test.model))
		})
	}
}

func TestOpenCodeRequestURLs(t *testing.T) {
	tests := []struct {
		name        string
		channelType int
		model       string
		stream      bool
		want        string
		wantErr     string
	}{
		{
			name:        "chat protocol",
			channelType: constant.ChannelTypeOpenCodeGo,
			model:       "glm-5.3",
			want:        "https://opencode.ai/zen/v1/chat/completions",
		},
		{
			name:        "responses protocol",
			channelType: constant.ChannelTypeOpenCodeGo,
			model:       "gpt-6-luna",
			want:        "https://opencode.ai/zen/v1/responses",
		},
		{
			name:        "messages protocol",
			channelType: constant.ChannelTypeOpenCodeGo,
			model:       "qwen3.7-max",
			want:        "https://opencode.ai/zen/v1/messages",
		},
		{
			name:        "gemini protocol",
			channelType: constant.ChannelTypeOpenCodeZen,
			model:       "gemini-3.8-flash",
			want:        "https://opencode.ai/zen/v1/models/gemini-3.8-flash:generateContent",
		},
		{
			name:        "gemini streaming uses sse",
			channelType: constant.ChannelTypeOpenCodeZen,
			model:       "gemini-3.8-flash",
			stream:      true,
			want:        "https://opencode.ai/zen/v1/models/gemini-3.8-flash:streamGenerateContent?alt=sse",
		},
		{
			name:        "go gemini is rejected",
			channelType: constant.ChannelTypeOpenCodeGo,
			model:       "gemini-3.8-flash",
			wantErr:     "not served on a supported endpoint",
		},
		{
			name:        "systemone is rejected",
			channelType: constant.ChannelTypeOpenCodeZen,
			model:       "jev-1.13",
			wantErr:     "not served on a supported endpoint",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			info := newRelayInfo(test.channelType, test.model)
			info.IsStream = test.stream
			adaptor := &opencode.Adaptor{}
			adaptor.Init(info)
			url, err := adaptor.GetRequestURL(info)
			if test.wantErr != "" {
				require.ErrorContains(t, err, test.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, test.want, url)
		})
	}
}

func TestOpenCodeSetupRequestHeader(t *testing.T) {
	tests := []struct {
		name              string
		model             string
		clientHeaders     map[string]string
		wantAuthorization string
		wantAPIKey        string
		wantVersion       string
		wantGoogleKey     string
	}{
		{
			name:              "chat uses bearer",
			model:             "glm-5.3",
			wantAuthorization: "Bearer test-key",
		},
		{
			name:              "messages uses anthropic headers",
			model:             "qwen3.7-max",
			clientHeaders:     map[string]string{"anthropic-version": "2024-06-01"},
			wantAPIKey:        "test-key",
			wantVersion:       "2024-06-01",
			wantAuthorization: "Bearer test-key",
		},
		{
			name:              "gemini uses google key",
			model:             "gemini-3.8-flash",
			wantGoogleKey:     "test-key",
			wantAuthorization: "Bearer test-key",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			info := newRelayInfo(constant.ChannelTypeOpenCodeZen, test.model)
			adaptor := &opencode.Adaptor{}
			adaptor.Init(info)
			c, _ := newGinContext(t, "/v1/chat/completions", test.clientHeaders)
			headers := http.Header{}
			require.NoError(t, adaptor.SetupRequestHeader(c, &headers, info))
			assert.Equal(t, test.wantAuthorization, headers.Get("Authorization"))
			assert.Equal(t, test.wantAPIKey, headers.Get("x-api-key"))
			assert.Equal(t, test.wantVersion, headers.Get("anthropic-version"))
			assert.Equal(t, test.wantGoogleKey, headers.Get("x-goog-api-key"))
		})
	}
}

func TestOpenCodeConvertOpenAIRequestFollowsProtocol(t *testing.T) {
	chatRequest := func() *dto.GeneralOpenAIRequest {
		return &dto.GeneralOpenAIRequest{
			Model:    "target",
			Messages: []dto.Message{{Role: "user", Content: "hello"}},
		}
	}

	t.Run("chat protocol preserves stream options", func(t *testing.T) {
		info := newRelayInfo(constant.ChannelTypeOpenCodeGo, "glm-5.3")
		info.RelayFormat = types.RelayFormatOpenAI
		info.IsStream = true
		info.SupportStreamOptions = true
		request := chatRequest()
		request.StreamOptions = &dto.StreamOptions{IncludeUsage: true}
		adaptor := &opencode.Adaptor{}
		adaptor.Init(info)
		c, _ := newGinContext(t, "/v1/chat/completions", nil)
		converted, err := adaptor.ConvertOpenAIRequest(c, info, request)
		require.NoError(t, err)
		chat, ok := converted.(*dto.GeneralOpenAIRequest)
		require.True(t, ok)
		require.NotNil(t, chat.StreamOptions)
		assert.True(t, chat.StreamOptions.IncludeUsage)
	})

	t.Run("responses protocol converts to responses request", func(t *testing.T) {
		info := newRelayInfo(constant.ChannelTypeOpenCodeGo, "gpt-6-luna")
		info.RelayFormat = types.RelayFormatOpenAI
		adaptor := &opencode.Adaptor{}
		adaptor.Init(info)
		c, _ := newGinContext(t, "/v1/chat/completions", nil)
		converted, err := adaptor.ConvertOpenAIRequest(c, info, chatRequest())
		require.NoError(t, err)
		_, ok := converted.(*dto.OpenAIResponsesRequest)
		require.True(t, ok, "expected an OpenAI Responses request, got %T", converted)
	})

	t.Run("messages protocol converts to claude request", func(t *testing.T) {
		info := newRelayInfo(constant.ChannelTypeOpenCodeGo, "qwen3.7-max")
		info.RelayFormat = types.RelayFormatOpenAI
		adaptor := &opencode.Adaptor{}
		adaptor.Init(info)
		c, _ := newGinContext(t, "/v1/chat/completions", nil)
		converted, err := adaptor.ConvertOpenAIRequest(c, info, chatRequest())
		require.NoError(t, err)
		_, ok := converted.(*dto.ClaudeRequest)
		require.True(t, ok, "expected a Claude request, got %T", converted)
	})

	t.Run("gemini protocol converts to gemini request", func(t *testing.T) {
		info := newRelayInfo(constant.ChannelTypeOpenCodeZen, "gemini-3.8-flash")
		info.RelayFormat = types.RelayFormatOpenAI
		adaptor := &opencode.Adaptor{}
		adaptor.Init(info)
		c, _ := newGinContext(t, "/v1/chat/completions", nil)
		converted, err := adaptor.ConvertOpenAIRequest(c, info, chatRequest())
		require.NoError(t, err)
		_, ok := converted.(*dto.GeminiChatRequest)
		require.True(t, ok, "expected a Gemini request, got %T", converted)
	})
}

func TestOpenCodeDoResponseRejectsUnsupportedClientFormat(t *testing.T) {
	info := newRelayInfo(constant.ChannelTypeOpenCodeGo, "gpt-6-luna")
	info.RelayFormat = types.RelayFormatClaude
	adaptor := &opencode.Adaptor{}
	adaptor.Init(info)
	c, _ := newGinContext(t, "/v1/messages", nil)
	_, apiErr := adaptor.DoResponse(c, &http.Response{Header: http.Header{}}, info)
	require.NotNil(t, apiErr)
	assert.Equal(t, http.StatusBadRequest, apiErr.StatusCode)
	assert.Contains(t, apiErr.Error(), "does not support the claude client format")
}

func TestOpenCodeEndpointTypes(t *testing.T) {
	assert.Equal(t,
		[]constant.EndpointType{constant.EndpointTypeOpenAI},
		common.GetEndpointTypesByChannelType(constant.ChannelTypeOpenCodeGo, "glm-5.3"))
	assert.Equal(t,
		[]constant.EndpointType{constant.EndpointTypeOpenAIResponse, constant.EndpointTypeOpenAI},
		common.GetEndpointTypesByChannelType(constant.ChannelTypeOpenCodeGo, "grok-4.7"))
	assert.Equal(t,
		[]constant.EndpointType{constant.EndpointTypeAnthropic, constant.EndpointTypeOpenAI},
		common.GetEndpointTypesByChannelType(constant.ChannelTypeOpenCodeZen, "claude-sonnet-4-5"))
	assert.Equal(t,
		[]constant.EndpointType{constant.EndpointTypeGemini, constant.EndpointTypeOpenAI},
		common.GetEndpointTypesByChannelType(constant.ChannelTypeOpenCodeZen, "gemini-3.8-flash"))
	assert.Empty(t, common.GetEndpointTypesByChannelType(constant.ChannelTypeOpenCodeGo, "gemini-3.8-flash"))
}

func TestOpenCodeChannelRegistration(t *testing.T) {
	for _, channelType := range []int{constant.ChannelTypeOpenCodeZen, constant.ChannelTypeOpenCodeGo} {
		apiType, ok := common.ChannelType2APIType(channelType)
		require.True(t, ok, "channel type %d has no API type", channelType)
		assert.Equal(t, constant.APITypeOpenCode, apiType)
		adaptor := relay.GetAdaptor(apiType)
		require.NotNil(t, adaptor)
		assert.NotEmpty(t, constant.GetChannelBaseURL(channelType))
		assert.NotEmpty(t, constant.GetChannelTypeName(channelType))
	}
}
