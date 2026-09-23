// Package web embeds the built SPA. dist/.gitkeep keeps the embed valid
// before the frontend has been built.
package web

import "embed"

//go:embed all:dist
var Dist embed.FS
