# ADR 0003 — Yerel owner IPC ve steering sınırı

Durum: 0.3.0-dev offline/native profilinde uygulanmış; tam supervisor ve platform adversarial conformance açık.

Tek Session SQLite owner kilidini tutar. İkinci CLI ikinci writer açmadan owner'a bağlanır. Her owner yeni generation, rastgele endpoint identity ve PID yayınlar. Private store içindeki owner.json en son yayınlanır. IPC paketi kilidin caller tarafından alınmış olmasını gerektirir; owner paketi owned Session kullanır.

Windows: Microsoft go-winio v0.6.2, first-instance named pipe, yalnız current SID için DACL, OS remote-client rejection. İki taraf kernel pipe peer PID'sini okur, process token SID'sini doğrular; client descriptor PID'sini de pin eder. Linux SO_PEERCRED, macOS LOCAL_PEERCRED + LOCAL_PEERPID ile aynı UID ve owner PID kontrol edilir. Unix socket 0600; uzun store yolu için yeni 0700 geçici endpoint dizini kullanılır. TCP ve endpoint formatı dışındaki adresler reddedilir. Farklı kullanıcı/hostile namespace acceptance matrisi bu unit testlerle kapanmaz.

4-byte big-endian frame, 8 MiB üst sınır, strict JSON, 32 eşzamanlı bağlantı, read/write deadlines ve 15 dakika command sınırı vardır. Request/response task/command/request ID, owner ID ve generation ile bağlanır. Raw transcript/continuation status cevabında taşınmaz. Durum ve event çıktısı explicit operator okumasıdır; server tool/shell/live-write RPC yoktur.

Resume dispatch öncesinde durable admission, bitiminde durable result/error receipt kaydeder. Aynı command ID aynı tarihsel sonucu döndürür; farklı input COMMAND_ID_CONFLICT olur. Admission var, completion yoksa aynı ID kör tekrar edilmez. İletim başladıktan sonra response kaybı UNKNOWN_OPERATION_OUTCOME olur; CLI başka owner açıp tekrar çalıştırmaz. Ulaşılamayan stale descriptor yalnız OS store kilidi alınarak reconcile edilir.

Pause/cancel aktif loop'un admission'ını kapatıp bounded drain yapar. Ctrl+C ve owner kapanışı pause eder. Cancel CANCELLED atar. Pending native/model intent UNKNOWN kalabilir; token reservation silinmez. Eski pause receipt'i yeni invocation'ı kesmez. Terminal business outcome/quality değişmez; yalnız yeni generation ile kontrol receipt audit'i eklenebilir.

Steering ham UTF-8 byte'larını önce immutable blob'a, ardından InputRecorded event'ine yazar. Bariyer, in-flight candidate pointer'ın eski task sequence ile commit etmesini engeller. Aktif native loop duraklatılır. Semantik yorum yapılıp izin verilmez. Eksik raw input load/backup/revision'ı reddeder.

Bound revise komutu oldest pending input/spec/epoch/candidate ve yeni offline fixture'a bağlıdır. Başlangıç gereksinimlerini ve protected origin'i korur; yeni source-span requirement ekler; spec/epoch yükseltir; eski onayları ve model continuation'ını invalid eder. Budget ve candidate korunur; unknown effect revise ile temizlenemez. Limited delivery izni yeniden alınır. Queue'da başka input varsa barrier sürer. Revise model/tool çalıştırmaz; explicit resume gerekir.

Offline fixture fresh scope için yeni fixture gerektirir. Bu davranış gerçek provider semantic planning/revision değildir. Real model loop, process fencing, automatic supervisor/detach/attach/TUI, streaming cursor/backpressure ve OS hostile-principal testleri tamamlanmadı. Stable A–D kapıları kapalı kalır.

Kaynaklar: [Microsoft go-winio](https://github.com/microsoft/go-winio), [go-winio API](https://pkg.go.dev/github.com/Microsoft/go-winio).
