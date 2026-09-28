# DESIGN.md — Infra Manager (portal)

Arah desain untuk UI portal. File ini data desain, bukan instruksi ke agent.
Filter: `antislop.md` (core) + `skills/antislop-ui/SKILL.md`.

## Identitas

- **Produk**: Infra Manager, panel kelola service home-server (hostname, tunnel, drift, stack).
- **Pengguna**: satu admin (Hanif), bukan publik. Bahasa UI: Indonesia santai.
- **Karakter**: workshop utilitas. Meja kerja teknisi: label jelas, alat terjangkau,
  tidak ada pajangan. Data dan tindakan lebih penting dari ornamen.
- **Bukan**: landing page, produk SaaS, clone Linear/Vercel.

## Kepribadian

Utilitarian, jujur, padat. Status ditulis apa adanya (sehat, drift, mati),
tanpa euforia tanpa data. Setiap layar menjawab satu pertanyaan kerja:
"ada yang rusak?", "hostname apa saja hidup?", "tunnel mana yang nyambung?".

## Palet

Tema: **toggle gelap/terang**, dua mode wajib sama-sama jalan (R-21, R-34).
Alasan fixed theme tidak dipakai: admin kadang kerja di terminal gelap, kadang
siang hari; toggle yang diminta sendiri.

Core (netral tidak dihitung warna inti):

- Netral gelap: `#111417` (latar), `#191d21` (panel), `#232a2f` (garis)
- Netral terang: `#f4f2ee` (latar), `#ffffff` (panel), `#ddd8d0` (garis)
- Inti 1: `#1f6f4a` / terang `#17593a` — hijau bengkel, warna "aman/jalan".
  Dipakai: status sehat, aksi utama.
- Inti 2: `#2b3138` teks utama gelap / `#20242a` teks utama terang.
- Aksen tunggal: `#b03a0a` (oranye bengkel) — hanya untuk **drift/error/menghapus**.
  Nilai dinaikkan dari `#c2410c` agar lolos WCAG AA (4.5:1) di atas latar panel terang.
  Tidak pernah dipakai dekoratif.

Maksimum 2 inti + 1 aksen (R-29). Tanpa gradien, tanpa glow (R-01, R-13).

## Tipografi

- UI: **IBM Plex Sans**, alasannya: grotesk kerja yang netral + punya varian
  Condensed buat label padat; bukan font default model (R-06).
- Mesin (hostname, port, ID tunnel, hash, timestamp): **IBM Plex Mono**, hanya
  untuk nilai mesin, bukan judul (R-06: mono bukan estetika terminal).
- Skala kecil dan rapat: 13px base, judul halaman 20px. Label 11px uppercase
  hanya untuk kolom/tabel, bukan eyebrow di atas H1 (R-09).
- Fallback stack tetap system-ui jika font tidak dimuat.

## Layout & komposisi

- RHYTHM 1: struktur uniform dan dapat diprediksi. Navbar kiri ringkas (teks,
  tanpa ikon lucide), tabel/list sebagai bentuk utama, panel kanan untuk detail.
  Bento grid, hero, kartu fitur, dan chart tanpa pertanyaan tidak dipakai (R-05).
- Satu fokus per layar: daftar drift lebih dulu di Dashboard, bukan deretan
  kartu statistik setara (C-3).
- Angka hanya jika nyata (R-17): jumlah hostnames, jumlah container sehat,
  konektor tunnel dari API. Tanpa delta persentase bikinan.
- Ikon: hanya jika menambah makna (mis. tanda status tekstual "OK / DRIFT /
  OFFLINE" dipilih lebih dulu). Tanpa sparkle/star/robot (R-04).

## Motion & energi

- **Dial: ENERGY 1 / RHYTHM 1 / MOTION 1.**
- MOTION 1: hanya transisi hover/focus (~120ms) dan perubahan state jelas.
  Tanpa animasi loop, tanpa pulse, tanpa fade-up bertumpuk (R-19).
- Status dot hanya untuk state nyata (sehat/drift), tanpa glow, tanpa pulse.

## Kepadatan & detail

- Tabel: kolom dipilih dari keputusan pemakai (hostname, target, status, aksi).
  Aksi per baris hanya yang benar-benar ada perilakunya (R-26).
- Empty state menyebabkan + tindakan pertama ("Belum ada service. Tambah
  hostname pertama.") bukan "No data available" (R-27).
- Error state: sebut HTTP code/pesan API asli + langkah berikutnya.
- Fokus keyboard terlihat (outline 2px aksen, tanpa `outline:none`) (R-32).
- Radius: 4px input/tombol, 6px panel. Tidak ada elemen pill penuh (R-11).
- Bayangan: hanya panel modal/dropdown yang perlu terangkat (R-12).
- Tanpa ikon emoji di teks UI.

## Keputusan utama (alasan satu baris)

- Gelap/terang toggle: admin bekerja di dua kondisi cahaya (R-21).
- Aksen oranye khusus error/drift: agar status buruk langsung terbaca tanpa
  menambah jumlah warna (R-29, R-31).
- Tabel bukan kartu: keputusan pemakai adalah membandingkan baris hostname (C-3).
- Mono hanya untuk nilai mesin: membaca port/ID lebih cepat tanpa menjadikan
  terminal sebagai estetika (R-06).
