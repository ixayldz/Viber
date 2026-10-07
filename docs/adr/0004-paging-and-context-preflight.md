# ADR 0004 — Bounded native paging ve offline context preflight

Durum: 0.4.0-dev offline/native diliminde uygulanmış; production tokenizer, compaction ve provider semantic context kabulü açık.

fs_read exact byte sayfaları döndürür. Default 16 KiB, en çok 64 KiB; exact_bytes base64 authoritative veridir. content yalnız geçerli UTF-8 segmentte bulunur. Split Unicode rune veya binary candidate lossy replacement ile metne çevrilmez. Byte range/total/next cursor açık; FILE read condition tüm dosyanın hash precondition'ıdır, bütün dosyanın görülmüş olduğu iddiası değildir. Random offset candidate digest pin gerektirir. Baseline binary capture hâlâ excluded'dır; binary candidate proposal paging desteklenir.

fs_list default 64/en çok 256 satırla FILE/DIRECTORY/EXCLUDED kayıtlarını sayfalar. Listing condition bütün captured directory manifest'ini bağlar; bir sayfada bulunmayan isim yokluk kanıtı değildir. fs_search default 32/en çok 128 captured match satırını döndürür. 2048-byte excerpt match etrafındadır; line/excerpt/match offsets ve excerpt_complete açıktır. fs_read offset ile exact hydration yapılır. Arama negatif sonucu yalnız captured policy scope içindir; excluded count görünür.

Cursor canonical metadata'nın bounded base64url gösterimidir. Kind/candidate/path veya query/limit/policy digest/generation ile bağlıdır; farklı profile'da sessiz rebinding olmaz. Cursor bir izin değildir; her sayfa mevcut kernel policy/input barrier'dan geçer. Kısıtlı capture dışında daha geniş repository coverage iddiası yoktur.

Offline context compiler raw intent/source-span spec, restriction layers, budget, tools ve bütün paired model/tool protocol history'yi mandatory tutar. Optional drop veya sessiz truncate uygulanmaz. OFFLINE_BYTE_UPPER_BOUND_V1 profili 512 KiB conservative UTF-8 byte-count upper bound, 512 output reserve ve 4096 safety margin kullanır. Bu gerçek provider tokenizer/pricing değildir.

Mandatory context sığmazsa WAITING_RESOURCE + CONTEXT_TOO_SMALL; fixture cursor, steps ve reservation ilerlemez. Successful preflight request bytes'ı CAS'a, manifest'i document'a admission öncesinde bağlar. Request hash, manifest digest/count/estimator load ve current/historical backup closure'da yeniden denetlenir. Status yalnız audit metadata'sını döndürür. Scope revision eski context'i temizler; eski request tarihsel kanıtta korunur.

Replay ve restore model/tool çalıştırmaz. Context compaction, source filters, provider count conformance, stable prefix optimization, pinned model switching ve semantic coverage review henüz yoktur. Bu slice VERIFIED üretmez ve kararlı A–D kapılarını açmaz.
