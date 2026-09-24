package common

import (
	"strings"

	"github.com/QuantumNous/new-api/constant"
)

// OpenCodeProtocol identifies the upstream endpoint protocol that serves an
// OpenCode model. OpenCode documents exactly one endpoint per model (see
// https://opencode.ai/v2/docs/console/models and
// https://opencode.ai/v2/docs/console/go), so the protocol is a property of the
// model rather than of the client request that reaches this gateway.
type OpenCodeProtocol string

const (
	OpenCodeProtocolChat        OpenCodeProtocol = "chat_completions"
	OpenCodeProtocolResponses   OpenCodeProtocol = "responses"
	OpenCodeProtocolMessages    OpenCodeProtocol = "messages"
	OpenCodeProtocolGemini      OpenCodeProtocol = "gemini_generate_content"
	OpenCodeProtocolUnsupported OpenCodeProtocol = "unsupported"
)

// OpenCodeProtocolForModel maps an upstream model id to its documented endpoint
// protocol. OpenCode serves each model family on a fixed endpoint, so the family
// prefix is the routing key. Unknown ids fall back to the OpenAI-compatible chat
// protocol, which covers the open-weight chat models and any newly added model
// that is not yet listed here.
func OpenCodeProtocolForModel(model string) OpenCodeProtocol {
	model = strings.ToLower(strings.TrimSpace(model))
	switch {
	case strings.HasPrefix(model, "gemini-"):
		return OpenCodeProtocolGemini
	case strings.HasPrefix(model, "claude-"),
		strings.HasPrefix(model, "qwen"),
		strings.HasPrefix(model, "minimax-"):
		return OpenCodeProtocolMessages
	case strings.HasPrefix(model, "gpt-"), strings.HasPrefix(model, "grok-"), strings.HasPrefix(model, "muse-spark-"):
		return OpenCodeProtocolResponses
	case strings.HasPrefix(model, "jev-"):
		// System One evaluation models use the undocumented /v1/systemone schema.
		return OpenCodeProtocolUnsupported
	default:
		return OpenCodeProtocolChat
	}
}

// OpenCodeModelProtocolForChannel resolves the upstream protocol for a model on
// an OpenCode channel. OpenCode Go does not serve Gemini-native models, so those
// ids are reported as unsupported instead of being routed to a non-existent
// endpoint.
func OpenCodeModelProtocolForChannel(channelType int, model string) OpenCodeProtocol {
	if channelType != constant.ChannelTypeOpenCodeZen && channelType != constant.ChannelTypeOpenCodeGo {
		return OpenCodeProtocolUnsupported
	}
	protocol := OpenCodeProtocolForModel(model)
	if protocol == OpenCodeProtocolGemini && channelType == constant.ChannelTypeOpenCodeGo {
		return OpenCodeProtocolUnsupported
	}
	return protocol
}

// OpenCodeSupportedEndpointTypes reports the client-facing endpoints a model can
// be reached through. The model's documented protocol comes first, and the OpenAI
// chat endpoint is always included because the channel converts a chat request to
// the documented upstream protocol.
func OpenCodeSupportedEndpointTypes(channelType int, model string) []constant.EndpointType {
	switch OpenCodeModelProtocolForChannel(channelType, model) {
	case OpenCodeProtocolChat:
		return []constant.EndpointType{constant.EndpointTypeOpenAI}
	case OpenCodeProtocolResponses:
		return []constant.EndpointType{constant.EndpointTypeOpenAIResponse, constant.EndpointTypeOpenAI}
	case OpenCodeProtocolMessages:
		return []constant.EndpointType{constant.EndpointTypeAnthropic, constant.EndpointTypeOpenAI}
	case OpenCodeProtocolGemini:
		return []constant.EndpointType{constant.EndpointTypeGemini, constant.EndpointTypeOpenAI}
	default:
		return nil
	}
}
