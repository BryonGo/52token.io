package service

import (
	"context"
	"gorm.io/driver/postgres"
	gormlogger "gorm.io/gorm/logger"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/channelcost"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

type metadataTransport func(*http.Request) (*http.Response, error)

func (f metadataTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestChannelMetadataUsesOwnSourceVendorIDsAndExactMappedNames(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "metadata.db")), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.Channel{}))
	previousDB, previousHTTP, previousOptions := model.DB, channelCostHTTP, common.OptionMap
	model.DB = db
	common.OptionMap = map[string]string{}
	t.Cleanup(func() {
		model.DB, channelCostHTTP, common.OptionMap = previousDB, previousHTTP, previousOptions
		sql, _ := db.DB()
		_ = sql.Close()
	})
	sources := map[string]channelcost.Source{}
	for _, id := range []string{"one", "two"} {
		sources[id] = channelcost.Source{ID: id, Name: id, Enabled: true, Format: "newapi", URL: "https://" + id + ".example/pricing", ChannelOrigin: "https://" + id + ".example", UnitTokens: 1e6, InputScale: 1, Fields: channelcost.SourceFields{Items: "data", Model: "model_name", Input: "model_ratio", Output: "completion_ratio", Success: "success"}}
		ch := model.Channel{Name: id, Type: 1, BaseURL: common.GetPointer("https://" + id + ".example"), Models: "grok-local,shared"}
		if id == "one" {
			ch.ModelMapping = common.GetPointer(`{"grok-local":"grok-4.2"}`)
		} else {
			ch.Models = "shared,only-two"
		}
		ch.SetOtherSettings(dto.ChannelOtherSettings{CostBilling: &dto.ChannelCostSettings{Source: id}})
		require.NoError(t, db.Create(&ch).Error)
	}
	raw, err := common.Marshal(sources)
	require.NoError(t, err)
	common.OptionMap["channel_price_sources"] = string(raw)
	calls := map[string]int{}
	channelCostHTTP = &http.Client{Transport: metadataTransport(func(r *http.Request) (*http.Response, error) {
		calls[r.URL.Host]++
		body := `{"success":true,"vendors":[{"id":10,"name":"x.ai","icon":"XAI"}],"data":[{"model_name":"grok-4.2","vendor_id":10},{"model_name":"shared","vendor_id":10},{"model_name":"only-two","vendor_id":10}]}`
		if r.URL.Host == "two.example" {
			body = `{"success":true,"vendors":[{"id":10,"name":"Anthropic","icon":"Claude.Color"}],"data":[{"model_name":"shared","vendor_id":10},{"model_name":"only-two","vendor_id":10}]}`
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body)), Header: http.Header{}}, nil
	})}
	result, err := FetchChannelMetadata(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "xAI", result.Models["grok-local"].Vendor)
	assert.Equal(t, "grok-4.2", result.Origins["grok-local"][0].UpstreamModel)
	assert.Equal(t, "Anthropic", result.Models["only-two"].Vendor)
	assert.Empty(t, result.Models["shared"].Vendor, "conflicting vendors must not be chosen from the first channel")
	assert.Contains(t, result.Conflicts["shared"], "vendor")
	assert.NotContains(t, result.Models, "grok-4.2", "unconfigured upstream names must not be imported")
	assert.Len(t, result.Origins["shared"], 2)
	assert.Equal(t, map[string]int{"one.example": 1, "two.example": 1}, calls)
}

func TestMappedMetadataFieldsSupportDifferentUpstreamFormats(t *testing.T) {
	source := channelcost.Source{Fields: channelcost.SourceFields{Items: "models", Model: "name", Description: "info.description", Vendor: "maker", Icon: "logo", Endpoints: "protocols"}}
	result, err := ParseSourceMetadata([]byte(`{"models":[{"name":"grok-4.2","maker":" XAI ","info":{"description":"Source description"},"protocols":["openai"],"model_price":999}]}`), source)
	require.NoError(t, err)
	assert.Equal(t, "xAI", result.Models["grok-4.2"].Vendor)
	assert.Equal(t, "Source description", result.Models["grok-4.2"].Description)
	assert.JSONEq(t, `["openai"]`, result.Models["grok-4.2"].Endpoints)
	_, err = ParseSourceMetadata([]byte(`{"models":[{"name":"same"},{"name":"same"}]}`), source)
	require.Error(t, err)
	_, err = ParseSourceMetadata([]byte(`not-json`), source)
	require.Error(t, err)
}

// This opt-in check uses a temporary read-only role on the development database.
// It never calls a model or writes channel settings, metadata, or pricing.
func TestLiveBoundChannelMetadata(t *testing.T) {
	dsn := os.Getenv("TEST_CHANNEL_METADATA_DSN")
	if dsn == "" {
		t.Skip("set TEST_CHANNEL_METADATA_DSN to inspect development channel metadata")
	}
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{Logger: gormlogger.Default.LogMode(gormlogger.Silent)})
	require.NoError(t, err)
	previousDB, previousOptions := model.DB, common.OptionMap
	model.DB = db
	common.OptionMap = map[string]string{}
	t.Cleanup(func() { model.DB, common.OptionMap = previousDB, previousOptions; sql, _ := db.DB(); _ = sql.Close() })
	var options []model.Option
	require.NoError(t, db.Where(&model.Option{Key: "channel_price_sources"}).Find(&options).Error)
	for _, option := range options {
		common.OptionMap[option.Key] = option.Value
	}
	result, err := FetchChannelMetadata(context.Background())
	require.NoError(t, err)
	for _, name := range []string{"grok-4-0709", "grok-4.2"} {
		require.Contains(t, result.Models, name)
		assert.Equal(t, "xAI", result.Models[name].Vendor)
		require.NotEmpty(t, result.Origins[name])
		for _, origin := range result.Origins[name] {
			t.Logf("%s: channel=%d source=%s upstream_model=%s vendor=%s", name, origin.ChannelID, origin.SourceID, origin.UpstreamModel, result.Models[name].Vendor)
		}
	}
}
