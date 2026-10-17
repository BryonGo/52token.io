package model

import (
	"fmt"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	filterdto "github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/pkg/channelcost"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/schema"
)

func TestChannelValidateSettingsRejectsInvalidHTTPTransport(t *testing.T) {
	tests := []struct {
		name    string
		setting dto.ChannelSettings
		wantErr string
	}{
		{
			name:    "auto with shards is valid",
			setting: dto.ChannelSettings{HTTPProtocol: "auto", HTTP2ConnectionShards: 4},
		},
		{
			name:    "http1 with shards greater than one rejected",
			setting: dto.ChannelSettings{HTTPProtocol: "http1", HTTP2ConnectionShards: 2},
			wantErr: "http2_connection_shards",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			channel := &Channel{}
			channel.SetSetting(tt.setting)
			err := channel.ValidateSettings()
			if tt.wantErr == "" {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErr)
		})
	}
}

func TestAdvancedCustomChannelRequiresModelListRouteOnlyWhenUpdateChecksEnabled(t *testing.T) {
	inferenceRoute := dto.AdvancedCustomRoute{
		IncomingPath: "/v1/chat/completions",
		UpstreamPath: "/v1/chat/completions",
		Converter:    "none",
	}

	tests := []struct {
		name          string
		checksEnabled bool
		routes        []dto.AdvancedCustomRoute
		wantErr       string
	}{
		{
			name:   "legacy channel without discovery route remains valid",
			routes: []dto.AdvancedCustomRoute{inferenceRoute},
		},
		{
			name:          "enabled checks require discovery route",
			checksEnabled: true,
			routes:        []dto.AdvancedCustomRoute{inferenceRoute},
			wantErr:       dto.AdvancedCustomModelListPath,
		},
		{
			name:          "enabled checks accept discovery route",
			checksEnabled: true,
			routes: []dto.AdvancedCustomRoute{
				inferenceRoute,
				{
					IncomingPath: dto.AdvancedCustomModelListPath,
					UpstreamPath: dto.AdvancedCustomModelListPath,
					Converter:    "none",
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			channel := &Channel{Type: constant.ChannelTypeAdvancedCustom}
			channel.SetOtherSettings(dto.ChannelOtherSettings{
				UpstreamModelUpdateCheckEnabled: tt.checksEnabled,
				AdvancedCustom: &dto.AdvancedCustomConfig{
					Routes: tt.routes,
				},
			})

			err := channel.ValidateSettings()
			if tt.wantErr == "" {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErr)
		})
	}
}

func TestInferencePresetSettingsAndDatabaseRoundTrip(t *testing.T) {
	for _, dialect := range []string{"sqlite", "mysql", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			var driver gorm.Dialector
			switch dialect {
			case "sqlite":
				driver = sqlite.Open(filepath.Join(t.TempDir(), "presets.db"))
			case "mysql":
				dsn := os.Getenv("TEST_MYSQL_DSN")
				if dsn == "" {
					t.Skip("TEST_MYSQL_DSN is not configured")
				}
				driver = mysql.Open(dsn)
			case "postgres":
				dsn := os.Getenv("TEST_POSTGRES_DSN")
				if dsn == "" {
					t.Skip("TEST_POSTGRES_DSN is not configured")
				}
				driver = postgres.Open(dsn)
			}
			db, err := gorm.Open(driver, &gorm.Config{})
			require.NoError(t, err)
			sqlDB, err := db.DB()
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, sqlDB.Close()) })
			table := db.Table("inference_preset_channels").Session(&gorm.Session{})
			require.NoError(t, table.AutoMigrate(&Channel{}))
			t.Cleanup(func() { require.NoError(t, db.Migrator().DropTable("inference_preset_channels")) })
			var version string
			if dialect == "sqlite" {
				require.NoError(t, db.Raw("select sqlite_version()").Scan(&version).Error)
			} else {
				require.NoError(t, db.Raw("select version()").Scan(&version).Error)
			}
			t.Logf("%s version: %s", dialect, version)
			for _, channelType := range []int{constant.ChannelTypeVLLM, constant.ChannelTypeSGLang} {
				t.Run(fmt.Sprint(channelType), func(t *testing.T) {
					channel := &Channel{Type: channelType, Key: "EMPTY", Name: "inference", Status: common.ChannelStatusEnabled}
					require.NoError(t, channel.ValidateSettings())
					require.NotNil(t, channel.GetOtherSettings().AdvancedCustom)
					require.NoError(t, table.Create(channel).Error)
					for range 2 {
						var loaded Channel
						require.NoError(t, table.First(&loaded, channel.Id).Error)
						assert.Equal(t, channelType, loaded.Type)
						assert.Empty(t, loaded.OtherSettings)
						defaults := loaded.GetOtherSettings().AdvancedCustom
						require.NotNil(t, defaults)
						assert.True(t, defaults.SupportsPath("/v1/messages"))
						assert.Empty(t, loaded.OtherSettings, "reading defaults must not rewrite saved settings")
						defaults.Routes[0].UpstreamPath = "/changed-locally"
						assert.Equal(t, "/v1/chat/completions", loaded.GetOtherSettings().AdvancedCustom.Routes[0].UpstreamPath)
					}
					settings := dto.ChannelOtherSettings{AdvancedCustom: &dto.AdvancedCustomConfig{Routes: []dto.AdvancedCustomRoute{{IncomingPath: "/v1/chat/completions", UpstreamPath: "/custom/chat", Models: []string{"allowed"}, Auth: &dto.AdvancedCustomRouteAuth{Type: "none"}}}}}
					channel.SetOtherSettings(settings)
					require.NoError(t, channel.ValidateSettings())
					require.NoError(t, table.Save(channel).Error)
					for range 2 {
						var loaded Channel
						require.NoError(t, table.First(&loaded, channel.Id).Error)
						actual := loaded.GetOtherSettings().AdvancedCustom
						require.Equal(t, common.GetAdvancedCustomPreset(channelType), actual, "named channels must ignore editable advanced_custom overrides")
						assert.Equal(t, channel.OtherSettings, loaded.OtherSettings)
						for _, tc := range []struct {
							path, model string
							allowed     bool
						}{
							{"/v1/chat/completions", "allowed", true},
							{"/v1/chat/completions", "other", true},
							{"/v1/messages", "allowed", true},
							{"/v1/images/generations", "allowed", false},
						} {
							ok, _ := ChannelSatisfiesFilters(&loaded, tc.model, []filterdto.ChannelFilter{{Kind: filterdto.FilterRequestPath, RequestPath: tc.path}})
							assert.Equal(t, tc.allowed, ok)
						}
						endpoints := getPricingEndpointTypesForAbility(AbilityWithChannel{ChannelType: channelType, Ability: Ability{Model: "allowed", ChannelId: loaded.Id}}, map[int]*dto.AdvancedCustomConfig{loaded.Id: actual})
						expectedEndpoints := []constant.EndpointType{constant.EndpointTypeOpenAI, constant.EndpointTypeOpenAIResponse, constant.EndpointTypeAnthropic, constant.EndpointTypeEmbeddings}
						if channelType == constant.ChannelTypeSGLang {
							expectedEndpoints = append(expectedEndpoints, constant.EndpointTypeJinaRerank)
						}
						assert.ElementsMatch(t, expectedEndpoints, endpoints)
					}
				})
			}
		})
	}
}

func TestChannelCostStorageDatabaseMatrix(t *testing.T) {
	for _, dialect := range []string{"sqlite", "mysql", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			var driver gorm.Dialector
			switch dialect {
			case "sqlite":
				driver = sqlite.Open(filepath.Join(t.TempDir(), "cost.db"))
			case "mysql":
				dsn := os.Getenv("TEST_MYSQL_DSN")
				if dsn == "" {
					t.Skip("TEST_MYSQL_DSN not configured")
				}
				driver = mysql.Open(dsn)
			case "postgres":
				dsn := os.Getenv("TEST_POSTGRES_DSN")
				if dsn == "" {
					t.Skip("TEST_POSTGRES_DSN not configured")
				}
				driver = postgres.Open(dsn)
			}
			db, err := gorm.Open(driver, &gorm.Config{NamingStrategy: schema.NamingStrategy{TablePrefix: "channel_cost_test_"}})
			require.NoError(t, err)
			sqlDB, err := db.DB()
			require.NoError(t, err)
			sqlDB.SetMaxOpenConns(1)
			oldDB, oldType := DB, common.MainDatabaseType()
			DB = db
			common.SetMainDatabaseType(common.DatabaseType(dialect))
			initCol()
			common.OptionMapRWMutex.Lock()
			oldOptions := common.OptionMap
			common.OptionMap = map[string]string{}
			common.OptionMapRWMutex.Unlock()
			t.Cleanup(func() {
				require.NoError(t, db.Migrator().DropTable(&Ability{}, &Option{}, &Channel{}))
				DB = oldDB
				common.SetMainDatabaseType(oldType)
				initCol()
				common.OptionMapRWMutex.Lock()
				common.OptionMap = oldOptions
				common.OptionMapRWMutex.Unlock()
				require.NoError(t, sqlDB.Close())
			})
			require.NoError(t, db.AutoMigrate(&Option{}, &Channel{}))
			var version string
			if dialect == "sqlite" {
				require.NoError(t, db.Raw("select sqlite_version()").Scan(&version).Error)
			} else {
				require.NoError(t, db.Raw("select version()").Scan(&version).Error)
			}
			t.Log(dialect, version)
			for _, input := range []float64{1, 1.2} {
				ch := &Channel{Name: "same model different channel", Type: 43, Key: "fixture", Models: "DeepSeek-V3"}
				cfg := &dto.ChannelCostSettings{Enabled: true, Source: "deepseek", CreditsPerCNY: 1}
				ch.SetOtherSettings(dto.ChannelOtherSettings{CostBilling: cfg})
				require.NoError(t, ch.ValidateSettings())
				require.NoError(t, db.Create(ch).Error)
				fixed, free := input/100, 0.0
				snapshot := &channelcost.Snapshot{Fingerprint: ChannelCostFingerprint(ch), GroupRatio: 1, Prices: map[string]channelcost.Price{"DeepSeek-V3": {OffPeak: channelcost.Rates{Input: input, Output: 4}}, "request-model": {RequestPrice: &fixed}, "free-request": {RequestPrice: &free}}}
				for range 2 {
					require.NoError(t, SaveChannelCostSnapshot(ch.Id, snapshot))
					var loaded Channel
					require.NoError(t, db.First(&loaded, ch.Id).Error)
					assert.Equal(t, cfg, loaded.GetOtherSettings().CostBilling)
					assert.Equal(t, snapshot, GetChannelCostSnapshot(ch.Id))
					var option Option
					require.NoError(t, db.Where(&Option{Key: fmt.Sprintf("channel_cost.%d", ch.Id)}).First(&option).Error)
					var stored channelcost.Snapshot
					require.NoError(t, common.UnmarshalJsonStr(option.Value, &stored))
					assert.Equal(t, *snapshot, stored)
				}
			}
			var count int64
			require.NoError(t, db.Model(&Option{}).Count(&count).Error)
			assert.EqualValues(t, 2, count)
			_, registryVersion, err := GetChannelPriceSources()
			require.NoError(t, err)
			source := channelcost.DefaultSources()["happyapi"]
			source.ID = "custom-json"
			source.Name = "Custom source"
			require.NoError(t, SaveChannelPriceSource(source, registryVersion))
			require.Error(t, SaveChannelPriceSource(source, registryVersion), "stale registry registryVersions cannot overwrite another source edit")
			loadedSource, err := GetChannelPriceSource(source.ID)
			require.NoError(t, err)
			assert.Equal(t, source, loadedSource)
			bound := &Channel{Id: 999, Models: "m"}
			bound.SetOtherSettings(dto.ChannelOtherSettings{CostBilling: &dto.ChannelCostSettings{Enabled: true, Source: source.ID, CreditsPerCNY: 5, UpstreamGroup: "default"}})
			beforeFingerprint := ChannelCostFingerprint(bound)
			_, registryVersion, err = GetChannelPriceSources()
			require.NoError(t, err)
			source.InputScale = 2
			require.NoError(t, SaveChannelPriceSource(source, registryVersion))
			assert.NotEqual(t, beforeFingerprint, ChannelCostFingerprint(bound), "editing a source invalidates prices on its bound channel")
			var registryOption Option
			require.NoError(t, db.Where(&Option{Key: priceSourcesOption}).First(&registryOption).Error)
			storedSources := map[string]channelcost.Source{}
			require.NoError(t, common.UnmarshalJsonStr(registryOption.Value, &storedSources))
			assert.Equal(t, source, storedSources[source.ID])

			oldMemory := common.MemoryCacheEnabled
			channelSyncLock.Lock()
			oldChannels, oldGroups, oldAdvanced := channelsIDM, group2model2channels, channel2advancedCustomConfig
			channelSyncLock.Unlock()
			common.MemoryCacheEnabled = true
			t.Cleanup(func() {
				common.MemoryCacheEnabled = oldMemory
				channelSyncLock.Lock()
				channelsIDM, group2model2channels, channel2advancedCustomConfig = oldChannels, oldGroups, oldAdvanced
				channelSyncLock.Unlock()
			})
			cached := &Channel{Id: 940003, Type: 1, Key: "fixture", Name: "last good cache"}
			CacheUpdateChannel(cached)
			require.NotPanics(t, InitChannelCache)
			loaded, err := CacheGetChannel(cached.Id)
			require.NoError(t, err)
			assert.Equal(t, cached.Name, loaded.Name)
			require.NoError(t, db.AutoMigrate(&Ability{}))
			require.NotPanics(t, InitChannelCache)
			_, err = CacheGetChannel(1)
			require.NoError(t, err)

		})
	}
}

func TestChannelCacheRetainsSnapshotWhenAbilityQueryFails(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "cache.db")), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, sqlDB.Close()) })
	oldDB, oldMemory := DB, common.MemoryCacheEnabled
	channelSyncLock.Lock()
	oldChannels, oldGroups, oldAdvanced := channelsIDM, group2model2channels, channel2advancedCustomConfig
	channelSyncLock.Unlock()
	DB = db
	common.MemoryCacheEnabled = true
	t.Cleanup(func() {
		DB = oldDB
		common.MemoryCacheEnabled = oldMemory
		channelSyncLock.Lock()
		channelsIDM, group2model2channels, channel2advancedCustomConfig = oldChannels, oldGroups, oldAdvanced
		channelSyncLock.Unlock()
	})
	require.NoError(t, db.AutoMigrate(&Channel{}))
	cached := &Channel{Id: 940001, Name: "last good cache", Type: 1, Status: 1, Models: "model", Group: "default", Key: "fixture"}
	CacheUpdateChannel(cached)
	require.NoError(t, db.Create(&Channel{Id: 940002, Name: "incomplete reload", Type: 1, Status: 1, Models: "model", Group: "new-group", Key: "fixture"}).Error)
	require.NotPanics(t, InitChannelCache)
	loaded, err := CacheGetChannel(cached.Id)
	require.NoError(t, err)
	assert.Equal(t, cached.Name, loaded.Name)
	_, err = CacheGetChannel(940002)
	require.Error(t, err, "must not publish a partial cache")
	require.NoError(t, db.AutoMigrate(&Ability{}))
	require.NotPanics(t, InitChannelCache, "channel group without an ability row must not panic")
	loaded, err = CacheGetChannel(940002)
	require.NoError(t, err)
	assert.Equal(t, "incomplete reload", loaded.Name)
}

func TestChannelCostCatalogKeepsSameNamePricesSeparate(t *testing.T) {
	oldFX := operation_setting.USDExchangeRate
	operation_setting.USDExchangeRate = 7
	common.OptionMapRWMutex.Lock()
	oldOptions := common.OptionMap
	common.OptionMap = map[string]string{}
	common.OptionMapRWMutex.Unlock()
	t.Cleanup(func() {
		operation_setting.USDExchangeRate = oldFX
		common.OptionMapRWMutex.Lock()
		common.OptionMap = oldOptions
		common.OptionMapRWMutex.Unlock()
	})
	now := time.Date(2026, 10, 16, 5, 0, 0, 0, time.UTC)
	channels := []*Channel{}
	for i, input := range []float64{1, 1.2} {
		ch := &Channel{Id: i + 1, Name: fmt.Sprintf("provider-%d", i), Type: 1, Key: "fixture", Models: "DeepSeek-V3", Group: "default", Status: 1}
		ch.SetOtherSettings(dto.ChannelOtherSettings{CostBilling: &dto.ChannelCostSettings{Enabled: true, Source: "happyapi", CreditsPerCNY: 5, UpstreamGroup: "default"}})
		channels = append(channels, ch)
		snapshot := &channelcost.Snapshot{Fingerprint: ChannelCostFingerprint(ch), SyncedAt: now.Unix(), GroupRatio: 1, Prices: map[string]channelcost.Price{"DeepSeek-V3": {OffPeak: channelcost.Rates{Input: input, Output: 4 + float64(i)}}}}
		raw, err := common.Marshal(snapshot)
		require.NoError(t, err)
		common.OptionMap[fmt.Sprintf("channel_cost.%d", ch.Id)] = string(raw)
	}
	base := []Pricing{{ModelName: "DeepSeek-V3", ModelRatio: 0.5, CompletionRatio: 4, EnableGroup: []string{"default"}, SupportedEndpointTypes: []constant.EndpointType{constant.EndpointTypeOpenAI, constant.EndpointTypeImageGeneration}}}
	result := channelCostCatalog(base, channels, now)
	require.Len(t, result, 2)
	assert.InDelta(t, 0.2, result[0].ChannelCost.CNY.Input, 1e-9)
	assert.InDelta(t, 0.8, result[0].ChannelCost.CNY.Output, 1e-9)
	assert.InDelta(t, 0.24, result[1].ChannelCost.CNY.Input, 1e-9)
	assert.InDelta(t, 1.0, result[1].ChannelCost.CNY.Output, 1e-9)
	assert.NotEqual(t, result[0].ChannelID, result[1].ChannelID)
	assert.Equal(t, []constant.EndpointType{constant.EndpointTypeOpenAI, constant.EndpointTypeImageGeneration}, result[0].SupportedEndpointTypes)
	assert.Len(t, base[0].SupportedEndpointTypes, 2)
	assert.Equal(t, 0.5, base[0].ModelRatio, "catalog cache must not be modified")
	expired := channelCostCatalog(base, channels, now.Add(25*time.Hour))
	require.Len(t, expired, 2)
	assert.Nil(t, expired[0].ChannelCost.CNY)
	assert.NotEmpty(t, expired[0].ChannelCost.Error)
}
