package opencode

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/relay/channel"
	"github.com/QuantumNous/new-api/relay/channel/claude"
	"github.com/QuantumNous/new-api/relay/channel/gemini"
	"github.com/QuantumNous/new-api/relay/channel/openai"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/service"

	"github.com/gin-gonic/gin"
)

// Adaptor serves OpenCode Zen and OpenCode Go. Both gateways expose one API key
// across several protocol-native endpoints, and the endpoint a model answers on
// is fixed by the model id rather than by the client request. The adaptor
// resolves the upstream protocol from the model and reuses the native Anthropic,
// Gemini and OpenAI adaptors to convert the request and the response.
type Adaptor struct {
	ChannelType int

	openaiAdaptor openai.Adaptor
	claudeAdaptor claude.Adaptor
	geminiAdaptor gemini.Adaptor
}

func (a *Adaptor) Init(info *relaycommon.RelayInfo) {
	if info == nil {
		return
	}
	a.ChannelType = info.ChannelType
	a.openaiAdaptor.Init(info)
	a.claudeAdaptor.Init(info)
	a.geminiAdaptor.Init(info)
}

func (a *Adaptor) GetModelList() []string {
	return ModelList
}

func (a *Adaptor) GetChannelName() string {
	if a.ChannelType == constant.ChannelTypeOpenCodeGo {
		return ChannelNameGo
	}
	return ChannelNameZen
}

// upstreamProtocol resolves the endpoint protocol documented for this request's
// model on this channel.
func (a *Adaptor) upstreamProtocol(info *relaycommon.RelayInfo) common.OpenCodeProtocol {
	if info == nil {
		return common.OpenCodeProtocolUnsupported
	}
	return common.OpenCodeModelProtocolForChannel(info.ChannelType, upstreamModel(info))
}

func upstreamModel(info *relaycommon.RelayInfo) string {
	if info == nil {
		return ""
	}
	model := strings.TrimSpace(info.UpstreamModelName)
	if model == "" {
		model = strings.TrimSpace(info.OriginModelName)
	}
	return model
}

func (a *Adaptor) GetRequestURL(info *relaycommon.RelayInfo) (string, error) {
	baseURL := strings.TrimRight(info.ChannelBaseUrl, "/")
	switch a.upstreamProtocol(info) {
	case common.OpenCodeProtocolChat:
		return relaycommon.GetFullRequestURL(baseURL, "/v1/chat/completions", info.ChannelType), nil
	case common.OpenCodeProtocolResponses:
		return relaycommon.GetFullRequestURL(baseURL, "/v1/responses", info.ChannelType), nil
	case common.OpenCodeProtocolMessages:
		return relaycommon.GetFullRequestURL(baseURL, "/v1/messages", info.ChannelType), nil
	case common.OpenCodeProtocolGemini:
		// OpenCode serves Gemini on {base}/v1/models/{model}:generateContent, not
		// the Google v1beta route, so the native Gemini URL builder is not used.
		action := "generateContent"
		if info.IsStream {
			action = "streamGenerateContent?alt=sse"
			info.DisablePing = true
		}
		path := fmt.Sprintf("/v1/models/%s:%s", upstreamModel(info), action)
		return relaycommon.GetFullRequestURL(baseURL, path, info.ChannelType), nil
	default:
		return "", unsupportedModelError(info)
	}
}

func (a *Adaptor) SetupRequestHeader(c *gin.Context, req *http.Header, info *relaycommon.RelayInfo) error {
	channel.SetupApiRequestHeader(info, c, req)
	req.Set("Authorization", "Bearer "+info.ApiKey)
	switch a.upstreamProtocol(info) {
	case common.OpenCodeProtocolMessages:
		// OpenCode accepts the gateway bearer token and the native Anthropic
		// header; sending both keeps the request valid either way.
		req.Set("x-api-key", info.ApiKey)
		anthropicVersion := ""
		if c != nil && c.Request != nil {
			anthropicVersion = c.Request.Header.Get("anthropic-version")
		}
		if anthropicVersion == "" {
			anthropicVersion = "2023-06-01"
		}
		req.Set("anthropic-version", anthropicVersion)
		if c != nil {
			claude.CommonClaudeHeadersOperation(c, req, info)
		}
	case common.OpenCodeProtocolGemini:
		req.Set("x-goog-api-key", info.ApiKey)
	}
	return nil
}

func (a *Adaptor) ConvertOpenAIRequest(c *gin.Context, info *relaycommon.RelayInfo, request *dto.GeneralOpenAIRequest) (any, error) {
	if request == nil {
		return nil, errors.New("request is nil")
	}
	switch a.upstreamProtocol(info) {
	case common.OpenCodeProtocolChat:
		return a.convertOpenAICompatibleRequest(c, info, request)
	case common.OpenCodeProtocolResponses:
		return convertOpenCodeRequest(c, info, types.RelayFormatOpenAIResponses, request)
	case common.OpenCodeProtocolMessages:
		return convertOpenCodeRequest(c, info, types.RelayFormatClaude, request)
	case common.OpenCodeProtocolGemini:
		return convertOpenCodeRequest(c, info, types.RelayFormatGemini, request)
	default:
		return nil, unsupportedModelError(info)
	}
}

func (a *Adaptor) ConvertClaudeRequest(c *gin.Context, info *relaycommon.RelayInfo, request *dto.ClaudeRequest) (any, error) {
	if request == nil {
		return nil, errors.New("request is nil")
	}
	switch a.upstreamProtocol(info) {
	case common.OpenCodeProtocolMessages:
		return a.claudeAdaptor.ConvertClaudeRequest(c, info, request)
	case common.OpenCodeProtocolChat:
		return convertOpenCodeChatRequest(c, info, request)
	case common.OpenCodeProtocolResponses:
		return convertOpenCodeRequest(c, info, types.RelayFormatOpenAIResponses, request)
	case common.OpenCodeProtocolGemini:
		return convertOpenCodeRequest(c, info, types.RelayFormatGemini, request)
	default:
		return nil, unsupportedModelError(info)
	}
}

func (a *Adaptor) ConvertGeminiRequest(c *gin.Context, info *relaycommon.RelayInfo, request *dto.GeminiChatRequest) (any, error) {
	if request == nil {
		return nil, errors.New("request is nil")
	}
	switch a.upstreamProtocol(info) {
	case common.OpenCodeProtocolGemini:
		return a.geminiAdaptor.ConvertGeminiRequest(c, info, request)
	case common.OpenCodeProtocolChat:
		return convertOpenCodeChatRequest(c, info, request)
	case common.OpenCodeProtocolMessages:
		return convertOpenCodeRequest(c, info, types.RelayFormatClaude, request)
	case common.OpenCodeProtocolResponses:
		return convertOpenCodeRequest(c, info, types.RelayFormatOpenAIResponses, request)
	default:
		return nil, unsupportedModelError(info)
	}
}

func (a *Adaptor) ConvertOpenAIResponsesRequest(c *gin.Context, info *relaycommon.RelayInfo, request dto.OpenAIResponsesRequest) (any, error) {
	switch a.upstreamProtocol(info) {
	case common.OpenCodeProtocolResponses:
		return a.convertOpenAIResponsesCompatibleRequest(c, info, request)
	case common.OpenCodeProtocolChat:
		return convertOpenCodeChatRequest(c, info, &request)
	case common.OpenCodeProtocolMessages:
		return convertOpenCodeRequest(c, info, types.RelayFormatClaude, &request)
	case common.OpenCodeProtocolGemini:
		return convertOpenCodeRequest(c, info, types.RelayFormatGemini, &request)
	default:
		return nil, unsupportedModelError(info)
	}
}

func (a *Adaptor) ConvertEmbeddingRequest(c *gin.Context, info *relaycommon.RelayInfo, request dto.EmbeddingRequest) (any, error) {
	return nil, errors.New("opencode channel: /v1/embeddings endpoint not supported")
}

func (a *Adaptor) ConvertRerankRequest(c *gin.Context, relayMode int, request dto.RerankRequest) (any, error) {
	return nil, errors.New("opencode channel: /v1/rerank endpoint not supported")
}

func (a *Adaptor) ConvertImageRequest(c *gin.Context, info *relaycommon.RelayInfo, request dto.ImageRequest) (any, error) {
	return nil, errors.New("opencode channel: image generation endpoint not supported")
}

func (a *Adaptor) ConvertAudioRequest(c *gin.Context, info *relaycommon.RelayInfo, request dto.AudioRequest) (io.Reader, error) {
	return nil, errors.New("opencode channel: audio endpoint not supported")
}

func (a *Adaptor) DoRequest(c *gin.Context, info *relaycommon.RelayInfo, requestBody io.Reader) (any, error) {
	return channel.DoApiRequest(a, c, info, requestBody)
}

func (a *Adaptor) DoResponse(c *gin.Context, resp *http.Response, info *relaycommon.RelayInfo) (usage any, err *types.NewAPIError) {
	switch a.upstreamProtocol(info) {
	case common.OpenCodeProtocolChat:
		// The upstream is OpenAI-compatible chat. A Responses client needs the
		// dedicated chat->responses renderer; every other client format is handled
		// by the OpenAI handler, which knows RelayFormat.
		if info.RelayFormat == types.RelayFormatOpenAIResponses {
			if info.IsStream {
				return openai.OaiChatToResponsesStreamHandler(c, info, resp)
			}
			return openai.OaiChatToResponsesHandler(c, info, resp)
		}
		return a.openaiAdaptor.DoResponse(c, resp, info)
	case common.OpenCodeProtocolResponses:
		switch info.RelayFormat {
		case types.RelayFormatOpenAIResponses:
			return a.openaiAdaptor.DoResponse(c, resp, info)
		case types.RelayFormatOpenAI:
			if isEventStreamResponse(resp) {
				if info.IsStream {
					return openai.OaiResponsesToChatStreamHandler(c, info, resp)
				}
				return openai.OaiResponsesToChatBufferedStreamHandler(c, info, resp)
			}
			return openai.OaiResponsesToChatHandler(c, info, resp)
		default:
			return nil, unsupportedResponseFormatError(info)
		}
	case common.OpenCodeProtocolMessages:
		// The Anthropic handler renders Claude, OpenAI, Responses and Gemini
		// client formats.
		return a.claudeAdaptor.DoResponse(c, resp, info)
	case common.OpenCodeProtocolGemini:
		if info.RelayFormat == types.RelayFormatClaude {
			return nil, unsupportedResponseFormatError(info)
		}
		return a.geminiAdaptor.DoResponse(c, resp, info)
	default:
		return nil, types.NewErrorWithStatusCode(unsupportedModelError(info), types.ErrorCodeInvalidRequest, http.StatusBadRequest, types.ErrOptionWithSkipRetry())
	}
}

func isEventStreamResponse(resp *http.Response) bool {
	if resp == nil {
		return false
	}
	return strings.Contains(strings.ToLower(resp.Header.Get("Content-Type")), "text/event-stream")
}

// convertOpenCodeChatRequest converts a Claude, Gemini or Responses request into
// an OpenAI chat completions request for a chat-protocol upstream. Streaming
// requests ask the upstream for usage because the downstream protocol reports
// usage in its own format and never carries stream_options itself.
func convertOpenCodeChatRequest(c *gin.Context, info *relaycommon.RelayInfo, request any) (any, error) {
	converted, err := convertOpenCodeRequest(c, info, types.RelayFormatOpenAI, request)
	if err != nil {
		return nil, err
	}
	chatRequest, ok := converted.(*dto.GeneralOpenAIRequest)
	if !ok {
		return nil, fmt.Errorf("expected OpenAI chat completions request, got %T", converted)
	}
	if info.SupportStreamOptions && info.IsStream {
		chatRequest.StreamOptions = &dto.StreamOptions{IncludeUsage: true}
	}
	return chatRequest, nil
}

func convertOpenCodeRequest(c *gin.Context, info *relaycommon.RelayInfo, target types.RelayFormat, request any) (any, error) {
	result, err := service.ConvertRequest(c, info, target, request)
	if err != nil {
		return nil, err
	}
	return result.Value, nil
}

func (a *Adaptor) convertOpenAICompatibleRequest(c *gin.Context, info *relaycommon.RelayInfo, request *dto.GeneralOpenAIRequest) (any, error) {
	return withOpenAICompatibleChannel(info, func() (any, error) {
		return a.openaiAdaptor.ConvertOpenAIRequest(c, info, request)
	})
}

func (a *Adaptor) convertOpenAIResponsesCompatibleRequest(c *gin.Context, info *relaycommon.RelayInfo, request dto.OpenAIResponsesRequest) (any, error) {
	return withOpenAICompatibleChannel(info, func() (any, error) {
		return a.openaiAdaptor.ConvertOpenAIResponsesRequest(c, info, request)
	})
}

// withOpenAICompatibleChannel runs fn with the channel type presented as OpenAI.
// The shared OpenAI adaptor preserves stream_options and applies OpenAI-specific
// normalization only for the OpenAI/Azure channel types, and the OpenCode
// endpoints are OpenAI-compatible.
func withOpenAICompatibleChannel(info *relaycommon.RelayInfo, fn func() (any, error)) (any, error) {
	original := info.ChannelType
	info.ChannelType = constant.ChannelTypeOpenAI
	defer func() {
		info.ChannelType = original
	}()
	return fn()
}

func unsupportedModelError(info *relaycommon.RelayInfo) error {
	return fmt.Errorf("opencode channel: model %q is not served on a supported endpoint", upstreamModel(info))
}

func unsupportedResponseFormatError(info *relaycommon.RelayInfo) *types.NewAPIError {
	return types.NewErrorWithStatusCode(
		fmt.Errorf("opencode channel: model %q does not support the %s client format", upstreamModel(info), info.RelayFormat),
		types.ErrorCodeInvalidRequest, http.StatusBadRequest, types.ErrOptionWithSkipRetry(),
	)
}
