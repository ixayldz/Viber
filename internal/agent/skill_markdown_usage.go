package agent

import (
	"bytes"
	"encoding/json"

	c "github.com/ixayldz/Viber/internal/contracts"
	"github.com/ixayldz/Viber/internal/model"
)

// Called only for a successful kernel skill_advice reply. The durable marker
// survives transcript compaction, so managed selection cannot silently switch
// a used imported-skill format to an older strict decoder.
func recordSkillMarkdownUsage(doc *Document, reply model.Reply) error {
	used := false
	var value map[string]json.RawMessage
	if c.DecodeStrict([]byte(reply.Content), &value) != nil {
		return c.Fail(c.StoreIntegrityError, "skill advice returned an invalid format-use receipt")
	}
	var bindings []struct {
		Manifest string `json:"markdown_manifest_digest"`
	}
	if raw, present := value["bindings"]; present {
		if json.Unmarshal(raw, &bindings) != nil || len(bindings) > 128 {
			return c.Fail(c.StoreIntegrityError, "skill advice binding list is invalid")
		}
		for _, binding := range bindings {
			if binding.Manifest != "" {
				if !c.ValidDigest(binding.Manifest) {
					return c.Fail(c.StoreIntegrityError, "imported skill format marker is invalid")
				}
				used = true
			}
		}
	}
	if raw, present := value["definition"]; present {
		var definition map[string]json.RawMessage
		if c.DecodeStrict(raw, &definition) != nil {
			return c.Fail(c.StoreIntegrityError, "skill advice definition envelope is invalid")
		}
		if source, present := definition["imported_markdown"]; present && !bytes.Equal(bytes.TrimSpace(source), []byte("null")) {
			var marker struct {
				Manifest string `json:"manifest_digest"`
			}
			if json.Unmarshal(source, &marker) != nil || !c.ValidDigest(marker.Manifest) {
				return c.Fail(c.StoreIntegrityError, "imported skill definition marker is invalid")
			}
			used = true
		}
	}
	doc.SkillMarkdownUsed = doc.SkillMarkdownUsed || used
	return nil
}
