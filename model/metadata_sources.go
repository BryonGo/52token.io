package model

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"strings"
	"unicode"

	"github.com/QuantumNous/new-api/common"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const metadataSourcesOption = "model_metadata_sources"

type MetadataAlias struct {
	Match         string `json:"match"`
	Name          string `json:"name"`
	MetadataModel string `json:"metadata_model"`
	Vendor        string `json:"vendor"`
}

type MetadataSources struct {
	ChannelFallback bool            `json:"channel_fallback"`
	Aliases         []MetadataAlias `json:"aliases"`
}

func DefaultMetadataSources() MetadataSources {
	return MetadataSources{ChannelFallback: true, Aliases: []MetadataAlias{{Match: "prefix", Name: "grok-", Vendor: "xAI"}}}
}

func (s MetadataSources) Validate() error {
	if len(s.Aliases) > 200 {
		return errors.New("Too many metadata aliases")
	}
	seen := map[string]bool{}
	for _, a := range s.Aliases {
		if a.Match != "exact" && a.Match != "prefix" {
			return errors.New("Invalid metadata alias match type")
		}
		for _, v := range []string{a.Name, a.MetadataModel, a.Vendor} {
			if strings.TrimSpace(v) != v || len(v) > 200 || strings.ContainsFunc(v, unicode.IsControl) {
				return errors.New("Invalid metadata alias value")
			}
		}
		if a.Name == "" || a.MetadataModel == "" && a.Vendor == "" || a.Name == a.MetadataModel {
			return errors.New("Metadata aliases need a name and a target model or vendor")
		}
		key := a.Match + ":" + a.Name
		if seen[key] {
			return errors.New("Duplicate metadata alias")
		}
		seen[key] = true
	}
	return nil
}

func getMetadataSources(db *gorm.DB) (MetadataSources, string, error) {
	var option Option
	err := db.Where(commonKeyCol+" = ?", metadataSourcesOption).First(&option).Error
	cfg := DefaultMetadataSources()
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return cfg, "", err
	}
	if option.Value != "" {
		if err = common.UnmarshalJsonStr(option.Value, &cfg); err != nil {
			return cfg, "", errors.New("Invalid saved metadata sources")
		}
	}
	if cfg.Aliases == nil {
		cfg.Aliases = []MetadataAlias{}
	}
	if err = cfg.Validate(); err != nil {
		return cfg, "", err
	}
	raw, _ := common.Marshal(cfg)
	return cfg, fmt.Sprintf("%x", sha256.Sum256(raw)), nil
}

func GetMetadataSources() (MetadataSources, string, error) { return getMetadataSources(DB) }

func SaveMetadataSources(cfg MetadataSources, expected string) error {
	if err := cfg.Validate(); err != nil {
		return err
	}
	raw, err := common.Marshal(cfg)
	if err != nil {
		return err
	}
	err = metadataTransaction(func(tx *gorm.DB) error {
		_, version, err := getMetadataSources(tx)
		if err != nil {
			return err
		}
		if version != expected {
			return errors.New("Metadata sources changed; reload before saving")
		}
		return tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "key"}}, DoUpdates: clause.AssignmentColumns([]string{"value"})}).Create(&Option{Key: metadataSourcesOption, Value: string(raw)}).Error
	})
	if err == nil {
		common.OptionMapRWMutex.Lock()
		if common.OptionMap == nil {
			common.OptionMap = map[string]string{}
		}
		common.OptionMap[metadataSourcesOption] = string(raw)
		common.OptionMapRWMutex.Unlock()
	}
	return err
}
