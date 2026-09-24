package opencode

const (
	ChannelNameZen = "opencode-zen"
	ChannelNameGo  = "opencode-go"
)

// ModelList is empty because models and their per-model endpoint protocols are
// discovered dynamically from the upstream /v1/models endpoint and
// common.OpenCodeProtocolForModel.
var ModelList = []string{}
