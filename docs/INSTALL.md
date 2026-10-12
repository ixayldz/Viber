# GitHub portable paketini kullanma

Mevcut kanal **engineering snapshot**; `doctor.release_ready=false`. Kararlı ürün veya PRD tamamlanma beyanı değildir. Native kurulum smoke ve GitHub build provenance sonucu başarılı olmayan paketi kullanmayın. Aktif kapsam [CAPABILITIES](CAPABILITIES.md), tam PRD kapıları [RELEASE_GATES](RELEASE_GATES.md) içindedir.

## İndirme ve doğrulama

Güncel [public engineering ön sürümü](https://github.com/ixayldz/Viber/releases/tag/engineering-5e6a006ff6c0cde18caa8ba48e9341075275d6eb) yayımlandı. Exact source `5e6a006ff6c0cde18caa8ba48e9341075275d6eb`; [16/16 native CI, installer ve provenance işi](https://github.com/ixayldz/Viber/actions/runs/38224054501) ve [normal tag publisher](https://github.com/ixayldz/Viber/actions/runs/38233321334) SUCCESS; public release ID `410459735`. Anonim readback'te 11 varlığın tamamı indirildi, dokuz OIDC subject `ci.yml`/`main`/exact source revizyonuna bağlandı ve üç platformun native managed-install raporları arşiv SHA-256'sıyla doğrulandı. Fresh-archive raporları da ilk offline görevi, manuel kaldırmayı ve dış store'un korunmasını kapsar. Stable release değildir; `doctor.release_ready=false` kalır.

| Sistem | İndirilecek asset |
|---|---|
| Windows x64 | [Windows ZIP](https://github.com/ixayldz/Viber/releases/download/engineering-5e6a006ff6c0cde18caa8ba48e9341075275d6eb/viber-5e6a006ff6c0-windows-amd64.zip) |
| Linux x64 | [Linux tar.gz](https://github.com/ixayldz/Viber/releases/download/engineering-5e6a006ff6c0cde18caa8ba48e9341075275d6eb/viber-5e6a006ff6c0-linux-amd64.tar.gz) |
| macOS Apple Silicon | [macOS tar.gz](https://github.com/ixayldz/Viber/releases/download/engineering-5e6a006ff6c0cde18caa8ba48e9341075275d6eb/viber-5e6a006ff6c0-darwin-arm64.tar.gz) |

Intel macOS, Windows arm64 ve Linux arm64 bu portable kanalın native kabulüne dahil değildir. `engineering-release` workflow yalnız repo sahibinin explicit full-SHA tag push'unda çalışır; her main commit'ini otomatik yayımlamaz. Prerelease, “Latest” veya stable ürün diye işaretlenmez. `release.json` ve `SHA256SUMS` dahil toplam 11 public asset'in anonim indirme/readback'i geçti. Dokuz attested subject, her platform için arşiv, dağıtım raporu ve managed-install raporunu kapsar; üç `*-installation.json` raporu exact source ve archive digest'ine bağlıdır. Önceki sürümlerin başarısızlık ve recovery kayıtları [VALIDATION](VALIDATION.md) içinde korunur.

[GitHub Actions](https://github.com/ixayldz/Viber/actions/workflows/ci.yml) içinde exact commit'e ait başarılı `core` run'ını açın. Foundation'ın yanında üç `packages` işi ve `attest-packages` da SUCCESS olmalıdır. Bu akış yalnız bu repo'nun `main` push'unda signing yapar; PR/fork paketleri bu güven zincirine girmez.

Artifact adları `portable-windows-latest-SHA`, `portable-ubuntu-latest-SHA`, `portable-macos-latest-SHA` biçimindedir. Native hedefler sırasıyla Windows amd64, Linux amd64, macOS arm64'tür. Actions artifact download için GitHub hesabıyla oturum gerekir. Artifact wrapper'ını açınca portable ZIP/tar.gz, `SHA256SUMS` ve `distribution.json` bulunur. Public release Assets bölümü üç arşiv, her platform için bir `*-distribution.json` ve bir `*-installation.json`, birleşik checksum ve `release.json` taşır. Public stable release kapısı kapalıdır.

Rapor varlıkları arşivle aynı [release sayfasındadır](https://github.com/ixayldz/Viber/releases/tag/engineering-5e6a006ff6c0cde18caa8ba48e9341075275d6eb):

| Platform | Taze arşiv raporu | Managed-install raporu |
|---|---|---|
| Windows amd64 | [distribution.json](https://github.com/ixayldz/Viber/releases/download/engineering-5e6a006ff6c0cde18caa8ba48e9341075275d6eb/viber-5e6a006ff6c0-windows-amd64-distribution.json) | [installation.json](https://github.com/ixayldz/Viber/releases/download/engineering-5e6a006ff6c0cde18caa8ba48e9341075275d6eb/viber-5e6a006ff6c0-windows-amd64-installation.json) |
| Linux amd64 | [distribution.json](https://github.com/ixayldz/Viber/releases/download/engineering-5e6a006ff6c0cde18caa8ba48e9341075275d6eb/viber-5e6a006ff6c0-linux-amd64-distribution.json) | [installation.json](https://github.com/ixayldz/Viber/releases/download/engineering-5e6a006ff6c0cde18caa8ba48e9341075275d6eb/viber-5e6a006ff6c0-linux-amd64-installation.json) |
| macOS arm64 | [distribution.json](https://github.com/ixayldz/Viber/releases/download/engineering-5e6a006ff6c0cde18caa8ba48e9341075275d6eb/viber-5e6a006ff6c0-darwin-arm64-distribution.json) | [installation.json](https://github.com/ixayldz/Viber/releases/download/engineering-5e6a006ff6c0cde18caa8ba48e9341075275d6eb/viber-5e6a006ff6c0-darwin-arm64-installation.json) |

Portable archive'ı **çalıştırmadan/açmadan önce** resmi GitHub CLI ile repo, signer workflow, source ref ve seçtiğiniz tam SHA'yı doğrulayın:

```sh
gh attestation verify ARCHIVE -R ixayldz/Viber --signer-workflow ixayldz/Viber/.github/workflows/ci.yml --source-ref refs/heads/main --source-digest 5e6a006ff6c0cde18caa8ba48e9341075275d6eb
gh attestation verify INSTALLATION_JSON -R ixayldz/Viber --signer-workflow ixayldz/Viber/.github/workflows/ci.yml --source-ref refs/heads/main --source-digest 5e6a006ff6c0cde18caa8ba48e9341075275d6eb
gh attestation verify DISTRIBUTION_JSON -R ixayldz/Viber --signer-workflow ixayldz/Viber/.github/workflows/ci.yml --source-ref refs/heads/main --source-digest 5e6a006ff6c0cde18caa8ba48e9341075275d6eb
```

SHA'yı indirdiğiniz JSON içinden körlemesine güvenerek seçmeyin; GitHub'da incelemeyi seçtiğiniz commit ile karşılaştırın. Checksum taşıma hasarını gösterir; tek başına yayımlayanın kimliğini doğrulamaz. GitHub CLI authentication/model API credential farklıdır; paket kullanmak OpenAI/Anthropic anahtarı istemez. [Resmi verification sözleşmesi](https://cli.github.com/manual/gh_attestation_verify).

GitHub CLI doğrulamada oturum istiyorsa `gh auth login --hostname github.com --web` ile GitHub hesabınıza bağlanın, ardından attestation komutunu yeniden çalıştırın. Bu, Viber'ın `auth login` ile yaptığı ChatGPT girişinden ayrı bir işlemdir; model API anahtarı oluşturmanızı gerektirmez. [Resmî GitHub CLI giriş kılavuzu](https://cli.github.com/manual/gh_auth_login).

Platformunuza ait `*-installation.json` ve `*-distribution.json` varlıklarını aynı provenance komutuyla ayrı ayrı doğrulayın. Installation raporu exact source, platform ve arşiv SHA-256'sını bağlar. Üç rapor da managed install, ilk offline fixture görevi, aynı revizyonu yeniden kurma/geri seçme, owned uninstall ve dış store'un korunması için PASS verir. Ayrıca önceki public `90bc97` paketiyle gerçek upgrade → rollback → upgrade ve mevcut task projection korunması PASS; baseline arşiv SHA-256 raporda bağlıdır. Distribution raporundaki `archive_install_smoke.scope=FRESH_ARCHIVE_FIRST_TASK_MANUAL_UNINSTALL_ONLY` PATH boşken ve Git bulunmazken ilk görevi çalıştırır, kaynakla dış store'u korur ve manuel uninstall yapar. Gerçek uygun ChatGPT hesabıyla ilk görev kabulü ayrıca **NOT_RUN**.

GitHub OIDC build provenance, archive byte'larının bu workflow/commit'ten çıktığını bağlar. Windows Authenticode veya macOS Developer ID/notarization değildir; OS bunları unsigned uygulama olarak gösterebilir. Provenance ürün kalitesi veya PRD evaluator verdict'i değildir.

## Native kurulum ve ilk görev

Doğrulanmış archive'ı sahip olduğunuz yeni bir sürüm dizinine açın. Örneğin Windows'ta `$env:LOCALAPPDATA\ViberApps\viber-SHA-windows-amd64`; Unix'te kendi private uygulama dizininizde `viber-SHA-linux-amd64` veya `viber-SHA-darwin-arm64`. Var olan uygulama/kullanıcı dosyasının üzerine açmayın. Go derleyicisi gerekmez. Binary ve offline fixture paketin içindedir. PATH'e eklemek isteğe bağlıdır; örnekler tam paket dizininde çalışır.

Windows/PowerShell:

```powershell
.\viber.exe version
.\viber.exe doctor --json
$demoStore = Join-Path $env:LOCALAPPDATA "ViberData\demo-$([guid]::NewGuid().ToString('N'))"
.\viber.exe run "Update hello.txt to the fixture greeting" --offline --fixture examples/offline/greeting.json --root examples/offline/source --store $demoStore --task greeting --allow-unverified --json
$LASTEXITCODE # 2
.\viber.exe diff greeting --store $demoStore --json
```

Linux/macOS:

```sh
./viber version
./viber doctor --json
./viber run "Update hello.txt to the fixture greeting" --offline --fixture examples/offline/greeting.json --root examples/offline/source --store "$HOME/viber-data/fresh-demo" --task greeting --allow-unverified --json
echo $? # 2
./viber diff greeting --store "$HOME/viber-data/fresh-demo" --json
```

Her demo için yeni store seçin; store kaynak/paket dizininin dışında olmalıdır. Beklenen sonuç `TERMINATED / FINISHED / UNVERIFIED / SATISFIED`, run exit2'dir. Kaynak `hello.txt` aynı kalır, candidate exact `hello, Viber\n` üretir. Bu model muhakemesi veya production verification değildir. `doctor` mevcut capabilities'i bildirir; bütün sandbox/provider testlerini çalıştırmaz. Linux store directory-sync destekleyen yerel filesystem gerektirir; Docker Desktop/Windows bind-store native kabulü bu engineering profilinde geçmemiştir.

## Yönetilen kurulum, güncelleme, geri dönüş ve kaldırma

Portable arşivdeki binary bootstrap aracıdır. Önce arşivle iki raporu doğrulayın; ardından bootstrap klasöründen [managed install akışını](KEYLESS_OPERATIONS.md#yönetilen-kurulum) çalıştırın. `package install`, seçilen arşivi operatorün verdiği GitHub CLI ve exact source SHA ile yeniden doğrular; `--prefix` program dosyalarını ayrı sürüm dizininde tutar. `package status` seçili revizyonu gösterir, `package run` yönetilen binary'yi başlatır. Store'ları prefix dışında tutun ve geçiş öncesi çalışan owner'ı durdurun. Launcher yalnız açıkça kaydettiğiniz store owner'larının kilidini alır.

Bu exact-source release için Windows, Linux ve macOS managed-install raporları actual OIDC doğrulamasını, ilk offline fixture görevini, aynı revizyonun reinstall/rollback akışını, owned uninstall'ı ve dış store'un korunmasını PASS olarak bağlar. Public `90bc97` başlangıç sürümüyle üç native platformda gerçek upgrade → rollback → upgrade PASS; mevcut task projection korundu. Bu yalnız uyumlu schema/reducer profilidir. Bu kabul otomatik updater veya store migration anlamına gelmez. [Anahtarsız operasyonlar kılavuzu](KEYLESS_OPERATIONS.md#yönetilen-kurulum) install, status, rollback, deactivate ve uninstall komutlarını açıklar.

Store formatı değişiyorsa [CLI migration/backup yönergelerini](CLI_REFERENCE.md#eski-storeu-yükseltme) kullanın; validated backup ve explicit migration gerekir. Önceki binary'ye geri dönmek store/authority downgrade izni değildir. Incompatible format/policy read-only/fail-closed kalır; metadata'yı elle silerek devam etmeyin.

Uninstall yalnız yönetilen program dosyalarını kaldırır; task/store, backup ve credentials korunur. Windows Authenticode, macOS Developer ID/notarization ve stable PRD readiness bu engineering kabulünün dışındadır.
