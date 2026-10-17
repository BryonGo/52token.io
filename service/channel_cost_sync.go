package service

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/channelcost"
	"github.com/QuantumNous/new-api/relaykit/dto"
	hostreasoning "github.com/QuantumNous/new-api/setting/reasoning"
	"golang.org/x/net/html"
)

var channelCostSyncMu sync.Mutex
var channelCostHTTP = func() *http.Client {
	client := newProtectedFetchHTTPClient()
	client.Timeout = 20 * time.Second
	client.CheckRedirect = func(req *http.Request, via []*http.Request) error { return http.ErrUseLastResponse }
	return client
}()

// Prices are isolated by channel. Official reference prices require an explicit
// administrator-confirmed model mapping and authenticated Sub2API group rate.
func SyncChannelCost(ctx context.Context, id int) (*channelcost.Snapshot, error) {
	channelCostSyncMu.Lock()
	defer channelCostSyncMu.Unlock()
	channel, err := model.GetChannelById(id, true)
	if err != nil {
		return nil, err
	}
	cfg := channel.GetOtherSettings().CostBilling
	if cfg == nil || !cfg.Enabled {
		return nil, errors.New("Channel cost billing is disabled")
	}
	if err = channelcost.Validate(cfg); err != nil {
		return nil, err
	}
	base := strings.TrimRight(channel.GetBaseURL(), "/")
	if base == "" && channel.Type == 43 {
		base = "https://api.deepseek.com"
	}
	source, err := model.GetChannelPriceSource(cfg.Source)
	if err != nil {
		return nil, err
	}
	if source.Reference != nil && channel.Type != constant.ChannelTypeSub2API {
		return nil, errors.New("Confirmed reference pricing requires a Sub2API channel")
	}
	parsed, _ := url.Parse(base)
	origin, _ := url.Parse(source.ChannelOrigin)
	if parsed == nil || origin == nil || parsed.Scheme != origin.Scheme || !strings.EqualFold(parsed.Host, origin.Host) {
		return nil, errors.New("Pricing source does not match the channel endpoint")
	}
	if custom := channel.GetOtherSettings().AdvancedCustom; custom != nil {
		for _, route := range custom.Routes {
			target, err := url.Parse(strings.TrimSpace(route.UpstreamPath))
			if err != nil || target.Host != "" && (target.Scheme != origin.Scheme || !strings.EqualFold(target.Host, origin.Host)) {
				return nil, errors.New("Advanced custom route does not match the channel price source")
			}
		}
	}
	fingerprint := model.ChannelCostFingerprint(channel)
	currentSource, sourceErr := model.GetChannelPriceSource(cfg.Source)
	initialSourceJSON, _ := common.Marshal(source)
	currentSourceJSON, _ := common.Marshal(currentSource)
	if sourceErr != nil || string(initialSourceJSON) != string(currentSourceJSON) {
		return nil, errors.New("Channel changed during price synchronization")
	}
	fetched, err := FetchPriceSource(ctx, source, cfg.UpstreamGroup)
	if err != nil {
		return nil, err
	}
	if source.Format == "sub2api" || source.Reference != nil {
		fetched.GroupRatio, err = Sub2APIEffectiveRate(ctx, channel, fetched.GroupRatio)
		if err != nil {
			return nil, err
		}
	}
	prices := fetched.Prices
	snapshot := &channelcost.Snapshot{Fingerprint: fingerprint, SourceURL: source.URL, SyncedAt: time.Now().Unix(), GroupRatio: fetched.GroupRatio, Prices: map[string]channelcost.Price{}, Errors: map[string]string{}}
	mapping := map[string]string{}
	if raw := channel.GetModelMapping(); raw != "" {
		if common.UnmarshalJsonStr(raw, &mapping) != nil {
			return nil, errors.New("Invalid channel model mapping")
		}
	}
	for name := range strings.SplitSeq(channel.Models, ",") {
		name = strings.TrimSpace(name)
		upstream := name
		visited := map[string]bool{}
		for {
			mapped, exists := mapping[upstream]
			if (!exists || mapped == "") && hostreasoning.BaseModelName(upstream) != upstream {
				mapped, exists = mapping[hostreasoning.BaseModelName(upstream)]
			}
			if !exists || mapped == "" || mapped == upstream {
				break
			}
			if visited[mapped] {
				return nil, errors.New("Invalid channel model mapping")
			}
			visited[upstream] = true
			upstream = mapped
		}
		if price, ok := prices[upstream]; ok {
			snapshot.Prices[name] = price
		} else if reason := fetched.Errors[upstream]; reason != "" {
			snapshot.Errors[name] = reason
		}
	}
	if len(snapshot.Prices) == 0 && len(snapshot.Errors) == 0 {
		return nil, errors.New("No supported upstream token prices for this channel")
	}
	// A concurrent channel edit makes this snapshot unusable even if it occurs
	// after this check: the relay validates the fingerprint before charging.
	fresh, err := model.GetChannelById(id, true)
	if err != nil {
		return nil, err
	}
	if model.ChannelCostFingerprint(fresh) != snapshot.Fingerprint {
		return nil, errors.New("Channel changed during price synchronization")
	}
	if err = model.SaveChannelCostSnapshot(id, snapshot); err != nil {
		return nil, err
	}
	return snapshot, nil
}

func fetchChannelCostDocument(ctx context.Context, endpoint string) ([]byte, error) {
	return fetchChannelCostDocumentWithAuth(ctx, endpoint, "", 0)
}
func fetchChannelCostDocumentWithAuth(ctx context.Context, endpoint, token string, userID int) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "52token-cost-sync/1.0")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("New-Api-User", strconv.Itoa(userID))
	}
	response, err := channelCostHTTP.Do(req)
	if err != nil {
		return nil, errors.New("Unable to fetch upstream prices")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("Upstream price request returned HTTP %d", response.StatusCode)
	}
	const limit = 4 << 20
	body, err := io.ReadAll(io.LimitReader(response.Body, limit+1))
	if err != nil || len(body) > limit {
		return nil, errors.New("Invalid upstream price response")
	}
	return body, nil
}

func parseHappyCost(body []byte, group string, quotaUnit float64) (map[string]channelcost.Price, float64, error) {
	result, err := ParsePriceSource(body, channelcost.DefaultSources()["happyapi"], group, quotaUnit)
	if err != nil {
		return nil, 0, err
	}
	return result.Prices, result.GroupRatio, nil
}

func parseDeepSeekCost(body []byte) (map[string]channelcost.Price, error) {
	return parseDeepSeekCostCurrency(body, "CNY")
}
func parseDeepSeekCostCurrency(body []byte, currency string) (map[string]channelcost.Price, error) {
	if currency != "CNY" && currency != "USD" {
		return nil, errors.New("Invalid upstream price currency")
	}

	doc, err := html.Parse(strings.NewReader(string(body)))
	if err != nil {
		return nil, err
	}
	rows := [][]string{}
	var visit func(*html.Node)
	var textContent func(*html.Node) string
	textContent = func(n *html.Node) string {
		if n.Type == html.TextNode {
			return n.Data
		}
		var b strings.Builder
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			b.WriteString(textContent(c))
		}
		return b.String()
	}
	visit = func(n *html.Node) {
		if n.Type == html.ElementNode && n.Data == "tr" {
			row := []string{}
			for c := n.FirstChild; c != nil; c = c.NextSibling {
				if c.Data == "td" || c.Data == "th" {
					row = append(row, strings.Join(strings.Fields(textContent(c)), ""))
				}
			}
			rows = append(rows, row)
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			visit(c)
		}
	}
	visit(doc)
	headerOK := false
	values := [][2]float64{}
	for _, row := range rows {
		if len(row) == 3 && (row[0] == "模型" || row[0] == "MODEL") && strings.HasPrefix(row[1], "deepseek-flash") && row[2] == "deepseek-v4-pro" {
			headerOK = true
		}
		if len(row) < 3 {
			continue
		}
		label := strings.Join(row[:len(row)-2], "")
		expected := []string{"缓存命中", "高峰时段", "缓存未命中", "高峰时段", "输出", "高峰时段"}
		if currency == "USD" {
			if !strings.Contains(label, "OFF-PEAK") && !strings.Contains(label, "PEAK") {
				continue
			}
			expected = []string{"CACHEHIT", "PEAK", "CACHEMISS", "PEAK", "OUTPUT", "PEAK"}
		} else if !strings.Contains(label, "空闲时段") && !strings.Contains(label, "高峰时段") {
			continue
		}
		if len(values) >= 6 || !strings.Contains(label, expected[len(values)]) {
			return nil, errors.New("DeepSeek pricing format has changed")
		}
		pair := [2]float64{}
		for i := range 2 {
			s := row[len(row)-2+i]
			if currency == "USD" {
				if !strings.HasPrefix(s, "$") {
					return nil, errors.New("DeepSeek USD prices are unavailable")
				}
				s = strings.TrimPrefix(s, "$")
			} else {
				if !strings.HasSuffix(s, "元") {
					return nil, errors.New("DeepSeek CNY prices are unavailable")
				}
				s = strings.TrimSuffix(s, "元")
			}
			v, err := strconv.ParseFloat(s, 64)
			if err != nil || !channelcost.Positive(v) {
				return nil, errors.New("Invalid upstream token prices")
			}
			pair[i] = v
		}
		values = append(values, pair)
	}
	// Guard the schedule contract too: a pricing policy change requires review.
	content := strings.Join(strings.Fields(textContent(doc)), "")
	scheduleOK := strings.Contains(content, "9:00-12:00、14:00-18:00") && strings.Contains(content, "不含中国法定节假日")
	if currency == "USD" {
		content = strings.NewReplacer("–", "-", "—", "-").Replace(content)
		scheduleOK = strings.Contains(content, "01:00-04:00and06:00-10:00UTC") && strings.Contains(content, "MondaythroughFriday") && strings.Contains(content, "excludingChinesepublicholidays")
	}
	if !headerOK || len(values) != 6 || !scheduleOK {
		return nil, errors.New("DeepSeek pricing format has changed")
	}
	prices := map[string]channelcost.Price{}
	for i, name := range []string{"deepseek-flash", "deepseek-v4-pro"} {
		peak := channelcost.Rates{Input: values[3][i], Output: values[5][i], CacheRead: values[1][i], CacheWrite: values[3][i], CacheWrite1h: values[3][i]}
		prices[name] = channelcost.Price{UpstreamModel: name, OffPeak: channelcost.Rates{Input: values[2][i], Output: values[4][i], CacheRead: values[0][i], CacheWrite: values[2][i], CacheWrite1h: values[2][i]}, Peak: &peak}
	}
	for _, alias := range []string{"deepseek-v4-flash", "deepseek-v4-flash-vision-exp"} {
		prices[alias] = prices["deepseek-flash"]
	}
	return prices, nil
}

// Enabled channels refresh at startup and every hour. Failed refreshes preserve
// the last good snapshot; relay refuses snapshots older than 24 hours.
func RefreshChannelCosts() {
	var channels []model.Channel
	if model.DB.Select("id", "settings").Find(&channels).Error != nil {
		return
	}
	for _, ch := range channels {
		var settings dto.ChannelOtherSettings
		if common.UnmarshalJsonStr(ch.OtherSettings, &settings) != nil {
			continue
		}
		cfg := settings.CostBilling
		if cfg == nil || !cfg.Enabled {
			continue
		}
		if _, err := SyncChannelCost(context.Background(), ch.Id); err != nil {
			common.SysError(common.LogText("channel cost sync failed: channel=%d error=%s", ch.Id, err))
		}
	}
}
func RunChannelCostSync() {
	RefreshChannelCosts()
	ticker := time.NewTicker(time.Hour)
	defer ticker.Stop()
	for range ticker.C {
		RefreshChannelCosts()
	}
}
