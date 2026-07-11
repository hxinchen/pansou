package web

import "embed"

// Assets contains the dependency-free management SPA distributed with PanSou.
//
//go:embed index.html assets/*
var Assets embed.FS
