package controller

import (
	"github.com/QuantumNous/new-api/pkg/channelcost"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/gin-gonic/gin"
)

func GetChannelCosts(c *gin.Context) {
	var channels []model.Channel
	if err := model.DB.Find(&channels).Error; err != nil {
		common.ApiError(c, err)
		return
	}
	rows := []gin.H{}
	for _, ch := range channels {
		cfg := ch.GetOtherSettings().CostBilling
		if cfg == nil || !cfg.Enabled {
			continue
		}
		snap := model.GetChannelCostSnapshot(ch.Id)
		prices := []gin.H{}
		for name := range strings.SplitSeq(ch.Models, ",") {
			name = strings.TrimSpace(name)
			if name == "" {
				continue
			}
			receipt, _, err := snap.Resolve(cfg, name, model.ChannelCostFingerprint(&ch), time.Now(), operation_setting.USDExchangeRate)
			row := gin.H{"model": name, "cost": receipt}
			if err != nil {
				row["error"] = err.Error()
			}
			prices = append(prices, row)
		}
		rows = append(rows, gin.H{"id": ch.Id, "name": ch.Name, "status": ch.Status, "config": cfg, "prices": prices, "source_name": func() string { s, _ := model.GetChannelPriceSource(cfg.Source); return s.Name }()})
	}
	common.ApiSuccess(c, rows)
}
func SyncChannelCosts(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		common.ApiError(c, err)
		return
	}
	snapshot, err := service.SyncChannelCost(c.Request.Context(), id)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	common.ApiSuccess(c, gin.H{"synced_at": snapshot.SyncedAt, "models": len(snapshot.Prices)})
}

func GetChannelPriceSources(c *gin.Context) {
	sources, version, err := model.GetChannelPriceSources()
	if err != nil {
		common.ApiError(c, err)
		return
	}
	rows := make([]channelcost.Source, 0, len(sources))
	for _, source := range sources {
		rows = append(rows, source)
	}
	slices.SortFunc(rows, func(a, b channelcost.Source) int { return strings.Compare(a.ID, b.ID) })
	common.ApiSuccess(c, gin.H{"sources": rows, "version": version})
}
func SaveChannelPriceSource(c *gin.Context) {
	var request struct {
		Source          channelcost.Source `json:"source"`
		ExpectedVersion string             `json:"expected_version"`
	}
	if err := c.ShouldBindJSON(&request); err != nil {
		common.ApiError(c, err)
		return
	}
	if err := model.SaveChannelPriceSource(request.Source, request.ExpectedVersion); err != nil {
		common.ApiError(c, err)
		return
	}
	go service.RefreshChannelCosts()
	common.ApiSuccess(c, nil)
}
func PreviewChannelPriceSource(c *gin.Context) {
	var request struct {
		Source channelcost.Source `json:"source"`
		Group  string             `json:"group"`
	}
	if err := c.ShouldBindJSON(&request); err != nil {
		common.ApiError(c, err)
		return
	}
	result, err := service.FetchPriceSource(c.Request.Context(), request.Source, request.Group)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	names := make([]string, 0, len(result.Prices))
	for name := range result.Prices {
		names = append(names, name)
	}
	slices.Sort(names)
	preview := map[string]channelcost.Price{}
	for _, name := range names[:min(len(names), 20)] {
		preview[name] = result.Prices[name]
	}
	diagnostics := map[string]string{}
	errorNames := make([]string, 0, len(result.Errors))
	for name := range result.Errors {
		errorNames = append(errorNames, name)
	}
	slices.Sort(errorNames)
	for _, name := range errorNames[:min(len(errorNames), 20)] {
		diagnostics[name] = result.Errors[name]
	}
	common.ApiSuccess(c, gin.H{"prices": preview, "errors": diagnostics, "models": len(names), "unsupported": len(result.Errors), "group_ratio": result.GroupRatio})
}
