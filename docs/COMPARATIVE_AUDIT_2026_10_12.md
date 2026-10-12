# WrongStack/Codex karşılaştırması ve anahtarsız PRD durumu

12 Ekim 2026. İncelenen main `de66bbfd5319bad1e3bc095ad16ee332dc3edbbc`; yayımlanan engineering runtime `90bc97ebc9318e3542ffbc2507b8cb346dae69e5`. Girdi kullanıcının “WrongStack vs Viber vs Codex CLI” raporudur. Raporun puanları ve Git ağacı büyüklükleri ölçülmüş başarı, performans veya güvenlik karşılaştırması değildir. Bu belge tamamlanma yüzdesi üretmez; çalışan profil, eksik yazılım ve eksik dış kabulü ayırır.

## CI: yeni main başarısız, önceki public sürüm başarılı

[Core38198680928](https://github.com/ixayldz/Viber/actions/runs/38198680928) FAILURE. Dokuz foundation öncesi iş başarılı: macOS/Ubuntu testleri, Docker, rootless, iki vault ve üç paket build'i. Windows test işi `Migration regression` adımında FAILED; foundation doğru kapandı, package signing ve managed installer/installation signing atlandı. Bu commit için cross-revision upgrade/rollback **NOT_RUN**; hazır baseline veya unsigned package build'i bu kabulün yerine geçmez.

Windows migration komutu bütün paket testleri ve üç tekrar için ortak `-timeout 5m` kullanıyordu. Agent migration'ın ilk iki turunda success ve rejection parent'ları geçti. Üçüncü turun `corrupt-backup` alt testi 03:45:19.971 UTC'de başladı; 03:45:24.248'de paket 300.094s toplam sürede timeout oldu. Stack fixture hazırlığında `FlushFileBuffers → File.Sync → fileguard.Publish → Archive.PutControlBytes → Session.runLocked` gösteriyor. Alt test yalnız yaklaşık dört saniyedir çalışıyordu; bu stack tek başına deadlock veya bozuk backup'ın kabul edildiği kanıtı değildir. Windows full-suite validator daha önce 992 PASS / 24 SKIP / 0 FAIL raporladı; migration failure bu sonucu veya aggregate CI failure'ı değiştirmez.

Takip düzeltmesi `scripts/migration-regression.py`: aynı üç package'daki aynı regex'e uyan bütün Test/Fuzz/Example parent'ları Go envanterinden keşfedilir. Her parent kendi süreçte **üç tekrar ve beş dakika** sınırıyla çalışır. Süreçler seri; failure sonraki çalışmayı durdurur. JSONL ve mandatory migration validator korunur. Test silinmedi, `-count=3` azaltılmadı, runtime durability/Sync değişmedi. Yeni SHA'nın gerçek CI kabulü ayrıca gerekir; eski failure rerun ile silinmez.

Yeni runner native Windows odaklı migration turunda geçti: 15 parent'ın 45 root execution'ı (42 PASS, yerel symlink oluşturma yetkisi olmayan canonical backup alias parent'ı için 3 SKIP, 0 FAIL), beş mandatory parent için kanıt validator PASS. SKIP alias kabulüne dönüştürülmez. Yeni test/full yerel suite eklenmedi. Bu yerel kanıt hosted Windows CI'ın veya cross-revision kurulumun sonucu değildir.

[Public90bc97](https://github.com/ixayldz/Viber/releases/tag/engineering-90bc97ebc9318e3542ffbc2507b8cb346dae69e5) farklı exact source'tur: 16/16 başarılı CI, üç native yönetilen kurulum/ilk offline görev/reinstall/aynı revision rollback/owned uninstall; 11 public dosya anonim indirme/checksum ve dokuz exact-source OIDC subject doğrulaması geçti. İndirilebilir paket bu kabul edilen kaynakta kalır; başarısız main aynı kabulü miras almaz. `doctor.release_ready=false` devam eder.

## Rapordaki düzeltmeler

| Raporun değerlendirmesi | Kaynak koduna göre ayrım |
|---|---|
| Viber'da typed DAG yok | `internal/plan/plan.go` typed node, dependency contract, read/write scope, mutex isimleri, cycle kontrolü ve candidate/policy binding taşır. Çalışma **tek worker/tek writer** ile sınırlıdır. Eksik olan çoklu worker scheduler, lease/TTL/generation, mutex enforcement ve seri integration'dır. |
| Genel plugin izolasyonu uygulanmamış | Genel plugin profili kapanmadı; ancak pinned, her çağrısı onaylı, read-only/network-none MCP Session/CLI yolu ve gerçek Docker/protocol kabulü mevcut. “Hiç extension runtime yok” artık eski bilgidir. |
| Bellek/feedback sıfırdan kurulmalı | Task history/compaction/source pins, proje kapsamlı sürümlü skill/evaluator receipts ve source-bound DEV/HOLDOUT ranking mevcut. Bunlar genel oturumlar arası fact/decision belleği veya bağımsız relevance ölçümü değildir. |
| İki farklı revision upgrade eksik | Doğru. İlk uygun public baseline pinlendi fakat yeni main'in Windows migration failure'ı installer acceptance'ı engelledi. |
| Codex multi-agent kanıtı Agents API sayfasıyla açıklanıyor | Agents API ve Codex CLI ayrı ürün yüzeyleridir. CLI subagents ve instruction discovery doğrudan ilgili resmî Codex belgeleriyle doğrulandı. |
| 9/10 teslimat bütünlüğü, 7/10 context vb. | Öznel değerlendirmedir. Ortak saldırı/kalite/hardware deneyi olmadan ürün güvenlik veya hız sıralaması kabul edilmez. |

## Alınacak özellikler ve Viber'a uyarlama

Bu maddeler **seçilmiş geliştirme yönüdür**, bu inceleme turunda uygulanmış özellik diye sunulmaz. Mevcut runtime ve güvenlik kapıları önceliklidir. Rakip kodu veya bağımlılığı eklenmedi.

| Öncelik / fikir | Viber'da mevcut temel | Eklenecek davranış ve kabul | PRD |
|---|---|---|---|
| P1 — SAGE'den kaynağa bağlı proje belleği | Source pins, task history, project-scoped skills/evaluation receipt | Fact/decision/convention kayıtları exact project/source/candidate/span/policy/lineage'a bağlanır. ACTIVE/STALE/CONFLICT/RETIRED durumu, explicit kullanıcı düzeltme/silme, kota ve expiry olur. Kaynak değişince kayıt yeniden doğrulanmadan otomatik context'e alınmaz. Başka projeye taşıma varsayılan kapalı; kayıt bir izin veya yeni verification PASS olamaz. | FR-17/26/29/39, NFR-06/09/11 |
| P1 — Codex'ten kapsamlı instruction discovery | Bounded source read ve `SOURCE_BYTES_NOT_AUTHORITY` pins | Yalnız captured ve policy'nin izin verdiği root→hedef dizin instruction zinciri, exact digest/span, küçük toplam byte/token sınırı ve inclusion explanation. Global/operator talimatı repo talimatından ayrı authority origin taşır. Repo dosyası remote inference/shell/approval yetkisini artıramaz. Stale/büyük/duyarlı instruction fail-closed veya açık PARTIAL olur. | FR-01/08/14/17, NFR-02/09 |
| P2 — Director/mailbox ve Codex subagents'ten sınırlı worker koordinasyonu | Typed single-worker plan, durable goal queue, sealed parent allocation, owner IPC | Önce 2–4 read-only discovery/review worker. Scoped AgentLease, TTL/clock domain/generation, typed bounded mailbox ve cancellation. Unknown/expired child bütçesi ve etkisi uzlaşmadan yeniden kullanılmaz. Isolated writer ancak mutex ve seri integration tamamlandıktan sonra açılır; merge yeni candidate/verification üretir. Role isimleri güvenlik primitive'i değildir. | FR-33–37, NFR-01/02/06 |
| P3 — Ölçülen context geri bildirimi | `context-why`, exact spans/cache evidence, intent retrieval ve DEV/HOLDOUT ranking | Kaynaklı observation→context→tool outcome ilişkisi; cache hit/miss, token/latency ve insan etiketli relevance ölçümü. Future leakage/holdout mixing engellenir. Ranking advice required checks veya policy'yi azaltamaz. Modelin “işe yaradı” metni gold etiket olamaz. | FR-14/28–31/40, NFR-08/12 |

[WrongStack SAGE mimarisi](https://github.com/WrongStack/WrongStack/blob/main/docs/sage/ARCHITECTURE.md) kayıt türleri, anchor doğrulaması ve context injection mekanizmalarını belgeler. [WrongStack agents](https://github.com/WrongStack/WrongStack/blob/main/docs/agents.md) koordinasyon yapısını anlatır. Bunlar bu incelemede WrongStack'in bağımsız kalite/performans kabulü diye kullanılmadı. Dokümanlar arasındaki eski backend/modül satır sayıları güncel kod başarısı ölçümü değildir.

[Resmî Codex instruction discovery](https://learn.chatgpt.com/docs/agent-configuration/agents-md) root→çalışma dizini ve bounded instruction zincirini açıklar. [Resmî Codex subagents](https://learn.chatgpt.com/docs/agent-configuration/subagents) ayrı context, parallel sonuç toplama ve write-heavy koordinasyon maliyetini açıklar. Viber bunların kullanıcı faydasını kendi candidate/authority/privacy sınırlarıyla uygulamalıdır.

## Anahtarsız PRD: çalışan kapsam ve kalan işler

| Alan / PRD | Çalışan anahtarsız kapsam | Kalan yazılım/kabul |
|---|---|---|
| Task/kernel/workspace — FR-01–06/12/13 | Durable raw input, immutable capture/candidate, read receipts, stale edit rejection, journal/replay, CLI, checkpoint | Bütün power-loss/partial-publish, owner/UNKNOWN/catalog/anchor lifecycle ve tüm hostile interval matrisi |
| Sandbox/resource/policy — FR-07–09/18/20 | Ölçülen rootful/rootless Docker profilleri, current restriction/effect gates, token reserve/settle/UNKNOWN, physical control reserve ve birçok managed writer | Bütün mutable metadata/bootstrap/fence writer'larının fiziksel lifetime accounting'i, diğer backend profilleri ve tam escape/hostile-principal kabulü |
| Context — FR-10/14–17/27–31 | Source-bound FTS5/BM25/trigram, intent/log processing, exact spans/cursors/partitions, bounded compaction/history/pins; Go parser ve opt-in pinned helper; packed history; local semantic/ranking adapters | Genel TS/JS/Python resolved graph/LSP, runtime trace/coverage, genel proje belleği/instruction discovery ve bağımsız effectiveness/reference p95 |
| Verification/delivery — FR-11/15/21 | Protected exact STDIO observer, ayrı hidden evaluator, fresh merged checks; her platform review/export/patch; Windows fixed NTFS bounded exclusive apply/owned restore/reconcile | Genel runtime oracle ve V0–V5/hostile matrix kapanışı. Linux/macOS patch fallback PRD §13.3'e uygundur; koşulsuz live backend eksikliği diye sayılmaz |
| Supervisor/UI — FR-19/22/25/33–35 | Owner IPC, detach/attach/durable cursor, goal queue/child intent/shared parent allocation, provider delta/gap/resync, terminal UI | Çoklu worker lease/mutex/integration ve bütün native PTY/resize/paste/restore/cancel yolculuğu |
| Privacy — FR-26, NFR-09/11 | Explicit content deletion/default KEEP/consented EXPIRE, watermark/lineage/managed copies, lossless checkpoint/retirement/backup pins | Bütün catalog/legacy owner retirement, anchor history, UNKNOWN metadata ve tam interrupted lifecycle kabulü |
| Extension/learning/team — FR-32/36–42 | Blueprint/generation yolu, pinned read-only MCP, versioned skills/signed evaluator, advisory learned policy/shadow/canary/rollback, explicit training export, local operator policy | Ölçülmüş routing/external worker, genel extension sandbox, authenticated central team policy/remote executor; bağımsız blueprint/skill/learning başarı kabulü |
| Distribution/onboarding — FR-23/24, NFR-07/08/10/12 | Üç platform public engineering archive, licenses/SBOM, OIDC, native first offline task ve managed same-revision rollback/uninstall | Cross-revision actual acceptance CI engelli; eligible ChatGPT/Ollama gerçek kullanıcı görevi, platform signing/notarization, reference performance ve bağımsız pilot |

A–D stable kapıları **CLOSED**. A–D'de ciddi çalışan yazılım vardır fakat tam release matrisi kapanmamıştır. E source-bound intelligence kısmi; F single-worker/queue/shared budget temeli mevcut, tam multi-worker sistemi açık; G extension/skills/learning dilimleri mevcut, genel team/remote execution açık. Test sayısı veya 16-job engineering CI sonucu “PRD %100” yüzdesine dönüştürülmez. [Güncel capabilities](CAPABILITIES.md) ve [release matrix](RELEASE_GATES.md) birlikte okunur.

## API anahtarı ayrımı

**Model API anahtarı gerektiren:** Gerçek ücretli OpenAI/Anthropic endpoint'lerinde model/profile/usage/stream/cancel/UNKNOWN/rate-limit conformance ve gerçek model quality deneyleri; seçilen deney bu API'leri kullanıyorsa model erişim maliyeti. Offline fixture bunu kapatmaz.

**Model API anahtarı gerektirmeyen yazılım:** Yukarıdaki lifecycle/quota/verifier/worker/instruction/memory/LSP/team/installer işleridir. Bunları provider anahtarı bekliyor diye ertelemek doğru değildir.

**API anahtarı istemeyen, dış erişim/ortam isteyen kabul:** ChatGPT plan girişi ve ilk gerçek görev (uygun hesap/workspace consent), Ollama gerçek model (yüklü weights/hardware/endpoint), diğer platform/backend ölçümleri, OS certificate/notarization kimliği, bağımsız insan pilotu/reference hardware. Bunlar “anahtar yok, yapılamaz” başlığına alınmaz; gereken hesabı/ortamı ayrıca belirtiriz.

## Uygulama sırası

1. Yeni runner'ın exact-source CI'ını geçir; actual compatible upgrade→rollback→upgrade ve existing task preservation raporlarını al. Kırmızı main üzerinde yeni public paket yayımlama.
2. Catalog/UNKNOWN/metadata/fence accounting ve genel verification kapılarının kalan software/profile kapsamını kapat; supported/unsupported sınırını teknik kanıtla tut.
3. Proje belleği ve scoped instruction discovery'yi existing pins/skills/retrieval üzerine ekle; stale/permission/conflict/deletion/restore kabulünü kullanıcı akışıyla bağla.
4. Read-only workers/AgentLease/mailbox; ardından mutex/seri integration ile isolated writers. Shared parent budget yeniden yazılmaz.
5. Gerçek hesap/yerel model ve independent benchmark/pilot; model API kullanan deneyleri ayrı credential-bound track'te çalıştır. Stable kapıları yalnız ilgili gerçek kabul tamamlanınca aç.
