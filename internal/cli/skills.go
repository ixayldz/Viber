package cli

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/ixayldz/Viber/internal/agent"
	c "github.com/ixayldz/Viber/internal/contracts"
	"github.com/ixayldz/Viber/internal/fileguard"
	"github.com/ixayldz/Viber/internal/skills"
)

const skillTransferConsent = "I_REVIEWED_EXACT_SKILL_CONTENT_AND_LICENSE_FOR_EXTERNAL_REGISTRY"

func runSkills(args []string, out, errout io.Writer) int {
	action, args := promptFirst(args)
	f := flags("skills", errout)
	directory := f.String("registry", "", "explicit external advisory registry")
	trustFile := f.String("trust-policy", "", "separate operator-pinned evaluator/scope trust policy JSON")
	storeDirectory := f.String("store", "", "existing task store for exact source/current authority")
	task := f.String("task", "", "source/current task identity")
	definitionFile := f.String("definition", "", "reviewed exact scoped procedural skill JSON")
	receiptFile := f.String("evaluation", "", "canonical independently signed evaluation receipt")
	id := f.String("id", "", "skill identity")
	version := f.Uint64("version", 0, "exact skill version")
	scopeKind := f.String("scope-kind", "", "TASK, PROJECT, USER or GENERIC")
	scopeKey := f.String("scope-key", "", "exact activation scope key")
	acknowledge := f.String("acknowledge-external-copy", "", "exact-content/license transfer acknowledgement")
	approve := f.Bool("approve", false, "explicit reviewed procedural activation/rollback approval")
	crossScopeReviewed := f.Bool("review-cross-scope", false, "separate explicit USER/GENERIC content and privacy review; current cross-project permission still required")
	jsonMode := f.Bool("json", false, "structured local output")
	if f.Parse(args) != nil {
		return 4
	}
	if f.NArg() != 0 || action != "source" && action != "propose" && action != "evaluate" && action != "activate" && action != "rollback" && action != "revoke" && action != "expire" && action != "status" && action != "load" {
		return report(out, errout, c.Fail(c.InvalidArgument, "skills source/propose/evaluate/activate/rollback/revoke/expire/status/load required"), *jsonMode)
	}
	read := func(name string, limit int64) ([]byte, error) {
		abs, e := filepath.Abs(name)
		if e != nil {
			return nil, e
		}
		root, e := os.OpenRoot(filepath.Dir(abs))
		if e != nil {
			return nil, e
		}
		defer root.Close()
		return fileguard.ReadRegular(root, filepath.Base(abs), limit)
	}
	ctx := context.Background()
	var result any
	var err error
	var session *agent.Session
	authorityDigest := ""
	needsTask := action == "source" || action == "propose" || action == "activate" || action == "rollback" || action == "revoke" || action == "load"
	if needsTask {
		if *task == "" || *storeDirectory == "" {
			return report(out, errout, c.Fail(c.InvalidArgument, "exact task and existing store required"), *jsonMode)
		}
		session, err = agent.OpenExisting(ctx, *storeDirectory)
		if err != nil {
			return report(out, errout, err, *jsonMode)
		}
		defer session.Close()
		_, doc, e := session.Load(ctx, *task)
		if e != nil {
			return report(out, errout, e, *jsonMode)
		}
		if doc.Config == nil {
			return report(out, errout, c.Fail(c.StaleAuthority, "retained operator config required for procedural skills"), *jsonMode)
		}
		if e = doc.Config.CurrentAuthority(); e != nil {
			return report(out, errout, e, *jsonMode)
		}
		authorityDigest, e = c.Digest(doc.Config)
		if e != nil {
			return report(out, errout, e, *jsonMode)
		}
	}
	if action == "source" {
		source, e := session.SkillSource(ctx, *task)
		err = e
		result = struct {
			Source          skills.Source `json:"source"`
			AuthorityDigest string        `json:"authority_digest"`
		}{source, authorityDigest}
	} else {
		if *directory == "" || *trustFile == "" {
			return report(out, errout, c.Fail(c.InvalidArgument, "registry and separate evaluator trust policy required"), *jsonMode)
		}
		registryPath, e := filepath.Abs(*directory)
		if e != nil {
			return report(out, errout, e, *jsonMode)
		}
		policyPath, e := filepath.Abs(*trustFile)
		if e != nil {
			return report(out, errout, e, *jsonMode)
		}
		if !fileguard.Disjoint(registryPath, policyPath) {
			return report(out, errout, c.Fail(c.PolicyDenied, "trust policy must be outside registry"), *jsonMode)
		}
		if needsTask {
			_, doc, e := session.Load(ctx, *task)
			if e != nil {
				return report(out, errout, e, *jsonMode)
			}
			baseline, e := session.Archive.Get(doc.Baseline)
			if e != nil {
				return report(out, errout, e, *jsonMode)
			}
			if !fileguard.Disjoint(registryPath, baseline.Snapshot.Root) || !fileguard.Disjoint(registryPath, *storeDirectory) || !fileguard.Disjoint(policyPath, baseline.Snapshot.Root) || !fileguard.Disjoint(policyPath, *storeDirectory) {
				return report(out, errout, c.Fail(c.PolicyDenied, "skill registry and trust policy must be outside source/store"), *jsonMode)
			}
		}
		trust, e := skills.ReadTrustPolicy(policyPath)
		if e != nil {
			return report(out, errout, e, *jsonMode)
		}
		if needsTask && action == "load" {
			result, err = session.LoadActiveSkillsFromPolicyFile(ctx, registryPath, policyPath, *task)
		} else {
			registry, e := skills.Open(registryPath, trust)
			if e != nil {
				return report(out, errout, e, *jsonMode)
			}
			defer registry.Close()
			switch action {
			case "status":
				result, err = registry.Status(ctx)
			case "expire":
				result, err = registry.Expire(ctx, time.Now())
			case "propose":
				if *acknowledge != skillTransferConsent {
					return report(out, errout, c.Fail(c.PolicyDenied, "review exact skill payload/license and explicitly acknowledge external registry transfer"), *jsonMode)
				}
				raw, e := read(*definitionFile, skills.MaxDefinitionBytes)
				if e != nil {
					return report(out, errout, e, *jsonMode)
				}
				var definition skills.Definition
				if e = c.DecodeStrict(raw, &definition); e != nil {
					return report(out, errout, e, *jsonMode)
				}
				source, e := session.SkillSource(ctx, *task)
				if e != nil {
					return report(out, errout, e, *jsonMode)
				}
				if definition.Source != source {
					return report(out, errout, c.Fail(c.StaleBase, "skill source must match current retained task revision"), *jsonMode)
				}
				_, doc, e := session.Load(ctx, *task)
				if e != nil {
					return report(out, errout, e, *jsonMode)
				}
				baseline, e := session.Archive.Get(doc.Baseline)
				if e != nil {
					return report(out, errout, e, *jsonMode)
				}
				if !fileguard.Disjoint(registryPath, baseline.Snapshot.Root) || !fileguard.Disjoint(registryPath, *storeDirectory) || !fileguard.Disjoint(policyPath, baseline.Snapshot.Root) || !fileguard.Disjoint(policyPath, *storeDirectory) {
					return report(out, errout, c.Fail(c.PolicyDenied, "skill registry and trust policy must be outside source/store"), *jsonMode)
				}
				result, err = registry.Propose(ctx, definition)
			case "evaluate":
				raw, e := read(*receiptFile, skills.MaxEvaluationBytes)
				if e != nil {
					return report(out, errout, e, *jsonMode)
				}
				result, err = registry.ImportEvaluation(ctx, *id, *version, raw)
			case "activate", "rollback":
				if !*approve {
					return report(out, errout, c.Fail(c.PolicyDenied, "explicit reviewed activation approval required"), *jsonMode)
				}
				scope := skills.Scope{Kind: *scopeKind, Key: *scopeKey}
				registry.Close()
				result, err = session.ActivateSkillFromPolicyFile(ctx, registryPath, policyPath, *task, *id, *version, scope, *crossScopeReviewed, action == "rollback")
			case "revoke":
				result, err = registry.Revoke(ctx, *id, *version, authorityDigest)
			}
		}
	}
	if err != nil {
		return report(out, errout, err, *jsonMode)
	}
	if jsonWrite(out, result) != nil {
		return 4
	}
	return 0
}
