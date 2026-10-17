package controller

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/i18n"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/channelcost"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// Called by the existing real SQLite/MySQL/PostgreSQL matrix. It exercises the
// public handlers without a session or a model-call key.
func testPublicCatalogWithDatabase(t *testing.T, db *gorm.DB) {
	require.NoError(t, i18n.Init())
	oldPrice, oldFX := operation_setting.Price, operation_setting.USDExchangeRate
	operation_setting.Price, operation_setting.USDExchangeRate = 1, 1
	t.Cleanup(func() { operation_setting.Price, operation_setting.USDExchangeRate = oldPrice, oldFX })
	channels := []model.Channel{}
	for i := range 2 {
		groups, err := model.GetGroupCatalog()
		require.NoError(t, err)
		groupID, err := model.SaveManagedGroup(model.GroupMutation{Name: "HappyAPI DeepSeek", Ratio: 1, Selectable: true, Version: groups.Version})
		require.NoError(t, err)
		channel := model.Channel{Name: fmt.Sprintf("HappyAPI line %d", i+1), Status: common.ChannelStatusEnabled, Type: constant.ChannelTypeOpenAI, Group: groupID, Models: "deepseek-v4-pro", Key: fmt.Sprintf("sk-catalog-fixture-%d", i)}
		channel.SetOtherSettings(dto.ChannelOtherSettings{CostBilling: &dto.ChannelCostSettings{Enabled: true, Source: "happyapi", CreditsPerCNY: 5, UpstreamGroup: "default"}})
		require.NoError(t, db.Create(&channel).Error)
		snapshot := &channelcost.Snapshot{Fingerprint: model.ChannelCostFingerprint(&channel), SyncedAt: time.Now().Unix(), GroupRatio: 1, Prices: map[string]channelcost.Price{
			"deepseek-v4-pro": {UpstreamModel: "deepseek-v4-pro", OffPeak: channelcost.Rates{Input: float64(i + 1), Output: float64(4 + i)}},
		}}
		require.NoError(t, model.SaveChannelCostSnapshot(channel.Id, snapshot))
		channels = append(channels, channel)
	}
	require.NotEqual(t, channels[0].Group, channels[1].Group)
	engine := gin.New()
	engine.GET("/v1/model-groups", GetPublicModelGroups)
	engine.GET("/v1/model-catalog", GetPublicModelCatalog)
	engine.GET("/v1/model-prices/:pricing_id", GetPublicModelPrice)
	request := httptest.NewRequest(http.MethodGet, "/v1/model-catalog", nil)
	response := httptest.NewRecorder()
	engine.ServeHTTP(response, request)
	require.Equal(t, http.StatusOK, response.Code)
	assert.Equal(t, "public, max-age=15", response.Header().Get("Cache-Control"))
	var catalog struct {
		Object string                     `json:"object"`
		Data   []model.PublicCatalogModel `json:"data"`
	}
	require.NoError(t, common.Unmarshal(response.Body.Bytes(), &catalog))
	selected := []model.PublicCatalogModel{}
	for _, entry := range catalog.Data {
		if entry.ChannelID == channels[0].Id || entry.ChannelID == channels[1].Id {
			selected = append(selected, entry)
		}
	}
	catalog.Data = selected
	require.Len(t, catalog.Data, 2)
	assert.Equal(t, catalog.Data[0].ModelUID, catalog.Data[1].ModelUID)
	assert.NotEqual(t, catalog.Data[0].PricingID, catalog.Data[1].PricingID)
	assert.Equal(t, catalog.Data[0].GroupName, catalog.Data[1].GroupName)
	response = httptest.NewRecorder()
	engine.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/v1/model-groups", nil))
	require.Equal(t, http.StatusOK, response.Code)
	assert.Equal(t, "public, max-age=15", response.Header().Get("Cache-Control"))
	var publicGroups struct {
		Object string                     `json:"object"`
		Data   []model.PublicCatalogGroup `json:"data"`
	}
	require.NoError(t, common.Unmarshal(response.Body.Bytes(), &publicGroups))
	assert.Equal(t, "list", publicGroups.Object)
	foundGroups := map[string]bool{}
	for _, group := range publicGroups.Data {
		if group.GroupID == channels[0].Group || group.GroupID == channels[1].Group {
			assert.Equal(t, "HappyAPI DeepSeek", group.GroupName)
			assert.Equal(t, 1.0, group.GroupMultiplier)
			assert.Equal(t, 1, group.ModelCount)
			assert.False(t, foundGroups[group.GroupID])
			foundGroups[group.GroupID] = true
		}
	}
	require.Len(t, foundGroups, 2, "same names and multipliers retain separate fixed IDs")
	for _, channel := range channels {
		assert.NotContains(t, response.Body.String(), channel.Key)
	}
	for _, entry := range catalog.Data {
		response = httptest.NewRecorder()
		engine.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/v1/model-catalog?group_id="+url.QueryEscape(entry.GroupID), nil))
		require.Equal(t, http.StatusOK, response.Code)
		var filtered struct {
			Data []model.PublicCatalogModel `json:"data"`
		}
		require.NoError(t, common.Unmarshal(response.Body.Bytes(), &filtered))
		require.Len(t, filtered.Data, 1)
		assert.Equal(t, entry, filtered.Data[0])
	}
	for _, groupID := range []string{"not-found", "HappyAPI DeepSeek", "grp_%", "' OR 1=1 --"} {
		response = httptest.NewRecorder()
		engine.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/v1/model-catalog?group_id="+url.QueryEscape(groupID), nil))
		require.Equal(t, http.StatusOK, response.Code)
		assert.JSONEq(t, `{"object":"list","data":[]}`, response.Body.String())
	}
	for _, entry := range catalog.Data {
		response = httptest.NewRecorder()
		engine.ServeHTTP(response, httptest.NewRequest(http.MethodGet, entry.PricingURL, nil))
		require.Equal(t, http.StatusOK, response.Code)
		assert.Equal(t, "no-store", response.Header().Get("Cache-Control"))
		var quote struct {
			Data model.PublicModelPrice `json:"data"`
		}
		require.NoError(t, common.Unmarshal(response.Body.Bytes(), &quote))
		require.True(t, quote.Data.Available, quote.Data.Reason)
		expected := .2
		if entry.ChannelID == channels[1].Id {
			expected = .4
		}
		assert.InDelta(t, expected, quote.Data.Rates.Input, 1e-12)
		assert.Equal(t, 1.0, quote.Data.AppliedGroupMultiplier)
		for _, channel := range channels {
			assert.NotContains(t, response.Body.String(), channel.Key)
		}
		assert.NotContains(t, response.Body.String(), "credits_per_cny")
		assert.NotContains(t, response.Body.String(), "upstream_group")
	}
	groups, err := model.GetGroupCatalog()
	require.NoError(t, err)
	_, err = model.SaveManagedGroup(model.GroupMutation{ID: channels[0].Group, Name: "Renamed display", Ratio: 1, Selectable: true, Version: groups.Version})
	require.NoError(t, err)
	response = httptest.NewRecorder()
	engine.ServeHTTP(response, httptest.NewRequest(http.MethodGet, catalog.Data[0].PricingURL, nil))
	var renamed struct {
		Data model.PublicModelPrice `json:"data"`
	}
	require.NoError(t, common.Unmarshal(response.Body.Bytes(), &renamed))
	assert.Equal(t, catalog.Data[0].PricingID, renamed.Data.PricingID)
	assert.Equal(t, "Renamed display", renamed.Data.GroupName)
	response = httptest.NewRecorder()
	engine.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/v1/model-groups", nil))
	require.NoError(t, common.Unmarshal(response.Body.Bytes(), &publicGroups))
	for _, group := range publicGroups.Data {
		if group.GroupID == channels[0].Group {
			assert.Equal(t, "Renamed display", group.GroupName)
		}
	}
	require.NoError(t, db.Model(&channels[0]).Update("models", "another-model").Error)
	response = httptest.NewRecorder()
	engine.ServeHTTP(response, httptest.NewRequest(http.MethodGet, catalog.Data[0].PricingURL, nil))
	assert.Equal(t, http.StatusNotFound, response.Code, "a stale ID cannot quote a removed model")
	for _, path := range []string{"/v1/model-prices/invalid", "/v1/model-prices/prc_999999_0000000000000000000000000000000000000000000000000000000000000000"} {
		response = httptest.NewRecorder()
		engine.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
		assert.Equal(t, http.StatusNotFound, response.Code)
	}
	require.NoError(t, db.Model(&channels[1]).Update("status", common.ChannelStatusManuallyDisabled).Error)
	response = httptest.NewRecorder()
	engine.ServeHTTP(response, httptest.NewRequest(http.MethodGet, catalog.Data[1].PricingURL, nil))
	assert.Equal(t, http.StatusNotFound, response.Code)
	response = httptest.NewRecorder()
	engine.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/v1/model-groups", nil))
	require.NoError(t, common.Unmarshal(response.Body.Bytes(), &publicGroups))
	for _, group := range publicGroups.Data {
		assert.NotEqual(t, channels[1].Group, group.GroupID, "disabled lines are not published")
	}
	response = httptest.NewRecorder()
	engine.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/v1/model-catalog", nil))
	assert.Equal(t, http.StatusNotFound, response.Code)
}
