# Nawala Checker Bot

Bot Telegram untuk monitoring domain yang diblokir KOMINFO (Trustpositif). Auto-check berkala, alert notifikasi, dan manajemen domain via keyboard interaktif.

## Fitur

- Auto cek domain setiap X detik/menit (bisa diatur via bot)
- Parallel processing (10 domain sekaligus)
- Alert cycle: notif aktif → cooldown → repeat
- Sticky block: domain yang pernah blocked diingat permanen
- Multi-grup support
- Keyboard interaktif: Add / Check / Remove / List / Info / Interval

## Instalasi di VPS

### 1. Install Go

```bash
wget https://go.dev/dl/go1.22.0.linux-amd64.tar.gz
tar -C /usr/local -xzf go1.22.0.linux-amd64.tar.gz
echo 'export PATH=$PATH:/usr/local/go/bin' >> ~/.bashrc
source ~/.bashrc
go version
```

### 2. Clone repo

```bash
git clone https://github.com/baxiumren/cek-do.git
cd cek-do
```

### 3. Setup .env

```bash
cp .env.example .env
nano .env
```

Isi dengan data asli:
```
GROUP_1_BOT_TOKEN=token_dari_botfather
GROUP_1_CHAT_ID=-100xxxxxxxxxxxxxxx
GROUP_1_ADMIN_IDS=user_id_admin
```

### 4. Setup domain list

```bash
cp urls_grup_1.txt.example urls_grup_1.txt
nano urls_grup_1.txt
```

Format file:
```
KATEGORI|domain.com
KATEGORI|domain2.net
```

### 5. Build & jalankan

```bash
go build -o nawala-bot .
./nawala-bot
```

### 6. Jalankan sebagai service (agar tetap jalan setelah SSH ditutup)

```bash
sudo nano /etc/systemd/system/nawala-bot.service
```

Isi:
```ini
[Unit]
Description=Nawala Checker Bot
After=network.target

[Service]
Type=simple
User=root
WorkingDirectory=/root/cek-do
EnvironmentFile=/root/cek-do/.env
ExecStart=/root/cek-do/nawala-bot
Restart=always
RestartSec=5

[Install]
WantedBy=multi-user.target
```

```bash
sudo systemctl daemon-reload
sudo systemctl enable nawala-bot
sudo systemctl start nawala-bot
sudo systemctl status nawala-bot
```

### Perintah service

```bash
# Lihat log
sudo journalctl -u nawala-bot -f

# Restart
sudo systemctl restart nawala-bot

# Stop
sudo systemctl stop nawala-bot
```

### Update bot

```bash
cd /root/cek-do
git pull
go build -o nawala-bot .
sudo systemctl restart nawala-bot
```

## Multi-Grup

Tambah grup kedua di `.env`:
```
GROUP_2_BOT_TOKEN=token_bot_kedua
GROUP_2_CHAT_ID=-100xxxxxxxxxxxxxxx
GROUP_2_ADMIN_IDS=user_id_admin
```

Buat file domain:
```bash
cp urls_grup_1.txt.example urls_grup_2.txt
```

## Catatan

- Bot hanya bisa cek domain dari IP Indonesia (API Trustpositif)
- `.env` dan `urls_grup_*.txt` **tidak di-commit** ke GitHub (sudah di `.gitignore`)
- Token bot harus sudah di-delete webhook sebelum jalankan (polling mode)
