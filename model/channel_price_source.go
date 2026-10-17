package model

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"maps"
	"net/url"
	"slices"
	"strings"
	"sync"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/pkg/channelcost"
)

const priceSourcesOption = "channel_price_sources"

var priceSourcesMu sync.Mutex

func GetChannelPriceSources() (map[string]channelcost.Source, string, error) {
	common.OptionMapRWMutex.RLock()
	raw := common.OptionMap[priceSourcesOption]
	common.OptionMapRWMutex.RUnlock()
	sources := channelcost.DefaultSources()
	if raw != "" {
		if err := common.UnmarshalJsonStr(raw, &sources); err != nil {
			return nil, "", errors.New("Invalid saved price sources")
		}
	}
	revision := raw
	for _, id := range slices.Sorted(maps.Keys(sources)) {
		source := sources[id]
		common.OptionMapRWMutex.RLock()
		credentialRaw := common.OptionMap["channel_price_source."+id+".Secret"]
		common.OptionMapRWMutex.RUnlock()
		source.AuthConfigured = credentialRaw != ""
		if credentialRaw != "" {
			var credential PriceSourceCredential
			if common.UnmarshalJsonStr(credentialRaw, &credential) != nil {
				return nil, "", errors.New("Invalid price source credentials")
			}
			source.AuthUserID = credential.UserID
			revision += "\x00" + id + fmt.Sprintf("%x", sha256.Sum256([]byte(credentialRaw)))
		}
		sources[id] = source
	}
	return sources, fmt.Sprintf("%x", sha256.Sum256([]byte(revision))), nil
}
func GetChannelPriceSource(id string) (channelcost.Source, error) {
	sources, _, err := GetChannelPriceSources()
	if err != nil {
		return channelcost.Source{}, err
	}
	s, ok := sources[id]
	if !ok || !s.Enabled {
		return s, errors.New("Price source is missing or disabled")
	}
	return s, nil
}

type PriceSourceCredential struct {
	Token      string `json:"token"`
	UserID     int    `json:"user_id"`
	PricingURL string `json:"pricing_url"`
	StatusURL  string `json:"status_url"`
}

func ValidatePriceSourceCredential(source channelcost.Source, credential PriceSourceCredential) error {
	if credential.UserID <= 0 || credential.UserID > 2147483647 || credential.Token == "" || len(credential.Token) > 2048 || strings.ContainsAny(credential.Token, "\r\n\x00") {
		return errors.New("Invalid price source credentials")
	}
	for _, endpoint := range []string{source.URL, source.StatusURL} {
		if endpoint == "" {
			continue
		}
		u, err := url.Parse(endpoint)
		if err != nil || u.Scheme != "https" {
			return errors.New("Price source credentials require HTTPS")
		}
		origin, err := url.Parse(source.ChannelOrigin)
		if err != nil || origin.Scheme != u.Scheme || !strings.EqualFold(origin.Host, u.Host) {
			return errors.New("Price source credentials must use the bound origin")
		}
	}
	return nil
}

// Secrets are separate from the source registry and ordinary option responses.
// Exact endpoint binding prevents preview edits from forwarding a saved token.
func GetPriceSourceCredential(source channelcost.Source) (string, int, error) {
	common.OptionMapRWMutex.RLock()
	raw := common.OptionMap["channel_price_source."+source.ID+".Secret"]
	common.OptionMapRWMutex.RUnlock()
	if raw == "" {
		return "", 0, nil
	}
	var credential PriceSourceCredential
	if common.UnmarshalJsonStr(raw, &credential) != nil {
		return "", 0, errors.New("Invalid price source credentials")
	}
	if credential.PricingURL != source.URL || credential.StatusURL != source.StatusURL {
		return "", 0, errors.New("Price source endpoints changed; enter its credentials again")
	}
	return credential.Token, credential.UserID, nil
}
func SaveChannelPriceSource(source channelcost.Source, expected string, credentials ...PriceSourceCredential) error {
	if err := source.Validate(); err != nil {
		return err
	}
	priceSourcesMu.Lock()
	defer priceSourcesMu.Unlock()
	sources, version, err := GetChannelPriceSources()
	if err != nil {
		return err
	}
	if expected != version {
		return errors.New("Price sources changed; reload before saving")
	}
	if _, ok := sources[source.ID]; !ok && len(sources) >= 100 {
		return errors.New("Too many price sources")
	}
	source.AuthConfigured = false
	source.AuthUserID = 0
	sources[source.ID] = source
	raw, err := common.Marshal(sources)
	if err != nil {
		return err
	}
	values := map[string]string{priceSourcesOption: string(raw)}
	if len(credentials) > 0 && credentials[0].Token != "" {
		credential := credentials[0]
		if err := ValidatePriceSourceCredential(source, credential); err != nil {
			return err
		}
		credential.PricingURL = source.URL
		credential.StatusURL = source.StatusURL
		encoded, err := common.Marshal(credential)
		if err != nil {
			return err
		}
		values["channel_price_source."+source.ID+".Secret"] = string(encoded)
	} else if _, _, err := GetPriceSourceCredential(source); err != nil {
		return err
	}
	return UpdateOptionsBulk(values)
}
