# Kullanıcı editlerini koruyan delivery preview

`delivery-preview` terminal ve quiescent task'ın baseline B, candidate C ve kaynak dizininden yeni alınan current target U capture'ını karşılaştırır. Canlı dosya, Git index veya tarihsel task durumu değişmez. Güçlü filesystem exclusivity kabulü açık olduğu için auto-apply açılmaz.

~~~powershell
.\bin\viber.exe delivery-preview TASK --store STORE_OUTSIDE_SOURCE --output FRESH_PREVIEW_DIRECTORY --json
~~~

Aktif background owner varsa önce `owner-stop` ile quiescent kapanış sağlanır; eksik store yaratılmaz. Output yeni olmalı, parent dizini önceden bulunmalı; source/store içine output verilmez.

## Sonuç

`READY`: candidate'a ait değişiklikler ve bağımsız kullanıcı editleri birlikte immutable review paketine girer. `merged/` tüm yakalanmış merged source byte'larını, `merged-snapshot.json` source manifest'ini, `changes.patch` U→merged text diff'ini, `current/` değişen path'lerin exact preimage byte'larını içerir. Binary patch eksikse `patch_complete:false` kalır; exact merged bytes korunur.

`CONFLICT`: `preview.json` path başına karar ve reason; `manifest.json` tamamlanmış review işaretidir. Kısmi merged tree/patch yayımlanmaz. Aynı line'daki farklı edit, delete/edit, farklı add/add, mode/namespace çakışması veya bounded merge maliyetinin aşılması açık conflict üretir. HEAD/index/ignore/root değişimi typed stale precondition'dır.

Exit 0 READY preview komutudur; apply, quality veya fulfillment başarı iddiası değildir. Conflict review exit 1 verir; invalid/unsafe/stale argümanlar existing typed error exit sözleşmesini kullanır.

## Binding ve kalite

Plan task seq/document/spec/policy, B/C/U/result manifest digest'leri, algoritma sürümü ve preview digest'i taşır. Paket dosyaları size/SHA ile bound'dur; `manifest.json` en son publish edilir. Kaynak capture BEST_EFFORT'tür; preview, hash ile gelecekteki write arasındaki yarışın kapandığını iddia etmez.

`historical_candidate_quality` yalnız C'nin tarihsel durumudur. `result_verification:UNVERIFIED` ve `requires_reverification:true` her preview'da açıkça kalır. Disjoint text merge semantik uyumluluk kanıtı değildir. Birleşen sonuç için yeni protected origin/goal review/check admission ve fresh doğrulama gerekir; eski receipt yeni sonuç veya live workspace adına kullanılamaz.

Line diff LF/CRLF ve final newline yokluğunu aynen korur. Dosya başına 2 MiB/65536 line; LCS middle matrix en fazla 4 milyon cell, karşılaştırma bütçesi 64 MiB'dir. Uygun olmayan/binary divergent dosyaya tahmini merge yapılmaz. Aynı edit idempotent birleştirilir; bağımsız ekleme/değiştirme/silme korunur, örtüşen insertion sınırları muhafazakâr conflict'tir.

Bu komut durable live apply/restore intent/receipt veya destekli exclusive backend değildir. [Tamamlama planı K05](KEYLESS_COMPLETION_PLAN.md) bu işleri açık tutar; kullanıcı onayı filesystem assurance yerine geçmez.
