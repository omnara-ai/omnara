//go:build requirespa

package web

import _ "embed"

//go:embed dist/index.html
var spaBundle []byte
