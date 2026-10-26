package plugin

import (
	"bytes"
	"encoding/json"
	"strings"

	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
	libplugin "github.com/Mininglamp-OSS/octo-plugin-lib/plugin"
)

// CanonicalDocuments carries contract-canonical plugin documents and their
// hashes. Canonicalization, schema validation, per-type file rules, and the
// plugin_hash formula all come from octo-plugin-lib; the marketplace adds only
// host-owned rules on top (storage-key scoping, tags==labels).
type CanonicalDocuments struct {
	Manifest json.RawMessage
	Package  json.RawMessage
	Tags     json.RawMessage
	// AttachmentKeys is the host-private storage sidecar (path -> managed object
	// key) split out of Package so the package stays a valid 2.0 contract
	// document. nil when the package has no storage attachments.
	AttachmentKeys json.RawMessage
	ManifestHash   string
	PluginHash     string
}

// CanonicalizeManifest validates a manifest through the octo-plugin-lib
// contract and returns its canonical bytes plus the canonical tags derived
// from the host tags==labels invariant. Exposed so out-of-band writers (the
// legacy backfill, import) share one validator instead of drifting.
func CanonicalizeManifest(name string, typ model.PluginType, tags, manifest json.RawMessage) (json.RawMessage, json.RawMessage, error) {
	trimmed := strings.TrimSpace(name)
	if !validName(trimmed) || !validPluginType(typ) {
		return nil, nil, ErrInvalidRequest
	}
	if len(manifest) == 0 || len(manifest) > maxJSONBytes {
		return nil, nil, ErrInvalidRequest
	}
	decoded, err := libplugin.DecodeManifest(manifest)
	if err != nil {
		return nil, nil, ErrInvalidRequest
	}
	// The outer row fields are authoritative; the manifest must agree with
	// them exactly (same rule libplugin.ValidatePlugin applies to full rows).
	if decoded.PluginName != trimmed || string(decoded.PluginType) != string(typ) {
		return nil, nil, ErrInvalidRequest
	}
	canonical, err := libplugin.CanonicalJSON(manifest)
	if err != nil {
		return nil, nil, ErrInvalidRequest
	}
	// Host invariant: the plugins.tags_json column mirrors manifest labels.
	normalizedTags, err := normalizeTags(tags)
	if err != nil {
		return nil, nil, err
	}
	labels := decoded.Labels
	if labels == nil {
		labels = []string{}
	}
	canonicalLabels, err := canonicalJSONValue(labels)
	if err != nil || !bytes.Equal(canonicalLabels, normalizedTags) {
		return nil, nil, ErrInvalidRequest
	}
	return canonical, normalizedTags, nil
}

// CanonicalizeDocuments validates a manifest/package pair through the
// octo-plugin-lib contract (canonical form, per-type file rules, connector
// descriptor, hash), then applies the host-only rules: approved
// storage-key scoping, and size caps. plugin_hash uses the lib formula
// sha256(canonicalManifest || canonicalPackage); manifest_hash stays a
// host-only column over the canonical manifest.
//
// This is the CALLER write path: a legacy skill/ref.json pointer may reference
// only this Space's own managed prefix, so a caller cannot persist a legacy-root
// or cross-Space pointer through upsert.
func CanonicalizeDocuments(name string, typ model.PluginType, tags, manifest, pkg json.RawMessage, spaceID string) (*CanonicalDocuments, error) {
	return canonicalizeDocuments(name, typ, tags, manifest, pkg, spaceID, false)
}

// CanonicalizeMigratedDocuments is the trusted out-of-band variant used only by
// the deterministic backfill, which legitimately embeds legacy-root skill/ref.json
// pointers (skills/, experts/, squads/) it migrated from the source catalogs.
// Because only this variant admits those pointers, every legacy-root pointer in
// the plugin catalog is provably backfill-written — never planted by a caller —
// which is what lets the expand-skills migration trust them.
func CanonicalizeMigratedDocuments(name string, typ model.PluginType, tags, manifest, pkg json.RawMessage, spaceID string) (*CanonicalDocuments, error) {
	return canonicalizeDocuments(name, typ, tags, manifest, pkg, spaceID, true)
}

func canonicalizeDocuments(name string, typ model.PluginType, tags, manifest, pkg json.RawMessage, spaceID string, trustLegacyRefs bool) (*CanonicalDocuments, error) {
	m, t, err := CanonicalizeManifest(name, typ, tags, manifest)
	if err != nil {
		return nil, err
	}
	if len(pkg) == 0 || len(pkg) > maxJSONBytes {
		return nil, ErrInvalidRequest
	}
	// The 2.0 contract package forbids a host `storage_uri` inside an attachment
	// (DecodePackage rejects unknown fields), so split each storage attachment's
	// object key out into the host-private sidecar map and strip it from the
	// package BEFORE canonicalization/validation/hashing. The sidecar is never
	// part of plugin_hash; the stored package stays a valid 2.0 document.
	stripped, keys, err := splitStorageKeys(pkg, spaceID)
	if err != nil {
		return nil, ErrInvalidRequest
	}
	p, err := libplugin.CanonicalJSON(stripped)
	if err != nil {
		return nil, ErrInvalidRequest
	}
	if _, err := libplugin.DecodePackage(libplugin.Type(typ), p); err != nil {
		return nil, ErrInvalidRequest
	}
	// Host rule (caller path only): a legacy skill/ref.json pointer is a raw
	// attachment whose JSON body carries object keys the lib never inspects. Scope
	// those keys to this Space's managed prefix so a caller cannot persist a
	// forged legacy-root/cross-Space pointer for the expand-skills migration to
	// later dereference with service credentials. The trusted backfill bypasses
	// this gate (trustLegacyRefs) because it writes those pointers deliberately.
	if !trustLegacyRefs && !skillRefKeysScoped(p, spaceID) {
		return nil, ErrInvalidRequest
	}
	hash, err := libplugin.ComputePluginHash(m, p)
	if err != nil {
		return nil, ErrInvalidRequest
	}
	return &CanonicalDocuments{
		Manifest:       m,
		Package:        p,
		Tags:           t,
		AttachmentKeys: keys,
		ManifestHash:   hashJSON(m),
		PluginHash:     hash,
	}, nil
}

// splitStorageKeys separates the host-private object keys from a package
// document. For every storage attachment it validates the key against this
// Space's managed prefix, records path -> key in the returned sidecar map, and
// removes the `storage_uri` field so the returned package is a 2.0-legal
// contract document. A package with no storage attachments returns (pkg, nil)
// and the row's attachment_keys_json stays NULL.
func splitStorageKeys(pkg json.RawMessage, spaceID string) (json.RawMessage, json.RawMessage, error) {
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(pkg, &doc); err != nil {
		return nil, nil, err
	}
	rawAttachments, ok := doc["attachments"]
	if !ok {
		return pkg, nil, nil
	}
	var attachments []map[string]json.RawMessage
	if err := json.Unmarshal(rawAttachments, &attachments); err != nil {
		return nil, nil, err
	}
	keys := map[string]string{}
	changed := false
	for _, attachment := range attachments {
		var contentType string
		if raw, ok := attachment["content_type"]; ok {
			_ = json.Unmarshal(raw, &contentType)
		}
		if contentType != "storage" {
			continue
		}
		var path, key string
		_ = json.Unmarshal(attachment["path"], &path)
		_ = json.Unmarshal(attachment["storage_uri"], &key)
		if !safeObjectSegment.MatchString(spaceID) || !validReferencedObjectKey(key, spaceID) {
			return nil, nil, ErrInvalidRequest
		}
		keys[path] = key
		delete(attachment, "storage_uri")
		changed = true
	}
	if !changed {
		return pkg, nil, nil
	}
	reencoded, err := json.Marshal(attachments)
	if err != nil {
		return nil, nil, err
	}
	doc["attachments"] = reencoded
	strippedPkg, err := json.Marshal(doc)
	if err != nil {
		return nil, nil, err
	}
	keysJSON, err := json.Marshal(keys)
	if err != nil {
		return nil, nil, err
	}
	return strippedPkg, keysJSON, nil
}

// storageContentHashes maps each storage attachment's path to its content_hash
// in a package document, used to bind a re-injected key to unchanged content.
func storageContentHashes(pkg json.RawMessage) map[string]string {
	var doc struct {
		Attachments []struct {
			Path        string `json:"path"`
			ContentType string `json:"content_type"`
			ContentHash string `json:"content_hash"`
		} `json:"attachments"`
	}
	if json.Unmarshal(pkg, &doc) != nil {
		return nil
	}
	out := map[string]string{}
	for _, a := range doc.Attachments {
		if a.ContentType == "storage" {
			out[a.Path] = a.ContentHash
		}
	}
	return out
}

// reinjectUpdateStorageKeys re-embeds storage object keys into an incoming update
// package for storage attachments that arrive without an inline storage_uri —
// the shape a client gets from GET, since the 2.0 read path strips the key into
// the host sidecar and never returns it. Without this, a fetch-edit-save
// round-trip on a storage-backed plugin is rejected by splitStorageKeys. A key is
// re-injected only when the incoming attachment's path AND content_hash match the
// stored row's attachment (so a client cannot bind a stale key to changed
// content) and the old sidecar holds a key for that path. A genuinely new storage
// attachment (different content) is left keyless and correctly rejected — clients
// cannot mint storage content through a raw upsert.
func reinjectUpdateStorageKeys(reqPkg, oldPkg, oldKeys json.RawMessage) json.RawMessage {
	keys := attachmentKeyMap(oldKeys)
	if len(keys) == 0 {
		return reqPkg
	}
	oldHashes := storageContentHashes(oldPkg)
	var doc map[string]json.RawMessage
	if json.Unmarshal(reqPkg, &doc) != nil {
		return reqPkg
	}
	rawAttachments, ok := doc["attachments"]
	if !ok {
		return reqPkg
	}
	var attachments []map[string]json.RawMessage
	if json.Unmarshal(rawAttachments, &attachments) != nil {
		return reqPkg
	}
	changed := false
	for _, attachment := range attachments {
		var contentType string
		if raw, ok := attachment["content_type"]; ok {
			_ = json.Unmarshal(raw, &contentType)
		}
		if contentType != "storage" {
			continue
		}
		if _, has := attachment["storage_uri"]; has {
			continue
		}
		var path, hash string
		_ = json.Unmarshal(attachment["path"], &path)
		_ = json.Unmarshal(attachment["content_hash"], &hash)
		key, ok := keys[path]
		if !ok || key == "" {
			continue
		}
		// Bind only to unchanged content: the incoming content_hash must match the
		// stored row's attachment at the same path.
		if prev, ok := oldHashes[path]; !ok || prev == "" || prev != hash {
			continue
		}
		encoded, err := json.Marshal(key)
		if err != nil {
			continue
		}
		attachment["storage_uri"] = encoded
		changed = true
	}
	if !changed {
		return reqPkg
	}
	reencoded, err := json.Marshal(attachments)
	if err != nil {
		return reqPkg
	}
	doc["attachments"] = reencoded
	merged, err := json.Marshal(doc)
	if err != nil {
		return reqPkg
	}
	return merged
}

// skillRefKeysScoped reports whether a package's legacy skill/ref.json pointer
// (if any) references only this Space's own managed prefix. A package without a
// skill/ref.json passes trivially. Every object key the pointer can resolve to
// (object_key, zip_object_key, file_url) must clear trustedArtifactKey; a
// legacy-root or cross-Space key fails closed. This is the provenance gate on
// the caller write path — see canonicalizeDocuments.
func skillRefKeysScoped(pkg json.RawMessage, spaceID string) bool {
	raw, ok := rawAttachmentContent(pkg, "skill/ref.json")
	if !ok {
		return true
	}
	var ref skillRefDocument
	if json.Unmarshal([]byte(raw), &ref) != nil {
		return false
	}
	sp := &spaceID
	for _, key := range []string{ref.ObjectKey, ref.ZipObjectKey, ref.FileURL} {
		if key == "" {
			continue
		}
		if !trustedArtifactKey(key, sp) {
			return false
		}
	}
	return true
}

// rawAttachmentContent returns the raw_content of one inline package
// attachment; storage attachments and missing paths report false.
func rawAttachmentContent(pkg json.RawMessage, path string) (string, bool) {
	var doc struct {
		Attachments []struct {
			Path        string `json:"path"`
			ContentType string `json:"content_type"`
			RawContent  string `json:"raw_content"`
		} `json:"attachments"`
	}
	if json.Unmarshal(pkg, &doc) != nil {
		return "", false
	}
	for _, attachment := range doc.Attachments {
		if attachment.Path == path && attachment.ContentType == "raw" {
			return attachment.RawContent, true
		}
	}
	return "", false
}

// attachmentKeyMap decodes the host-private storage sidecar (attachment path ->
// managed object key) persisted in the row's attachment_keys_json column. A nil
// or malformed value yields an empty map so lookups miss cleanly.
func attachmentKeyMap(raw json.RawMessage) map[string]string {
	if len(raw) == 0 {
		return nil
	}
	var m map[string]string
	if json.Unmarshal(raw, &m) != nil {
		return nil
	}
	return m
}

// storageAttachmentKey resolves the managed object key for a storage attachment
// path. It prefers the host sidecar (attachment_keys_json); if the row has not
// yet been migrated to the sidecar (NULL/missing entry) it falls back to the
// inline storage_uri the pre-2.0 package still carries, so a storage-backed row
// stays readable during the window between deploying this code and running the
// backfill. The caller must still scope-check the returned key.
func storageAttachmentKey(p *model.Plugin, path string) (string, bool) {
	if key, ok := attachmentKeyMap(p.AttachmentKeys)[path]; ok && key != "" {
		return key, true
	}
	return inlineStorageURI(p.Package, path)
}

// inlineStorageURI reads the pre-2.0 inline storage_uri of one storage
// attachment from a package document. Migrated packages no longer carry it and
// report false; it exists only as the sidecar fallback for un-migrated rows.
func inlineStorageURI(pkg json.RawMessage, path string) (string, bool) {
	var doc struct {
		Attachments []struct {
			Path        string `json:"path"`
			ContentType string `json:"content_type"`
			StorageURI  string `json:"storage_uri"`
		} `json:"attachments"`
	}
	if json.Unmarshal(pkg, &doc) != nil {
		return "", false
	}
	for _, attachment := range doc.Attachments {
		if attachment.Path == path && attachment.ContentType == "storage" && attachment.StorageURI != "" {
			return attachment.StorageURI, true
		}
	}
	return "", false
}

// ConnectorToolCount counts the entries of the canonical connector/tools.json
// raw attachment. Tools metadata is optional display data, so a missing or
// malformed attachment counts as zero instead of failing the write. Exported
// so the backfill enrichment recomputes counts through the same rule.
func ConnectorToolCount(pkg json.RawMessage) int { return model.ConnectorToolCount(pkg) }
