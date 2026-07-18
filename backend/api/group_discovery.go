package api

import (
	"errors"
	"io"
	"net/http"

	"github.com/bejix/upstream-ops/backend/discovery"
	"github.com/gin-gonic/gin"
)

func registerGroupDiscovery(g *gin.RouterGroup, d *Deps) {
	if d.GroupDiscovery == nil {
		return
	}
	gp := g.Group("/upstream-sync/group-discovery")
	gp.GET("/candidates", func(c *gin.Context) { listGroupDiscoveryCandidates(c, d) })
	gp.POST("/scan", func(c *gin.Context) { scanGroupDiscovery(c, d) })
	gp.POST("/candidates/:id/approve", func(c *gin.Context) { approveGroupDiscoveryCandidate(c, d) })
	gp.POST("/candidates/:id/reject", func(c *gin.Context) { rejectGroupDiscoveryCandidate(c, d) })
	gp.POST("/apply", func(c *gin.Context) { applyGroupDiscoveryCandidates(c, d) })
}

func listGroupDiscoveryCandidates(c *gin.Context, d *Deps) {
	list, err := d.GroupDiscovery.List()
	if err != nil {
		fail(c, http.StatusInternalServerError, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": list})
}

func scanGroupDiscovery(c *gin.Context, d *Deps) {
	var options discovery.ScanOptions
	if err := c.ShouldBindJSON(&options); err != nil && !errors.Is(err, io.EOF) {
		fail(c, http.StatusBadRequest, err)
		return
	}
	result, err := d.GroupDiscovery.Scan(c.Request.Context(), options)
	if err != nil {
		fail(c, http.StatusInternalServerError, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": result})
}

func approveGroupDiscoveryCandidate(c *gin.Context, d *Deps) {
	id, err := uintParam(c, "id")
	if err != nil {
		fail(c, http.StatusBadRequest, err)
		return
	}
	var in discovery.ApprovalInput
	if err := c.ShouldBindJSON(&in); err != nil {
		fail(c, http.StatusBadRequest, err)
		return
	}
	item, err := d.GroupDiscovery.Approve(c.Request.Context(), id, in)
	if err != nil {
		fail(c, http.StatusBadRequest, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": item})
}

func rejectGroupDiscoveryCandidate(c *gin.Context, d *Deps) {
	id, err := uintParam(c, "id")
	if err != nil {
		fail(c, http.StatusBadRequest, err)
		return
	}
	item, err := d.GroupDiscovery.Reject(id)
	if err != nil {
		fail(c, http.StatusBadRequest, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": item})
}

func applyGroupDiscoveryCandidates(c *gin.Context, d *Deps) {
	var in struct {
		CandidateIDs []uint `json:"candidate_ids"`
	}
	if err := c.ShouldBindJSON(&in); err != nil {
		fail(c, http.StatusBadRequest, err)
		return
	}
	result, err := d.GroupDiscovery.Apply(c.Request.Context(), in.CandidateIDs)
	if err != nil {
		fail(c, http.StatusBadRequest, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": result})
}
