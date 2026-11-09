package gallery

import _ "embed"

// The static heif-dec of the release build (see deploy/heif-dec); the file
// in the repository is empty.
//
//go:embed heifdec/linux_amd64.gz
var bundledHEIFGz []byte
