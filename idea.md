# IDEA.md — Model-Agnostic Long-Horizon Coding CLI Harness

> **Durum:** Mimari fikir kanvası — PRD değildir.  
> **Amaç:** Eylül 2026 itibarıyla; kapalı, açık-ağırlıklı ve lokal modelleri aynı çekirdek üzerinde çalıştırabilen, uzun soluklu yazılım mühendisliği görevlerinde bağlamı kaybetmeyen, halüsinasyonu mimari olarak azaltan, güvenli, sade ve yüksek verimli bir coding CLI/harness tasarımını tanımlamak.

---

# 1. Tek cümlelik vizyon

**Modeli ürünün kendisi değil, kalıcı bir yazılım mühendisliği işletim sisteminin değiştirilebilir muhakeme motoru olarak ele alan; sınırlı model context window'u arkasında sürümlenmiş, doğrulanabilir ve adreslenebilir bir "sonsuz context" alanı sunan model-bağımsız coding harness.**

Bu ürün "daha iyi bir chat CLI" değildir.

Bu ürün:

- repository'yi canlı olarak anlayan,
- görev durumunu kalıcı tutan,
- doğru context'i her adım için yeniden derleyen,
- gerekli olduğunda planlayan,
- gerekli olduğunda sub-agent kullanan,
- değişiklikleri transactional olarak uygulayan,
- test ve kanıt olmadan işi tamamlanmış saymayan,
- başarılardan ve hatalardan eval/training datası çıkarabilen,
- ama tüm bu karmaşıklığı kullanıcıdan büyük ölçüde saklayan

bir **Software Engineering Runtime** olacaktır.

---

# 2. Çözmek istediğimiz temel problem

Bugünkü coding agent sistemlerinin çoğu aynı temel kısıtlardan etkilenir:

1. **Conversation transcript'i state gibi kullanılır.**  
   Uzun görevlerde transcript büyür; eski dosya snapshot'ları, eski kararlar, tool çıktıları ve yeni durum birbirine karışır.

2. **Context window büyütmek, context yönetmekle aynı şey değildir.**  
   Çok büyük context kullanılabilse bile tüm bilginin eşit derecede etkin kullanılacağı garanti değildir. Alakasız bilgi reasoning kalitesini düşürebilir.

3. **Repository bir metin torbası gibi ele alınır.**  
   Oysa repository; symbol'lar, import/call ilişkileri, testler, config, git history, issue history ve çalışma zamanı davranışlarıyla ilişkisel bir sistemdir.

4. **RAG çoğu sistemde tek atımlıdır.**  
   İlk sorguda yanlış veya eksik dosyalar seçildiğinde agent yanlış yolda uzun süre ilerleyebilir.

5. **Memory ile current world state birbirine karıştırılır.**  
   Daha önce doğru olan bir bilgi, bir edit sonrasında yanlış olabilir. "Hatırlamak" yeterli değildir; bilginin hangi repository sürümünde doğru olduğunun bilinmesi gerekir.

6. **Compaction çoğu zaman prose summary üretir.**  
   Kritik kararlar, başarısız hipotezler, test durumu, modified files ve açık sorular kaybolabilir.

7. **Multi-agent sistemler gereksiz yere fazla agent spawn edebilir.**  
   Sonuç: context duplication, merge conflict, koordinasyon maliyeti ve token patlaması.

8. **"Done" kararı modele bırakılır.**  
   Bir modelin "bitti" demesi, build'in geçtiğini, acceptance criteria'nın sağlandığını veya regresyon olmadığını kanıtlamaz.

9. **Harness'lar modele fazla bağımlıdır.**  
   Prompt, tool seti, planning derinliği ve context politikası tek bir model ailesine göre optimize edildiğinde lokal veya farklı modellerle performans çöker.

10. **Kullanıcı arayüzü ya fazla chat-benzeri ya da fazla gürültülüdür.**  
    Kullanıcı aslında dört şeyi bilmek ister: ne oluyor, ne değişti, sorun var mı, benden karar bekleniyor mu?

---

# 3. Ana tasarım tezi

Bu sistemin en önemli ayrımı:

> **Conversation history source of truth değildir.**

Source of truth şunların birleşimidir:

- canlı workspace state,
- repository structural graph,
- symbol/index verisi,
- git state/history,
- test/build/runtime state,
- typed task state,
- versioned evidence,
- decisions,
- failures,
- user constraints,
- verified checkpoints.

Conversation transcript ise yalnızca geçici bir etkileşim kaydıdır.

Bundan dört ana invariant çıkar:

## 3.1 Model geçici, state kalıcıdır

Model değişebilir.  
Context sıfırlanabilir.  
Terminal kapanabilir.  
Harness yeniden başlatılabilir.

Ama görev durumu ve mühendislik bilgisi kaybolmaz.

## 3.2 Context birikmez, her adımda derlenir

Context append-only transcript değildir.

Her model çağrısından önce runtime:

- görevi,
- aktif subtask'i,
- gerekli evidence'i,
- current file versions'ı,
- kararları,
- failure memory'yi,
- tool schema'larını,
- gerekli plan bilgisini

yeniden assemble eder.

## 3.3 Evidence olmadan yüksek etkili karar alınmaz

Agent'ın önemli iddiaları mümkün olduğunca evidence reference ile bağlıdır.

"Bu fonksiyon X'i çağırıyor" deniyorsa bunun:

- file,
- symbol,
- line range,
- SHA/version

kaynağı tutulmalıdır.

## 3.4 Verification olmadan tamamlanma yoktur

"Done" bir LLM cevabı değildir.

Runtime tarafından hesaplanan bir durumdur.

---

# 4. Sistem resmi

```text
┌─────────────────────────────────────────────────────────────────────┐
│                         USER / TUI / CLI                            │
│ prompt • steer • queue • approve • diff • status • why • evidence │
└───────────────────────────────┬─────────────────────────────────────┘
                                │
┌───────────────────────────────▼─────────────────────────────────────┐
│                    SOFTWARE ENGINEERING RUNTIME                     │
│                                                                     │
│  ┌────────────────────┐   ┌─────────────────────────────────────┐   │
│  │ Task State Machine │   │ Orchestrator + Dependency Scheduler │   │
│  └──────────┬─────────┘   └────────────────┬────────────────────┘   │
│             │                              │                        │
│             └──────────────┬───────────────┘                        │
│                            ▼                                        │
│                   ┌──────────────────┐                               │
│                   │   CONTEXT MMU    │                               │
│                   └───────┬──────────┘                               │
│                           │                                          │
│      ┌────────────────────┼────────────────────────┐                 │
│      ▼                    ▼                        ▼                 │
│ Live Registry       Task / Memory Plane      Repo Intelligence     │
│ current files       decisions/failures       symbol index          │
│ file SHAs           checkpoints              lexical index         │
│ test state          user rules               vectors               │
│ runtime state       subtask memory           heterogeneous graph   │
│                                              git/repo memory        │
│      └────────────────────┬────────────────────────┘                 │
│                           ▼                                          │
│                  CONTEXT COMPILER                                    │
│          finite high-signal working set per call                    │
│                           │                                          │
│                           ▼                                          │
│  ┌──────────────────────────────────────────────────────────────┐    │
│  │                   MODEL FABRIC / ROUTER                      │    │
│  │ OpenAI • Anthropic • Gemini • open-weight API • local LLM  │    │
│  └───────────────────────────┬──────────────────────────────────┘    │
│                              │                                       │
│                              ▼                                       │
│  ┌──────────────────────────────────────────────────────────────┐    │
│  │                 CAPABILITY / TOOL KERNEL                     │    │
│  │ native FS • search • git • shell • tests • LSP • MCP • web │    │
│  └───────────────────────────┬──────────────────────────────────┘    │
│                              │                                       │
│                              ▼                                       │
│  ┌──────────────────────────────────────────────────────────────┐    │
│  │ Proposal → Transaction → Validation → Commit State Update   │    │
│  └───────────────────────────┬──────────────────────────────────┘    │
│                              │                                       │
│                              ▼                                       │
│  ┌──────────────────────────────────────────────────────────────┐    │
│  │ Verification + Eval + Telemetry + Learning Plane            │    │
│  └──────────────────────────────────────────────────────────────┘    │
└─────────────────────────────────────────────────────────────────────┘
```

---

# 5. Çekirdek: Context MMU

Context MMU ürünün en önemli farklılaştırıcısıdır.

Buradaki "MMU" benzetmesi yalnızca isim değildir.

Gerçek hedef:

> Modelin fiziksel context window'u küçük olsa bile, modelin adresleyebildiği mühendislik bilgisini çok büyük hale getirmek.

Model 32K, 128K, 1M veya daha büyük context'e sahip olabilir.  
Ancak runtime'ın arkasındaki addressable context onlarca veya yüzlerce milyon token olabilir.

## 5.1 Context tier'ları

### L0 — Attention / Immediate Working Context

Model çağrısına gerçekten giren içerik.

İçeriği:

- current goal,
- aktif subtask,
- son kritik interaction'lar,
- active evidence,
- immediate constraints,
- gerekli tool schemas,
- current plan slice.

Bu katman pahalıdır ve küçük tutulur.

---

### L1 — Active Working Set

Şu anda üzerinde çalışılan symbol/file kümeleri.

Örnek:

- `AuthService.login`
- direct dependencies,
- ilgili testler,
- modified files,
- son compiler errors.

Modelin yakın çalışma belleğidir.

---

### L2 — Task State Memory

Görev boyunca kalıcı olan typed state.

İçeriği:

- task specification,
- acceptance criteria,
- subtasks,
- decisions,
- unresolved questions,
- hypotheses,
- current plan,
- failure ledger,
- changed files,
- test status,
- dependency blockers.

---

### L3 — Repository Intelligence

Repository'nin yapılandırılmış temsili.

- file index,
- symbol index,
- lexical/BM25 index,
- semantic vector index,
- AST relationships,
- dependency graph,
- call graph,
- test-to-code links,
- config relations,
- git co-change relationships,
- commit history memory,
- linked issues/PR summaries.

---

### L4 — Raw Artifact Store

Tam kaynak gerçekliği.

- repository files,
- git objects,
- tool outputs,
- build logs,
- test logs,
- traces,
- external docs,
- task attachments.

Bu katman hiçbir zaman sırf büyük olduğu için model prompt'una doğrudan basılmaz.

---

### L5 — Historical Engineering Memory

Uzun dönem birikim.

- önceki task summaries,
- verified decisions,
- recurring failures,
- user corrections,
- project-specific procedures,
- proven skills,
- historical fix patterns,
- repository evolution.

---

# 6. Context bir "string" değil, adreslenebilir bir alan olacak

Modelin önüne şu şekilde dev bir string koymak istemiyoruz:

```text
context = 20 MB repository dump
```

Bunun yerine opaque references:

```text
file://src/auth/service.ts@sha256:...
symbol://AuthService.login@version:42
evidence://ev_019af...
task://TASK-120/subtask/AUTH-3
test://pytest/auth/test_login::test_expired_token
```

Model context'i ihtiyaç halinde tool aracılığıyla açar.

Örnek:

```text
ctx.search("where JWT expiration is validated")
ctx.symbol("AuthService.login")
ctx.expand_graph("AuthService.login", relation=["CALLS","TESTED_BY"], depth=2)
ctx.read(evidence_ref)
```

Böylece model "her şeyi görüyormuş" hissine sahip olur; fakat fiziksel working set küçük ve yüksek sinyalli kalır.

---

# 7. Live Context Registry

Bu katman Context MMU'nun kritik güvenilirlik mekanizmasıdır.

Her dosya için:

```text
FileState
- path
- current_sha
- version
- modified_at
- parse_version
- index_version
- dirty
- last_verified
```

tutulur.

Her evidence:

```text
EvidenceRef
- id
- source_path
- source_symbol
- line_range
- source_sha
- created_at_step
- confidence
- stale
```

şeklindedir.

Dosya değiştiğinde eski evidence otomatik stale olur.

Bu şu problemi önler:

```text
step 5  → auth.ts eski hali okunuyor
step 19 → auth.ts editleniyor
step 43 → model step 5 bilgisini hâlâ doğru sanıyor
```

Yeni sistemde step 19'daki write sonrası:

```text
auth.ts version 17 → 18
old evidence -> STALE
dependent facts -> NEEDS_REVALIDATION
```

olur.

**Coding agent için en tehlikeli hafıza problemi unutmak değil; artık yanlış olan bilgiyi hatırlamaktır.**

---

# 8. Repository Intelligence Plane

Repository'yi yalnızca embedding chunk'larından oluşan bir koleksiyon olarak görmeyeceğiz.

## 8.1 Çoklu index

Aynı repository için paralel index'ler:

### Lexical index

- exact identifier,
- path,
- error strings,
- config keys,
- dependency names,
- stack traces.

Araçlar:

- ripgrep benzeri exact search,
- BM25,
- fuzzy path/symbol search.

### Symbol index

Tree-sitter/LSP/AST üzerinden:

- function,
- method,
- class,
- interface,
- type,
- constant,
- module.

### Semantic index

Embedding retrieval:

- doğal dil issue ↔ code,
- kavramsal benzerlik,
- docs ↔ implementation.

### Heterogeneous graph

Node'lar:

```text
Repository
File
Module
Class
Function
Method
Type
Test
Config
Package
DatabaseSchema
Endpoint
```

Edge'ler:

```text
DEFINES
IMPORTS
CALLS
REFERENCES
EXTENDS
IMPLEMENTS
TESTS
CONFIGURES
DEPENDS_ON
READS
WRITES
CO_CHANGES_WITH
OWNED_BY
AFFECTED_BY
```

### Git / Repository Memory

- commit history,
- co-change frequencies,
- past fixes,
- issue ↔ commit relationships,
- hot modules,
- historical architecture evolution.

---

# 9. Retrieval bir kez yapılmaz; iteratiftir

İlk retrieval yalnızca başlangıç hipotezidir.

Akış:

```text
Issue / User Goal
       │
       ▼
Query classification
       │
       ▼
Candidate generation
 lexical + symbol + semantic
       │
       ▼
Graph expansion
       │
       ▼
Reranking
       │
       ▼
Working set
       │
       ▼
Model reads / reasons
       │
       ▼
New names/errors/dependencies discovered
       │
       └──────────────► Retrieval again
```

Bu sayede ilk sorgunun yanlış localization yapması görev boyunca zincirleme hata üretmez.

---

# 10. Query-adaptive ranking

Sabit `40% vector + 40% graph + 20% rule` gibi oranlardan kaçınacağız.

Ranker task tipine göre davranacak.

Örnek scoring:

```text
score(node | task,state) =
    α semantic_relevance
  + β lexical_match
  + γ graph_proximity
  + δ symbol_exactness
  + ε failing_test_proximity
  + ζ git_cochange_signal
  + η working_set_continuity
  + θ project_rule_relevance
  + ι recency
  - λ redundancy
  - μ staleness
  - ν context_cost
```

Katsayılar görev tipine göre değiştirilir.

### Stack trace / compiler error

Lexical + symbol + failure proximity ağır basar.

### Mimari refactor

Graph + repository history + architecture memory ağır basar.

### Explicit symbol request

Exact symbol index en üst sinyal olur.

### Yeni feature

Plan graph + related modules + tests + patterns ağır basar.

---

# 11. Context Compiler

Model çağrısından hemen önce çalışan deterministik/yarı-deterministik katmandır.

Girdileri:

```text
TaskState
SubtaskState
ModelProfile
WorkingSet
EvidenceLedger
CurrentRepositoryState
ToolRequirements
TokenBudget
```

Çıktısı:

```text
CompiledContext
```

## 11.1 Context slot'ları

Her çağrıda potansiyel slot'lar:

```text
[system/runtime contract]
[task constitution]
[current subtask]
[current plan slice]
[project constraints]
[active evidence]
[recent high-fidelity interactions]
[relevant failure memory]
[tool schemas]
[output contract]
[response reserve]
```

Hepsinin aynı anda kullanılması zorunlu değildir.

Context Compiler task phase'e göre slot bütçelerini ayarlar.

---

# 12. Dinamik context budget

Statik yüzdeler kullanmayacağız.

Budget şunlara göre dinamik ayarlanır:

- model context kapasitesi,
- model long-context güvenilirliği,
- task complexity,
- phase,
- retrieval confidence,
- uncertainty,
- recent failures,
- gerekli tool schemas,
- output ihtiyacı.

Örnek:

### Debug phase

```text
45% active evidence
20% failure/runtime logs
10% task state
10% project rules
15% output/tool reserve
```

### Architecture phase

```text
25% active evidence
30% strategic/repository context
20% requirements
10% decisions
15% reserve
```

Bunlar sabit ürün değerleri değil; policy tarafından öğrenilebilir parametrelerdir.

---

# 13. Compaction = özetleme değildir

En kritik tasarım kararlarından biri.

Yanlış yaklaşım:

```text
"Önceki 80K tokenı 5K tokenlık prose özet yap."
```

Doğru yaklaşım:

> Raw trajectory'den typed state yeniden inşa et.

Compaction sonucunda:

```text
Checkpoint
- task_goal
- acceptance_criteria
- completed_subtasks
- active_subtask
- verified_decisions[]
- open_questions[]
- failed_attempts[]
- active_hypotheses[]
- modified_files[]
- test_state
- evidence_refs[]
- user_constraints[]
- next_actions[]
- resource_budget
- repository_version
```

saklanır.

Raw trajectory silinmez; archive edilir.

Model gerekirse geçmişe page fault yapabilir.

---

# 14. Compaction trigger'ları

Yalnızca "%80 context doldu" gibi mekanik trigger kullanmayacağız.

İki trigger sınıfı:

## Capacity trigger

- context budget yaklaşması,
- tool result explosion,
- large logs.

## Semantic trigger

- subtask tamamlandı,
- test suite geçti,
- plan branch kapandı,
- major hypothesis çözüldü,
- agent handoff,
- development epoch bitti,
- model switch yapılacak.

Semantic compaction tercih edilir.

---

# 15. Compaction verifier

Compaction'ın kendisi de hata yapabilir.

Bu yüzden compaction sonrasında verifier:

- kritik decisions kaybolmuş mu,
- current file versions doğru mu,
- unresolved blockers korunmuş mu,
- failed attempts korunmuş mu,
- acceptance criteria eksilmemiş mi,
- next action state tutarlı mı

kontrol eder.

Gerekirse checkpoint reject edilir ve yeniden oluşturulur.

---

# 16. Task Runtime: chat değil state machine

Her görev açık bir runtime state'e sahiptir.

```text
CREATED
  ↓
SCOPING
  ↓
PLANNING? ─── optional
  ↓
READY
  ↓
EXECUTING
  ↓
VERIFYING
  ├─ PASS ──► COMPLETED
  ├─ FAIL ──► REPLANNING
  └─ BLOCK ─► BLOCKED
```

Ek state'ler:

```text
WAITING_USER
WAITING_RESOURCE
PAUSED
CANCELLED
RECOVERING
```

Model state'i kendi başına değiştirmez.

Model öneri yapar.

Runtime geçişi doğrular.

---

# 17. Planning her task için zorunlu değildir

Planlama bir dogma olmayacak.

Task classifier karar verir:

### Mikro görev

```text
typo
single-line bug
explicit change
```

Doğrudan execution.

### Orta görev

Kısa structured plan.

### Repository-level görev

Dependency-aware execution plan.

### Çok uzun görev

Plan graph + development epochs.

Plan free-form prose olmamalı.

---

# 18. Plan Graph

Uzun görevlerde:

```text
Task
├── Subtask A
│   ├── symbols
│   ├── dependencies
│   ├── acceptance criteria
│   └── estimated risk
├── Subtask B
└── Subtask C
```

Edge türleri:

```text
DEPENDS_ON
CAN_PARALLELIZE
MUTEX
PRODUCES_CONTRACT_FOR
INVALIDATES
VERIFIES
```

Böylece scheduler doğal dille yazılmış bir to-do listesinden daha doğru karar verebilir.

---

# 19. Orchestrator ve Scheduler ayrımı

Bu ayrım çok önemli.

## Orchestrator = semantic intelligence

Model tabanlı olabilir.

Görevleri:

- task decomposition,
- architecture reasoning,
- unknowns discovery,
- subtask generation,
- replanning recommendation.

## Scheduler = runtime authority

Deterministik olmalıdır.

Görevleri:

- dependency çözümü,
- parallelism,
- file/symbol leases,
- resource budget,
- capability permissions,
- agent lifecycle,
- conflicts,
- retries,
- cancellation.

Orchestrator "bunları paralel yapalım" diyebilir.

Ama nihai paralellik kararını scheduler verir.

---

# 20. Multi-agent prensibi

Amaç çok agent kullanmak değildir.

Amaç **minimum coordination cost ile maksimum güvenli paralellik**tir.

## Tek agent kullan

Eğer:

- görev küçükse,
- dosyalar sıkı bağlıysa,
- aynı symbol seti üzerinde çalışılıyorsa,
- shared mutable state fazlaysa.

## Multi-agent kullan

Eğer:

- subtasks bağımsızsa,
- file/symbol ownership ayrılabiliyorsa,
- output contract tanımlanabiliyorsa,
- paralellik gerçek latency avantajı sağlıyorsa.

---

# 21. Context Lease

Her sub-agent'a tüm repository ve tüm history verilmez.

Agent'a lease verilir:

```text
AgentLease
- agent_id
- subtask_id
- goal
- acceptance_criteria
- allowed_paths
- allowed_symbols
- read_scope
- write_scope
- tool_capabilities
- context_budget
- token_budget
- time_budget
- parent_evidence
- expected_output_schema
```

Bu hem güvenlik hem context kalitesi sağlar.

---

# 22. File / Symbol ownership

Parallel agent'lar için scheduler ownership takip eder.

```text
Agent A owns:
  src/auth/**
  AuthService
  TokenService

Agent B owns:
  tests/auth/**
```

İki agent aynı mutable symbol'a yazacaksa:

- serialize edilir,
- parent contract oluşturulur,
- veya task yeniden bölünür.

---

# 23. Shared state = typed blackboard

Agent'lar birbirine serbest metin roman yazmaz.

Shared state:

```text
Fact
Decision
Hypothesis
Warning
Dependency
Proposal
TestResult
Contract
```

olarak typed objelerdir.

Örnek:

```yaml
type: dependency
subject: AuthService.login
relation: CALLS
object: TokenService.issueToken
evidence: ev_38f1
confidence: 1.0
source_version: 81
```

---

# 24. Evidence Ledger

Blackboard'ın daha güçlü sürümü.

Her önemli bulgu kaynağıyla birlikte tutulur.

```text
Evidence
- id
- claim
- source_type
- file/path
- symbol
- lines
- SHA
- tool_call
- generated_at_step
- confidence
- validity
- dependencies
```

Dosya/symbol değişirse bağlı evidence invalidate edilir.

Bu halüsinasyonu tamamen yok etmez; fakat "modelin hafızasında oluşmuş fakat source'u olmayan" iddiaların execution üzerinde sınırsız etkili olmasını önler.

---

# 25. Decision Ledger

Kararların ayrı tutulması gerekir.

```text
Decision
- id
- question
- chosen_option
- alternatives
- rationale
- evidence[]
- made_by
- approved_by
- scope
- invalidation_conditions
```

Örnek:

```text
"JWT refresh token DB'de saklanacak."
```

Bu karar gelecekte tekrar tartışılmamalı; ancak dependency değişirse re-open edilebilir.

---

# 26. Failure Ledger

Başarısızlıkları prompt içine "do not do X" olarak kalıcı gömmek tehlikelidir.

Typed failure:

```text
Failure
- attempted_action
- failure_type
- evidence
- environment_version
- cause_hypothesis
- validated_cause?
- retry_conditions
- invalidation_conditions
```

Örnek:

```text
Package X unavailable at package-lock version Y.
Revalidate if package manifest changes.
```

Bu sayede agent aynı hatayı körlemesine tekrarlamaz ama eski bir failure yüzünden sonsuza kadar engellenmez.

---

# 27. Procedural Skill Layer

Uzun vadede sistem yalnız "facts" değil, doğrulanmış çalışma prosedürleri de öğrenebilir.

Örnek skill:

```text
Skill: Fix TypeScript circular dependency
Trigger:
  compiler diagnostics + import cycle graph
Procedure:
  1. locate SCC
  2. identify shared contract
  3. extract interface
  4. adjust imports
  5. run affected tests
Verification:
  tsc + cycle check
```

Skill kullanımı:

- project-scoped,
- user-scoped,
- generic,
- confidence/version kontrollü

olmalıdır.

Kullanıcı correction'ları mümkün olduğunda yalnız memory olarak değil, runtime enforcement rule'a dönüştürülebilir.

---

# 28. Proposal / Transaction Engine

Agent doğrudan production workspace'e rastgele write yapmamalı.

Edit:

```text
Model
  ↓
Proposal
  ↓
Conflict check
  ↓
Syntax / policy validation
  ↓
Transactional apply
  ↓
Re-index affected area
  ↓
Tests / verifier
  ↓
Commit state update
```

Proposal:

```text
Proposal
- base_workspace_version
- base_file_shas
- changes[]
- affected_symbols[]
- reason
- linked_subtask
- expected_invariants[]
```

---

# 29. Optimistic concurrency

Bir file agent tarafından okunduktan sonra başka actor tarafından değişmişse stale edit uygulanmaz.

```text
read sha = abc
current sha = xyz

abc != xyz
=> REBASE / RE-READ / CONFLICT
```

Bu multi-agent sistemde zorunludur.

---

# 30. Shadow workspace

Riskli veya paralel işler için:

- worktree,
- overlay FS,
- shadow filesystem,
- container workspace

kullanılabilir.

Agent'ın changeset'i doğrulanmadan ana workspace'e merge edilmez.

---

# 31. Verification stack

Tek başına test geçmesi yeterli değildir.

Verifier katmanlıdır.

## Level 0 — Mechanical

- patch applies,
- parse,
- syntax,
- format.

## Level 1 — Static

- typecheck,
- linter,
- static analysis,
- dependency constraints.

## Level 2 — Target tests

Değişiklikle ilgili testler.

## Level 3 — Broader regression

Risk/impact graph'a göre seçilen test subset'i veya full suite.

## Level 4 — Behavioral acceptance

User goal ve acceptance criteria.

## Level 5 — Invariant verification

Örneğin:

- public API kırılmadı,
- forbidden dependency eklenmedi,
- security rule ihlal edilmedi,
- migration compatibility korunuyor.

---

# 32. "Done" predicate

Task:

```text
done =
    acceptance_criteria_satisfied
 AND required_tests_pass
 AND required_build_checks_pass
 AND no_unresolved_critical_conflict
 AND no_unverified_required_change
 AND runtime_policy_allows_completion
```

Modelin "I think this is done" demesi predicate'i değiştirmez.

---

# 33. Watchdog / Progress Engine

Long-horizon agent loop'larında yalnız max-turn yeterli değildir.

Watchdog şunları izler:

- aynı file'ın gereksiz reread'i,
- aynı tool pattern'inin tekrarı,
- aynı compiler error'ın dönmesi,
- step without state progress,
- failed hypothesis repetition,
- token burn rate,
- time burn rate,
- agent deadlocks,
- repeated context retrieval.

"Progress" şu şekilde tanımlanabilir:

```text
new verified evidence
OR
new successful state transition
OR
new accepted edit
OR
new resolved blocker
OR
new test signal
```

Output uzunluğunun artması progress değildir.

---

# 34. Global Resource Ledger

Recursive/sub-agent sistemlerde budget child'a yeniden sıfırlanamaz.

Tek global ledger:

```text
ResourceLedger
- input_tokens
- output_tokens
- model_cost
- wall_clock
- tool_calls
- test_runtime
- retrieval_budget
- subagent_count
- parallel_slots
```

Sub-agent:

```text
lease 20K tokens
lease 3 min
lease 2 write files
```

alabilir.

Parent'ın global budget'ından düşer.

---

# 35. Model Fabric

Harness tek model için optimize edilmeyecek.

Destek:

- OpenAI,
- Anthropic,
- Gemini,
- diğer API provider'ları,
- OpenAI-compatible endpoints,
- açık-ağırlıklı modeller,
- Ollama/vLLM/llama.cpp benzeri local endpoints.

Ama abstraction yalnız "chatCompletion()" olmayacak.

---

# 36. Model Capability Profile

Her model için capability profile:

```text
ModelProfile
- context_limit
- recommended_working_context
- output_limit
- tool_call_reliability
- structured_output_reliability
- coding_strength
- planning_strength
- long_context_reliability
- patch_accuracy
- instruction_adherence
- latency
- cost
- local/private
- vision_support
- reasoning_modes
```

Profile statik metadata + local calibration eval'leri ile oluşturulur.

---

# 37. Model Adaptation Policy

Aynı runtime farklı modele göre kendini adapte eder.

## Küçük / lokal model

- daha küçük context pages,
- daha explicit tool schemas,
- daha küçük subtasks,
- daha sık verification,
- daha deterministic planner,
- daha az concurrent responsibilities.

## Güçlü frontier model

- daha geniş working set,
- daha yüksek-level tools,
- daha seyrek compaction,
- daha büyük subtask scope,
- daha fazla semantic planning yetkisi.

Ama güvenlik sınırları model gücüne bağlı olarak gevşetilmez.

---

# 38. Model Router

Her çağrının aynı modelde yapılması gerekmez.

Potential roles:

```text
fast locator
planner
implementer
reviewer
verifier
summarizer/compactor
cheap classifier
local privacy-sensitive worker
```

Routing objective:

```text
quality
latency
cost
privacy
capability
```

Optimizasyonu yapılır.

Kullanıcı isterse tek model lock edebilir.

---

# 39. Tool Kernel

Araç sistemi üç seviye olacak.

## Tier 1 — Native hot-path tools

En çok kullanılan ve en güvenilir olması gerekenler:

- read file/range,
- write/patch,
- grep,
- symbol lookup,
- graph traversal,
- git status/diff/log,
- shell,
- tests,
- build,
- LSP diagnostics,
- file listing.

Bunlar MCP overhead'ine mahkûm edilmemeli.

## Tier 2 — Plugins / adapters

Özel local veya provider entegrasyonları.

## Tier 3 — MCP

Harici ecosystem:

- databases,
- issue trackers,
- docs,
- SaaS,
- remote tools.

MCP önemli bir interoperability layer'dır; runtime'ın temel state mekanizması değildir.

---

# 40. Capability security

Tool access boolean değildir.

Capability token mantığı:

```text
read:
  src/**

write:
  src/auth/**
  tests/auth/**

shell:
  allow ["npm test *", "git diff", "git status"]
  ask ["npm install *"]
  deny ["git push *", "rm -rf *"]

network:
  deny by default
```

Sub-agent'a yalnız lease kapsamındaki capability'ler verilir.

---

# 41. Risk-adaptive approvals

Kullanıcıyı her tool çağrısında rahatsız etmek istemiyoruz.

Eylemler risk sınıfına ayrılır.

### Low risk

- read,
- grep,
- git status,
- test.

Auto allow.

### Medium

- workspace edit,
- dependency changes,
- migrations.

Policy'ye göre auto/ask.

### High

- network secrets,
- destructive filesystem,
- deploy,
- push,
- external irreversible actions.

Explicit approval.

---

# 42. MCP yaklaşımı

MCP desteklenir ama şunlar korunur:

- MCP server'ın tool description'ı source of truth sayılmaz; capability policy üsttedir.
- Tool sonuçları doğrudan transcript'e gömülmek zorunda değildir; artifact/evidence store'a yazılır.
- Large MCP outputs page'lenir.
- MCP task/long-running capabilities desteklenebilir.
- MCP auth ve permission ayrı runtime policy ile yönetilir.

---

# 43. TUI: karmaşık runtime, sade kullanıcı deneyimi

Ana UX ilkesi:

> Kullanıcı runtime'ın karmaşıklığını görmek zorunda değil; fakat istediği anda neden-sonuç zincirini açabilmeli.

Ana ekran yaklaşık:

```text
┌ Project: api-server                  model: auto / opus │
│ Task: Add refresh-token rotation                         │
│ Status: EXECUTING · 3/5 subtasks                         │
├──────────────────────────────────────────────────────────┤
│ ✓ Located auth flow                                      │
│ ✓ Added token persistence                                │
│ → Updating refresh endpoint                              │
│ ○ Regression tests                                       │
│ ○ Final verification                                     │
│                                                          │
│ Agent: editing src/auth/refresh.ts                       │
│ Tests: 18 pass · 1 pending                               │
│ Diff: +84 -21 · 4 files                                  │
├──────────────────────────────────────────────────────────┤
│ > steer the agent…                                       │
└──────────────────────────────────────────────────────────┘
```

---

# 44. TUI temel davranışları

Kullanıcı alışkanlıkları korunmalı:

- normal prompt girişi,
- `@file` fuzzy reference,
- `/commands`,
- model switch,
- session/task resume,
- diff review,
- shell quick mode,
- undo/restore,
- steering while running,
- prompt queue,
- plan mode,
- compact status line.

Ama ana görünüm log yağmuruna dönüşmemeli.

---

# 45. Progressive disclosure

Varsayılan kullanıcı şunları görür:

- current task,
- phase,
- recent meaningful actions,
- tests,
- diff,
- pending approvals.

İleri kullanıcı:

```text
/context
/why
/evidence
/graph
/agents
/budget
/trace
/eval
```

ile iç sistemi açabilir.

---

# 46. `/why` birinci sınıf özellik olmalı

Kullanıcı:

```text
/why src/auth/session.ts
```

dediğinde:

```text
Included because:
1. exact symbol match: SessionStore
2. called by RefreshController
3. failing test touches same flow
4. co-changed with auth/token.ts in 12 commits

Evidence:
...
```

görebilmeli.

Bu Context MMU'nun güven oluşturması açısından önemlidir.

---

# 47. `/context` görünümü

Token sayısından daha faydalı bilgi:

```text
Working Set
  8 files
  14 symbols
  21 evidence refs

Context
  active evidence      18.2K
  task state            3.1K
  recent interaction    7.8K
  rules                 1.4K
  tools                 4.5K
  reserve              16.0K

External address space
  repo               ~8.2M
  history            ~21.6M
  archived task       ~4.1M
```

Bu kullanıcıya gerçekten "sonsuz context" mimarisinin ne yaptığını gösterir.

---

# 48. Session değil Task / Development Epoch

Chat session ürünün temel persistence birimi olmayacak.

Ana birimler:

```text
Project
Task
Development Epoch
Subtask
Execution
Checkpoint
```

Development Epoch:

- coherent work interval,
- sonunda verified checkpoint,
- geri dönülebilir,
- context yeniden kurulabilir.

Bir task günlerce sürebilir.

---

# 49. Resume

Resume:

```text
load chat transcript
```

değildir.

Resume:

```text
1. workspace fingerprint
2. task checkpoint
3. current repository graph/index
4. changed file versions
5. decisions/failures
6. open subtasks
7. revalidate stale evidence
8. compile fresh context
9. continue
```

demektir.

---

# 50. Eval Plane

Eval sonradan eklenen benchmark script'i olmayacak.

Runtime event modelinin içine baştan yerleştirilecek.

Her task trajectory:

```text
goal
initial_world_state
model_profile
context selections
retrievals
plans
tool calls
edits
failures
recovery actions
user interventions
verifier outputs
final state
cost
latency
```

olarak yapılandırılmış şekilde kaydedilebilir.

---

# 51. Eval türleri

## Outcome eval

- task solved?
- tests pass?
- acceptance criteria?
- regression?

## Process eval

- gereksiz file reads,
- repeated actions,
- plan churn,
- context waste,
- invalid tool calls,
- stale evidence usage.

## Context eval

- retrieval recall,
- localization accuracy,
- evidence freshness,
- compression loss,
- context utilization.

## Runtime eval

- recovery after crash,
- merge conflict behavior,
- scheduler efficiency,
- parallel speedup,
- deadlock avoidance.

## UX eval

- approval burden,
- user corrections,
- steering success,
- time-to-first-useful-action.

---

# 52. Learning / Data Engine

Doğru izin ve privacy politikasıyla başarılı kullanıcı task'ları çok değerli training data üretebilir.

Ama "başarılı final answer"dan daha değerli olan şey tam trajectory'dir.

Data örnekleri:

### SFT

İyi:

```text
state → next action
```

çiftleri.

### Preference data

Aynı state için:

```text
trajectory A > trajectory B
```

### Process reward

- doğru localization,
- doğru tool choice,
- test progress,
- invalid action avoidance.

### Context policy data

```text
hangi evidence seçimi başarı getirdi?
hangi compaction bilgi kaybettirdi?
```

### Planner data

```text
task → dependency-aware plan
```

### Router data

```text
task state → best model/tool strategy
```

---

# 53. RL / post-training için reward sinyalleri

Potansiyel objektif sinyaller:

```text
+ tests passed
+ build passed
+ acceptance criterion verified
+ failing tests reduced
+ correct localization
+ minimal diff
+ user accepted
+ no regressions
+ no policy violations

- repeated failed command
- stale evidence action
- unnecessary wide context
- invalid patch
- reverted edit
- user correction
- excessive cost
- unresolved regression
```

Reward hacking riskine karşı tek metric kullanılmaz.

---

# 54. Privacy-by-design training data

Kullanıcı kodu otomatik olarak training datasına dönüşmez.

Data plane:

```text
OFF
LOCAL_ONLY
ANONYMIZED_TELEMETRY
OPT_IN_TRAINING
ENTERPRISE_POLICY
```

gibi net modlara sahip olmalıdır.

Secrets, PII, proprietary code ve licensed content için filtreleme/policy gerekir.

Eval datası ile model-training datası ayrı saklanmalıdır.

---

# 55. Self-improving harness

Harness yalnız model fine-tune ederek gelişmez.

Öğrenilebilecek policy'ler:

- retrieval ranking,
- context packing,
- compaction timing,
- model routing,
- subtask decomposition,
- verifier selection,
- skill triggering,
- approval recommendation.

Ancak learned policy doğrudan yüksek riskli runtime authority olmamalı.

Policy önerir; invariants ve capability kernel sınırlar.

---

# 56. Context failure attribution

Bir task kötü sonuçlandıysa "model kötü" demek yeterli değildir.

Root cause taxonomy:

```text
localization failure
retrieval failure
stale context
missing context
bad tool description
planner failure
scheduler failure
edit failure
verification gap
model capability mismatch
user ambiguity
environment failure
```

Bu telemetry gelecekte harness'in kendi context katmanını iyileştirmesini sağlar.

---

# 57. Observability

Her execution dağıtık sistem gibi trace edilebilir.

Span'ler:

```text
task
planner
retrieval
context_compile
model_call
tool_call
proposal
validation
test
agent_spawn
agent_join
compaction
checkpoint
```

Metrics:

- tokens,
- cost,
- latency,
- context size,
- retrieval hit rate,
- file rereads,
- failed actions,
- test deltas,
- agent utilization.

---

# 58. Replay

Deterministik parçalar mümkün olduğunca replay edilebilmeli.

Bir failure sonrası:

```text
harness replay TASK-42 --until step:81
```

ile:

- task state,
- workspace snapshot,
- tool outputs,
- model inputs metadata

incelenebilmeli.

Model generation birebir deterministic olmak zorunda değildir; runtime state provenance korunur.

---

# 59. Güvenlik

Security modeli prompt tabanlı olamaz.

Katmanlar:

1. capability permissions,
2. sandbox,
3. filesystem scope,
4. network scope,
5. secret redaction,
6. command policy,
7. transaction layer,
8. audit log,
9. explicit approval boundaries.

Prompt injection gören model bu katmanları bypass edemez.

---

# 60. Context integrity / privacy

Memory'deki her bilgi her task'e verilmez.

Memory retrieval ayrıca scope policy uygular:

```text
project scope
task scope
user scope
sensitivity
allowed agents
```

"Hatırlıyor olmak" bilginin her bağlamda kullanılabileceği anlamına gelmez.

---

# 61. ContextOS'tan alınacaklar

ContextOS'taki güçlü fikirler:

## Al

- Context'i programatik external environment olarak ele alan RLM yönü.
- Hybrid retrieval fikri.
- Dependency graph.
- Blackboard.
- Proposal layer.
- Scope / negative context.
- Watchdog.
- Model adapters.
- MCP gateway.
- Context budgeting fikri.

## Yeniden tasarla

### Vector search

Gerçek global ANN veya doğru exhaustive retrieval.

### Graph

File-import graph'tan heterogeneous symbol graph'a.

### Context budget

Statikten task/model-aware dynamic policy'ye.

### Blackboard

Basit fact store'dan versioned Evidence Ledger'a.

### Proposal

Basit conflict check'ten workspace-versioned transaction'a.

### Watchdog

Standalone helper'dan runtime progress invariant'ına.

### RLM

Raw global context string yerine opaque context address space.

### Recursive budget

Her child için ayrı budget değil global Resource Ledger.

---

# 62. Context Manager'dan alınacaklar

Güçlü parçalar:

- BlobStore / MetadataStore / VectorStore ayrımı.
- Session/episodic/procedural memory fikri.
- Symbol index.
- RAG gateway abstraction.
- provider adapters.
- repository ingest.
- file watcher fikri.
- map/reduce/verify yardımcı workflow'u.

## Yeniden tasarla

- semantic-only retrieval yerine hybrid retrieval,
- memory'yi inference hot path'e gerçek anlamda bağla,
- file watcher'ı gerçek incremental index yap,
- verification'ı source-grounded yap,
- chunking'i AST-native hale getir,
- map/reduce/verify'i "RLM" olarak adlandırma; ayrı analysis primitive yap.

---

# 63. Chunking stratejisi

Arbitrary 500-char chunk yerine semantic unit.

Primary units:

```text
module preamble
function
method
class
interface
type
test
config block
doc section
```

Parent-child bilgisi korunur.

Model bir method aldığında gerektiğinde:

```text
parent class signature
imports
direct callees
tests
```

eklenebilir.

---

# 64. Structure peeking

Model büyük file'ı okumadan önce:

```text
outline
symbols
imports
exports
size
recent changes
test links
```

görebilir.

Sonra gerekli range'i page eder.

Bu agent tool ergonomisinin temel parçasıdır.

---

# 65. Runtime execution intelligence

Static graph her zaman yeterli değildir.

Uzun vadede runtime traces:

- actual call paths,
- test coverage,
- profiler traces,
- failing execution paths

Repository Intelligence'a ek sinyal olabilir.

Static "possibly related" ile dynamic "actually executed together" ayrılır.

---

# 66. Test selection

Her editte full test suite koşmak pahalı olabilir.

Impact graph:

```text
changed symbol
   ↓
dependents
   ↓
tests covering affected area
```

ile targeted test seçilir.

Final veya yüksek-risk boundary'de broader suite çalışır.

---

# 67. TUI'da plan ve autonomy

Kullanıcı autonomy seviyesini task bazlı değiştirebilir:

```text
review
guided
auto
```

Ancak bunlar basit approval flag'leri değil policy presets'tir.

### review

Edit öncesi approval.

### guided

Normal low-risk execution otomatik; major proposal approval.

### auto

Sandbox/capability sınırlarında autonomous.

Irreversible external actions ayrıca policy'ye tabidir.

---

# 68. Steering while running

Kullanıcı agent çalışırken:

```text
"database migration yapma"
"önce testleri düzelt"
"şu dosyaya dokunma"
```

diyebilir.

Bu mesaj sadece transcript'e eklenmez.

Runtime:

1. constraint çıkarır,
2. TaskState'e yazar,
3. gerekiyorsa current agents'i interrupt eder,
4. leases'i günceller,
5. planı yeniden değerlendirir.

---

# 69. User correction → enforcement

Bir kullanıcı tekrar tekrar:

```text
"pnpm kullan, npm kullanma"
```

diyorsa bunu yalnız memory'de prose olarak tutmak yerine project/user rule'a dönüştürme sistemi olabilir.

Örnek:

```yaml
rule:
  command: "npm *"
  action: deny
  replacement: "pnpm"
scope: project
source: user_correction
```

Kullanıcı doğrularsa future runtime enforcement olur.

---

# 70. Minimalism ilkesi

Dünyanın en iyi harness'i = en fazla feature değil.

Her özellik şu testten geçmeli:

```text
Bu özellik:
- task success artırıyor mu?
- context quality artırıyor mu?
- reliability artırıyor mu?
- UX friction azaltıyor mu?
```

Hayırsa core'a girmez.

Fancy multi-agent visualizations, onlarca panel, gereksiz persona agent'ları core ürünün parçası olmamalı.

---

# 71. Anti-pattern'ler

Sistem bilinçli olarak şunlardan kaçınmalı:

- tüm repo'yu prompt'a dump etmek,
- yalnız embedding RAG,
- transcript'i memory saymak,
- her task'i planlamak,
- her task'te multi-agent,
- serbest metin agent-to-agent sohbeti,
- child agents'e bağımsız sınırsız budget,
- modelin "done" kararına güvenmek,
- compaction sonrası doğrulama yapmamak,
- stale evidence'i current kabul etmek,
- MCP'yi core runtime haline getirmek,
- kullanıcıyı her low-risk tool call'da onaya boğmak,
- eval'i yalnız final pass rate'e indirgemek,
- kullanıcı verisini varsayılan olarak training datasına çevirmek.

---

# 72. Bir task nasıl akar?

Kullanıcı:

```text
"Refresh token rotation ekle ve eski token reuse attack'ını önle."
```

## 1. Intake

Runtime goal, constraints ve risk tipini çıkarır.

## 2. Localization

- lexical search: "refresh", "token"
- symbol index
- semantic retrieval
- graph expansion
- git history memory

## 3. Context compile

Model yalnız gerekli architecture/evidence ile çağrılır.

## 4. Planning decision

Task repository-level olduğundan structured plan oluşturulur.

## 5. Plan graph

```text
A: token model/storage
B: rotation logic
C: reuse detection
D: API endpoint
E: tests
```

Dependencies belirlenir.

## 6. Scheduling

A tamamlanmadan B/C başlamaz.

Endpoint ve tests uygun noktalarda paralel olabilir.

## 7. Agent leases

Her agent'a sınırlı context + write scope.

## 8. Proposals

Edits shadow workspace'te uygulanır.

## 9. Re-index

Modified symbols ve graph incrementally güncellenir.

Eski evidence invalidate edilir.

## 10. Verification

- typecheck
- auth tests
- reuse security scenarios
- regression tests

## 11. Replan

Failure varsa root cause evidence ile yeni plan branch.

## 12. Final verifier

Acceptance criteria + invariants.

## 13. Checkpoint

Typed state yazılır.

## 14. Eval

Trajectory metrics hesaplanır.

## 15. Learning candidate

User izin veriyorsa success/failure trajectory training/eval dataset'e aday olabilir.

---

# 73. Halüsinasyonu nasıl azaltıyoruz?

"Halüsinasyon olmayacak" mutlak olarak garanti edilemez.

Ama sistem halüsinasyonun etkisini ciddi şekilde sınırlayabilir.

Katmanlar:

```text
exact source retrieval
versioned evidence
typed outputs
schema validation
transactional edits
compiler/tests
source-grounded verifier
runtime invariants
capability restrictions
stale data invalidation
```

Model yanlış düşünebilir.

Ama yanlış düşüncenin doğrulanmadan source of truth haline gelmesine izin verilmez.

---

# 74. "Sınırsız context hissi" nasıl ortaya çıkıyor?

Kullanıcı/model açısından:

```text
"Geçen hafta bu modülde neden Redis tercih etmiştik?"
```

Context window'da bilgi yok.

MMU:

1. task/history memory arar,
2. ilgili Decision Ledger entry'sini bulur,
3. kaynak commit/evidence'i getirir,
4. current state ile validity kontrolü yapar,
5. birkaç yüz tokenlık evidence'i page eder.

Model cevap verir.

Dolayısıyla fiziksel context sınırlıdır; **logical context address space** büyük ve kalıcıdır.

---

# 75. Araştırma hattından alınan ana dersler

Bu mimari tek bir paper'ın implementasyonu değildir. Birden fazla araştırma yönünün sentezidir.

## MemGPT (2023)

Ders:

> Context window'u tek bellek olarak görme; hiyerarşik/virtual memory düşün.

Bizdeki karşılığı:

- Context MMU,
- L0–L5 memory tiers,
- paging.

Kaynak:  
https://arxiv.org/abs/2310.08560

---

## RepoCoder (EMNLP 2023)

Ders:

> Retrieval tek atımlı olmak zorunda değil; generation/analysis yeni retrieval sinyalleri üretebilir.

Bizdeki karşılığı:

- iterative retrieval,
- task-state-aware search.

Kaynak:  
https://aclanthology.org/2023.emnlp-main.151/

---

## CodePlan (2023/2024)

Ders:

> Repository-level coding dependency-aware, incremental planlama gerektirebilir.

Bizdeki karşılığı:

- Plan Graph,
- impact analysis,
- dependency scheduler.

Kaynak:  
https://www.microsoft.com/en-us/research/publication/codeplan-repository-level-coding-using-llms-and-planning-2/

---

## SWE-agent / Agent-Computer Interface (2024)

Ders:

> Model kadar agent'ın bilgisayarla etkileştiği interface de performansı belirler.

Bizdeki karşılığı:

- native hot-path coding tools,
- structure peeking,
- concise tool outputs,
- bounded edit/test interface.

Kaynak:  
https://arxiv.org/abs/2405.15793

---

## LocAgent (ACL 2025)

Ders:

> Code localization heterogeneous graph ve multi-hop reasoning'den ciddi fayda görebilir.

Bizdeki karşılığı:

- symbol-level repository graph,
- graph expansion retrieval.

Kaynak:  
https://aclanthology.org/2025.acl-long.426/

---

## Context as a Tool / CAT (2025)

Ders:

> Context management passive bir compression heuristic değil, agent'ın aktif yönetebildiği bir capability olabilir.

Bizdeki karşılığı:

- context tools,
- semantic checkpoints,
- task/context workspace ayrımı.

Kaynak:  
https://arxiv.org/abs/2512.22087

---

## Recursive Language Models / RLM (2025)

Ders:

> Büyük context'i prompt'a sığdırmak yerine external environment olarak programatik incelemek ölçeklenebilir.

Bizdeki karşılığı:

- opaque context address space,
- search/read/page operations,
- recursive scoped analysis.

Kaynak:  
https://arxiv.org/abs/2512.24601

---

## Repository Memory (ICLR 2026)

Ders:

> Commit history ve repository evolution coding agent için uzun dönem hafıza olarak kullanılabilir.

Bizdeki karşılığı:

- git/repository memory,
- co-change,
- historical fix signals.

Kaynak:  
https://proceedings.iclr.cc/paper_files/paper/2026/hash/b4c06f095368497f3ac19422efef8133-Abstract-Conference.html

---

## Repository Planning Graph / RPG (ICLR 2026)

Ders:

> Serbest doğal dil planları yerine explicit graph blueprint büyük repository üretiminde daha tutarlı olabilir.

Bizdeki karşılığı:

- structured Plan Graph,
- dependency contracts,
- proposal/implementation separation.

Kaynak:  
https://proceedings.iclr.cc/paper_files/paper/2026/hash/9482f45fdd89aba9130bb04c44f788a9-Abstract-Conference.html

---

## SWERank (ICLR 2026)

Ders:

> Her localization problemi pahalı agent loop gerektirmez; güçlü retrieve/rerank sistemleri hızlı ve ucuz front-end olabilir.

Bizdeki karşılığı:

- deterministic/cheap localization first,
- model reasoning only when necessary.

Kaynak:  
https://proceedings.iclr.cc/paper_files/paper/2026/hash/7f6901ebab786e43b21530328fc989ca-Abstract-Conference.html

---

## Context compression reliability (2026)

Ders:

> Compaction yürütme davranışını bozabilir; summary kalitesi yalnız metin kalitesiyle ölçülmemeli.

Bizdeki karşılığı:

- compaction verifier,
- boundary-local eval,
- typed checkpoint.

Kaynak:  
https://arxiv.org/abs/2608.06503

---

## Runtime enforcement from user corrections / TRACE (2026)

Ders:

> Bir tercihi memory'de hatırlamak, ona uymayı garanti etmez; bazı corrections runtime rule'a derlenebilir.

Bizdeki karşılığı:

- correction → project/user rule,
- enforcement before completion.

Kaynak:  
https://arxiv.org/abs/2606.13174

---

## Trajectory Attribution for Context Engineering / TRACE (2026)

Ders:

> Agent trajectory'leri context source hatalarını teşhis etmek ve iyileştirmek için kullanılabilir.

Bizdeki karşılığı:

- context failure attribution,
- self-improving retrieval/prompt/tool descriptions.

Kaynak:  
https://arxiv.org/abs/2608.09153

---

# 76. Mevcut coding CLI ürünlerinden alınacak ürün dersleri

Bu mimari kopya değil; başarılı UX/runtimes'dan ders çıkarır.

## Claude Code

Alınacak ilkeler:

- interactive + headless kullanım,
- resume/continue,
- permission modes,
- plan mode,
- bounded turns,
- güçlü terminal-first workflow.

Referans:  
https://docs.anthropic.com/en/docs/claude-code/cli-usage

---

## Codex CLI

Alınacak ilkeler:

- sandbox + approvals birlikteliği,
- düşük riskli eylemlerde az friction,
- riskli sınır geçişlerinde review,
- local terminal-centric execution.

Referans:  
https://openai.com/index/running-codex-safely/

---

## OpenCode

Alınacak ilkeler:

- model/provider bağımsız UX,
- full TUI + minimal mode,
- steer while running,
- queued prompts,
- session/fork ergonomisi,
- `@file`,
- slash commands,
- permission rules,
- background service yaklaşımı.

Referanslar:  
https://opencode.ai/v2/docs/cli  
https://opencode.ai/v2/docs/cli/tui/  
https://opencode.ai/v2/docs/permissions

---

## Gemini CLI

Alınacak ilkeler:

- checkpointing,
- shadow state,
- restore,
- sandboxing,
- headless automation.

Referans:  
https://google-gemini.github.io/gemini-cli/docs/cli/checkpointing.html

---

## MCP

Alınacak ilkeler:

- tool/resource/prompt interoperability,
- external integration ecosystem,
- güncel spec'in stateless-core yönü ve uzun-running task extension yaklaşımı.

Ama MCP core state manager değildir.

Referans:  
https://blog.modelcontextprotocol.io/posts/2026-07-28/  
https://modelcontextprotocol.io/specification/draft/server/index

---

# 77. Neden bu mimari mevcut harness'lardan ayrışır?

Fark tek bir özellik değildir.

Birleşimdir:

```text
Model-agnostic
+
Persistent engineering runtime
+
Context MMU
+
Live versioned evidence
+
Hybrid repository intelligence
+
Dependency-aware orchestration
+
Transactional editing
+
Verification-defined completion
+
Eval/learning plane
+
Minimal TUI
```

Birçok sistem bunların birkaçına sahip olabilir.

Hedef bu parçaların tek bir state modelinde birbirini tamamlamasıdır.

---

# 78. North Star metrikleri

Başarı yalnız SWE-bench score değildir.

## Reliability

- verified task success,
- rerun stability,
- recovery success.

## Context

- localization recall,
- stale evidence rate,
- context tokens per solved task,
- compaction recovery quality.

## Engineering

- regression rate,
- diff correctness,
- unnecessary edits,
- conflict rate.

## Runtime

- wall-clock time,
- tool efficiency,
- parallel speedup,
- loop rate.

## Cost

- tokens,
- API cost,
- local compute.

## UX

- approval prompts/task,
- user corrections/task,
- intervention recovery,
- time-to-useful-result.

---

# 79. Eval suites

Harness kendisini farklı boyutlarda test etmeli.

- SWE-bench Verified / Live benzeri issue fixing,
- repository generation,
- long-horizon multi-file refactors,
- dependency migrations,
- hidden regression tests,
- context stress tests,
- interruption/resume tests,
- model-switch tests,
- local-small-model tests,
- parallel agent conflict tests,
- stale-index adversarial tests,
- compaction boundary tests.

Ayrıca kendi synthetic runtime eval'lerimiz olmalı.

---

# 80. Model-independent benchmark

Aynı harness:

```text
small local model
mid-tier API model
frontier model
```

ile test edilir.

Ama soru yalnız:

```text
"hangi model daha iyi?"
```

değil:

```text
"harness, model kapasitesindeki düşüşü ne kadar telafi ediyor?"
```

olmalıdır.

Bu ürünün gerçek iddiasını ölçer.

---

# 81. Core data model

Minimum kalıcı objeler:

```text
Project
RepositorySnapshot
Task
DevelopmentEpoch
Subtask
PlanNode
AgentRun
AgentLease
ModelProfile
Evidence
Fact
Decision
Hypothesis
Failure
Proposal
Patch
ToolCall
TestResult
Checkpoint
EvalResult
Skill
Rule
ResourceLedger
```

Bu objeler birbiriyle ID/reference üzerinden bağlıdır.

---

# 82. Event log

Runtime event-sourced yaklaşım kullanabilir.

Örnek:

```text
TaskCreated
PlanGenerated
SubtaskStarted
EvidenceAdded
FileRead
ProposalCreated
ProposalApplied
FileVersionChanged
EvidenceInvalidated
TestExecuted
DecisionRecorded
CompactionStarted
CheckpointCommitted
TaskVerified
```

Current state event log'dan veya materialized views'tan üretilebilir.

Avantaj:

- audit,
- recovery,
- replay,
- eval extraction,
- debugging.

---

# 83. Storage yaklaşımı

Tek database dogması yok.

Mantıksal ayrım:

```text
Metadata Store
Artifact / Blob Store
Search Index
Vector Index
Graph Store
Event Log
Cache
```

Başlangıçta bunların çoğu SQLite/local file üzerinde çalışabilir.

Mimari dağıtık database zorunlu kılmaz.

CLI için local-first önemlidir.

---

# 84. Local-first

Varsayılan:

- task state local,
- index local,
- history local,
- tool execution local,
- secrets local.

Remote modele yalnız context compiler'ın seçtiği minimum gerekli içerik gönderilir.

Lokal model kullanıldığında tamamen offline çalışma mümkün olmalıdır.

---

# 85. Server / daemon

TUI ile runtime process'i ayrılabilir.

```text
TUI
  │
  ▼
Local daemon/runtime
  ├── task state
  ├── index
  ├── agents
  └── tools
```

Avantaj:

- TUI kapanabilir,
- task devam edebilir,
- başka terminal attach olabilir,
- headless/CI aynı runtime'ı kullanabilir.

Ama basit kullanım için single-process mode korunabilir.

---

# 86. API / headless

CLI yalnız insan arayüzü değildir.

```text
harness run "fix issue..."
harness task status
harness eval ...
```

gibi headless JSON/JSONL streaming mode olmalıdır.

CI, IDE ve external automation aynı runtime'ı çağırabilir.

---

# 87. Extensibility

Core küçük kalmalı.

Extension noktaları:

```text
ModelAdapter
ToolProvider
Retriever
Ranker
Verifier
Skill
Policy
UIExtension
MCP bridge
Eval suite
```

Ancak extension plugin core invariants'i bypass edemez.

---

# 88. "Mükemmel harness" için felsefe

Bu sistemin üstünlüğü daha çok prompt yazmaktan gelmemeli.

Şuradan gelmeli:

### Doğru state

Model ne olduğunu bilir.

### Doğru evidence

Model neden bildiğini bilir.

### Doğru scope

Model neye dokunabileceğini bilir.

### Doğru runtime

Modelin hata yapabileceği varsayılır.

### Doğru verification

Başarı objektif olarak ölçülür.

### Doğru context

Modelin penceresine yalnız gerekli bilgi girer.

### Doğru UX

Kullanıcı sistemi yönetmek zorunda kalmaz.

---

# 89. Kısa mimari özeti

```text
                          USER
                           │
                           ▼
                     Minimal TUI
                           │
                           ▼
                  Task Runtime Kernel
                           │
          ┌────────────────┼─────────────────┐
          │                │                 │
          ▼                ▼                 ▼
    Orchestrator      Context MMU       Capability Kernel
          │                │                 │
          ▼                ▼                 ▼
      Scheduler      Context Compiler      Tools/MCP
          │                │                 │
          └─────────┬──────┴───────┬─────────┘
                    ▼              ▼
                Model Fabric   Workspace
                    │              │
                    ▼              ▼
                 Actions       Proposal Tx
                    │              │
                    └──────┬───────┘
                           ▼
                       Verifier
                           │
                 ┌─────────┴─────────┐
                 ▼                   ▼
             Task State           Eval/Learn
```

---

# 90. Sonuç

Hedefimiz tek tek Claude Code, Codex, OpenCode veya Gemini CLI'nin bir kopyasını yapmak değildir.

Hedef:

> Bugünkü coding agent ürünlerinde dağınık halde bulunan iyi fikirleri; 2023–2026 arasındaki context, memory, repository localization, planning, agent-computer interface ve long-horizon agent araştırmalarındaki kanıtlarla birleştirip; modelden bağımsız, kalıcı, doğrulanabilir ve sade bir Software Engineering Runtime oluşturmaktır.

Bu runtime'ın merkezinde **Context MMU** vardır.

Context MMU'nun amacı büyük prompt oluşturmak değildir.

Amacı:

> **Her model çağrısında, o anda gerekli olan en doğru, en güncel ve en yüksek sinyalli mühendislik working set'ini üretirken; geri kalan bilgi evrenini güvenli ve adreslenebilir biçimde erişilebilir tutmaktır.**

Böylece:

```text
finite model context
        +
versioned external engineering memory
        +
programmatic retrieval
        +
structured task state
        +
verification
        =
practically unbounded long-horizon engineering context
```

Agent'ın "zekâsı" modelden gelir.

Ama **hafızası, disiplini, güvenilirliği, araçları, sürekliliği ve mühendislik süreci harness'tan gelir.**

İyi bir model harness'ı daha güçlü yapar.

İyi bir harness ise modeli değiştirilebilir hale getirir.

Ve bu projenin asıl amacı budur.
