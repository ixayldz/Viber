# GitHub dağıtımı ve kurulum teslimi

10 Ekim 2026. Kullanıcı public GitHub indirme/kurulum akışını istedi. Yetkili üretim kapısı [PRD §25–29](../prd.md); engineering build, public preview ve kararlı ürün farklı kabul taşır. Bu belge uygulama sırasıdır. Native Windows/Linux portable smoke ve archive readback ölçüldü; üç-platform Actions paket/OIDC doğrulama akışı hazırlanmıştır ve kendi exact-SHA kabulünü bekler. Aktif indirme yönergeleri [INSTALL](INSTALL.md) içindedir.

## Paket sözleşmesi

- İlk native hedefler: Windows amd64, Linux amd64, macOS arm64. Başka platform cross-build ile destek kazanmaz.
- Build immutable exact Git commit archive'ından, Go 1.27.2 ve doğrulanmış go.sum ile yapılır. SBOM/lisans envanteri her hedefin gerçek binary dependency closure union'ıdır; test/tool graph binary lisansı diye sunulmaz.
- İki build byte hash eşitliği; binary version/platform/build source identity; private per-target manifest; MIT, dependency attribution, CycloneDX SBOM, kullanım kılavuzu ve offline ilk görev fixture'ı birlikte paketlenir.
- Archive byte hash'leri, exact revision ve native smoke kabulünü taşıyan dağıtım manifest'i en son yayınlanır. Partial output complete package sayılamaz.
- GitHub Actions OIDC build provenance archive/binary hash'lerine bağlanır. Consumer repo, signer workflow, source ref ve digest'i doğrular. Yalnız indirme sayfasındaki checksum authenticity değildir. Windows Authenticode/macOS Developer ID/notarization ayrı platform signing kapsamıdır; OIDC bunlar diye gösterilmez.

## Native install/update/rollback

1. Kullanıcı doğrulanmış paketi kendi private uygulama dizinine kurar; sistem/admin PATH yazımı zorunlu değildir. Native hedef uyuşmazlığı veya unsupported architecture allocation öncesi reddedilir.
2. Sürümler ayrı dizinlerde kalır. Temp/final publication ayrı, mevcut binary/user files/store üzerinde silent overwrite yoktur. Running owner veya destination conflict explicit sonuç verir.
3. Update önce signature/manifest/platform/hash/compatibility doğrular, sonra yeni sürümü stage eder, doctor ve ilk görev smoke kabulünden sonra current pointer değişir. Başarısız stage mevcut kurulumu korur.
4. Rollback explicit seçilen daha önce doğrulanmış binary sürümüne döner; task journal/schema/privacy authority downgrade veya restore otomatik değildir. Incompatible store read-only/fail-closed kalır. Uninstall programı kaldırır, kullanıcı task/store/credentials'ını açık ayrı consent olmadan silmez.
5. Windows/macOS/Linux native CI: fresh install, reinstall/dedup, altered package/manifest, wrong platform, partial stage/process death, concurrent updater, running owner, update, rollback, uninstall ve ilk offline görev. Fixture gate kriptografik provenance veya gerçek platform kabulü diye sayılmaz.

## GitHub publication

Foundation güncel exact-SHA'da bütün platform full/vet/vulnerability/migration, Linux full race/fuzz, rootful/rootless ve native vault kabulünü ister. Kırmızı/atlanan kapılar üzerinde stable release yoktur. Yeni release workflow güven sınırı pinned action commit'leri ve minimum job permissions kullanır; PR/fork code'u release write token veya OIDC yetkisi alamaz.

Release assets Windows ZIP ve Unix tarball, checksum inventory, SBOM, lisanslar, bounded install/update/rollback yönergeleri ve build provenance içerir. Public stable tag yalnız ilgili A–D PRD test matrisi, privacy/resource/verifier/supervisor/delivery ve bağımsız pilot kapıları kapandıktan sonra yayımlanır. API kabulünün eksik kalması açıkça provider/profile kapsamını sınırlar; mühendislik veya anahtarsız dış kabul eksikleri API anahtarı diye etiketlenmez.

[GitHub build attestation](https://docs.github.com/en/actions/how-tos/secure-your-work/use-artifact-attestations/use-artifact-attestations) ve [consumer verification flags](https://cli.github.com/manual/gh_attestation_verify) resmi protokol kaynaklarıdır. Kriptografik build provenance, source'un PRD'yi karşıladığına dair evaluator verdict değildir.
