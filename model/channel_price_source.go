package model

import (
	"crypto/sha256"
	"errors"
	"fmt"
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
	return sources, fmt.Sprintf("%x", sha256.Sum256([]byte(raw))), nil
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
func SaveChannelPriceSource(source channelcost.Source, expected string) error {
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
	sources[source.ID] = source
	raw, err := common.Marshal(sources)
	if err != nil {
		return err
	}
	return UpdateOptionsBulk(map[string]string{priceSourcesOption: string(raw)})
}
