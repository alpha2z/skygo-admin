package app

import (
	"context"
	"encoding/json"
	"errors"
	assets "github.com/alpha2z/skygo-admin/admin-web"
	"github.com/alpha2z/skygo-admin/plugin"
	"github.com/gin-gonic/gin"
	"net/http"
	"strings"
	"time"
)

type PluginDataPanelConfig struct {
	ID         string `json:"id"`
	Title      string `json:"title"`
	Permission string `json:"permission"`
}

func installDataPanels(x *Extension, p PluginProviderConfig, files map[string][]byte) error {
	if len(p.DataPanels) == 0 {
		return nil
	}
	for _, r := range p.Routes {
		if r.Path == "/data" || strings.HasPrefix(r.Path, "/data/") {
			return errors.New("data routes are reserved")
		}
	}
	script, err := assets.FS.ReadFile("data-panels.js")
	if err != nil {
		return err
	}
	if _, ok := files["core-data.js"]; ok {
		return errors.New("reserved core data asset")
	}
	files["core-data.js"] = script
	seen := map[string]bool{}
	for _, panel := range p.DataPanels {
		if !extensionID.MatchString(panel.ID) || seen[panel.ID] || panel.Title == "" || panel.Permission != "ops.read" {
			return errors.New("invalid data panel configuration")
		}
		seen[panel.ID] = true
		x.Pages = append(x.Pages, ExtensionPage{ID: panel.ID, Title: panel.Title, Permission: panel.Permission, Script: "core-data.js"})
	}
	handler := func(c *gin.Context) {
		ctx, cancel := context.WithTimeout(c.Request.Context(), 5*time.Second)
		defer cancel()
		actor := plugin.Actor{ID: uint32(c.GetUint("admin_id")), Role: c.GetString("admin_role"), Permission: "ops.read"}
		call := func(path string, q map[string][]string) (plugin.ResourceResponse, error) {
			var out plugin.ResourceResponse
			err := p.Endpoint.Call(ctx, plugin.Request{Operation: "resource", Resource: &plugin.ResourceRequest{Actor: actor, Method: http.MethodGet, Path: path, Query: q}}, &out)
			return out, err
		}
		response, err := call("/data/catalog", nil)
		var catalog plugin.DataCatalog
		if err != nil || response.Status != 200 || len(response.Body) > 256<<10 || plugin.DecodeData(response.Body, &catalog) != nil || catalog.Validate() != nil {
			c.JSON(503, gin.H{"error": "data catalog unavailable"})
			return
		}
		permitted := map[string]bool{}
		panels := []plugin.DataPanel{}
		for _, panel := range catalog.Panels {
			if seen[panel.ID] {
				panels = append(panels, panel)
				for _, w := range panel.Widgets {
					permitted[w.Dataset] = true
				}
			}
		}
		catalog.Panels = panels
		sets := []plugin.DataSet{}
		for _, d := range catalog.Datasets {
			if permitted[d.ID] {
				sets = append(sets, d)
			}
		}
		catalog.Datasets = sets
		if c.Param("dataset") == "" {
			if len(c.Request.URL.Query()) > 0 {
				c.JSON(400, gin.H{"error": "unexpected catalog query"})
				return
			}
			c.JSON(200, catalog)
			return
		}
		d, ok := catalog.Dataset(c.Param("dataset"))
		if !ok {
			c.JSON(404, gin.H{"error": "dataset not available"})
			return
		}
		mode := c.Param("mode")
		if err := plugin.ValidateDataQuery(d, mode, c.Request.URL.Query()); err != nil {
			c.JSON(400, gin.H{"error": err.Error()})
			return
		}
		response, err = call("/data/datasets/"+d.ID+"/"+mode, c.Request.URL.Query())
		if err != nil {
			c.JSON(503, gin.H{"error": "data provider unavailable"})
			return
		}
		if response.Status != 200 {
			status := response.Status
			if status != 400 && status != 404 && status != 503 {
				status = 502
			}
			c.JSON(status, gin.H{"error": "data query unavailable"})
			return
		}
		var result plugin.DataResult
		if plugin.DecodeData(response.Body, &result) != nil || result.Validate(d, mode) != nil {
			c.JSON(502, gin.H{"error": "invalid data result"})
			return
		}
		if result.SampledAt != nil && (time.Since(*result.SampledAt) > 60*time.Second || result.SampledAt.After(time.Now().Add(5*time.Second))) && mode != "series" && result.State == "ok" {
			result.State = "stale"
		}
		raw, _ := json.Marshal(result)
		if len(raw) > 256<<10 {
			c.JSON(502, gin.H{"error": "data response exceeds limit"})
			return
		}
		c.Data(200, "application/json", raw)
	}
	x.Routes = append(x.Routes, ExtensionRoute{Method: "GET", Path: "/data/catalog", Permission: "ops.read", Handler: handler}, ExtensionRoute{Method: "GET", Path: "/data/datasets/:dataset/:mode", Permission: "ops.read", Handler: handler})
	return nil
}
