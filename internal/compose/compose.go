// Package compose embeds the docker-compose template and default service
// configuration files, and exposes a Render function that produces the final
// docker-compose.yml bytes from typed parameters.
//
// Design (AD-3): a single template with the Mongo image as the only
// substitution point replaces the two 700-line install scripts. Credentials
// are never rendered into the compose file — they stay in .env and appear as
// ${VAR} compose-interpolation references.
package compose

import (
	"bytes"
	_ "embed"
	"fmt"
	"text/template"

	"github.com/PazNicolas/crenein-agent-tui/internal/cnerr"
)

//go:embed docker-compose.yml.tmpl
var composeTmpl string

//go:embed vsftpd.conf
var DefaultVsftpdConf []byte

//go:embed tftpd-hpa
var DefaultTftpdHpa []byte

// ComposeParams holds the parameters used to render docker-compose.yml.
// Only MongoImage varies between installations; everything else is fixed by
// the bash reference scripts.
type ComposeParams struct {
	// MongoImage is the full image reference for the mongodb service.
	// Use detect.MongoImage(avx) to derive it from AVX detection, or supply an
	// explicit override.
	//
	// Valid values (from the scripts):
	//   "mongodb/mongodb-community-server:7.0-ubuntu2204"  (AVX present)
	//   "mongo:4.4"                                        (no AVX)
	MongoImage string
}

// Render executes the embedded template with params and returns the rendered
// docker-compose.yml bytes. Returns a *cnerr.Error on template parse or
// execution failure (should never happen unless the embedded template is
// malformed).
func Render(params ComposeParams) ([]byte, error) {
	if params.MongoImage == "" {
		return nil, cnerr.New("compose.Render",
			"supply a MongoImage (e.g. detect.MongoImage(avx))")
	}
	tmpl, err := template.New("docker-compose").Parse(composeTmpl)
	if err != nil {
		return nil, cnerr.Wrap("compose.Render", err,
			"the embedded docker-compose template is malformed; this is a bug")
	}
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, params); err != nil {
		return nil, cnerr.Wrap("compose.Render", err,
			fmt.Sprintf("template execution failed with MongoImage=%q", params.MongoImage))
	}
	return buf.Bytes(), nil
}
