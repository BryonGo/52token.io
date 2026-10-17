package controller

import (
	"net/http"
	"time"

	"github.com/QuantumNous/new-api/i18n"
	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
)

// These endpoints intentionally publish catalog/price metadata without a key.
// Model inference and account/key administration retain their existing auth.
func GetPublicModelCatalog(c *gin.Context) {
	rows, err := model.GetPublicModelCatalog(c.Query("group_id"))
	if err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": gin.H{"code": "catalog_unavailable", "message": i18n.T(c, i18n.MsgCatalogTemporarilyUnavailable)}})
		return
	}
	c.Header("Cache-Control", "public, max-age=15")
	c.JSON(http.StatusOK, gin.H{"object": "list", "data": rows})
}

func GetPublicModelGroups(c *gin.Context) {
	groups, err := model.GetPublicModelGroups()
	if err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": gin.H{"code": "catalog_unavailable", "message": i18n.T(c, i18n.MsgCatalogTemporarilyUnavailable)}})
		return
	}
	c.Header("Cache-Control", "public, max-age=15")
	c.JSON(http.StatusOK, gin.H{"object": "list", "data": groups})
}
func GetPublicModelPrice(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	price, err := model.PublicModelPriceByID(c.Param("pricing_id"), time.Now())
	if err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": gin.H{"code": "catalog_unavailable", "message": i18n.T(c, i18n.MsgCatalogTemporarilyUnavailable)}})
		return
	}
	if price == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": gin.H{"code": "pricing_id_not_found", "message": i18n.T(c, i18n.MsgCatalogPricingIDNotFound)}})
		return
	}
	c.JSON(http.StatusOK, gin.H{"object": "model_price", "data": price})
}
