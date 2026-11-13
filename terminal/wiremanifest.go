package terminal

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
)

// web/wire-compatibility.json is rendered from the TypeScript constants by
// web/src/test-helpers/wire-manifest.ts and ships in every npm and JSR release.
// A Go release gate reads it to learn the client half, which is pinned apart
// from this module.

// WireManifestSchemaVersion is the only manifest schema this decoder reads. It
// changes only with the manifest's layout, never with a wire revision; a test
// pins it to the TypeScript WIRE_MANIFEST_SCHEMA_VERSION.
const WireManifestSchemaVersion = 1

// ErrWireManifestSchema reports a manifest whose schemaVersion this decoder does
// not read: the consumer is behind the artifact and must be upgraded, which is
// never a wire incompatibility.
var ErrWireManifestSchema = errors.New("terminal: unsupported wire-compatibility manifest schema")

// WireManifest is the decoded manifest, flattened out of the file's
// `wireCompatibility` object.
type WireManifest struct {
	// SchemaVersion is the layout revision, already validated against
	// WireManifestSchemaVersion by the time a caller sees it.
	SchemaVersion int
	// ProtocolVersion is the client's current wire revision.
	ProtocolVersion int
	// MinimumServerProtocolVersion is the client's directional floor: the oldest
	// server revision it will still decode.
	MinimumServerProtocolVersion int
	// IncompatibleCloseCode is the close code the runtime handshake uses to
	// refuse a declared-incompatible peer. Informational for a build gate.
	IncompatibleCloseCode int
}

// ReadWireManifest reads and validates the `wire-compatibility.json` at path,
// the root of a vendored engine npm package. No error is a compatibility
// verdict: an unreadable, malformed, unknown-schema
// or non-positive manifest is a broken gate, never a reason to bump a pin.
func ReadWireManifest(path string) (WireManifest, error) {
	data, err := os.ReadFile(path) // #nosec G304 -- the path is the caller's own build argument
	if err != nil {
		return WireManifest{}, fmt.Errorf("terminal: read wire-compatibility manifest: %w", err)
	}
	return DecodeWireManifest(data)
}

// DecodeWireManifest is ReadWireManifest over bytes already in hand, for a caller
// that obtained the artifact some other way (an embedded copy, a registry fetch).
func DecodeWireManifest(data []byte) (WireManifest, error) {
	// Only the published fields are declared; an unknown field is tolerated so an
	// additive manifest revision does not have to break every consumer at once,
	// which is what the schemaVersion gate is for.
	var raw struct {
		WireCompatibility struct {
			ProtocolVersion              int `json:"protocolVersion"`
			MinimumServerProtocolVersion int `json:"minimumServerProtocolVersion"`
			IncompatibleCloseCode        int `json:"incompatibleCloseCode"`
		} `json:"wireCompatibility"`
		SchemaVersion int `json:"schemaVersion"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return WireManifest{}, fmt.Errorf("terminal: parse wire-compatibility manifest: %w", err)
	}
	if raw.SchemaVersion != WireManifestSchemaVersion {
		return WireManifest{}, fmt.Errorf("%w: manifest declares %d, this build reads %d",
			ErrWireManifestSchema, raw.SchemaVersion, WireManifestSchemaVersion)
	}
	m := WireManifest{
		SchemaVersion:                raw.SchemaVersion,
		ProtocolVersion:              raw.WireCompatibility.ProtocolVersion,
		MinimumServerProtocolVersion: raw.WireCompatibility.MinimumServerProtocolVersion,
		IncompatibleCloseCode:        raw.WireCompatibility.IncompatibleCloseCode,
	}
	if m.ProtocolVersion <= 0 || m.MinimumServerProtocolVersion <= 0 {
		return WireManifest{}, fmt.Errorf(
			"terminal: wire-compatibility manifest carries no usable revisions (protocolVersion %d, minimumServerProtocolVersion %d)",
			m.ProtocolVersion, m.MinimumServerProtocolVersion,
		)
	}
	return m, nil
}
