package model

import (
	"crypto/sha256"
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"
	"sync"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/setting"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	"gorm.io/gorm"
)

const groupProfilesOption = "GroupProfiles"

var groupCatalogMutex sync.Mutex

type GroupProfile struct {
	Name string `json:"name"`
}

type ManagedGroup struct {
	ID string `json:"id"`
	GroupProfile
	Ratio       float64        `json:"ratio"`
	Selectable  bool           `json:"selectable"`
	Description string         `json:"description"`
	Channels    []GroupChannel `json:"channels"`
	TokenCount  int            `json:"token_count"`
	UserCount   int            `json:"user_count"`
	Defined     bool           `json:"defined"`
}
type GroupChannel struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
}
type GroupCatalog struct {
	Version string         `json:"version"`
	Groups  []ManagedGroup `json:"groups"`
}
type GroupMutation struct {
	ID          string  `json:"id"`
	Name        string  `json:"name"`
	Ratio       float64 `json:"ratio"`
	Selectable  bool    `json:"selectable"`
	Description string  `json:"description"`
	Version     string  `json:"version"`
}

func GetGroupProfiles() map[string]GroupProfile {
	common.OptionMapRWMutex.RLock()
	raw := common.OptionMap[groupProfilesOption]
	common.OptionMapRWMutex.RUnlock()
	profiles := map[string]GroupProfile{}
	if raw != "" {
		_ = common.UnmarshalJsonStr(raw, &profiles)
	}
	return profiles
}

// The management catalog shows live bindings, including explicit Auto choices.
// The persistent ID sequence prevents reuse after a group is deleted.
func GetGroupCatalog() (*GroupCatalog, error) {
	profiles, ratios, usable, version, err := readGroupDefinitions(DB)
	if err != nil {
		return nil, err
	}
	groups := map[string]*ManagedGroup{}
	ensure := func(id string) *ManagedGroup {
		if g := groups[id]; g != nil {
			return g
		}
		profile := profiles[id]
		if profile.Name == "" {
			profile.Name = id
		}
		ratio, defined := ratios[id]
		desc, selectable := usable[id]
		g := &ManagedGroup{ID: id, GroupProfile: profile, Ratio: ratio, Defined: defined, Selectable: selectable, Description: desc, Channels: []GroupChannel{}}
		groups[id] = g
		return g
	}
	for id := range ratios {
		ensure(id)
	}
	var channels []Channel
	if err := DB.Select("id", "name", commonGroupCol).Find(&channels).Error; err != nil {
		return nil, err
	}
	for _, ch := range channels {
		for id := range strings.SplitSeq(ch.Group, ",") {
			if id != "" {
				g := ensure(id)
				g.Channels = append(g.Channels, GroupChannel{ch.Id, ch.Name})
			}
		}
	}
	var tokens []Token
	if err := DB.Select(commonGroupCol, "auto_groups").Find(&tokens).Error; err != nil {
		return nil, err
	}
	for _, token := range tokens {
		ids, err := token.GetAutoGroups()
		if err != nil {
			return nil, err
		}
		if token.Group != "" && token.Group != "auto" {
			ids = append(ids, token.Group)
		}
		seen := map[string]bool{}
		for _, id := range ids {
			if !seen[id] {
				ensure(id).TokenCount++
				seen[id] = true
			}
		}
	}
	var users []User
	if err := DB.Select(commonGroupCol).Find(&users).Error; err != nil {
		return nil, err
	}
	for _, u := range users {
		if u.Group != "" {
			ensure(u.Group).UserCount++
		}
	}
	result := &GroupCatalog{Groups: []ManagedGroup{}}
	for _, g := range groups {
		result.Groups = append(result.Groups, *g)
	}
	slices.SortFunc(result.Groups, func(a, b ManagedGroup) int { return strings.Compare(a.ID, b.ID) })
	result.Version = version
	return result, nil
}

// Revision covers pricing and visibility as well as names, so saving a stale
// editor cannot overwrite another administrator's changes.
func readGroupDefinitions(db *gorm.DB) (map[string]GroupProfile, map[string]float64, map[string]string, string, error) {
	profiles := map[string]GroupProfile{}
	ratios := ratio_setting.GetGroupRatioCopy()
	usable := setting.GetUserUsableGroupsCopy()
	var options []Option
	if err := db.Where(commonKeyCol+" IN ?", []string{groupProfilesOption, "GroupRatio", "UserUsableGroups"}).Order(commonKeyCol).Find(&options).Error; err != nil {
		return nil, nil, nil, "", err
	}
	for _, option := range options {
		if option.Value == "" {
			continue
		}
		var err error
		switch option.Key {
		case groupProfilesOption:
			err = common.UnmarshalJsonStr(option.Value, &profiles)
		case "GroupRatio":
			ratios = map[string]float64{}
			err = common.UnmarshalJsonStr(option.Value, &ratios)
		case "UserUsableGroups":
			usable = map[string]string{}
			err = common.UnmarshalJsonStr(option.Value, &usable)
		}
		if err != nil {
			return nil, nil, nil, "", err
		}
	}
	raw, err := common.Marshal(options)
	if err != nil {
		return nil, nil, nil, "", err
	}
	return profiles, ratios, usable, fmt.Sprintf("%x", sha256.Sum256(raw)), nil
}

func SaveManagedGroup(input GroupMutation) (string, error) {
	input.Name = strings.TrimSpace(input.Name)
	if input.Name == "" || len([]rune(input.Name)) > 80 || len(input.Description) > 1000 || math.IsNaN(input.Ratio) || math.IsInf(input.Ratio, 0) || input.Ratio < 0 {
		return "", common.NewMessage("Invalid group settings")
	}
	groupCatalogMutex.Lock()
	defer groupCatalogMutex.Unlock()
	values := map[string]string{}
	create := input.ID == ""

	err := DB.Transaction(func(tx *gorm.DB) error {
		// All managed writes lock the same existing row. The revision is checked
		// inside the transaction, before any values are changed.
		var guard Option
		// Lock if persisted; the unique option key serializes first creation.
		if err := lockForUpdate(tx).Where(Option{Key: "GroupRatio"}).Limit(1).Find(&guard).Error; err != nil {
			return err
		}
		profiles, ratios, usable, version, err := readGroupDefinitions(tx)
		if err != nil {
			return err
		}
		if version != input.Version {
			return common.NewMessage("Group settings changed. Reload before saving")
		}
		if create {
			var sequence Option
			if err := tx.Where(Option{Key: "GroupIDSequence"}).Limit(1).Find(&sequence).Error; err != nil {
				return err
			}
			n, err := strconv.Atoi(sequence.Value)
			if sequence.Value != "" && (err != nil || n < 0 || n > 1000000000) {
				return common.NewMessage("Invalid group settings")
			}
			for {
				n++
				input.ID = "g" + strconv.Itoa(n)
				if _, exists := ratios[input.ID]; exists {
					continue
				}
				// Sequence survives deletion; also avoid identifiers imported by old tools.
				var count int64
				if err := tx.Unscoped().Model(&Token{}).Where(commonGroupCol+" = ?", input.ID).Count(&count).Error; err != nil {
					return err
				}
				if count == 0 {
					break
				}
			}
			values["GroupIDSequence"] = strconv.Itoa(n)
		}
		if !create {
			if _, ok := ratios[input.ID]; !ok {
				return common.NewMessage("Group is not registered")
			}
		}
		profiles[input.ID] = GroupProfile{Name: input.Name}
		ratios[input.ID] = input.Ratio
		if input.Selectable {
			usable[input.ID] = input.Description
		} else {
			delete(usable, input.ID)
		}
		for key, value := range map[string]any{groupProfilesOption: profiles, "GroupRatio": ratios, "UserUsableGroups": usable} {
			raw, err := common.Marshal(value)
			if err != nil {
				return err
			}
			values[key] = string(raw)
		}
		for key, value := range values {
			option := Option{Key: key}
			if err := tx.FirstOrCreate(&option, Option{Key: key}).Error; err != nil {
				return err
			}
			if err := tx.Model(&option).Update("value", value).Error; err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	for key, value := range values {
		if err := updateOptionMap(key, value); err != nil {
			return "", err
		}
	}
	return input.ID, nil
}

// Legacy JSON editors may change ratios, but removing a referenced identifier
// is not a rename. Display-name edits must go through the catalog.
func ValidateGroupRatioReferences(raw string) error {
	var next map[string]float64
	if err := common.UnmarshalJsonStr(raw, &next); err != nil {
		return err
	}
	for _, ratio := range next {
		if math.IsNaN(ratio) || math.IsInf(ratio, 0) || ratio < 0 {
			return common.NewMessage("Invalid group settings")
		}
	}
	removed := map[string]bool{}
	for id := range ratio_setting.GetGroupRatioCopy() {
		if _, ok := next[id]; !ok {
			removed[id] = true
		}
	}
	if len(removed) == 0 {
		return nil
	}
	catalog, err := GetGroupCatalog()
	if err != nil {
		return err
	}
	for _, g := range catalog.Groups {
		if removed[g.ID] && (len(g.Channels) > 0 || g.TokenCount > 0 || g.UserCount > 0) {
			return common.NewMessage("Cannot remove a group that is still in use. Change its display name instead")
		}
	}
	for _, id := range setting.GetAutoGroups() {
		if removed[id] {
			return common.NewMessage("Cannot remove a group used by Auto routing")
		}
	}
	return nil
}

// DeleteManagedGroup refuses live routing/account references. Removing an unused
// group also removes its visibility and optional ratio rules atomically.
func DeleteManagedGroup(id, version string) error {
	if id == "" || id == "default" || id == "auto" {
		return common.NewMessage("Cannot delete the system default group")
	}
	groupCatalogMutex.Lock()
	defer groupCatalogMutex.Unlock()
	values := map[string]string{}
	err := DB.Transaction(func(tx *gorm.DB) error {
		var guard Option
		if err := lockForUpdate(tx).Where(Option{Key: "GroupRatio"}).Limit(1).Find(&guard).Error; err != nil {
			return err
		}
		profiles, ratios, usable, current, err := readGroupDefinitions(tx)
		if err != nil {
			return err
		}
		if version != current {
			return common.NewMessage("Group settings changed. Reload before saving")
		}
		var count int64
		for _, ref := range []struct {
			table  any
			column string
		}{{&Token{}, commonGroupCol}, {&User{}, commonGroupCol}, {&SubscriptionPlan{}, "upgrade_group"}, {&SubscriptionPlan{}, "downgrade_group"}, {&UserSubscription{}, "upgrade_group"}, {&UserSubscription{}, "downgrade_group"}, {&UserSubscription{}, "prev_user_group"}, {&Task{}, commonGroupCol}} {
			if !tx.Migrator().HasTable(ref.table) {
				continue
			}
			if err := tx.Model(ref.table).Where(ref.column+" = ?", id).Count(&count).Error; err != nil {
				return err
			}
			if count > 0 {
				return common.NewMessage("Cannot remove a group that is still in use. Change its display name instead")
			}
		}
		var channels []Channel
		if err := tx.Select("id", commonGroupCol).Find(&channels).Error; err != nil {
			return err
		}
		for _, ch := range channels {
			if slices.Contains(strings.Split(ch.Group, ","), id) {
				return common.NewMessage("Cannot remove a group that is still in use. Change its display name instead")
			}
		}
		for _, group := range setting.GetAutoGroups() {
			if group == id {
				return common.NewMessage("Cannot remove a group used by Auto routing")
			}
		}
		var tokens []Token
		if err := tx.Select("id", "auto_groups").Find(&tokens).Error; err != nil {
			return err
		}
		for _, token := range tokens {
			groups, err := token.GetAutoGroups()
			if err != nil {
				return err
			}
			if slices.Contains(groups, id) {
				return common.NewMessage("Cannot remove a group used by Auto routing")
			}
		}
		delete(profiles, id)
		delete(ratios, id)
		delete(usable, id)
		for key, value := range map[string]any{groupProfilesOption: profiles, "GroupRatio": ratios, "UserUsableGroups": usable} {
			raw, err := common.Marshal(value)
			if err != nil {
				return err
			}
			values[key] = string(raw)
		}
		var options []Option
		if err := tx.Where(commonKeyCol+" IN ?", []string{"TopupGroupRatio", "GroupGroupRatio", "group_ratio_setting.group_special_usable_group"}).Find(&options).Error; err != nil {
			return err
		}
		for _, option := range options {
			var data map[string]any
			if err := common.UnmarshalJsonStr(option.Value, &data); err != nil {
				return err
			}
			delete(data, id)
			for _, value := range data {
				if nested, ok := value.(map[string]any); ok {
					delete(nested, id)
					delete(nested, "+:"+id)
					delete(nested, "-:"+id)
				}
			}
			raw, err := common.Marshal(data)
			if err != nil {
				return err
			}
			values[option.Key] = string(raw)
		}
		// Deleted keys are not active bindings. Clear only their obsolete route,
		// leaving identity, balances, projects and consumption history intact.
		if err := tx.Unscoped().Model(&Token{}).Where("deleted_at IS NOT NULL AND "+commonGroupCol+" = ?", id).Update("group", "").Error; err != nil {
			return err
		}
		for key, value := range values {
			option := Option{Key: key}
			if err := tx.FirstOrCreate(&option, Option{Key: key}).Error; err != nil {
				return err
			}
			if err := tx.Model(&option).Update("value", value).Error; err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	for key, value := range values {
		if err := updateOptionMap(key, value); err != nil {
			return err
		}
	}
	return nil
}
