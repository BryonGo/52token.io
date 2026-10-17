package model

import (
	"sort"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
)

// A project owns keys for reporting only. It never participates in routing,
// group selection, pricing, or wallet settlement.
type TokenProject struct {
	ID     int    `json:"id"`
	UserID int    `json:"-" gorm:"uniqueIndex:idx_token_project_owner_name,priority:1"`
	Name   string `json:"name" gorm:"type:varchar(80);uniqueIndex:idx_token_project_owner_name,priority:2"`
}

func ListTokenProjects(userID int) ([]TokenProject, error) {
	projects := []TokenProject{}
	err := DB.Where("user_id = ?", userID).Order("id").Find(&projects).Error
	return projects, err
}
func SaveTokenProject(userID, id int, name string) (*TokenProject, error) {
	name = strings.TrimSpace(name)
	if userID <= 0 || id < 0 || name == "" || len([]rune(name)) > 80 {
		return nil, common.NewMessage("Invalid project name")
	}
	project := TokenProject{ID: id, UserID: userID, Name: name}
	if id == 0 {
		return &project, DB.Create(&project).Error
	}
	result := DB.Model(&TokenProject{}).Where("id = ? AND user_id = ?", id, userID).Update("name", name)
	if result.Error != nil {
		return nil, result.Error
	}
	if result.RowsAffected == 0 {
		if err := ValidateTokenProject(userID, id); err != nil {
			return nil, err
		}
	}
	return &project, nil
}
func ValidateTokenProject(userID, projectID int) error {
	if projectID == 0 {
		return nil
	}
	var count int64
	if projectID < 0 {
		return common.NewMessage("Project is unavailable or unauthorized")
	}
	if err := DB.Model(&TokenProject{}).Where("id = ? AND user_id = ?", projectID, userID).Count(&count).Error; err != nil {
		return err
	}
	if count != 1 {
		return common.NewMessage("Project is unavailable or unauthorized")
	}
	return nil
}

// Project reports use current key assignments, including soft-deleted keys.
// The cumulative total survives log retention; daily/model breakdowns do not.
type ProjectUsageAmount struct {
	Name     string `json:"name"`
	Quota    int64  `json:"quota"`
	Requests int64  `json:"requests"`
}
type TokenProjectReport struct {
	TotalQuota  int64                `json:"total_quota"`
	PeriodQuota int64                `json:"period_quota"`
	Requests    int64                `json:"requests"`
	Timezone    string               `json:"timezone"`
	Daily       []ProjectUsageAmount `json:"daily"`
	Families    []ProjectUsageAmount `json:"families"`
	Models      []ProjectUsageAmount `json:"models"`
}

func GetTokenProjectUsage(userID int, projectID *int, days int) (*TokenProjectReport, error) {
	if days != 1 && days != 7 && days != 30 {
		return nil, common.NewMessage("Invalid usage period")
	}
	if projectID != nil {
		if err := ValidateTokenProject(userID, *projectID); err != nil {
			return nil, err
		}
	}
	query := DB.Unscoped().Model(&Token{}).Where("user_id = ?", userID)
	if projectID != nil {
		query = query.Where("project_id = ?", *projectID)
	}
	var tokens []struct {
		Id        int
		UsedQuota int64
	}
	if err := query.Select("id", "used_quota").Find(&tokens).Error; err != nil {
		return nil, err
	}
	report := &TokenProjectReport{Timezone: "Asia/Shanghai", Daily: []ProjectUsageAmount{}, Families: []ProjectUsageAmount{}, Models: []ProjectUsageAmount{}}
	zone := time.FixedZone("Asia/Shanghai", 8*3600)
	now := time.Now().In(zone)
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, zone)
	start := today.AddDate(0, 0, 1-days)
	daily := map[int64]int{}
	for i := range days {
		day := start.AddDate(0, 0, i)
		daily[day.Unix()] = i
		report.Daily = append(report.Daily, ProjectUsageAmount{Name: day.Format("2006-01-02")})
	}
	ids := make([]int, 0, len(tokens))
	for _, token := range tokens {
		ids = append(ids, token.Id)
		report.TotalQuota += token.UsedQuota
	}
	if len(ids) == 0 {
		return report, nil
	}
	models := map[string]*ProjectUsageAmount{}
	// Integer modulo works on all supported SQL engines, including separate log DBs.
	const dayExpr = "(created_at - ((created_at + 28800) % 86400))"
	for offset := 0; offset < len(ids); offset += 500 {
		var rows []struct {
			DayStart  int64
			ModelName string
			Quota     int64
			Requests  int64
		}
		err := LOG_DB.Model(&Log{}).Select(dayExpr+" AS day_start, model_name, SUM(quota) AS quota, COUNT(*) AS requests").
			Where("user_id = ? AND token_id IN ? AND type = ? AND created_at >= ? AND created_at <= ?", userID, ids[offset:min(offset+500, len(ids))], LogTypeConsume, start.Unix(), now.Unix()).
			Group(dayExpr + ", model_name").Scan(&rows).Error
		if err != nil {
			return nil, err
		}
		for _, row := range rows {
			report.PeriodQuota += row.Quota
			report.Requests += row.Requests
			if index, ok := daily[row.DayStart]; ok {
				report.Daily[index].Quota += row.Quota
				report.Daily[index].Requests += row.Requests
			}
			if models[row.ModelName] == nil {
				models[row.ModelName] = &ProjectUsageAmount{Name: row.ModelName}
			}
			models[row.ModelName].Quota += row.Quota
			models[row.ModelName].Requests += row.Requests
		}
	}
	var metadata []Model
	if err := DB.Select("id", "model_name", "vendor_id", "name_rule").Order("id").Find(&metadata).Error; err != nil {
		return nil, err
	}
	var vendors []Vendor
	if err := DB.Select("id", "name").Find(&vendors).Error; err != nil {
		return nil, err
	}
	vendorNames := map[int]string{}
	for _, vendor := range vendors {
		vendorNames[vendor.Id] = vendor.Name
	}
	names := make([]string, 0, len(models))
	for name := range models {
		names = append(names, name)
	}
	resolved := resolveModelMetadata(metadata, names)
	families := map[string]*ProjectUsageAmount{}
	for name, amount := range models {
		family := projectModelFamily(name)
		if family == "Unclassified" {
			if record := resolved[name]; record != nil && vendorNames[record.VendorID] != "" {
				family = vendorNames[record.VendorID]
			}
		}
		if families[family] == nil {
			families[family] = &ProjectUsageAmount{Name: family}
		}
		families[family].Quota += amount.Quota
		families[family].Requests += amount.Requests
		report.Models = append(report.Models, *amount)
	}
	for _, amount := range families {
		report.Families = append(report.Families, *amount)
	}
	for _, rows := range [][]ProjectUsageAmount{report.Families, report.Models} {
		sort.Slice(rows, func(i, j int) bool {
			if rows[i].Quota == rows[j].Quota {
				return rows[i].Name < rows[j].Name
			}
			return rows[i].Quota > rows[j].Quota
		})
	}
	return report, nil
}

func projectModelFamily(name string) string {
	name = strings.ToLower(name)
	// Families are reporting labels only; exact IDs still determine prices/routing.
	for _, family := range []struct{ prefix, label string }{
		{"seedream", "Seedream"}, {"seedance", "Seedance"}, {"deepseek", "DeepSeek"},
		{"claude", "Claude"}, {"gpt", "GPT"}, {"gemini", "Gemini"}, {"grok", "Grok"},
		{"qwen", "Qwen"}, {"glm", "GLM"}, {"minimax", "MiniMax"}, {"kimi", "Kimi"},
	} {
		for _, part := range strings.FieldsFunc(name, func(r rune) bool { return r == '/' || r == ':' || r == '-' }) {
			if strings.HasPrefix(part, family.prefix) {
				return family.label
			}
		}
	}
	return "Unclassified"
}
