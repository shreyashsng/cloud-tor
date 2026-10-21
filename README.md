# Cloud-Tor

> **v1.0.0 — High-Speed Cloud Storage Downloader & Automated 6-Hour Purge**

Cloud-Tor is an ultra-lightweight, high-performance self-hosted Go engine that downloads BitTorrent swarms directly into S3-compatible cloud object storage (e.g. [Backblaze B2](https://www.backblaze.com/b2/cloud-storage.html)). Once ingested, users receive a secure **6-hour signed download URL**, after which files are automatically and permanently purged.

---

## Features in v1.0.0

| Feature | Description |
| :--- | :--- |
| **Backblaze B2 & S3 Storage** | Native AWS S3 SDK v2 multipart streaming directly from swarm into Backblaze B2 buckets with zero local host disk usage. |
| **Automated 6-Hour Auto-Purge** | Persistent storage ledger with an automated background janitor that actively purges expired objects when the 6-hour retention window elapses. |
| **Multi-Bucket Distribution** | Supports multiple buckets on the same provider (or across multiple S3 providers) with round-robin load distribution. |
| **Consumer-Facing Interface** | Completely anonymized UI hiding internal infrastructure details—users simply paste a magnet link, click download, and get a fast 6-hour link. |
| **Script File Security Sandbox** | Automatically blocks execution scripts (`.bat`, `.cmd`, `.sh`, `.vbs`, `.ps1`, `.scr`) for safety while allowing applications (`.exe`), media, and archives. |

---

## How 6-Hour Retention Works on Backblaze B2

Standard cloud object lifecycle rules only support a minimum expiration granularity of **1 Day (24 hours)**. Sub-day lifecycle rules cannot be configured in cloud storage dashboards.

Cloud-Tor solves this cleanly:
1. **6-Hour Pre-Signed URL**: Every download URL is signed cryptographically to expire in exactly 6 hours (`Expires: 6 * time.Hour`).
2. **Persistent Storage Ledger**: The server records object metadata, upload time, and exact expiration timestamp (`CreatedAt + 6h`) in `storage_ledger.json`.
3. **Automated Janitor Worker**: A background sweeper routine runs every 2 minutes. When an object passes its 6-hour mark, Cloud-Tor calls the S3 `DeleteObject` API to permanently remove it from your bucket.

---

## Quick Start (Local)

### 1. Build and Run
```bash
# Pure Go mode (no C compiler required)
go env -w CGO_ENABLED=0
go run main.go
```

Or build an optimized single binary:
```bash
go build -ldflags="-s -w" -o cloud-tor.exe .
./cloud-tor.exe
```

### 2. Configure Backblaze B2 Environment (`.env`)
Copy `.env.example` to `.env`:
```env
PORT=3000
PUBLIC_URL=http://localhost:3000

# Backblaze B2 Cloud Storage Configuration
B2_APPLICATION_KEY_ID=your_key_id_here
B2_APPLICATION_KEY=your_application_key_here
B2_ENDPOINT=https://s3.eu-central-003.backblazeb2.com
B2_REGION=eu-central-003

# Bucket Name(s):
B2_BUCKETS=webt-temp

# Retention Lifetime in hours (Default: 6)
STORAGE_RETENTION_HOURS=6
```

*(You can also configure or update storage credentials dynamically directly in the web UI via the **Storage Config** button).*

Open **`http://localhost:3000`** in your browser.

---

## Verification Test

To verify your Backblaze B2 bucket connection, upload, 6-hour signed link generation, and deletion:
```bash
go run cmd/test_storage/main.go
```

---

## Security Sandbox (Disallowed Scripts)

To protect the server and client environment from script-based attacks, the following file types are automatically blocked:
- **Batch / Command**: `.bat`, `.cmd`, `.btm`
- **Shell Scripts**: `.sh`, `.bash`, `.zsh`, `.csh`, `.ksh`, `.command`
- **PowerShell**: `.ps1`, `.ps1xml`, `.ps2`, `.psc1`, `.psc2`
- **Windows Script Host / VBS**: `.vbs`, `.vbe`, `.js`, `.jse`, `.wsf`, `.wsh`, `.hta`, `.scr`, `.pif`, `.com`, `.cpl`, `.reg`

**Permitted Files**:
- Application executables (`.exe`, `.msi`, `.dll`)
- Disk images & archives (`.iso`, `.zip`, `.rar`, `.7z`, `.tar`, `.gz`)
- Video & Audio (`.mp4`, `.mkv`, `.avi`, `.mov`, `.mp3`, `.flac`, etc.)
- Documents & eBooks (`.pdf`, `.epub`, `.docx`, etc.)

---

## API Reference

| Endpoint | Method | Description |
| :--- | :--- | :--- |
| `POST /api/torrent/resolve` | `POST` | Inspects magnet metadata and returns file list with security status flags. |
| `POST /api/storage/prepare` | `POST` | Downloads file from swarm into B2 and generates 6-hour signed link. |
| `GET /api/jobs/:jobId` | `GET` | Returns real-time upload progress, speed, ETA, and final signed download link. |
| `GET /api/storage/status` | `GET` | Returns count of active buckets, storage readiness, and active stored objects. |
| `POST /api/storage/config` | `POST` | Dynamically updates storage credentials without server restarts. |
| `GET /api/stream/:hash/:index` | `GET` | Preview stream with HTTP 206 Partial Content (disallowed for script files). |
| `GET /api/health` | `GET` | Memory diagnostics, uptime, and configuration status. |

---

## License
MIT
