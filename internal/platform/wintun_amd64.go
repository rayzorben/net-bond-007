//go:build windows && amd64

package platform

import _ "embed"

//go:embed wintun/amd64/wintun.dll
var wintunDLL []byte
