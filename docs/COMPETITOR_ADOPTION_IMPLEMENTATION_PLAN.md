# Proje belleği, context ve kullanıcı akışı uygulama planı

12 Ekim 2026. Başlangıç `5e6a006`; girdi [karşılaştırmalı denetim](COMPARATIVE_AUDIT_2026_10_12.md) ve kullanıcının Codex/Claude Code alışkanlığına uygun TUI isteğidir. Rakiplerin fikirleri mevcut kernel, captured candidate, privacy lineage ve shared budget üzerine uygulanır. Özellik adları veya sadece interface eklemek kabul değildir.

## Çalışma yöntemi

TUI/CLI entegrasyonu ve birleşik commit ana ajanda. Instruction discovery, memory ve read-only worker hatları ayrı dosya sahipliğiyle paralel ilerler; shared Document/record/compiler blokları koordine edilir. Kullanıcının dosya silmeleri ve PRD_STATUS değişikliği stage edilmez. Yerel full suite tekrarı yerine entegrasyon sonunda anlamlı focused kontroller, build/vet ve mandatory exact-source CI kullanılır. Testler kaldırılmaz; düşük etkili UI metinleri için yeni test yazılmaz. Klavye/paste, context yetkisi ve durable bütçe/gizlilik değişiklikleri için güvenlik açısından gerekli kontroller korunur.

## 1. Terminal kullanıcı akışı — FR-12/19/22/23/25

- `/` ile görünür ve filtrelenen komut paleti; tek katalogdan menu/help açıklamaları. ↑/↓ seçer, Tab tamamlar, Enter seçimi girişe koyar; açıkça tamamlanan komut ikinci Enter ile çalışır. Tam komut yazıp Enter mevcut doğrudan akışı korur. Esc kapatır. Paste komut çalıştırmaz.
- Argument isteyen komutlar literal `ID` placeholder'ını çalıştırmaz; sadece komut ve boşluk girişe eklenir, usage ayrı gösterilir. Komut seçimi authority veya approval üretmez.
- Görünür caret, viewport içinde yatay input kaydırma ve Unicode metin; OSC/escape/control verisi ekrana güvenli alıntı olarak girer. Dar terminalde palet görünür kalır; `--line` erişilebilir alternatif korunur.
- Plan JSON'u yerine aktif iş özeti; `/status`, `/diff`, `/requests`, `/respond`, `/model`, `/context`, `/memory`, `/workers` için açıklamalı keşif. Prompt kaybı olmadan pending kontrol; Ctrl+C pause/cancel, detach arka plan işini durdurmaz.

Kapanış: canlı editor seçim/Enter/Tab/Esc/paste akışı ve terminal-bound viewport, owner-backed line komutları, README gerçek tuşları açıklar. Bu native PTY/platform saldırı matrisinin bütünü yerine sayılmaz.

## 2. Captured proje talimatları — FR-01/08/14/17

Root→hedef dizin `AGENTS.override.md`/`AGENTS.md` zinciri yalnız captured byte'lardan çıkarılır. Source/candidate/hash/span ve current read-policy ile bağlıdır; küçük total byte/token sınırı, açık PARTIAL/denied/stale sonuç vardır. Repo metni spec/policy, remote inference, shell, approval veya verification yetkisini artıramaz. Global operatör yetkisi ile repo style guidance ayrı tutulur. Gerçek model request, context manifest ve token preflight'e eklenir; compaction/model-switch aynı maliyeti görür.

Kapanış: gerçek Session.Run compiler entegrasyonu, override/ancestor sırası, sensitive/denied/stale reddi, manifestin exact instruction byte'larını içermesi. Yalnız model tool açıklaması eklemek bu işin kapanışı değildir.

## 3. Kaynağa bağlı proje belleği — FR-17/26/29/39

Fact/decision/convention kayıtları ordinary task-scoped immutable artifact ve typed Document refs üzerinde append-only log olarak tutulur; yeni unmanaged SQLite/index yaratılmaz. Mutation owner/privacy lease altında journal'a bağlanır. Search aynı store ve exact project scope içindeki readable task kayıtlarını bounded inventory ile birleştirir. Source/candidate/doc/policy/lineage değişikliği ACTIVE/STALE/CONFLICT/RETIRED olarak görünür. Asserted bilgi, observed byte ve bağımsız verification birbirine çevrilmez.

CLI/TUI record/search/correct/retire kullanıcı tarafından açıkça yapılır. GC/backup/restore/deletion closure refs'i taşır; origin task deletion belleği de kaldırır, copies lineage ile bound kalır. Retired kaydı raw purge veya retention yerine sunulmaz. Eski decoder yeni memory-bearing Document desteklemiyorsa fail-closed; eski canonical docs değişmez. Automatic prompt injection yalnız current-source/policy/budget ve retirement kontrolleri tamamlandığında açılır.

Kapanış: oturumlar arası aynı proje read/search, stale ve conflict, exact kullanıcı correction/retirement, backup/GC/privacy integration, CLI/TUI wiring. Sadece skill registry'ye kayıt koymak veya host dizinine JSON yazmak yeterli değildir.

## 4. Bounded salt okuma koordinasyonu — FR-33–35

İlk kullanılabilir hat 2–4 sabit source analyzer: inventory, Go graph hints, requirements, search. Exact immutable candidate ve read policy; typed/deterministic sonuç ve digest; cancellation/byte/output/time sınırı. Dispatch öncesi typed kernel admission shared parent steps/tool-call/active-time havuzunu child holds ile birlikte kontrol eder. Crash/cancel admitted ceiling'i konservatif charged tutar; aynı command iki kez admission oluşturamaz.

Bu hat model subagent, isolated external process veya full AgentLease değildir. Tam F1 için TTL/clock domain/generation/scoped lease + typed mailbox + read-only model worker gerekir. F2 için candidate writers, gerçek mutex, seri integration ve her merge sonrası fresh verification tamamlanır. Mevcut typed single-worker DAG ve shared parent budget yeniden yazılmaz.

Kapanış: gerçek owner/CLI/TUI çağrısı, budget/policy/source/operation bindings, cancellation ve duplicate admission, structured sonuç. Goroutine paralelliği tek başına multi-agent tamamlanması sayılmaz.

## 5. Context geri bildirimi — FR-14/28–31/40, NFR-08/12

Mevcut exact spans/cache evidence/context-why ve DEV/HOLDOUT ranking üzerine observation→context→tool outcome kaydı; token/latency/cache/relevance ve independent task success ayrımı. Future leakage, holdout mixing ve model self-report gold etiketi reddedilir. Ranking required check veya policy'yi azaltamaz. Referans hardware/p95 ve bağımsız ablation ayrı dış kabul olarak kaydedilir.

## 6. Kalan anahtarsız çekirdek kapanışı

Bu turun yeni özellikleri aşağıdaki açıkları kapandı diye yeniden etiketlemez:

1. Bütün catalog/anchor/UNKNOWN/legacy owner retirement ve interrupted restore/deletion yaşam döngüsü.
2. Bütün mutable metadata/bootstrap/control/fence writer'ları için gerçek physical lifetime accounting ve kapasite kabulü.
3. Genel protected runtime verification/V0–V5 ve bütün hostile interval matrisi.
4. TS/JS/Python resolved graph/LSP ve runtime trace/coverage; conservative check selection korunur.
5. Tam typed multi-worker DAG/AgentLease/mutex/integration/measured routing/external workers.
6. Authenticated central team policy/remote executor ve tenant recovery.
7. OS signing/notarization ve farklı schema/reducer için açık migration/downgrade kabulü. Uyumlu formatta actual cross-revision install→upgrade→rollback→upgrade üç native platformda `5e6a006` CI/public readback ile kapandı.
8. Native PTY/paste/resize/reconnect recovery ve independent pilot/reference performance.

Her başlık mevcut [PRD teslim planının](KEYLESS_DELIVERY_PLAN.md) acceptance koşullarıyla kapatılır; desteklenmeyen profil destekli diye açılmaz. Linux/macOS patch fallback PRD §13.3'ün geçerli teslim profilidir.

## Anahtar ve dış kabul ayrımı

Yazılım başlıkları model API anahtarı istemez. Gerçek OpenAI/Anthropic ücretli endpoint conformance seçildiğinde API anahtarı gerekir. Eligible ChatGPT hesap/workspace consent, Ollama weights/endpoint/hardware, OS signing identity ve independent pilot API anahtarı işi değildir; uygun dış erişim/ortam gerektirir. Engineering CI, fixtures veya feature varlığı stable A–D kapılarını açmaz. %100 tamamlanma ancak bütün ilgili yazılım ve uygulanabilir gerçek kabul kanıtları mevcutken ilan edilir.

## Uygulanan dilim ve devam sırası

1–4'ün kullanıcıya açık sınırlı profilleri kodlandı: slash palette/caret/Unicode/paging, `/compact /clear /exit`, captured instructions gerçek model compiler'da, durable project memory ve origin-only mutation/privacy closure, parent bütçesiyle sabit source analyzers. CLI, owner ve TUI aynı API'lere bağlıdır. Otomatik memory context aynı görevle sınırlıdır; cross-task manual search desteklenir. Capability taşımayan eski pakete seçim yeni feature kullanılmış store'da owner lock altında reddedilir. Eski schema/reducer sessizce değiştirilmez. Focused kontroller/build/vet ve source CI kabulü [VALIDATION](VALIDATION.md) içinde ayrı tutulur.

5'in bağımsız effectiveness/reference benchmark'ı ve 6'nın geniş lifecycle/physical accounting/protected verification/full multiworker/team kapsamı açık kalır. Sıradaki çekirdek çalışmalar existing writer inventory/lineage ve typed scheduler üzerine uygulanır; bu turun sabit analiz araçları full AgentLease/isolated writer kapanışı diye sayılmaz. Public `5e6a006` önceki kabul edilmiş runtime'dır; yeni main özellikleri kendi exact-source CI ve publisher kabulünden sonra indirilebilir olur.
