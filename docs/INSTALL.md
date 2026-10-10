# GitHub portable paketini kullanma

Mevcut kanal **engineering snapshot**; `doctor.release_ready=false`. Kararlı ürün veya PRD tamamlanma beyanı değildir. Native kurulum smoke ve GitHub build provenance sonucu başarılı olmayan paketi kullanmayın. Aktif kapsam [CAPABILITIES](CAPABILITIES.md), tam PRD kapıları [RELEASE_GATES](RELEASE_GATES.md) içindedir.

## İndirme ve doğrulama

İlk [public engineering ön sürümü](https://github.com/ixayldz/Viber/releases/tag/engineering-1f145f6d1062129a3b2506b1a7ab570ad0739416) yayımlandı. Exact source `1f145f6d1062129a3b2506b1a7ab570ad0739416`; [12 native CI/paket/provenance işi](https://github.com/ixayldz/Viber/actions/runs/38087257996) SUCCESS. Aşağıdaki hedefi seçin; GitHub oturumu olmadan indirme ve beş asset'in exact byte readback'i ayrıca doğrulandı.

| Sistem | İndirilecek asset |
|---|---|
| Windows x64 | [Windows ZIP](https://github.com/ixayldz/Viber/releases/download/engineering-1f145f6d1062129a3b2506b1a7ab570ad0739416/viber-1f145f6d1062-windows-amd64.zip) |
| Linux x64 | [Linux tar.gz](https://github.com/ixayldz/Viber/releases/download/engineering-1f145f6d1062129a3b2506b1a7ab570ad0739416/viber-1f145f6d1062-linux-amd64.tar.gz) |
| macOS Apple Silicon | [macOS tar.gz](https://github.com/ixayldz/Viber/releases/download/engineering-1f145f6d1062129a3b2506b1a7ab570ad0739416/viber-1f145f6d1062-darwin-arm64.tar.gz) |

Intel macOS, Windows arm64 ve Linux arm64 bu portable kanalın native kabulüne dahil değildir. `engineering-release` workflow yalnız repo sahibinin explicit full-SHA tag push'unda çalışır; her main commit'ini otomatik yayımlamaz. Prerelease, “Latest” veya stable ürün diye işaretlenmez. İlk workflow'un draft/tag lookup hatası ve doğrulanan explicit-ID recovery kaydı [VALIDATION](VALIDATION.md) içindedir; sonraki publisher düzeltmesi kendi exact revision kabulünü ister.

[GitHub Actions](https://github.com/ixayldz/Viber/actions/workflows/ci.yml) içinde exact commit'e ait başarılı `core` run'ını açın. Foundation'ın yanında üç `packages` işi ve `attest-packages` da SUCCESS olmalıdır. Bu akış yalnız bu repo'nun `main` push'unda signing yapar; PR/fork paketleri bu güven zincirine girmez.

Artifact adları `portable-windows-latest-SHA`, `portable-ubuntu-latest-SHA`, `portable-macos-latest-SHA` biçimindedir. Native hedefler sırasıyla Windows amd64, Linux amd64, macOS arm64'tür. Actions artifact download için GitHub hesabıyla oturum gerekir. Artifact wrapper'ını açınca portable ZIP/tar.gz, `SHA256SUMS` ve `distribution.json` bulunur. Public release Assets bölümü aynı arşivleri, birleşik checksum ve `release.json` taşır. Public stable release kapısı kapalıdır.

Portable archive'ı **çalıştırmadan/açmadan önce** resmi GitHub CLI ile repo, signer workflow, source ref ve seçtiğiniz tam SHA'yı doğrulayın:

```sh
gh attestation verify ARCHIVE -R ixayldz/Viber --signer-workflow ixayldz/Viber/.github/workflows/ci.yml --source-ref refs/heads/main --source-digest FULL_40_CHARACTER_COMMIT_SHA
```

SHA'yı indirdiğiniz JSON içinden körlemesine güvenerek seçmeyin; GitHub'da incelemeyi seçtiğiniz commit ile karşılaştırın. Checksum taşıma hasarını gösterir; tek başına yayımlayanın kimliğini doğrulamaz. GitHub CLI authentication/model API credential farklıdır; paket kullanmak OpenAI/Anthropic anahtarı istemez. [Resmi verification sözleşmesi](https://cli.github.com/manual/gh_attestation_verify).

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

## Güncelleme, geri dönüş ve kaldırma

Yeni archive'ı aynı provenance kontrolünden geçirip farklı sürüm dizinine açın. Aktif çalışmayı pause/settle edin, ilgili store owner'ını kapatın ve yeni binary'ye geçin. Eski dizini saklayın. Aktif kritik task üzerinde otomatik kernel/schema update yapılmaz; bu dağıtım manuel sürüm seçer. Kurulum store'u taşımaz veya migrate etmez.

Store formatı değişiyorsa [README migration/backup yönergelerini](../README.md) kullanın; validated backup ve explicit migration gerekir. Önceki binary'ye geri dönmek store/authority downgrade izni değildir. Incompatible format/policy read-only/fail-closed kalır; metadata'yı elle silerek devam etmeyin.

Kaldırma yalnız owner kapatıldıktan sonra seçtiğiniz program sürüm dizinini kaldırır. Task/store, backup ve credentials ayrı açık silme/revoke işlemi olmadan korunur. Dağıtımda otomatik updater/installer servisi veya OS certificate/notarization kabulü henüz yoktur. Public stable tag, yalnız bütün ilgili PRD kabulü kapandıktan sonra yayımlanabilir.
