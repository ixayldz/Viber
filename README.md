# Viber

Viber, terminalden kodlama görevleri başlatır; önerilen değişiklikleri kaynak projenizden ayrı bir adayda tutar ve incelemenize veya dışa aktarmanıza izin verir.

> **Engineering önizlemesi:** Public paket [`engineering-96fe0b8`](https://github.com/ixayldz/Viber/releases/tag/engineering-96fe0b84284e270806ac6607433ab1a75114b6cd) sürümüdür; indirme ve Windows kurulum kabulü geçti. Aşağıdaki yeni PRD seçimi ve model worker komutları güncel kaynakta bulunur, bu pakette henüz yoktur. Viber kararlı veya üretime hazır bir sürüm değildir.

[Kurulum](docs/INSTALL.md) · [CLI kılavuzu](docs/CLI_REFERENCE.md) · [Yetenekler ve sınırlar](docs/CAPABILITIES.md)

## Görev başlatın

Viber kurulduktan sonra projenizin klasöründe terminal açın ve çalıştırın:

```sh
viber
```

**ChatGPT**'yi seçin, geçerli proje klasörünü kabul edin veya başka bir yol girin; ardından bir PRD seçin:

- Listelenen kök `PRD.md` dosyalarından birini seçin. Adlar büyük-küçük harf duyarsız eşleşir; alt klasörler listelenmez.
- Başka bir belge için `docs/product-spec.md` gibi proje göreli bir yol yazın.
- PRD kullanmamak için `NONE` yazın.

Sonra görevi kendi sözlerinizle yazın. Seçtiğiniz PRD, özel bir görev metni girseniz de göreve bağlı kalır. Viber seçim sırasında PRD içeriğini açmaz; görev akışı projenin yakalanmış kopyasını kullanır. Komut satırındaki `--prd FILE`, proje göreli dosyayı açıkça seçer ve seçim sorusunu atlar.

Görev ve proje bağlamının ChatGPT'ye gönderilmesi için Viber sizden `YES` yazmanızı ister. Plan kullanımı izni olan bağlı bir ChatGPT profili yoksa resmi ChatGPT tarayıcı akışıyla giriş yapmak için `LOGIN` yazın ve plan iznini gözden geçirin. **API anahtarı gerekmez.**

## Görevi inceleyin

Görev arayüzü slash komutlarını kabul eder. Derlemenizdeki komutları görmek için `/help` yazın.

```text
/plan
/requests
/approve [REQUEST_ID]
/reject [REQUEST_ID]
/resume
/diff
/export
/status
/verification
```

Yakalanmış aday hakkında ayrı model incelemesi başlatmak için `/explore SORU` veya
`/review SORU` yazın. `/worker result` son incelemeyi gösterir; `/worker cancel GOAL_ID`
seçilen işi durdurur. Bu işler ana görevin kalan bütçesinden pay alır, aynı model
bağlantısını kullanır ve dosya değiştiremez. Sonuçları model tavsiyesidir;
doğrulanmış başarı sayılmaz. Owner aynı anda bir model görevini yürütür.

Onaylamadan önce `/requests` ile önerileri okuyun. Birden fazla bekleyen istek varsa doğru kimliği `/approve` veya `/reject` komutuna ekleyin. Onay görevi sürdürmez; devam etmek için `/resume` yazın. Önerileri `/diff` ile inceleyin, `/export` ile teslim paketini kaydedin. Varsayılan akış kaynak projenizdeki dosyaları değiştirmez.

İlk görev varsayılan olarak en fazla 16 model turu, 64 araç çağrısı ve 2 dakika etkin çalışma bütçesi alır. Büyük bir PRD'yi daha dar görevlere bölün; mevcut kullanımı `/budget` ile izleyin. Yeni görev için daha geniş ama sınırlı bir bütçe seçebilirsiniz:

```sh
viber start --max-steps 32 --max-tool-calls 128 --max-active-ms 300000 --max-input-tokens 1048576
```

`--max-steps`, `--max-tool-calls`, `--max-active-ms`, `--max-input-tokens` ve `--max-output-tokens` görev başlangıcında doğrulanır. `--goal-review` mutlak yollu, proje ve görev verisinin dışındaki operatör JSON dosyasını bağlar. Resume mevcut görevin kalan bütçesini kullanır; yeni bütçe başlatmaz.

Varsayılan olarak build veya test check'i seçilmez. Check kaydetmek için hem proje klasörünün hem görev verisinin dışında duran, operatör tarafından hazırlanmış yapılandırmayı verin:

```sh
viber start --check-config "ABSOLUTE_PATH_TO_CHECK_CONFIG"
```

Viber bu dosyadaki sabit check kimliklerini ve komutlarını kaydeder; depodaki rastgele scriptleri çalıştırmaz. İzole süreç check'leri desteklenen bir Linux Docker motoru ve sabitlenmiş image gerektirir. Check yapılandırması yoksa adayı inceleyin ve doğrulamayı bilinmiyor kabul edin.

Duraklatılmış veya bitmiş görevi `/preview` ile güncel projeyle karşılaştırın. `/export` inceleme paketini kaynak projenin dışına yazar. `/quit` arayüzden çıkar; görev başlarken gösterilen kimlik ve özel kayıt yoluyla tekrar bağlanın:

```sh
viber ui TASK_ID --store "PRIVATE_TASK_PATH"
```

`FINISHED` durumu veya 2 çıkış kodu doğrulanmış başarı anlamına gelmez. Kaydedilen sonucu ve sınırlarını görmek için `/status` ve `/verification` kullanın.

## Ayrıntılar

- [Paketi kurma, doğrulama, güncelleme veya kaldırma](docs/INSTALL.md)
- [Komutlar ve check yapılandırması](docs/CLI_REFERENCE.md)
- [Desteklenen yetenekler ve açık sınırlar](docs/CAPABILITIES.md)
- [Doğrulama kaydı](docs/VALIDATION.md)

Viber MIT lisanslıdır. Ürün gereksinimleri [prd.md](prd.md) içindedir.
