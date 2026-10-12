# Viber

**Kodlama görevlerini terminalden çalıştırın, değişiklikleri inceleyin ve kaldığınız yerden devam edin.** Viber projenizi okur; önerilen değişiklikleri ayrı bir çalışma kopyasında tutar. Görev geçmişi ve sonuçlar bilgisayarınızda saklanır.

ChatGPT hesabınızla kullanabilirsiniz. **Bu giriş için OpenAI API anahtarı gerekmez.** Yerel Ollama ve internetsiz demo da desteklenir.

> **Sürüm durumu:** 0.9.0-dev, engineering ön sürümü. İndirilebilir paket vardır; kararlı üretim sürümü henüz ilan edilmemiştir. Bu public paket için ChatGPT hesabıyla ilk görev kabulü **NOT_RUN**; her hesapta çalıştığı iddia edilmez. [Destek durumu](docs/CAPABILITIES.md).

[İndir](https://github.com/ixayldz/Viber/releases) · [Kurulum ayrıntıları](docs/INSTALL.md) · [CLI kılavuzu](docs/CLI_REFERENCE.md) · [PRD](prd.md) · [MIT lisansı](LICENSE)

İlk kullanımda yapacağınız şey: **arşivi doğrulayın → yeni klasöre çıkarın → o klasörde terminal açın → ChatGPT ile giriş yapın → proje yolunu seçin → görevi yazın → öneriyi onaylayın → diff'i inceleyip dışarı alın.** Aşağıdaki örnekleri sırayla çalıştırabilirsiniz. Windows için PowerShell, Linux/macOS için Terminal kullanın.

| Gereken | Ne için? |
| --- | --- |
| İşletim sisteminize uygun Viber paketi | Uygulamayı çalıştırmak |
| Uygun ChatGPT hesap/workspace izni ve internet | ChatGPT planıyla gerçek model kullanımı |
| Kodunuzun bulunduğu proje klasörü | Viber'ın çalışacağı kaynak |
| Proje dışında ayrı store klasörü | Görev geçmişi, çalışma kopyaları ve devam etme |

Go yalnız kaynaktan derleme için gerekir. Docker yalnız izole check/extension çalıştırmak için gerekir. İnternetsiz demoda ChatGPT hesabı da gerekmez.

## 1. İndirin ve kurun

**Go kurmanız gerekmez.** [Yayımlanmış engineering paketinin](https://github.com/ixayldz/Viber/releases/tag/engineering-f1b323d437e953411649db6fec2532a20095a524) Assets bölümünden işletim sisteminize uygun arşivi indirin:

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

Çıktı dizini yeni olmalıdır. Paket `changes.patch`, `report.json`, `manifest.json` ve değişen dosyaların exact before/after içeriğini taşır. Patch'i inceleyin; projenizdeki kullanıcı düzenlemeleriyle çakışmaları ayrıca değerlendirin. Public ön sürüm canlı proje dosyalarına otomatik apply/restore yapmaz.

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

Viber MIT lisanslıdır. Katkı geliştirirken değişikliğe uygun mevcut kontrolleri çalıştırın; gereksinimler ve mimari için [prd.md](prd.md) dosyasını esas alın.
