# Viber

Terminalden kodlama görevleri başlatın, önerilen değişiklikleri inceleyin ve istediğinizde dışarı aktarın. Viber çalışmanızı kaynak projenizden ayrı bir aday kopyada tutar.

> **Engineering önizlemesi:** Windows'ta public `08d4bf8` paketiyle ChatGPT girişi ve model listesi kabul edildi; gerçek kodlama çağrısı beklemede. Son başarılı CI tabanı `9e2a3f0` (16/16); daha yeni main özellikleri eşleşen paketi bekliyor, yeni commit CI'si beklemede. Viber kararlı veya üretime hazır ilan edilmedi.

[İndirilebilir sürüm](https://github.com/ixayldz/Viber/releases/tag/engineering-08d4bf868c02bfeea5e90cd095d11945b3cf8f6c) · [Kurulum ayrıntıları](docs/INSTALL.md) · [CLI kılavuzu](docs/CLI_REFERENCE.md) · [Destek kapsamı](docs/CAPABILITIES.md)

## İndirip `viber` komutunu kurun

Go kurmadan kullanılabilen paketler Windows x64, Linux x64 ve Apple Silicon macOS içindir. Paket doğrulaması ve yönetilen launcher için [GitHub CLI](https://cli.github.com/) (`gh`) gerekir. GitHub doğrulaması, aşağıdaki ChatGPT girişinden ayrıdır.

| Sistem | Paket |
| --- | --- |
| Windows x64 | [ZIP](https://github.com/ixayldz/Viber/releases/download/engineering-08d4bf868c02bfeea5e90cd095d11945b3cf8f6c/viber-08d4bf868c02-windows-amd64.zip) |
| Linux x64 | [tar.gz](https://github.com/ixayldz/Viber/releases/download/engineering-08d4bf868c02bfeea5e90cd095d11945b3cf8f6c/viber-08d4bf868c02-linux-amd64.tar.gz) |
| macOS Apple Silicon | [tar.gz](https://github.com/ixayldz/Viber/releases/download/engineering-08d4bf868c02bfeea5e90cd095d11945b3cf8f6c/viber-08d4bf868c02-darwin-arm64.tar.gz) |

Arşivi çalıştırmadan önce, tam kaynak SHA'sını kullanarak doğrulayın:

```sh
gh attestation verify ARCHIVE -R ixayldz/Viber --signer-workflow ixayldz/Viber/.github/workflows/ci.yml --source-ref refs/heads/main --source-digest 08d4bf868c02bfeea5e90cd095d11945b3cf8f6c
```

`ARCHIVE` yerine indirdiğiniz dosyanın tam yolunu yazın. Doğrulama tamamlanınca arşivi yeni bir klasöre çıkarın. Bu klasördeki binary ile yönetilen kurulumu başlatın:

Windows PowerShell:

```powershell
$archive = 'C:\Downloads\viber-08d4bf868c02-windows-amd64.zip'
$ghPath = (Get-Command gh -ErrorAction Stop).Source
.\viber.exe package install --archive $archive --source-sha 08d4bf868c02bfeea5e90cd095d11945b3cf8f6c --gh $ghPath --launcher
```

Linux / macOS (macOS için dosya adını `darwin-arm64` yapın):

```sh
archive="$HOME/Downloads/viber-08d4bf868c02-linux-amd64.tar.gz"
gh_path="$(command -v gh)"
./viber package install --archive "$archive" --source-sha 08d4bf868c02bfeea5e90cd095d11945b3cf8f6c --gh "$gh_path" --launcher
```

Windows kurulumu kullanıcı PATH'ine `viber` launcher'ını ekler. Yeni bir terminal açın. Linux/macOS'ta kurulumun yazdırdığı `export PATH=…` satırını çalıştırın; sonraki terminallerde de kullanmak için shell başlangıç dosyanıza ekleyin. Yönetilen program ve görev kayıtları ayrı klasörlerde tutulur.

## Güncel PRD akışını deneyin

PRD sihirbazı ve bu README'deki sade onay komutları yayımlanmış `08d4bf8` paketinde yoktur. Bunları denemek için Go 1.27.2 ile güncel kaynağı kurun:

```sh
git clone https://github.com/ixayldz/Viber.git
cd Viber
git checkout main
go mod download
go install -trimpath ./cmd/viber
```

`go install` dosyasını varsayılan olarak Windows'ta `%USERPROFILE%\go\bin`, Linux/macOS'ta `$(go env GOPATH)/bin` içine yazar (`GOBIN` ayarlıysa o kullanılır). Bu klasörü kullanıcı PATH'inize, yönetilen public launcher klasöründen önce ekleyin ve yeni terminal açın; böylece `viber` güncel kaynak sürümünü başlatır. Yeni source paketi yayımlanana kadar PRD deneyimi kaynak kodundan alınır.

## İlk göreviniz

Terminali projenizin klasöründe açıp çalıştırın:

```sh
viber
```

İlk ekranda:

1. **ChatGPT** seçin.
2. Proje klasörü sorulduğunda geçerli klasörü kabul edin veya başka bir yol girin.
3. Görevi yazın. Kök klasörde yalnızca bir `PRD.md` veya `prd.md` varsa, PRD iş akışını seçmek için bu soruda Enter'a basın.
4. Viber'ın görev ve proje bağlamını ChatGPT'ye göndermesine izin vermek için `YES` yazın.
5. Daha önce bağlanmış ve plan kullanım izni olan hesap yoksa `LOGIN` yazın. Tarayıcıda ChatGPT hesabınıza giriş yapıp plan kullanımı iznini gözden geçirin.

ChatGPT girişi resmi tarayıcı akışını kullanır; **OpenAI API anahtarı gerekmez**. ChatGPT hesabınızın veya workspace'inizin plan kullanımı izni ve kullanılabilir modeller hesaba bağlıdır. Viber giriş bilgilerinizi proje klasörüne koymaz. Proje bağlamı gönderilmeden önce her görevde açık onay ister.

PRD seçimi, Viber'a yakalanmış `PRD.md`/`prd.md` dosyasını okutmasını ve değişikliklerden önce kaynak bağlı bir plan oluşturmasını söyler. Planı `/plan` ile inceleyin; kod değişiklikleri ayrıca onayınızı bekler. PRD dosyasının proje içindeki başka bir yolunu kullanmak için [CLI kılavuzundaki `--prd` örneğine](docs/CLI_REFERENCE.md) bakın. Görev kimliği ve özel kayıt klasörü otomatik oluşturulur; ekranda gösterilen bilgileri saklayın.

## Önerileri inceleyin ve yönetin

Görev arayüzü Codex veya Claude'daki gibi `/` ile açılan bir komut seçicisi sunar. Komut adını yazdıkça liste daralır; ↑/↓ ile seçin, Tab ile girişe ekleyin, Enter ile çalıştırın. Tam komutu yazarak da çalıştırabilirsiniz.

```text
/plan
/requests
/approve
/resume
/diff
/preview
/export
```

Önerileri önce `/requests` ile okuyun. Tek bir bekleyen öneri varsa `/approve` veya `/reject` kullanabilirsiniz; birden çok öneri varsa `/requests` içinden doğru kimliği alıp komuta ekleyin. Onay, görevi kendiliğinden sürdürmez: `/resume` ile devam ettirin. `/plan` PRD planını, `/diff` dosya değişikliklerini gösterir.

Build/test komutlarını izole ortamda çalıştırmak için ayrıca Docker ve proje dışında hazırladığınız operator check dosyası gerekir. Bu dosya varsa `viber start --prd docs/PRD.md --check-config "ABSOLUTE_CHECK_CONFIG_PATH"` ile sihirbazı açabilirsiniz. [Check yapılandırması](docs/CLI_REFERENCE.md#kayıtlı-check-ve-lexical-outline-08) sabit image/komutları kaydeder; Viber depodaki rastgele scriptleri otomatik çalıştırmaz.

Görev duraklatılmış veya bitmişken `/preview` güncel proje ile öneriyi yeni bir klasörde karşılaştırır; `/export` patch, manifest ve dosyaların önce/sonra içeriklerini dışarı çıkarır. Klasör belirtmezseniz Viber kayıt klasörünün yanında, proje dışında yeni bir teslim klasörü seçer ve yolunu gösterir. Bu adımlar kaynak projenizdeki dosyaları değiştirmez. `/quit` arayüzden çıkar; arka plan görevi devam eder. Tekrar bağlanmak için `viber ui TASK_ID --store "KAYIT_KLASÖRÜ"` komutunda ekranda gösterilen kimliği ve yolu kullanın.

Varsayılan öneri modu, değişiklikleri incelemeniz için bekletir. `FINISHED` veya `UNVERIFIED` sonucu tek başına testlerin geçtiğini göstermez. Durum ve doğrulama sınırları için `/status`, `/verification` veya [destek kapsamına](docs/CAPABILITIES.md) bakın.

## Sık sorulanlar

**Viber kaynak dosyalarımı değiştirir mi?** Hayır. Aday değişiklikleri ayrı saklanır; `/diff`, `/preview` ve `/export` inceleme içindir. Varsayılan akış projeye otomatik yazmaz.

**Enter'a bastım ama PRD seçilmedi.** Otomatik seçim yalnız proje kökündeki tek bir `PRD.md` veya `prd.md` için geçerlidir. Görev sorusuna metin yazmak yerine Enter'a basın.

**Komut veya özellik bende görünmüyor.** İndirilebilir paket güncel main'den eskidir. Güncel PRD ve onay akışı için yukarıdaki source kurulumunu kullanın.

**Komut çıkışı 2 ne demek?** Bu, UNVERIFIED gibi sınırlı bir sonuç olabilir; doğrudan hata veya doğrulanmış başarı olarak yorumlamayın.

## Ayrıntılı belgeler

- [Kurulum, paket doğrulama, güncelleme ve kaldırma](docs/INSTALL.md)
- [CLI referansı ve gelişmiş komutlar](docs/CLI_REFERENCE.md)
- [Yetenekler ve açık sınırlar](docs/CAPABILITIES.md)
- [Doğrulama kayıtları](docs/VALIDATION.md)
- [Katkı yönergeleri](CONTRIBUTING.md) · [Güvenlik bildirimi](SECURITY.md)

Viber MIT lisanslıdır. Ürün gereksinimleri [prd.md](prd.md) içindedir.
