# Güvenlik politikası

Viber, yerel proje kaynaklarını, görev store'unu ve hesap kimlik bilgilerini işleyen bir engineering ön sürümüdür. Mevcut paketler kararlı/üretime hazır olarak ilan edilmemiştir; `doctor.release_ready` değeri `false` ve stable A–D release gate'leri kapalıdır. Desteklenen kapsam ve açık güvenlik/kabul sınırları için [CAPABILITIES](docs/CAPABILITIES.md), [RELEASE_GATES](docs/RELEASE_GATES.md) ve [INSTALL](docs/INSTALL.md) belgelerini okuyun.

## Bir güvenlik sorunu bildirme

Kimlik bilgisi, token, kullanıcı/proje verisi, çalıştırılabilir exploit veya istismar edilebilir ayrıntıları public issue, pull request ya da log ekinde yayımlamayın. Bu depo şu anda özel bir güvenlik e-posta adresi, başka bir özel iletişim kişisi, bug bounty veya yanıt süresi taahhüdü yayımlamıyor.

GitHub depo sayfasında **Report a vulnerability** özel bildirim seçeneği sunuluyorsa, hassas ayrıntıları bu GitHub özel kanalında paylaşın. Bu belge seçeneğin etkin olduğunu iddia etmez. Seçenek yoksa, public issue'da yalnız özel bildirim yolunu soran hassas olmayan bir not bırakın; özel kanal açıkça sağlanana kadar istismar ayrıntılarını paylaşmayın. Hassas olmayan güvenlik iyileştirmelerini normal GitHub issue veya pull request olarak sunabilirsiniz.

Güvenli bir kanalda bildirim yaparken, mümkünse etkilenen tam sürüm/source revision'ı, işletim sistemi/platformu, gerekli yapılandırma ve yeniden üretmek için gereken en küçük adımları ekleyin. Raporu paylaşmadan önce token'ları, yerel mutlak yolları, proje içeriğini ve kişisel verileri çıkarın. Bu politika bir yanıt veya çözüm süresi garantisi vermez.

## Paketleri ve yerel veriyi koruma

Yayımlanan paketi çalıştırmadan önce [kurulum kılavuzundaki provenance doğrulamasını](docs/INSTALL.md#indirme-ve-doğrulama) uygulayın. Store'u proje dizininin dışında tutun; kimlik bilgilerini source tree'ye veya paylaşılan test kanıtına kopyalamayın. Viber'ın canlı apply/restore desteği, işletim sistemi ve dosya sistemi profiline göre sınırlıdır; başka platformlarda candidate/patch fallback'i kullanılır. Bu sınırları [anahtarsız operasyonlar kılavuzundan](docs/KEYLESS_OPERATIONS.md#güvenli-teslim) kontrol edin.

Test, fixture veya engineering CI sonucu tek başına tüm güvenlik gereksinimlerinin kapandığını göstermez. Tam kabul durumu [release kanıt kaydında](docs/RELEASE_GATES.md) tutulur.
