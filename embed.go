// Package ductile is the repository-root package. Its sole job is to embed
// shared assets — the JSON Schemas under schemas/ — into the binary so they
// ship inside the executable and share its integrity. An on-disk schema lives
// in the worker's filesystem trust domain and can be rewritten by a popped
// plugin, so it cannot be a security/authoring control; the embedded copy can.
// See ADR "Ductile — PrivSec and Secrets" §11.
package ductile

import "embed"

// SchemaFS holds the embedded JSON Schemas (schemas/*.json). The files remain at
// schemas/ in the repo so editors and CI still resolve them; this embeds the
// same bytes into the binary.
//
//go:embed schemas/*.json
var SchemaFS embed.FS
