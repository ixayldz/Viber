# 12 Ekim bağımsız raporun kodla karşılaştırması

## 4cb840c kaynağını inceleyen takip raporu

Girdi: `68f21f13` ekindeki “Derinlemesine Teknik, Mimari ve Ürün İncelemesi”. Aynı özelliğin kod, native kabul ve kararlı release durumları ayrı değerlendirilmiştir. Önceki rapor için aşağıdaki tarihsel karşılaştırma korunur.

| Yeni bulgu | Uygulanan takip / açık kabul |
|---|---|
| HEAD CI sonucu belirsiz | [4cb CI38277989540](https://github.com/ixayldz/Viber/actions/runs/38277989540) tamamlandı ve FAILURE. Linux/macOS test, Docker/rootless, vault ve paket build işleri SUCCESS; Windows aktif steering owner yanıtını 10 saniyelik test deadline'ında alamadı. Foundation FAILED, dependent attestation/install işler SKIPPED. Bu source yayımlanmaz. |
| Windows suite disk rekabeti | Native shard sürücüsü agent shard'larını seri çalıştırırken ilk diğer paket grubunu Go'nun varsayılan paralelliğinde çalıştırıyordu. Her invocation'a `-p 1` eklendi; test inventory, deadline, repeat ve assert'ler korunur. Mevcut steering/dedup parent üç focused native Windows tekrarında PASS (11.926s toplam). Yük altında exact CI sonucu ayrıca gerekir; bu gözlem tek başına kök nedeni kesinleştirmez. |
| Development/store açıklamaları eski | 0.9 bounded compaction, purge/retention, observer, owner/TUI ve Windows NTFS apply kapsamları güncellendi. Genel privacy lifecycle, all-platform live write ve hostile verifier kabulü kapatılmadı. |
| Coverage görünürlüğü yok | Linux'un mevcut full test invocation'ı statement coverprofile üretir; package summary ve profile aynı source evidence artifact'inde tutulur. Branch coverage veya PRD kabulü değildir; ölçülmüş başarılı baseline olmadan floor icat edilmez. |
| Security tooling pin'i eski | Sabit Go/govulncheck sürümleri `scripts/pinned-tools.json` altında doğrulanır; scanner v1.9.0 resmi Go paket kaynağında kontrol edilmiştir. Tool uyumluluğu ve yeni CI ayrıca ölçülür; vulnerability database canlıdır. |
| Host paket sürümleri görünmüyor | Vault/rootless evidence'e observed OS/runner/runtime ve exact installed-package sürümleri eklendi. Descriptive inventory, package pin veya attestation değildir. |
| Katkı/güvenlik yönergeleri yok | Root CONTRIBUTING/SECURITY eklendi; kaynak/CI/release kapsamı ve hassas raporlama açıklanır. Uydurma iletişim adresi veya yanıt taahhüdü yoktur. |
| Terminal acceptance sürücüsü çıktı işaretini kaçırabilir | POSIX/ConPTY marker wait önce mevcut bounded capture'ı kontrol eder. Önceki read'in getirdiği ikinci frame için yeni idle output beklenmez; gerçek marker gerekliliği ve exit kontrolü korunur. |

Cross-task memory için ayrı privacy descendant kaydı ve explicit kullanıcı copy işlemi hazırlanır; automatic cross-task keşif güvenli lineage kabulü olmadan açılmaz. Model AgentLease, resolved TS/JS/Python, geniş lifecycle/quota/verifier, bağımsız benchmark/pilot ve platform signing aşağıdaki planda açık kalır. Bunların hepsi model API anahtarı bekleyen işler değildir.

Girdi: kullanıcının “Viber — Derinlemesine Teknik ve Mimari İnceleme” raporu; incelenen exact source `c8d2c8489b6c849ad11815a69d0b6a4c2dbfee53`. Raporun mimari puanları öznel değerlendirmedir. Dosya/test/bağımlılık sayıları üretim güvenliği, görev başarısı veya PRD yüzdesi göstermez. Bu kayıt önceki [uygulama planını](COMPETITOR_ADOPTION_IMPLEMENTATION_PLAN.md) sürdürür.

| Raporun tespiti | Kod / kabul karşılığı | Karar |
|---|---|---|
| Güncel main CI henüz kesinleşmedi | Rapor anındaki bilgi doğruydu; [exact c8 CI](https://github.com/ixayldz/Viber/actions/runs/38266912128) artık **16/16 SUCCESS**. Önceki f3 nil/empty admission-list regresyonu düzeltildi. Üç native, Linux race/fuzz, actual Docker/rootless/vault, paket/installer/attestation kabulü geçti. | c8 engineering kabulü kapandı; takip source ve public publisher/readback ayrıca gerekir. Stable/PRD tamamlanma sonucu değildir. |
| İlk gerçek görev çok fazla parametre istiyor | c8'de kullanıcı task/store/provider/autonomy bayraklarını yönetiyordu. Takip source `viber`/`start` ile yönlendirmeli seçim, otomatik private external store/task ve mevcut `run/auth/ui` üzerinden TUI'a geçiş ekler. Noninteractive help, consent refusal ve private offline örnek focused PASS; gerçek Windows wizard→TUI da gözlendi. | Yazılım ve bounded Windows journey uygulandı; takip exact-source CI ve gerçek eligible account/model acceptance ayrıca gerekir. |
| Gerçek ChatGPT/Ollama ilk görevi eksik | Resmî browser login ve adapter mevcut; kullanıcı uygun hesap/consent veya yerel weights/endpoint/hardware sağlamadan actual model acceptance üretilemez. | API anahtarı gerektirmeyen **dış kabul**; NOT_RUN korunur. Fixture PASS bu kabul değildir. |
| Proje talimatları/belleği mevcut | Captured root→target AGENTS override, typed metadata event, exact journal cursor, current read/edit gates, stale/conflict/redaction, correction/retire ve GC/backup/deletion bağlantısı mevcut. | Tekrar uygulanmaz; mevcut source contract korunur. Retained context/page/pin read gate boşluğu takip düzeltmesinde kapatıldı. |
| Otomatik cross-task bellek eksik | Aynı project içinde manual search vardır; automatic injection SAME_TASK_ONLY. | Gerçek yazılım açığı. Prompt'a türetilen kopyalar registered privacy lineage olmadan açılmaz; sadece scope filtresini kaldırmak çözüm değildir. |
| Read-workers tam multi-agent değil | Dört sabit Go analyzer, 2–4 dispatch, shared parent precharge, source/policy/cancel/output bounds mevcut. | Doğru. Scoped model AgentLease, bounded mailbox, isolated candidate writer ve serial fresh verification ayrı teslimlerdir. |
| Genel semantic graph / runtime coverage eksik | Go parser ve opt-in pinned resolved helper; TS/JS/Python lexical hints mevcut. | Resolved cross-language edges/coverage ayrı source/version/protocol-bound backend ve hostile acceptance ister. Lexical sonucu semantic diye etiketleme yok. |
| Context effectiveness ölçülmedi | Retrieval/cache/exact span ve DEV/HOLDOUT ranking mühendislik kabulü vardır. | Bağımsız gerçek task gold labels, repo/time-disjoint dataset, Recall@K/MRR, token/latency ve independent success/ablation ayrı kabul. Sentetik fixture gerçek benchmark yerine geçmez. |
| Güvenlik/release matrisi kısmi | Owner exclusivity, durable UNKNOWN, source stale rejection, control reserve, protected observers ve supported native fencing profilleri mevcut. A–D stable gates kapalı. | Bütün lifecycle/quota/hostile verification/team/pilot kapsamı açık; geniş production güvenliği ilan edilmez. |
| Session dosyaları büyük / domain coupling | Dosya boyutu tek başına hata değil; Session geniş domain'leri koordine ediyor. | Davranış/authority/replay sözleşmeleri korunarak ölçülü package extraction; release engelini çözmeyen geniş rewrite yapılmaz. |
| Web arayüzü önerisi | Raporun ürün önerisidir; kullanıcı bu turda terminal alışkanlığı istemiştir. | TUI/onboarding önceliği korunur; yeni web ürünü bu rapor geldi diye otomatik scope'a eklenmez. |

## Uygulama sırası

1. Exact CI ve somut policy/replay regresyonlarını kapat; installer/provenance/public readback yeni kaynak üzerinde doğrulanır.
2. Slash discovery + accessible line mode + başlangıç akışını tamamla; private offline görevde gerçek native PTY/ConPTY journey raporu üret. Bu bounded journey exhaustive terminal/hostile/platform matrisi değildir.
3. Cross-task derived-memory privacy lineage ve independent context benchmark altyapısı; insan etiketlerini/model erişimini açık dış kabul olarak ayır.
4. Mevcut typed DAG/shared budget üzerinde scoped read-only model lease/mailbox; sonra isolated writer/mutex/serial integration. Çocuk retry maliyeti sıfırlanmaz.
5. [PRD teslim planındaki](KEYLESS_DELIVERY_PLAN.md) aggregate physical writer accounting, catalog/UNKNOWN lifecycle, protected verifier, resolved language tools ve authenticated team/remote execution işleri; gerçek signing/pilot/reference performans kabulü.

Bu rapor önceki açık anahtarsız işler için yeni kanıt veya kapanış sağlamaz. **Yalnız API anahtarı işleri kaldı** sonucunu desteklemez. Model API anahtarı track'i gerçek OpenAI/Anthropic endpoint conformance ve bu endpoint'leri kullanan quality deneyleridir; uygun ChatGPT hesabı, Ollama donanımı, signing identity ve independent pilot ayrı dış gereksinimlerdir.
