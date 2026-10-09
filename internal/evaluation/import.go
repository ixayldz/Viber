package evaluation

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"

	c "github.com/ixayldz/Viber/internal/contracts"
	"github.com/ixayldz/Viber/internal/fileguard"
)

type ImportedInputs struct {
	Protocol              Protocol
	Observations          []Observation
	ProtocolFileDigest    string
	ObservationFileDigest string
}

func regularInput(path string, limit int64) ([]byte, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(filepath.Dir(absolute))
	if err != nil {
		return nil, err
	}
	defer root.Close()
	return fileguard.ReadRegular(root, filepath.Base(absolute), limit)
}
func ReadImportedInputs(protocolFile, observationFile string) (ImportedInputs, error) {
	var input ImportedInputs
	raw, err := regularInput(protocolFile, 8<<20)
	if err != nil {
		return input, err
	}
	input.ProtocolFileDigest = c.HashBytes(raw)
	if err = c.DecodeStrict(raw, &input.Protocol); err != nil {
		return input, err
	}
	if err = input.Protocol.Validate(); err != nil {
		return input, err
	}
	raw, err = regularInput(observationFile, 32<<20)
	if err != nil {
		return input, err
	}
	input.ObservationFileDigest = c.HashBytes(raw)
	lines := bytes.Split(raw, []byte{'\n'})
	if len(lines) > 0 && len(bytes.TrimSpace(lines[len(lines)-1])) == 0 {
		lines = lines[:len(lines)-1]
	}
	if len(raw) == 0 {
		lines = nil
	}
	if len(lines) > 10000 {
		return input, c.Fail(c.InvalidArgument, "measurement JSONL exceeds assignment bound")
	}
	input.Observations = []Observation{}
	for _, line := range lines {
		if len(line) > 16<<10 || len(bytes.TrimSpace(line)) == 0 {
			return input, c.Fail(c.InvalidArgument, "measurement JSONL has an empty or oversized row")
		}
		var row Observation
		if err = c.DecodeStrict(bytes.TrimSpace(line), &row); err != nil {
			return input, err
		}
		input.Observations = append(input.Observations, row)
	}
	return input, nil
}

type OutputFile struct {
	Path   string `json:"path"`
	Digest string `json:"digest"`
	Size   int64  `json:"size"`
}
type OutputManifest struct {
	SchemaVersion               int          `json:"schema_version"`
	ProtocolDigest              string       `json:"preregistered_protocol_digest"`
	SourceProtocolFileDigest    string       `json:"source_protocol_file_digest"`
	SourceObservationFileDigest string       `json:"source_observation_file_digest"`
	CanonicalObservationDigest  string       `json:"canonical_observation_digest"`
	EvidenceMode                string       `json:"evidence_mode"`
	Files                       []OutputFile `json:"files"`
}

// PublishImportedReport creates a fresh private bundle and publishes its
// integrity manifest last. Floating statistics are presentation JSON bytes;
// authoritative protocol/row/manifest hashes retain integer-only CanonicalV1.
func PublishImportedReport(input ImportedInputs, output string) (OutputManifest, error) {
	var manifest OutputManifest
	if !c.ValidDigest(input.ProtocolFileDigest) || !c.ValidDigest(input.ObservationFileDigest) {
		return manifest, c.Fail(c.InvalidArgument, "source file digests required")
	}
	report, err := ImportedReport(input.Protocol, input.Observations)
	if err != nil {
		return manifest, err
	}
	protocolRaw, err := c.CanonicalV1(input.Protocol)
	if err != nil {
		return manifest, err
	}
	reportRaw, err := json.Marshal(report)
	if err != nil {
		return manifest, err
	}
	observationRaw, err := c.CanonicalV1(input.Observations)
	if err != nil {
		return manifest, err
	}
	destination, err := fileguard.ResolveProspective(output)
	if err != nil {
		return manifest, err
	}
	if err = os.Mkdir(destination, 0700); err != nil {
		return manifest, c.Fail(c.Conflict, "fresh report directory required")
	}
	root, err := os.OpenRoot(destination)
	if err != nil {
		return manifest, err
	}
	defer root.Close()
	if err = fileguard.Private(root); err != nil {
		return manifest, err
	}
	manifest = OutputManifest{SchemaVersion: 1, ProtocolDigest: report.ProtocolDigest, SourceProtocolFileDigest: input.ProtocolFileDigest, SourceObservationFileDigest: input.ObservationFileDigest, CanonicalObservationDigest: report.ObservationDigest, EvidenceMode: report.EvidenceMode, Files: []OutputFile{}}
	for _, file := range []struct {
		name string
		raw  []byte
	}{{"protocol.json", protocolRaw}, {"observations.json", observationRaw}, {"report.json", reportRaw}} {
		if err = fileguard.Publish(root, file.name, file.raw); err != nil {
			return manifest, err
		}
		manifest.Files = append(manifest.Files, OutputFile{file.name, c.HashBytes(file.raw), int64(len(file.raw))})
	}
	raw, err := c.CanonicalV1(manifest)
	if err != nil {
		return manifest, err
	}
	if err = fileguard.Publish(root, "manifest.json", raw); err != nil {
		return manifest, err
	}
	return manifest, fileguard.SyncParents(root, ".")
}
