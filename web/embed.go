package web

import (
	"embed"
	"io/fs"
)

//go:embed all:dist
var distFS embed.FS

// Assets returns the embedded SPA bundle rooted at the dist directory.
// Note: `go build` and `go vet` fail if web/dist/ contains no files, because
// the all:dist pattern requires at least one match. Run `npm run build` in
// web/ before any go command on a fresh checkout.
func Assets() fs.FS {
	sub, err := fs.Sub(distFS, "dist")
	if err != nil {
		return distFS
	}
	return sub
}
