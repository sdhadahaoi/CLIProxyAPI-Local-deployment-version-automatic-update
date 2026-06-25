package management

import (
	"fmt"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/usagedashboard"
)

// GetUsageDashboard returns persisted token usage summaries and recent records.
func (h *Handler) GetUsageDashboard(c *gin.Context) {
	summary, err := usagedashboard.Summarize(parseUsageDashboardQuery(c))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, summary)
}

// GetUsageDashboardPrices returns the custom and simulated price tables.
func (h *Handler) GetUsageDashboardPrices(c *gin.Context) {
	c.JSON(http.StatusOK, usagedashboard.Prices())
}

// PutUsageDashboardPrices replaces the custom and simulated price tables.
func (h *Handler) PutUsageDashboardPrices(c *gin.Context) {
	var book usagedashboard.PriceBook
	if errBind := c.ShouldBindJSON(&book); errBind != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid body", "message": errBind.Error()})
		return
	}
	saved, errSave := usagedashboard.SavePrices(book)
	if errSave != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": errSave.Error()})
		return
	}
	c.JSON(http.StatusOK, saved)
}

// GetUsageDashboardFiles lists downloadable persistent usage dashboard files.
func (h *Handler) GetUsageDashboardFiles(c *gin.Context) {
	files, err := usagedashboard.ListFiles()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, files)
}

// DownloadUsageDashboardFile downloads one persisted dashboard data file.
func (h *Handler) DownloadUsageDashboardFile(c *gin.Context) {
	name := strings.TrimSpace(c.Param("name"))
	file, contentType, errOpen := usagedashboard.OpenFile(name)
	if errOpen != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": errOpen.Error()})
		return
	}
	defer func() {
		_ = file.Close()
	}()
	info, errStat := file.Stat()
	if errStat != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": errStat.Error()})
		return
	}
	c.Header("Content-Type", contentType)
	c.Header("Content-Disposition", fmt.Sprintf("attachment; filename=%q", filepath.Base(name)))
	c.DataFromReader(http.StatusOK, info.Size(), contentType, file, nil)
}

// UploadUsageDashboardFile merges a client-side backup file into the persistent dashboard store.
func (h *Handler) UploadUsageDashboardFile(c *gin.Context) {
	name := strings.TrimSpace(c.Param("name"))
	result, errMerge := usagedashboard.MergeUploadedFile(name, c.Request.Body)
	if errMerge != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": errMerge.Error()})
		return
	}
	c.JSON(http.StatusOK, result)
}

func parseUsageDashboardQuery(c *gin.Context) usagedashboard.Query {
	query := usagedashboard.Query{
		Model:    strings.TrimSpace(c.Query("model")),
		Provider: strings.TrimSpace(c.Query("provider")),
	}
	if limit, errLimit := strconv.Atoi(strings.TrimSpace(c.Query("limit"))); errLimit == nil {
		query.Limit = limit
	}
	query.From = parseUsageDashboardTime(c.Query("from"))
	query.To = parseUsageDashboardTime(c.Query("to"))
	return query
}

func parseUsageDashboardTime(value string) time.Time {
	value = strings.TrimSpace(value)
	if value == "" {
		return time.Time{}
	}
	if t, errParse := time.Parse(time.RFC3339, value); errParse == nil {
		return t
	}
	if t, errParse := time.Parse("2006-01-02", value); errParse == nil {
		return t
	}
	return time.Time{}
}
