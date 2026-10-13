# Viber

**Kodlama görevlerini terminalden çalıştırın, değişiklikleri inceleyin ve kaldığınız yerden devam edin.** Viber projenizi okur; önerilen değişiklikleri ayrı bir çalışma kopyasında tutar. Görev geçmişi ve sonuçlar bilgisayarınızda saklanır.

ChatGPT hesabınızla kullanabilirsiniz. **Bu giriş için OpenAI API anahtarı gerekmez.** Yerel Ollama ve internetsiz demo da desteklenir.

> **Sürüm durumu:** 0.9.0-dev, engineering ön sürümü. İndirilebilir paket vardır; kararlı üretim sürümü henüz ilan edilmemiştir. Windows public `08d4bf8` paketiyle gerçek ChatGPT giriş ve model listesi kabulü geçti. İlk kodlama çağrısı optional araç şeması nedeniyle `invalid_function_parameters` ile reddedildi; tamamlanmış inference kabulü henüz yoktur. Düzeltme kaynak kodunda hazırdır; yeni exact-source paket kabulünden sonra aynı özel görev sürdürülecektir. [Destek durumu](docs/CAPABILITIES.md).

[İndir](https://github.com/ixayldz/Viber/releases) · [Kurulum ayrıntıları](docs/INSTALL.md) · [CLI kılavuzu](docs/CLI_REFERENCE.md) · [PRD](prd.md) · [MIT lisansı](LICENSE)

İlk kullanımda yapacağınız şey: **arşivi doğrulayın → yeni klasöre çıkarın → o klasörde terminal açın → ChatGPT ile giriş yapın → proje yolunu seçin → görevi yazın → öneriyi onaylayın → diff'i inceleyip dışarı alın.** Aşağıdaki örnekleri sırayla çalıştırabilirsiniz. Windows için PowerShell, Linux/macOS için Terminal kullanın.

| Gereken | Ne için? |
| --- | --- |
| İşletim sisteminize uygun Viber paketi | Uygulamayı çalıştırmak |
| Uygun ChatGPT hesap/workspace izni ve internet | ChatGPT planıyla gerçek model kullanımı |
| Kodunuzun bulunduğu proje klasörü | Viber'ın çalışacağı kaynak |
| Proje dışında ayrı store klasörü | Görev geçmişi, çalışma kopyaları ve devam etme |

Go yalnız kaynaktan derleme için gerekir. Docker yalnız izole check/extension çalıştırmak için gerekir. İnternetsiz demoda ChatGPT hesabı da gerekmez.

## Hızlı başlangıç

[Güncel paketi](https://github.com/ixayldz/Viber/releases/tag/engineering-08d4bf868c02bfeea5e90cd095d11945b3cf8f6c) indirip doğruladıktan sonra arşivi yeni bir klasöre çıkarın. O klasörde terminal açıp Windows'ta `.\viber.exe`, Linux/macOS'ta `./viber` çalıştırın. PATH'e eklediyseniz yalnız `viber` yazın. Başlangıç ekranı proje, model ve görev seçimini adım adım yapar.

Başlangıç ekranında **1: ChatGPT**, **2: yerel Ollama**, **3: internetsiz örnek** seçilir. ChatGPT/Ollama için proje klasörünü seçip görevi kendi sözlerinizle yazın. Görev kimliği ve proje dışındaki özel kayıt klasörü otomatik oluşturulur. ChatGPT'ye proje bağlamı gönderilmeden önce `YES` istenir; giriş gerekiyorsa `LOGIN` seçimi resmî tarayıcı akışını açar. Ollama için kurulu modelin adını girin. `CANCEL` veya `q` başlangıcı durdurur.

Public `08d4bf8` paketinde ChatGPT/Ollama görevi oluşturulunca arayüz modelin bitmesini beklemeden açılır. Önerilen kod değişiklikleri incelemenizi bekler; `/requests` ve `/diff` ile bakıp `/respond REQUEST_ID approve` ve `/resume` ile devam edebilirsiniz. Bağımsız kontrolü olmayan sonucun teslimi ayrıca onay ister ve UNVERIFIED kalır. Bu akış public pakettedir; yukarıdaki gerçek ChatGPT kabul sınırını dikkate alın.

Gerçek terminalde görev başladıktan sonra TUI otomatik açılır. `/` ile komutlara bakın, `/requests` ile bekleyen öneriyi inceleyin, onayladıktan sonra `/resume` kullanın. `/diff` değişiklikleri gösterir; `/exit` arayüzden ayrılır ve geçmişi korur. İnternetsiz örnek yalnız özel bir `hello.txt` projesinde sabit demo çalıştırır; kendi projenizi okumaz, gerçek model kullanmaz ve **UNVERIFIED / exit 2** kalır.

`viber start` başlangıcı açıkça başlatır. Girdi/çıktı terminal değilse bare `viber` yardım gösterir; scriptler `run` komutunu kullanabilir. Elle kurulum ve gelişmiş komutlar aşağıdadır. Kaynak branch'indeki yeni eklemelerin paket kapsamı [destek tablosunda](docs/CAPABILITIES.md), son kod denetimi [13 Ekim raporunda](docs/REPOSITORY_AUDIT_2026_10_13.md) açıklanır.

## 1. İndirin ve kurun

**Go kurmanız gerekmez.** [Yayımlanmış engineering paketinin](https://github.com/ixayldz/Viber/releases/tag/engineering-08d4bf868c02bfeea5e90cd095d11945b3cf8f6c) Assets bölümünden işletim sisteminize uygun arşivi indirin. Kaynak revizyonu ve public varlık doğrulaması [kurulum kılavuzunda](docs/INSTALL.md) yer alır:

| Bilgisayarınız | Dosya |
| --- | --- |
| Windows, Intel/AMD 64 bit | `viber-…-windows-amd64.zip` |
| Linux, Intel/AMD 64 bit | `viber-…-linux-amd64.tar.gz` |
| macOS, Apple Silicon | `viber-…-darwin-arm64.tar.gz` |

Arşivin kaynağını [kurulum kılavuzundaki doğrulama adımıyla](docs/INSTALL.md#indirme-ve-doğrulama) kontrol etmek için GitHub CLI (`gh`) kurulu ve PATH'te olmalıdır. Doğrulamadan sonra arşivi yeni bir klasöre açın. Komutları bu program klasöründe çalıştırın; terminal başka yerde açıksa önce klasöre `cd` edin. Windows'ta `viber.exe`, Linux/macOS'ta `viber` dosyasını göreceksiniz.

Windows PowerShell:

```powershell
.\viber.exe version
.\viber.exe doctor --json
```

Linux / macOS:

```sh
./viber version
./viber doctor --json
```

`doctor` içinde `release_ready: false` mevcut ön sürüm için beklenen durumdur. İlk kurulumda Docker gerekmez. Projenin testlerini izole süreçte çalıştırmak için ayrıca desteklenen Docker ve kayıtlı check yapılandırması gerekir.

Main branch, son public paketten daha yeni geliştirmeler içerebilir. **Releases'ten indirilen pakette bütün main komutları bulunduğunu varsaymayın.** Güncel kaynak kodunu kullanmak için aşağıdaki [kaynaktan kurulum](#kaynaktan-kurulum) yolunu izleyin.

**Terminalden `viber` ile açma.** Doğrulanmış yeni arşivi `package install --launcher` ile kurunca Windows kullanıcı PATH'ine sahipliği izlenen `viber.cmd` eklenir; yeni terminalde `viber` çalışır. Linux/macOS için kurulumun verdiği `export PATH=…` satırını çalıştırın. [Tam kurulum komutu ve sınırlar](docs/INSTALL.md#terminalde-viber-komutu-yeni-main). Public `08d4bf8` bu seçeneği içerir; Windows kurulumu gerçek host üzerinde de kabul edildi.

Bu exact-source paket için üç native platformda yönetilen kurulum, ilk offline görev, önceki public `90bc97` sürümünden yükseltme → geri dönüş → yeniden yükseltme ve yalnız yönetilen program dosyalarını kaldırma kabulü geçti. Mevcut görevin durumu ve dış store korundu. `package install`, `status`, `rollback` ve `uninstall` adımları [anahtarsız operasyonlar kılavuzunda](docs/KEYLESS_OPERATIONS.md#yönetilen-kurulum) anlatılır.

## 2. ChatGPT hesabınızla giriş yapın

Windows:

```powershell
.\viber.exe auth login
.\viber.exe auth status --json
.\viber.exe auth models --json
```

Linux / macOS:

```sh
./viber auth login
./viber auth status --json
./viber auth models --json
```

İlk komut sistem tarayıcınızı açar. ChatGPT hesabınıza giriş yapın, kullanılacak workspace'i seçin ve Viber'ın plan kullanım iznini inceleyip onaylayın. Tarayıcıdaki işlem bitince terminale dönün.

`auth status` içinde **`chatgpt_plan_usage: true`** görünmelidir. Yalnız hesabınıza giriş yapmış olmak model kullanma izni vermez. Uygunluk, kullanılabilir modeller ve kullanım sınırları hesabınıza/workspace'inize bağlıdır; bunları OpenAI belirler. Akış [OpenAI'nin resmi Sign in with ChatGPT sözleşmesini](https://developers.openai.com/siwc/token-sharing-open-source/sign-in) kullanır; uygulama için ayrı client secret veya API anahtarı oluşturmanız gerekmez.

Viber şu anda ChatGPT yolunda `gpt-6.1-sol` model profilini destekler. `auth models` hesabınızdaki güncel katalogdur; katalogda görünmek her modelin bu Viber sürümünde desteklendiği anlamına gelmez. Diğer sağlayıcıların coding abonelikleriyle giriş henüz desteklenmez.

Token'lar Windows'ta kullanıcı DPAPI korumasıyla, Linux/macOS'ta OS credential vault desteğiyle saklanır. Linux'ta çalışan Secret Service, macOS'ta erişilebilir Keychain gerekir. Anahtarları veya token'ları projenize kopyalamayın.

## 3. Kendi projenizde ilk görevi başlatın

İki dizin seçin: **proje** kodunuzun bulunduğu klasör; **store** görev kayıtlarının tutulacağı ayrı klasör. Store'u proje klasörünün içine koymayın. Aşağıdaki proje yolunu kendi yolunuzla değiştirin.

Windows:

```powershell
$project = 'C:\Projects\my-app'
$store = Join-Path $env:LOCALAPPDATA 'ViberData\my-app'

.\viber.exe run "Projeyi incele, README'deki kurulum adımlarını düzelt ve değişiklikleri açıkla" --provider chatgpt --allow-remote --root $project --store $store --task first-task --autonomy review --allow-unverified
```

Linux / macOS:

```sh
project="$HOME/projects/my-app"
store="$HOME/.local/share/viber/tasks/my-app"

./viber run "Projeyi incele, README'deki kurulum adımlarını düzelt ve değişiklikleri açıkla" --provider chatgpt --allow-remote --root "$project" --store "$store" --task first-task --autonomy review --allow-unverified
```

`--allow-remote` seçili modelin proje bağlamını almasına izin verir. `--autonomy review` yazı önerilerini onayınıza sunar. `--allow-unverified` bağımsız kontrol yapılmadan inceleme paketi teslim edilmesine izin verir; sonucu doğrulanmış yapmaz. Hassas yolları görevden önce [config ile dışlayabilirsiniz](docs/CLI_REFERENCE.md#terminal-arayüzü-prompt-kuyruğu-ve-config-09).

Görev onay bekliyorsa arayüzü açın:

```powershell
.\viber.exe ui first-task --store $store
```

Linux/macOS'ta aynı komut `./viber ui first-task --store "$store"` olur. Arayüzde `/requests` bekleyen önerileri gösterir. İncelediğiniz isteği `/respond REQUEST_ID approve` ile onaylayın, ardından `/resume` yazın. `/diff` değişiklikleri, `/status` durumu, `/help` mevcut komutları gösterir. `REQUEST_ID` yerine ekrandaki gerçek kimliği yazın.

`/` komut menüsü, proje talimatları (AGENTS/CLAUDE), `/memory`, `/workers`, explicit memory copy, retrieval report/scoring, support ve delivery preview public `08d4bf8` paketindedir. Yeni GC receipt compaction, evaluator owner partition ve conflict metadata sınırları henüz sonraki kaynak değişikliğidir; kendi paket kabulünü bekler.

| Tuş / komut | Ne yapar? |
| --- | --- |
| `/`, ardından birkaç harf | Komutları gösterir ve filtreler |
| ↑ / ↓ | Menüde komut seçer; menü kapalıyken giriş geçmişini gösterir |
| Tab veya Enter | Seçilen komutu girişe tamamlar; kısmi seçim kendiliğinden çalışmaz |
| Sonraki Enter | Tamamladığınız komutu çalıştırır; tam komut yazıp Enter da çalışır |
| Esc | Menüyü kapatır, yazdığınızı korur |
| Page Up / Page Down | Komut çıktısını kaydırır |
| `/compact 4` | Son dört mesajı tutarak eski konuşmayı kaynak referanslarıyla arşivler |
| `/clear` | Görünen çıktıyı temizler; görev geçmişi saklanır |
| `/exit` veya `/quit` | Arayüzden ayrılır; arka plandaki görevi sürdürür |
| Ctrl+C | İlk basışta pause; sonraki basışta cancel ister |

Metin yapıştırmak komut çalıştırmaz. Normal metin göreve yeni yönlendirme olarak kaydedilir; kapsam değişikliği `/revise` ile açıkça yapılır. Ekran okuyucu veya basit terminal için `ui first-task --store … --line` kullanın.

Görev içinde proje kararı kaydetmek ve API anahtarı olmadan kaynak analizi yapmak için:

```text
/memory record DECISION storage Yerel görev günlüğünde SQLite kullan
/memory search SQLite
/workers inventory,requirements
/workers inventory,search TODO
```

Bellek sonuçları kaynak ve politika durumuna göre ACTIVE, STALE veya CONFLICT gösterir. Bunlar kullanıcı beyanıdır; doğrulama sonucu değildir. Düzeltme/saklamadan çıkarma için `/help` içindeki `correct`/`retire` biçimini ve sonuçtaki ID/digest değerlerini kullanın; işlemi kaydın oluşturulduğu görevde yapın. Aynı projenin diğer görevlerindeki kayıtlar elle aranabilir. Modele otomatik eklenen bellek yalnız mevcut görevin güncel kayıtlarıdır. `/workers` tamamlanmamış, boşta ve pending input/effect bulunmayan görevde 2–4 sabit yerel analiz çalıştırır; model çağırmaz ve görev bütçesine önceden yazar. [Detaylı kullanım ve sınırlar](docs/CLI_REFERENCE.md#proje-talimatları-bellek-ve-yerel-analizler).

Programı yeniden açınca aynı task ve store ile durum/geçmiş okunabilir. Yeni görev için farklı bir `--task` kimliği kullanın.

İlk komutun onay bekleyerek durması normaldir. Terminali kapattıysanız aynı `$store`/`store` değerini yeniden tanımlayıp `ui first-task` komutunu açın; görevi baştan oluşturmanız gerekmez. Örneklerde okunabilir terminal çıktısı kullanılır. Otomasyon için komutun sonuna `--json` ekleyebilirsiniz.

## 4. Sonucu inceleyin ve dışarı alın

Windows:

```powershell
.\viber.exe status first-task --store $store --json
.\viber.exe diff first-task --store $store --json
$delivery = Join-Path $env:LOCALAPPDATA ('ViberData\delivery-' + [guid]::NewGuid().ToString('N'))
.\viber.exe export first-task --store $store --output $delivery --json
```

Linux / macOS:

```sh
./viber status first-task --store "$store" --json
./viber diff first-task --store "$store" --json
delivery="$HOME/.local/share/viber/tasks/delivery-$(date +%s)"
./viber export first-task --store "$store" --output "$delivery" --json
```

Çıktı dizini yeni olmalıdır. Paket `changes.patch`, `report.json`, `manifest.json` ve değişen dosyaların exact before/after içeriğini taşır. Patch'i inceleyin; projenizdeki kullanıcı düzenlemeleriyle çakışmaları ayrıca değerlendirin. Varsayılan apply/restore canlı kaynak dosyalarına yazmaz. Windows'ta yerel fixed NTFS üzerinde sınırlı `--exclusive-ntfs` profili opt-in olarak kullanılabilir; kapsamı ve geçen Windows CI kabulü [anahtarsız operasyonlar kılavuzunda](docs/KEYLESS_OPERATIONS.md#güvenli-teslim) açıklanır. Linux/macOS live apply/restore desteklenmez.

| Görev çıkış kodu | Anlamı |
| --- | --- |
| `0` | Sıkı başarı koşulları karşılandı |
| `2` | İzin verilen sınırlı sonuç, örneğin UNVERIFIED teslim |
| `3` | Kullanıcı/kaynak kararı bekliyor |
| `4` | Hata; JSON/stderr açıklamasını inceleyin |

`FINISHED` tek başına testlerin geçtiği anlamına gelmez. `quality`, `fulfillment` ve açık yükümlülükleri birlikte okuyun.

## ChatGPT olmadan deneyin

Paketin içindeki offline demo API anahtarı, model servisi veya Docker istemez:

```powershell
$demoStore = Join-Path $env:LOCALAPPDATA ('ViberData\demo-' + [guid]::NewGuid().ToString('N'))
.\viber.exe run "Update hello.txt to the fixture greeting" --offline --fixture examples/offline/greeting.json --root examples/offline/source --store $demoStore --task greeting --allow-unverified --json
.\viber.exe diff greeting --store $demoStore --json
```

Linux/macOS için [aynı demonun komutları](docs/INSTALL.md#native-kurulum-ve-ilk-görev). Beklenen görev çıkışı `2`'dir: ayrı candidate `hello, Viber` içerir; kaynak `hello.txt` korunur. Fixture önceden belirlenmiş davranış gösterir, gerçek model değildir.

Yerel Ollama için kurulu bir model ve çalışan yerel endpoint gerekir:

```sh
./viber run "Projeyi incele ve düzeltme öner" --provider ollama --model YOUR_INSTALLED_MODEL --local-model --root "$project" --store "$store" --task local-task --allow-unverified --json
```

`YOUR_INSTALLED_MODEL` yerine yüklü model kimliğini yazın. Varsayılan endpoint `http://127.0.0.1:11434`'tür. Modeli Viber indirmez. [Yerel model ayrıntıları](docs/CLI_REFERENCE.md#anahtarsız-yerel-model-ollama-07).

## Günlük komutlar

Aşağıdaki örneklerde `viber`, PATH'e eklediğiniz binary'dir; aksi halde `./viber` veya `.\viber.exe` kullanın.

```sh
viber --help
viber requests TASK --store STORE --json
viber pause TASK --store STORE --json
viber resume TASK --store STORE --json
viber cancel TASK --store STORE --json
viber auth profiles --json
viber auth select --profile PROFILE_ID --json
viber auth logout --profile PROFILE_ID --json
viber store-backup --store STORE --output NEW_BACKUP --json
viber store-restore --backup BACKUP --store NEW_STORE --json
```

`TASK`, `STORE`, `PROFILE_ID` ve yedek yollarını kendi değerlerinizle değiştirin. Cancel kaynak dosyalarını geri alma işlemi değildir. Program klasörünü kaldırmak görev kayıtlarını ve hesap bağlantısını silmez; hesap için `auth logout`, görev içeriği için açık `delete-preview`/`delete` akışını kullanın.

## Sorun giderme

| Sorun | Yapılacak işlem |
| --- | --- |
| `viber` bulunamıyor | Binary klasöründe `./viber` / `.\viber.exe` kullanın veya klasörü PATH'e ekleyin |
| Paket doğrulaması GitHub oturumu istiyor | [GitHub CLI girişini](https://cli.github.com/manual/gh_auth_login) `gh auth login --hostname github.com --web` ile tamamlayıp doğrulamayı tekrar çalıştırın; bu ChatGPT girişinden ayrıdır |
| Login tarayıcısı açılmıyor | Yerel masaüstü oturumu ve varsayılan tarayıcıyla tekrar deneyin; `Ctrl+C` giriş denemesini iptal eder |
| `chatgpt_plan_usage: false` | Hesap/workspace izinlerini kontrol edin; kayıtlı profile `auth login --profile ID` ile yeniden izin verin |
| Model hesabınızda yok | `auth models --json` ve ChatGPT kullanım ayarlarını kontrol edin |
| `OS credential vault unavailable` | Linux Secret Service veya macOS Keychain'i açın/yapılandırın; plaintext fallback yoktur |
| Exit `2` | UNVERIFIED/sınırlı teslim olabilir; hata diye tekrar çalıştırmadan status'u okuyun |
| Exit `3` | `ui` / `requests` ile bekleyen kararı inceleyin |
| Store zaten kullanılıyor | Aynı store owner'ına bağlanın; lock dosyasını elle silmeyin |
| Source/store çakışıyor | Store'u proje dışına taşıyan yeni görev/restore dizini seçin |
| Eski scheduled child `UNSUPPORTED_CAPABILITY` veriyor | Eski child'ı `status`/`diff` ile inceleyin; [paylaşılan goal bütçesi kılavuzunu](docs/KEYLESS_OPERATIONS.md) okuyun; kayıtları silerek veya retry ile bütçeyi sıfırlamayın |

## Kaynaktan kurulum

Git ve **Go 1.27.2** gerekir. Normal kullanım için test suite'i çalıştırmanız gerekmez.

```sh
git clone https://github.com/ixayldz/Viber.git
cd Viber
go mod download
```

Windows PowerShell:

```powershell
New-Item -ItemType Directory -Path bin -Force | Out-Null
go build -trimpath -o bin/viber.exe ./cmd/viber
.\bin\viber.exe auth login
```

Linux / macOS:

```sh
mkdir -p bin
go build -trimpath -o bin/viber ./cmd/viber
./bin/viber auth login
```

Bu kurulumda önceki örneklerde `.\viber.exe` yerine `.\bin\viber.exe`, `./viber` yerine `./bin/viber` kullanın. `.tools`, `.cache` ve `bin` repo'ya dahil değildir; derleyiciyi kendiniz kurarsınız.

## Ayrıntılı belgeler

- [CLI referansı](docs/CLI_REFERENCE.md): review, checks, bütçe, context, migration, backup, deletion ve recovery.
- [Kurulum ve paket doğrulama](docs/INSTALL.md): provenance, platformlar, güncelleme ve kaldırma.
- [Main branch anahtarsız komutları](docs/KEYLESS_OPERATIONS.md): yönetilen kurulum, checkpoint ve yeni geliştirmeler.
- [Destek matrisi](docs/CAPABILITIES.md) ve [release kapıları](docs/RELEASE_GATES.md): çalışan kapsam ve kalan kabul.
- [Doğrulama kaydı](docs/VALIDATION.md) ve [uygulama planı](docs/ORCHESTRATED_KEYLESS_PLAN_2026_10_11.md).
- [Katkı yönergeleri](CONTRIBUTING.md) ve [güvenlik sorunlarını bildirme](SECURITY.md).

Viber MIT lisanslıdır. Katkı geliştirirken değişikliğe uygun mevcut kontrolleri çalıştırın; gereksinimler ve mimari için [prd.md](prd.md) dosyasını esas alın.
