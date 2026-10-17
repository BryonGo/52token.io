package controller

import (
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGetDeepSeekBalanceUSD(t *testing.T) {
	tests := []struct {
		name            string
		responseJSON    string
		usdExchangeRate float64
		want            float64
		wantErrContains string
	}{
		{
			name:            "prefers USD when USD precedes CNY",
			responseJSON:    `{"balance_infos":[{"currency":"USD","total_balance":"12.50"},{"currency":"CNY","total_balance":"73.00"}]}`,
			usdExchangeRate: 7.3,
			want:            12.5,
		},
		{
			name:            "prefers USD when CNY precedes USD",
			responseJSON:    `{"balance_infos":[{"currency":"CNY","total_balance":"73.00"},{"currency":"USD","total_balance":"12.50"}]}`,
			usdExchangeRate: 7.3,
			want:            12.5,
		},
		{
			name:            "converts CNY when USD is absent",
			responseJSON:    `{"balance_infos":[{"currency":"CNY","total_balance":"73.00"}]}`,
			usdExchangeRate: 7.3,
			want:            10,
		},
		{
			name:            "returns error when USD and CNY are absent",
			responseJSON:    `{"balance_infos":[{"currency":"EUR","total_balance":"10.00"}]}`,
			usdExchangeRate: 7.3,
			wantErrContains: "currency USD or CNY not found",
		},
		{
			name:            "returns USD parse error instead of falling back to CNY",
			responseJSON:    `{"balance_infos":[{"currency":"USD","total_balance":"invalid"},{"currency":"CNY","total_balance":"73.00"}]}`,
			usdExchangeRate: 7.3,
			wantErrContains: "invalid syntax",
		},
		{
			name:            "rejects NaN USD balance",
			responseJSON:    `{"balance_infos":[{"currency":"USD","total_balance":"NaN"}]}`,
			usdExchangeRate: 7.3,
			wantErrContains: "USD balance must be finite",
		},
		{
			name:            "rejects negative USD balance",
			responseJSON:    `{"balance_infos":[{"currency":"USD","total_balance":"-1.00"}]}`,
			usdExchangeRate: 7.3,
			wantErrContains: "USD balance must be non-negative",
		},
		{
			name:            "rejects positive infinity CNY balance",
			responseJSON:    `{"balance_infos":[{"currency":"CNY","total_balance":"+Inf"}]}`,
			usdExchangeRate: 7.3,
			wantErrContains: "CNY balance must be finite",
		},
		{
			name:            "rejects negative CNY balance",
			responseJSON:    `{"balance_infos":[{"currency":"CNY","total_balance":"-7.30"}]}`,
			usdExchangeRate: 7.3,
			wantErrContains: "CNY balance must be non-negative",
		},
		{
			name:            "returns error for non-positive CNY exchange rate",
			responseJSON:    `{"balance_infos":[{"currency":"CNY","total_balance":"73.00"}]}`,
			usdExchangeRate: 0,
			wantErrContains: "USD exchange rate must be greater than zero",
		},
		{
			name:            "rejects NaN CNY exchange rate",
			responseJSON:    `{"balance_infos":[{"currency":"CNY","total_balance":"73.00"}]}`,
			usdExchangeRate: math.NaN(),
			wantErrContains: "USD exchange rate must be finite",
		},
		{
			name:            "rejects positive infinity CNY exchange rate",
			responseJSON:    `{"balance_infos":[{"currency":"CNY","total_balance":"73.00"}]}`,
			usdExchangeRate: math.Inf(1),
			wantErrContains: "USD exchange rate must be finite",
		},
		{
			name:            "rejects CNY conversion overflow",
			responseJSON:    `{"balance_infos":[{"currency":"CNY","total_balance":"1.7976931348623157e+308"}]}`,
			usdExchangeRate: math.SmallestNonzeroFloat64,
			wantErrContains: "converted USD balance must be finite",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var response DeepSeekUsageResponse
			require.NoError(t, common.Unmarshal([]byte(test.responseJSON), &response))

			balance, err := getDeepSeekBalanceUSD(response, test.usdExchangeRate)
			if test.wantErrContains != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), test.wantErrContains)
				return
			}

			require.NoError(t, err)
			assert.InDelta(t, test.want, balance, 1e-12)
		})
	}
}

func TestUpdateChannelNewAPIBalanceUsesTheSmallerRemainingQuota(t *testing.T) {
	for _, tc := range []struct {
		name    string
		usage   string
		want    float64
		wantErr string
	}{
		{name: "user quota below the key quota", usage: `{"code":true,"data":{"total_available":2000000,"unlimited_quota":false,"user_quota":{"total_available":500000}}}`, want: 0.5},
		{name: "key quota below the user quota", usage: `{"code":true,"data":{"total_available":500000,"unlimited_quota":false,"user_quota":{"total_available":2000000}}}`, want: 0.5},
		{name: "unlimited key uses the user quota", usage: `{"code":true,"data":{"total_available":0,"unlimited_quota":true,"user_quota":{"total_available":3000000}}}`, want: 3},
		{name: "key quota alone without user quota", usage: `{"code":true,"data":{"total_available":1500000,"unlimited_quota":false}}`, want: 1.5},
		{name: "unlimited key without user quota", usage: `{"code":true,"data":{"total_available":0,"unlimited_quota":true}}`, wantErr: "Allow querying user quota"},
		{name: "upstream rejects the key", usage: `{"success":false,"message":"invalid token"}`, wantErr: "invalid token"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			setupSub2APIBalanceTest(t)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/api/usage/token/":
					assert.Equal(t, "Bearer sk-upstream", r.Header.Get("Authorization"))
					_, _ = w.Write([]byte(tc.usage))
				case "/api/status":
					assert.Empty(t, r.Header.Get("Authorization"), "the key is not sent to the public status endpoint")
					_, _ = w.Write([]byte(`{"success":true,"data":{"quota_per_unit":1000000}}`))
				default:
					http.NotFound(w, r)
				}
			}))
			defer server.Close()
			baseURL := server.URL + "/"
			channel := &model.Channel{Type: constant.ChannelTypeNewAPI, BaseURL: &baseURL, Key: "sk-upstream", Name: "new-api-upstream"}
			require.NoError(t, model.DB.Create(channel).Error)

			result, err := updateChannelBalance(channel)
			if tc.wantErr != "" {
				require.ErrorContains(t, err, tc.wantErr)
				return
			}
			require.NoError(t, err)
			assert.InDelta(t, tc.want, result.Balance, 1e-9)
			assert.InDelta(t, tc.want, channel.Balance, 1e-9)
		})
	}
}

func TestUpdateChannelNewAPILegacyBalanceUsesTheSuperAdminKey(t *testing.T) {
	setupSub2APIBalanceTest(t)

	sawUserSelf := false
	sawKeyUsage := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/user/self":
			sawUserSelf = true
			assert.Equal(t, "Bearer super-admin-token", r.Header.Get("Authorization"))
			assert.Equal(t, "11566", r.Header.Get("New-Api-User"))
			_, _ = w.Write([]byte(`{"success":true,"message":"","data":{"quota":1735350589,"used_quota":1764899411}}`))
		case "/api/usage/token/":
			sawKeyUsage = true
			_, _ = w.Write([]byte(`{"code":true,"data":{"total_available":500000,"unlimited_quota":false}}`))
		case "/api/status":
			_, _ = w.Write([]byte(`{"success":true,"data":{"quota_per_unit":500000}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	baseURL := server.URL
	channel := &model.Channel{
		Type:    constant.ChannelTypeNewAPILegacy,
		BaseURL: &baseURL,
		Key:     "sk-relay-key",
		Name:    "legacy-new-api",
	}
	channel.SetSetting(dto.ChannelSettings{SuperKey: "super-admin-token", SuperKeyUserID: 11566})
	require.NoError(t, model.DB.Create(channel).Error)

	result, err := updateChannelBalance(channel)

	require.NoError(t, err)
	assert.True(t, sawUserSelf, "the account endpoint answers when a super admin key is configured")
	assert.False(t, sawKeyUsage, "the relay key is not used for the balance when a super admin key is configured")
	assert.InDelta(t, 3470.701178, result.Balance, 1e-6)
	assert.InDelta(t, result.Balance, channel.Balance, 1e-9)
}

func TestUpdateChannelNewAPILegacyBalanceFallsBackToTheKeyQuery(t *testing.T) {
	setupSub2APIBalanceTest(t)

	sawUserSelf := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/user/self":
			sawUserSelf = true
			http.NotFound(w, r)
		case "/api/usage/token/":
			assert.Equal(t, "Bearer sk-relay-key", r.Header.Get("Authorization"))
			_, _ = w.Write([]byte(`{"code":true,"data":{"total_available":500000,"unlimited_quota":false}}`))
		case "/api/status":
			_, _ = w.Write([]byte(`{"success":true,"data":{"quota_per_unit":500000}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	baseURL := server.URL
	channel := &model.Channel{
		Type:    constant.ChannelTypeNewAPILegacy,
		BaseURL: &baseURL,
		Key:     "sk-relay-key",
		Name:    "legacy-new-api-without-super-key",
	}
	require.NoError(t, model.DB.Create(channel).Error)

	result, err := updateChannelBalance(channel)

	require.NoError(t, err)
	assert.False(t, sawUserSelf, "without a super admin key the account endpoint is not called")
	assert.InDelta(t, 1, result.Balance, 1e-9)
}

func TestUpdateChannelNewAPILegacyBalanceReportsUpstreamRejection(t *testing.T) {
	setupSub2APIBalanceTest(t)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/user/self" {
			_, _ = w.Write([]byte(`{"success":false,"message":"invalid access token","data":{}}`))
			return
		}
		http.NotFound(w, r)
	}))
	defer server.Close()

	baseURL := server.URL
	channel := &model.Channel{
		Type:    constant.ChannelTypeNewAPILegacy,
		BaseURL: &baseURL,
		Key:     "sk-relay-key",
		Name:    "legacy-new-api-rejected",
	}
	channel.SetSetting(dto.ChannelSettings{SuperKey: "stale-super-key"})
	require.NoError(t, model.DB.Create(channel).Error)

	_, err := updateChannelBalance(channel)

	require.ErrorContains(t, err, "rejected the account balance credentials")
}

func TestAdvancedChannelAccountBalanceUsesItsOwnCredentialAndCNYCost(t *testing.T) {
	setupSub2APIBalanceTest(t)
	testChannelAccountBalanceIsolation(t)
}

func testChannelAccountBalanceIsolation(t *testing.T) {
	t.Helper()
	previousFX := operation_setting.USDExchangeRate
	t.Cleanup(func() { operation_setting.USDExchangeRate = previousFX })
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/status" {
			assert.Empty(t, r.Header.Get("Authorization"))
			_, _ = w.Write([]byte(`{"success":true,"data":{"quota_per_unit":500000}}`))
			return
		}
		require.Equal(t, "/api/user/self", r.URL.Path)
		switch r.Header.Get("Authorization") {
		case "Bearer account-one":
			assert.Equal(t, "11", r.Header.Get("New-Api-User"))
			_, _ = w.Write([]byte(`{"success":true,"data":{"quota":50000000}}`))
		case "Bearer account-two":
			assert.Equal(t, "22", r.Header.Get("New-Api-User"))
			_, _ = w.Write([]byte(`{"success":true,"data":{"quota":10000000}}`))
		default:
			t.Error("unexpected credential; model key must not be used")
		}
	}))
	defer server.Close()
	for _, fx := range []float64{1, 7} {
		operation_setting.USDExchangeRate = fx
		for i := range 6 {
			channelType := constant.ChannelTypeAdvancedCustom
			pluginKey := ""
			if i >= 2 {
				channelType = constant.ChannelTypeTaskPlugin
				pluginKey = "newtoken"
			}
			if i >= 4 {
				channelType = constant.ChannelTypeOpenAI
				pluginKey = ""
			}
			account := i % 2
			channel := &model.Channel{Type: channelType, Name: fmt.Sprintf("account-%d-%g", i, fx), BaseURL: common.GetPointer(server.URL), Key: "model-call-key"}
			channel.SetSetting(dto.ChannelSettings{TaskPluginKey: pluginKey, SuperKey: []string{"account-one", "account-two"}[account], SuperKeyUserID: []int{11, 22}[account]})
			channel.SetOtherSettings(dto.ChannelOtherSettings{CostBilling: &dto.ChannelCostSettings{Enabled: true, Source: "happyapi", CreditsPerCNY: 5, UpstreamGroup: "vip"}})
			require.NoError(t, model.DB.Create(channel).Error)
			result, err := updateChannelBalance(channel)
			require.NoError(t, err)
			assert.InDelta(t, []float64{20, 4}[account], result.Balance*fx, 1e-10, "CNY acquisition cost, independent account and display FX")
			assert.Empty(t, result.RawResponse, "account profile fields must not be returned")
			var saved model.Channel
			require.NoError(t, model.DB.First(&saved, channel.Id).Error)
			assert.InDelta(t, result.Balance, saved.Balance, 1e-10)
			assert.Positive(t, saved.BalanceUpdatedTime)
			assert.Equal(t, "model-call-key", saved.Key)
			assert.Equal(t, channel.GetSetting().SuperKey, saved.GetSetting().SuperKey)
			clearChannelInfo(&saved)
			assert.Empty(t, saved.GetSetting().SuperKey, "management responses never disclose account credentials")
		}
	}
}

func TestNewTokenPluginBalanceManagementEndpoint(t *testing.T) {
	setupSub2APIBalanceTest(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/user/self":
			assert.Equal(t, "Bearer account-fixture", r.Header.Get("Authorization"))
			assert.Equal(t, "2752", r.Header.Get("New-Api-User"))
			_, _ = w.Write([]byte(`{"success":true,"data":{"quota":12500000,"access_token":"must-not-leak"}}`))
		case "/api/status":
			_, _ = w.Write([]byte(`{"success":true,"data":{"quota_per_unit":500000}}`))
		default:
			t.Errorf("unexpected balance request: %s", r.URL.Path)
		}
	}))
	defer server.Close()
	for _, pluginKey := range []string{"newtoken", "byteplus"} {
		channel := &model.Channel{Type: constant.ChannelTypeTaskPlugin, BaseURL: common.GetPointer(server.URL), Key: "model-call-key"}
		channel.SetSetting(dto.ChannelSettings{TaskPluginKey: pluginKey, SuperKey: "account-fixture", SuperKeyUserID: 2752})
		require.NoError(t, model.DB.Create(channel).Error)
		recorder := httptest.NewRecorder()
		ctx, _ := gin.CreateTestContext(recorder)
		ctx.Params = gin.Params{{Key: "id", Value: fmt.Sprint(channel.Id)}}
		UpdateChannelBalance(ctx)
		var response struct {
			Success bool    `json:"success"`
			Balance float64 `json:"balance"`
		}
		require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &response))
		assert.Equal(t, pluginKey == "newtoken", response.Success)
		if response.Success {
			assert.Equal(t, 25.0, response.Balance)
		}
		assert.NotContains(t, recorder.Body.String(), "must-not-leak")
		assert.NotContains(t, recorder.Body.String(), "account-fixture")
	}
}

func TestChannelAccountBalanceRejectsInvalidResponsesWithoutOverwritingBalance(t *testing.T) {
	for _, tc := range []struct {
		name, self, status, wantErr string
		want                        float64
	}{
		{name: "missing quota", self: `{"success":true,"data":{}}`, wantErr: "balance is missing"},
		{name: "rejected account", self: `{"success":false,"message":"secret-fixture private-profile"}`, wantErr: "rejected the account"},
		{name: "invalid quota unit", self: `{"success":true,"data":{"quota":100}}`, status: `{"success":true,"data":{"quota_per_unit":0}}`, wantErr: "quota unit"},
		{name: "oversized response", self: strings.Repeat("x", maxAdvancedCustomBalanceResponseBytes+1), wantErr: "Invalid upstream account"},
		{name: "explicit zero balance", self: `{"success":true,"data":{"quota":0}}`, status: `{"success":true,"data":{"quota_per_unit":500000}}`, want: 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			setupSub2APIBalanceTest(t)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/api/status" {
					_, _ = w.Write([]byte(tc.status))
					return
				}
				_, _ = w.Write([]byte(tc.self))
			}))
			defer server.Close()
			channel := &model.Channel{Type: constant.ChannelTypeAdvancedCustom, BaseURL: common.GetPointer(server.URL), Balance: 42}
			channel.SetSetting(dto.ChannelSettings{SuperKey: "secret-fixture"})
			require.NoError(t, model.DB.Create(channel).Error)
			result, err := updateChannelBalance(channel)
			if tc.wantErr != "" {
				require.ErrorContains(t, err, tc.wantErr)
				assert.NotContains(t, err.Error(), "secret-fixture")
				assert.NotContains(t, err.Error(), "private-profile")
				var saved model.Channel
				require.NoError(t, model.DB.First(&saved, channel.Id).Error)
				assert.Equal(t, 42.0, saved.Balance)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.want, result.Balance)
		})
	}
}

func TestChannelAccountBalanceDoesNotForwardCredentialsToRedirectsOrInsecureHosts(t *testing.T) {
	destinationCalls := 0
	destination := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { destinationCalls++ }))
	defer destination.Close()
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, destination.URL, http.StatusFound) }))
	defer redirect.Close()
	for _, tc := range []struct {
		name, url string
		insecure  bool
		userID    int
		key, want string
	}{
		{name: "redirect", url: redirect.URL, key: "secret-fixture", want: "HTTP"},
		{name: "remote HTTP", url: "http://upstream.invalid", key: "secret-fixture", want: "HTTPS"},
		{name: "skip certificate verification", url: redirect.URL, insecure: true, key: "secret-fixture", want: "certificate verification"},
		{name: "invalid user ID", url: redirect.URL, userID: -1, key: "secret-fixture", want: "Invalid upstream account balance credentials"},
		{name: "invalid token header", url: redirect.URL, key: "secret-fixture\r\ninjected:value", want: "Invalid upstream account balance credentials"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			channel := &model.Channel{Type: constant.ChannelTypeAdvancedCustom, BaseURL: common.GetPointer(tc.url)}
			channel.SetSetting(dto.ChannelSettings{SuperKey: tc.key, SuperKeyUserID: tc.userID, TLSInsecureSkipVerify: tc.insecure})
			_, err := updateChannelBalance(channel)
			require.ErrorContains(t, err, tc.want)
			assert.NotContains(t, err.Error(), "secret-fixture")
		})
	}
	assert.Zero(t, destinationCalls)
}

func TestChannelAccountBalanceSettingsRequireSensitiveWrite(t *testing.T) {
	origin := &model.Channel{Type: constant.ChannelTypeAdvancedCustom}
	updated := PatchChannel{Channel: *origin}
	updated.SetSetting(dto.ChannelSettings{SuperKey: "account-fixture", SuperKeyUserID: 42})
	assert.True(t, channelHasSensitiveChanges(&updated, origin, map[string]any{"setting": *updated.Setting}))
	for _, setting := range []dto.ChannelSettings{
		{SuperKey: strings.Repeat("a", 8193)},
		{SuperKey: "account-fixture\r\nheader:value"},
		{SuperKeyUserID: -1},
	} {
		channel := &model.Channel{Type: constant.ChannelTypeNewAPILegacy}
		channel.SetSetting(setting)
		require.ErrorContains(t, validateChannel(channel, false), "Invalid upstream account balance credentials")
	}
}
