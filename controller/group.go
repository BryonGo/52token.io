package controller

import (
	"github.com/QuantumNous/new-api/common"
	"net/http"

	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting"
	"github.com/QuantumNous/new-api/setting/ratio_setting"

	"github.com/gin-gonic/gin"
)

func GetGroups(c *gin.Context) {
	groupNames := make([]string, 0)
	for groupName := range ratio_setting.GetGroupRatioCopy() {
		groupNames = append(groupNames, groupName)
	}
	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": "",
		"data":    groupNames,
	})
}

func GetUserGroups(c *gin.Context) {
	usableGroups := make(map[string]map[string]any)
	profiles := model.GetGroupProfiles()
	userGroup := ""
	userId := c.GetInt("id")
	userGroup, _ = model.GetUserGroup(userId, false)
	userUsableGroups := service.GetUserUsableGroups(userGroup)
	for groupName, _ := range ratio_setting.GetGroupRatioCopy() {
		// UserUsableGroups contains the groups that the user can use
		if desc, ok := userUsableGroups[groupName]; ok {
			usableGroups[groupName] = map[string]any{
				"ratio": service.GetUserGroupRatio(userGroup, groupName),
				"desc":  desc,
				"name":  profiles[groupName].Name,
			}
		}
	}
	if _, ok := userUsableGroups["auto"]; ok {
		usableGroups["auto"] = map[string]any{
			"ratio": "auto",
			"desc":  setting.GetUsableGroupDescription("auto"),
		}
	}
	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": "",
		"data":    usableGroups,
	})
}

func GetManagedGroups(c *gin.Context) {
	catalog, err := model.GetGroupCatalog()
	if err != nil {
		common.ApiError(c, err)
		return
	}
	common.ApiSuccess(c, catalog)
}
func SaveManagedGroup(c *gin.Context) {
	var input model.GroupMutation
	if err := c.ShouldBindJSON(&input); err != nil {
		common.ApiError(c, err)
		return
	}
	id, err := model.SaveManagedGroup(input)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	common.ApiSuccess(c, gin.H{"id": id})
}

// Labels are visibility-scoped; identifiers remain the routing contract.
func GetGroupLabels(c *gin.Context) {
	profiles := model.GetGroupProfiles()
	allowed := map[string]string{}
	if c.GetInt("role") >= common.RoleAdminUser {
		for id := range ratio_setting.GetGroupRatioCopy() {
			allowed[id] = ""
		}
	} else {
		group := ""
		if id := c.GetInt("id"); id > 0 {
			group, _ = model.GetUserGroup(id, false)
		}
		allowed = service.GetUserUsableGroups(group)
	}
	result := map[string]model.GroupProfile{}
	for id := range allowed {
		p := profiles[id]
		if p.Name == "" {
			p.Name = id
		}
		result[id] = p
	}
	common.ApiSuccess(c, result)
}

func DeleteManagedGroup(c *gin.Context) {
	var input struct {
		ID      string `json:"id"`
		Version string `json:"version"`
	}
	if err := c.ShouldBindJSON(&input); err != nil {
		common.ApiError(c, err)
		return
	}
	if err := model.DeleteManagedGroup(input.ID, input.Version); err != nil {
		common.ApiError(c, err)
		return
	}
	common.ApiSuccess(c, nil)
}
