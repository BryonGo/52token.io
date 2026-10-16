package dto

// ChannelCostSettings describes purchased upstream credits, not a USD exchange rate.
// CreditsPerCNY is the number of upstream balance units received for one yuan.
type ChannelCostSettings struct {
	Enabled       bool    `json:"enabled"`
	Source        string  `json:"source"`
	CreditsPerCNY float64 `json:"credits_per_cny"`
	UpstreamGroup string  `json:"upstream_group,omitempty"`
}
