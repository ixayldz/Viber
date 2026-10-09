package evaluation

import (
	"encoding/json"
	c "github.com/ixayldz/Viber/internal/contracts"
	"github.com/ixayldz/Viber/internal/fileguard"
	"os"
)

// ReadImportedReport checks manifest hashes and recomputes every statistic.
// Source file hashes establish lineage, not authenticity of imported verdicts.
func ReadImportedReport(path string) (Report, error) {
	var result Report
	root, err := os.OpenRoot(path)
	if err != nil {
		return result, err
	}
	defer root.Close()
	raw, err := fileguard.ReadRegular(root, "manifest.json", 16<<10)
	if err != nil {
		return result, err
	}
	var manifest OutputManifest
	if err = c.DecodeStrict(raw, &manifest); err != nil {
		return result, err
	}
	if manifest.SchemaVersion != 1 || manifest.EvidenceMode != "IMPORTED_METADATA_UNATTESTED" || !c.ValidDigest(manifest.SourceProtocolFileDigest) || !c.ValidDigest(manifest.SourceObservationFileDigest) {
		return result, c.Fail(c.StoreIntegrityError, "invalid imported report manifest")
	}
	files, err := readManifestFiles(root, manifest.Files, []string{"protocol.json", "observations.json", "report.json"})
	if err != nil {
		return result, err
	}
	var protocol Protocol
	if err = c.DecodeStrict(files["protocol.json"], &protocol); err != nil {
		return result, err
	}
	var rows []Observation
	if err = c.DecodeStrict(files["observations.json"], &rows); err != nil {
		return result, err
	}
	result, err = ImportedReport(protocol, rows)
	if err != nil {
		return result, err
	}
	if manifest.ProtocolDigest != result.ProtocolDigest || manifest.CanonicalObservationDigest != result.ObservationDigest {
		return result, c.Fail(c.StoreIntegrityError, "imported report protocol/measurement lineage changed")
	}
	computed, err := json.Marshal(result)
	if err != nil {
		return result, err
	}
	if c.HashBytes(computed) != c.HashBytes(files["report.json"]) {
		return result, c.Fail(c.StoreIntegrityError, "reported statistics differ from assigned observations")
	}
	return result, nil
}
