package controller

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
)

const modelHealthInterval = 30 * time.Minute

var modelHealthRunning sync.Map

// A missing free discovery endpoint is not evidence that generation fails.
var modelProbeUnverified = types.NewError(errors.New("free model discovery unavailable"), types.ErrorCodeBadResponse)

type modelHealthSample struct {
	CheckedAt int64  `json:"checked_at"`
	Success   bool   `json:"success"`
	LatencyMS int64  `json:"latency_ms"`
	Checks    int64  `json:"checks"`
	Successes int64  `json:"successes"`
	Method    string `json:"method"`
}

func readModelHealth(channel *model.Channel) map[string]modelHealthSample {
	health := map[string]modelHealthSample{}
	if raw := channel.GetOtherInfo()["model_health"]; raw != nil {
		data, err := common.Marshal(raw)
		if err == nil {
			_ = common.Unmarshal(data, &health)
		}
	}
	return health
}

func chatHealthProbeAllowed(channel *model.Channel, name string) bool {
	if channel.Type == constant.ChannelTypeTaskPlugin {
		return false
	}
	if mapping := channel.GetModelMapping(); mapping != "" {
		var aliases map[string]string
		if common.UnmarshalJsonStr(mapping, &aliases) == nil && aliases[name] != "" {
			name = aliases[name]
		}
	}
	if billableGenerationModel(name) || strings.Contains(strings.ToLower(name), "embed") || strings.Contains(strings.ToLower(name), "rerank") || strings.Contains(strings.ToLower(name), "realtime") {
		return false
	}
	endpoints := common.GetEndpointTypesByChannelType(channel.Type, name)
	if custom := channel.GetOtherSettings().AdvancedCustom; channel.Type == constant.ChannelTypeAdvancedCustom && custom != nil {
		endpoints = custom.SupportedEndpointTypesForModel(name)
	}
	for _, endpoint := range endpoints {
		if endpoint == constant.EndpointTypeImageGeneration || endpoint == constant.EndpointTypeOpenAIVideo {
			return false
		}
	}
	for _, endpoint := range model.GetModelSupportEndpointTypes(name) {
		if endpoint == constant.EndpointTypeImageGeneration || endpoint == constant.EndpointTypeOpenAIVideo {
			return false
		}
	}
	// Unknown aliases get a free discovery check, never a speculative generation.
	lower := strings.ToLower(name)
	for _, prefix := range []string{"gpt-", "chatgpt-", "o1", "o3", "o4", "claude", "deepseek", "qwen", "qwq", "llama", "mistral", "gemini", "glm-", "minimax", "kimi", "grok-", "command-", "ernie", "doubao-seed-"} {
		if strings.HasPrefix(lower, prefix) {
			return true
		}
	}
	return false
}

// This probe has one fixed read-only request. It must never use task submission,
// generation endpoints, configurable discovery methods, or POST as a fallback.
func fetchModelHealthDirectory(ctx context.Context, channel *model.Channel) (map[string]bool, error) {
	if channel.Type == constant.ChannelTypeTaskPlugin && !slices.Contains([]string{"newtoken", "byteplus", "doubao"}, channel.GetSetting().TaskPluginKey) {
		return nil, modelProbeUnverified
	}
	if slices.Contains([]int{constant.ChannelTypeMidjourney, constant.ChannelTypeMidjourneyPlus, constant.ChannelTypeSunoAPI, constant.ChannelTypeKling, constant.ChannelTypeJimeng, constant.ChannelTypeDoubaoVideo, constant.ChannelTypeVidu}, channel.Type) {
		return nil, modelProbeUnverified
	}
	base := strings.TrimRight(channel.GetBaseURL(), "/")
	if base == "" {
		base = strings.TrimRight(constant.GetChannelBaseURL(channel.Type), "/")
	}
	path := "/v1/models"
	if channel.Type == constant.ChannelTypeVolcEngine || channel.Type == constant.ChannelTypeTaskPlugin && slices.Contains([]string{"byteplus", "doubao"}, channel.GetSetting().TaskPluginKey) {
		base = strings.TrimSuffix(base, "/api/v3")
		path = "/api/v3/models"
	} else {
		base = strings.TrimSuffix(base, "/v1")
	}
	endpoint, err := url.Parse(base)
	if err != nil || endpoint.Host == "" || endpoint.User != nil || endpoint.RawQuery != "" || endpoint.Fragment != "" {
		return nil, modelProbeUnverified
	}
	ip := net.ParseIP(endpoint.Hostname())
	loopback := strings.EqualFold(endpoint.Hostname(), "localhost") || ip != nil && ip.IsLoopback()
	if endpoint.Scheme != "https" && !(endpoint.Scheme == "http" && loopback) {
		return nil, modelProbeUnverified
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, base+path, nil)
	if err != nil {
		return nil, modelProbeUnverified
	}
	key, _, keyErr := channel.GetNextEnabledKey()
	if keyErr != nil {
		return nil, errors.New("no enabled upstream credential")
	}
	headers, err := buildFetchModelsHeaders(channel, strings.TrimSpace(key))
	if err != nil {
		return nil, errors.New("invalid model directory headers")
	}
	request.Header = headers
	settings := channel.GetSetting()
	if settings.TLSInsecureSkipVerify || common.TLSInsecureSkipVerify {
		return nil, modelProbeUnverified
	}
	client, err := service.GetHttpClientWithProxySettings(settings.Proxy, settings)
	if err != nil {
		return nil, errors.New("model directory connection failed")
	}
	isolated := *client
	isolated.Timeout = 20 * time.Second
	isolated.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	response, err := isolated.Do(request)
	if err != nil {
		return nil, errors.New("model directory connection failed")
	}
	defer response.Body.Close()
	if slices.Contains([]int{404, 405, 501}, response.StatusCode) {
		return nil, modelProbeUnverified
	}
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("model directory returned HTTP %d", response.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, 4<<20+1))
	if err != nil || len(data) > 4<<20 {
		return nil, modelProbeUnverified
	}
	var body struct {
		Data *[]struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if common.Unmarshal(data, &body) != nil || body.Data == nil {
		return nil, modelProbeUnverified
	}
	models := map[string]bool{}
	for _, item := range *body.Data {
		if item.ID != "" {
			models[item.ID] = true
		}
	}
	return models, nil
}

func newSafeModelHealthProbe(channel *model.Channel, userID int, health map[string]modelHealthSample, endpoint string, stream bool) modelProbeFunc {
	var directory map[string]bool
	var directoryErr error
	discoveryLoaded := false
	var directoryLatencyMS int64
	return func(ctx context.Context, name string) *types.NewAPIError {
		started := time.Now()
		method := "chat"
		var probeErr *types.NewAPIError
		if chatHealthProbeAllowed(channel, name) && endpoint != string(constant.EndpointTypeImageGeneration) && endpoint != string(constant.EndpointTypeOpenAIVideo) {
			result := testChannel(ctx, channel, userID, name, endpoint, stream)
			probeErr = result.newAPIError
			if probeErr == nil && result.localErr != nil {
				probeErr = types.NewError(errors.New("local chat probe failed"), types.ErrorCodeBadResponse)
			}
		} else {
			method = "directory"
			if !discoveryLoaded {
				directoryStarted := time.Now()
				directory, directoryErr = fetchModelHealthDirectory(ctx, channel)
				directoryLatencyMS = time.Since(directoryStarted).Milliseconds()
				discoveryLoaded = true
			}
			upstreamName := name
			var aliases map[string]string
			if common.UnmarshalJsonStr(channel.GetModelMapping(), &aliases) == nil && aliases[name] != "" {
				upstreamName = aliases[name]
			}
			if directoryErr == modelProbeUnverified {
				method = "unverified"
				probeErr = modelProbeUnverified
			} else if directoryErr != nil || !directory[upstreamName] {
				probeErr = types.NewError(errors.New("model directory unavailable or exact model absent"), types.ErrorCodeBadResponse)
			}
		}
		if ctx.Err() != nil {
			return modelProbeUnverified
		}
		sample := health[name]
		sample.CheckedAt = time.Now().Unix()
		sample.LatencyMS = time.Since(started).Milliseconds()
		if method != "chat" {
			sample.LatencyMS = directoryLatencyMS
		}
		sample.Method = method
		sample.Success = probeErr == nil
		if probeErr != modelProbeUnverified {
			sample.Checks++
			if sample.Success {
				sample.Successes++
			}
		}
		health[name] = sample
		return probeErr
	}
}

func GetModelHealth(c *gin.Context) {
	var channels []*model.Channel
	err := model.DB.Select("id", "name", "type", "status", "models", "group", "other_info").Order("id").Find(&channels).Error
	if err != nil {
		common.ApiError(c, err)
		return
	}
	profiles := model.GetGroupProfiles()
	rows := []gin.H{}
	for _, channel := range channels {
		health := readModelHealth(channel)
		current := channel.GetModels()
		all := append(append([]string{}, current...), readChannelDisabledModels(channel)...)
		groups := []gin.H{}
		for group := range strings.SplitSeq(channel.Group, ",") {
			name := profiles[group].Name
			if name == "" {
				name = group
			}
			groups = append(groups, gin.H{"id": group, "name": name})
		}
		for _, name := range all {
			if name == "" {
				continue
			}
			sample := health[name]
			state := "unknown"
			if sample.CheckedAt > 0 && sample.Method == "unverified" {
				state = "unverified"
			}
			if sample.CheckedAt > 0 && sample.Method != "unverified" {
				state = "unhealthy"
				if sample.Success {
					state = "healthy"
					if sample.Method == "directory" {
						state = "interface_only"
					}
				}
				if time.Now().Unix()-sample.CheckedAt > int64((2*modelHealthInterval)/time.Second) {
					state = "stale"
				}
			}
			routed := channel.Status == common.ChannelStatusEnabled && slices.Contains(current, name)
			if !routed {
				state = "disabled"
			}
			var rate *float64
			if sample.Checks > 0 {
				value := float64(sample.Successes) / float64(sample.Checks)
				rate = &value
			}
			rows = append(rows, gin.H{"channel_id": channel.Id, "channel_name": channel.Name, "model": name, "groups": groups, "state": state, "routed": routed, "checked_at": sample.CheckedAt, "latency_ms": sample.LatencyMS, "success_rate": rate, "checks": sample.Checks, "method": sample.Method})
		}
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": rows})
}
