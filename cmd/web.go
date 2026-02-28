package main

import (
	"embed"
	"io/fs"
)

//go:embed all:dashboard_dist
var dashboardEmbedFS embed.FS

// getDashboardFS returns the dashboard SPA filesystem rooted at the dist directory.
func getDashboardFS() (fs.FS, error) {
	return fs.Sub(dashboardEmbedFS, "dashboard_dist")
}
