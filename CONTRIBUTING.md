# Katkı

Viber, devam eden bir engineering ön sürümüdür. Yeni özellikler veya testler, [PRD](prd.md), [CAPABILITIES](docs/CAPABILITIES.md) ve [RELEASE_GATES](docs/RELEASE_GATES.md) sınırlarıyla uyumlu olmalıdır. Fixture, yerel test veya başarılı bir CI işi tek başına üretim ya da PRD tamamlanma kanıtı değildir; mevcut `doctor` sonucu `release_ready: false` kalır.

## Değişiklik gönderme

Değişikliği dar kapsamlı tutun. Davranış değişiyorsa mevcut ilgili kontrolleri çalıştırın; yeni bir riski doğrulamak gerektiğinde en küçük anlamlı regresyon testini ekleyin. Görev store'larını, kimlik bilgilerini, özel kullanıcı/proje verisini veya makineye özgü yerel kanıtları commit etmeyin. Güvenlik sınırlarını, teslim davranışını, replay/migration biçimlerini veya platforma özel kodu etkileyen değişikliklerde ilgili hata, crash, hostile-input ve desteklenen platform koşullarını doğrulayın; testlerin hangi kapsamı kanıtlamadığını PR açıklamasında belirtin.

Depo değişiklikleri için `main` hedefli bir GitHub pull request kullanın. Açıklamada gözlenen sorunu, yeni davranışı, çalıştırdığınız kontrolleri ve bilinen sınırları yazın. Bu yönerge inceleme veya yanıt süresi taahhüdü vermez.

## Yerel geliştirme

Kaynak derlemesi Go `1.27.2` kullanır. Depo kökünden:

```sh
go version
go mod download
go mod verify
gofmt -l cmd internal
go vet ./...
go test -count=1 -timeout 15m ./...
```

`go version` çıktısı `go1.27.2` olmalı, `gofmt -l` çıktı vermemeli ve kontroller başarılı olmalıdır. Windows CI tam testleri `scripts/native-test-shards.py` ile parçalara ayırır; diğer platformlarda `go test ./...` kullanır.

Derleme için `bin` klasörünü oluşturup şu komutu çalıştırın:

```sh
mkdir -p bin
go build -trimpath -o bin/viber ./cmd/viber
```

Windows PowerShell karşılığı:

```powershell
New-Item -ItemType Directory -Path bin -Force | Out-Null
go build -trimpath -o bin/viber.exe ./cmd/viber
```

Güncel zorunlu kontroller [.github/workflows/ci.yml](.github/workflows/ci.yml) içindedir. Bunlar format/vet, reachable vulnerability scan, tam testler, migration regression, native TUI/resize/detach journey, Linux race/fuzz ve platforma özgü Docker, vault, paket/kurulum/provenance işlerini kapsar. Değişikliğe göre ilgili yerel testleri çalıştırın; yerel bir alt kümenin tam CI kabulü olduğunu ileri sürmeyin.

Native TUI smoke mevcut bir binary'yi gerçek PTY/ConPTY içinde çalıştırır ve JSON kanıtı üretir. CI binary'yi oluşturduktan sonra aşağıdaki adımı her platform için çalıştırır:

```sh
python scripts/native-tui-smoke.py --binary ./viber --revision "$GITHUB_SHA"
```

Windows'ta binary yolu `./viber.exe` olur. `--revision`, çalıştırılan binary'nin kaynak SHA'sı olmalıdır; bu bayrak kaynak içeriğini hash'lemez. Dirty bir çalışma ağacını yalnız `git rev-parse HEAD` kullanarak temiz ve exact-source kabulü gibi göstermeyin.

Üretim, tag veya yayın adayı iddiası eklemeyin. Engineering yayınlama otomasyonu exact `main` commit'i, tüm zorunlu CI işlerinin başarıyla bitmesini ve attestation/provenance kanıtını ister. Güncel gate durumu ve açık kabul işleri [RELEASE_GATES](docs/RELEASE_GATES.md) içindedir; eksik veya ertelenmiş kabul `release_ready: true` anlamına gelmez.
