# Viber

Modelden bağımsız, uzun yazılım geliştirme görevlerini sürdüren coding CLI/harness projesi.

## Ürün ve geliştirme sözleşmesi

**[prd.md](prd.md), ürünün tek yetkili ve kendi başına yeterli gereksinim belgesidir.** Ürün kapsamı, mimari, veri modeli, güvenlik, context/hafıza, model ve araç sözleşmeleri, CLI/TUI, bütün kodlama fazları, eval ve release koşulları bu dosyada bulunur.

Depo henüz tasarım belgelerini içerir; çalışan uygulama ve ölçülmüş performans sonucu yoktur. Kodlama, PRD'deki A–G fazları ve kabul kapıları üzerinden ilerler.

## Tarihsel tasarım belgeleri

Aşağıdaki dosyalar önceki vizyon ve inceleme kayıtlarıdır. Uygulama için okunmaları gerekmez; çelişkide `prd.md` esas alınır.

- [Ürün ve mimari tasarımı v2](URUN_MIMARISI_V2.md)
- [Uygulama ve değerlendirme planı](UYGULAMA_VE_EVAL_PLANI.md)
- [Mimari inceleme](MIMARI_INCELEME.md)
- [İlk fikir belgesi](idea.md)

## Depo akışı

GitHub deposu: [ixayldz/Viber](https://github.com/ixayldz/Viber). Ana dal `main`, uzak depo `origin`'dir.

Tamamlanan güncellemeler ilgili doğrulamalardan sonra commit edilip `origin/main`'e push edilir. İlgisiz yerel değişiklikler, secrets ve geçici çıktılar commit'e dahil edilmez; force push yapılmaz. Depo çalışma talimatları [AGENTS.md](AGENTS.md) içindedir.
