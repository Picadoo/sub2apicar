package modelpricing

import _ "embed"

// FallbackJSON keeps standalone release binaries usable when the remote
// pricing source and the on-disk cache are both unavailable.
//
//go:embed model_prices_and_context_window.json
var FallbackJSON []byte
