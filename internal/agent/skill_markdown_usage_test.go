package agent

import (
	"fmt"
	"testing"

	c "github.com/ixayldz/Viber/internal/contracts"
	"github.com/ixayldz/Viber/internal/model"
)

func TestSkillMarkdownUsageMarksOnlySuccessfulImportedFormat(t *testing.T) {
	digest := c.HashBytes([]byte("source manifest"))
	for _, test := range []struct {
		name, reply string
		used, fail  bool
	}{
		{"legacy", `{"definition":{"id":"legacy"}}`, false, false},
		{"listing", fmt.Sprintf(`{"bindings":[{"markdown_manifest_digest":%q}]}`, digest), true, false},
		{"procedure", fmt.Sprintf(`{"definition":{"imported_markdown":{"manifest_digest":%q}}}`, digest), true, false},
		{"invalid", fmt.Sprintf(`{"bindings":[{"markdown_manifest_digest":%q},{"markdown_manifest_digest":"bad"}]}`, digest), false, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			var doc Document
			err := recordSkillMarkdownUsage(&doc, model.Reply{Content: test.reply})
			if (err != nil) != test.fail || doc.SkillMarkdownUsed != test.used {
				t.Fatalf("used=%t err=%v", doc.SkillMarkdownUsed, err)
			}
		})
	}
}
