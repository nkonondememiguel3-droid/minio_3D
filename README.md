# Multi-Tenant S3-Compatible File Storage SaaS

A production-ready backend service for uploading, retrieving, and managing files with:
- **Multi-tenancy** — every file is scoped to a user
- **Content-addressable deduplication** — SHA-256 prevents storing the same bytes twice
- **Reference counting** — safe shared-object deletion across users
- **Per-user quotas** — configurable storage limits enforced at the DB level
- **JWT authentication** — stateless, horizontally scalable
- **S3-compatible storage** — swap MinIO → AWS S3 → Cloudflare R2 via config

---

## Project Structure

```
.
├── config/                  # Environment-based configuration
├── db/
│   ├── db.go                # Connection pool + migration runner
│   └── migrations/
│       └── 001_init.sql     # Schema: users, files (ref_count, quotas)
├── handlers/
│   ├── auth.go              # POST /auth/register, POST /auth/login
│   ├── auth_test.go
│   ├── files.go             # POST /files/upload, GET /files, etc.
│   └── files_test.go
├── middleware/
│   ├── auth.go              # JWT Bearer token validation
│   └── auth_test.go
├── service/
│   ├── file_service.go      # Orchestration: dedup + quota + storage
│   └── file_service_test.go
├── storage/
│   ├── metadata.go          # PostgreSQL repository
│   ├── metadata_integration_test.go  # Real-DB tests (build-tagged)
│   ├── minio.go             # MinIO / S3 adapter
│   ├── sha256_test.go       # Pure function unit tests
│   ├── storage.go           # Storage interface
│   └── types.go             # User, FileMeta domain types
├── testutil/
│   └── testutil.go          # MockStorage + MockMeta for tests
├── .env.example             # Configuration template
├── .env                     # Local dev values (do not commit)
├── docker-compose.yml       # PostgreSQL + MinIO + API
├── Dockerfile               # Multi-stage build
├── go.mod
└── main.go                  # Server entry point
```

---

## Quick Start (Docker Compose)

The fastest way to get everything running:

```bash
# 1. Clone and enter the project
cd minio_s3

# 2. Start all services (Postgres + MinIO + API)
docker compose up --build

# 3. The API is now available at http://localhost:8080
# MinIO console: http://localhost:9001  (user: minioadmin / minioadmin)
```

---

## Local Development (without Docker for the API)

Run Postgres and MinIO in Docker, but the Go server natively for faster iteration.

```bash
# 1. Start only the infrastructure
docker compose up postgres minio -d

# 2. Copy and edit the environment file
cp .env.example .env
# Edit .env if needed (defaults work out of the box)

# 3. Load environment and run the server
export $(grep -v '^#' .env | xargs)
go run .

# Server starts on http://localhost:8080
```

---

## API Reference

### Authentication

#### Register
```
POST /auth/register
Content-Type: application/json

{
  "email": "alice@example.com",
  "password": "mysecurepassword"
}
```
Response `201`:
```json
{ "token": "<jwt>" }
```

#### Login
```
POST /auth/login
Content-Type: application/json

{
  "email": "alice@example.com",
  "password": "mysecurepassword"
}
```
Response `200`:
```json
{ "token": "<jwt>" }
```

---

### Files

All file endpoints require:
```
Authorization: Bearer <jwt>
```

#### Upload a file
```
POST /files/upload
Content-Type: multipart/form-data
Field: file (the file to upload)
```
Response `201` (new file) or `200` (duplicate detected):
```json
{
  "file": {
    "id": "uuid",
    "sha256": "...",
    "size": 12345,
    "content_type": "application/pdf",
    "original_name": "report.pdf",
    "created_at": "2025-01-01T00:00:00Z"
  },
  "duplicate": false
}
```

**Upload with curl:**
```bash
curl -X POST http://localhost:8080/files/upload \
  -H "Authorization: Bearer $TOKEN" \
  -F "file=@/path/to/your/file.pdf"
```

#### List files
```
GET /files
```
Response `200`:
```json
{ "files": [...], "count": 3 }
```

#### Get file metadata
```
GET /files/:id
```
Response `200`: file metadata object

#### Get presigned download URL
```
GET /files/:id/url
```
Response `200`:
```json
{ "url": "https://..." }
```
The URL is valid for `PRESIGNED_URL_MINUTES` (default: 15 minutes).

#### Delete a file
```
DELETE /files/:id
```
Response `200`:
```json
{ "message": "file deleted" }
```

#### Health check
```
GET /health
```
Response `200`: `{ "status": "ok" }`

---

## Error Codes

| Status | Meaning |
|--------|---------|
| 400 | Bad request (missing fields, invalid email, password too short) |
| 401 | Missing or invalid JWT |
| 403 | File does not belong to the authenticated user |
| 404 | File not found |
| 409 | Email already registered |
| 429 | Storage quota exceeded |
| 500 | Internal server or storage error |

---

## Running Tests

### Unit tests (no external dependencies)

```bash
go test ./...
```

This runs all tests that don't require Postgres or MinIO:
- `storage/sha256_test.go` — SHA-256 hashing
- `middleware/auth_test.go` — JWT validation
- `service/file_service_test.go` — full service logic with mocks
- `handlers/auth_test.go` — auth HTTP handlers
- `handlers/files_test.go` — file HTTP handlers

### Integration tests (require Postgres)

```bash
# Start Postgres
docker compose up postgres -d

# Run with build tag and DSN
TEST_DB_DSN="host=localhost port=5432 user=storageuser password=storagepass dbname=storagedb sslmode=disable" \
  go test -tags=integration ./storage/...
```

Integration tests cover:
- `Save` with real transactions (dedup, ref_count, quota enforcement)
- `Delete` with ref_count decrement and ShouldDeleteObject flag
- Quota enforcement at the Postgres level

---

## Configuration Reference

| Variable | Default | Description |
|----------|---------|-------------|
| `PORT` | `8080` | HTTP server port |
| `DB_HOST` | `localhost` | PostgreSQL host |
| `DB_PORT` | `5432` | PostgreSQL port |
| `DB_USER` | `storageuser` | PostgreSQL user |
| `DB_PASSWORD` | `storagepass` | PostgreSQL password |
| `DB_NAME` | `storagedb` | PostgreSQL database name |
| `DB_SSLMODE` | `disable` | PostgreSQL SSL mode |
| `STORAGE_ENDPOINT` | `localhost:9000` | MinIO / S3 endpoint |
| `STORAGE_ACCESS_KEY` | `minioadmin` | Access key |
| `STORAGE_SECRET_KEY` | `minioadmin` | Secret key |
| `STORAGE_BUCKET` | `documents` | Bucket name (auto-created) |
| `STORAGE_USE_SSL` | `false` | Use HTTPS for storage |
| `JWT_SECRET` | *(insecure default)* | **Change in production** |
| `JWT_EXPIRY_HOURS` | `24` | JWT lifetime in hours |
| `PRESIGNED_URL_MINUTES` | `15` | Presigned URL TTL |
| `DEFAULT_QUOTA_BYTES` | `10737418240` | Per-user quota (10 GB) |
| `GIN_MODE` | `release` | `debug` or `release` |

---

## Switching to AWS S3 or Cloudflare R2

No code changes needed — only `.env` changes:

**AWS S3:**
```env
STORAGE_ENDPOINT=s3.amazonaws.com
STORAGE_ACCESS_KEY=AKIAIOSFODNN7EXAMPLE
STORAGE_SECRET_KEY=wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY
STORAGE_BUCKET=your-bucket-name
STORAGE_USE_SSL=true
```

**Cloudflare R2:**
```env
STORAGE_ENDPOINT=<account-id>.r2.cloudflarestorage.com
STORAGE_ACCESS_KEY=<r2-access-key>
STORAGE_SECRET_KEY=<r2-secret-key>
STORAGE_BUCKET=your-bucket-name
STORAGE_USE_SSL=true
```

---

## Production Checklist

- [ ] Set `JWT_SECRET` to a random 32+ byte string (`openssl rand -hex 32`)
- [ ] Set `GIN_MODE=release`
- [ ] Set `DB_SSLMODE=require` and provision TLS certificates for Postgres
- [ ] Set `STORAGE_USE_SSL=true` for any non-local storage backend
- [ ] Set `DEFAULT_QUOTA_BYTES` to an appropriate limit for your use case
- [ ] Run behind a reverse proxy (nginx / Caddy) for TLS termination
- [ ] Set `ReadTimeout` / `WriteTimeout` according to your maximum expected file size

---

## Architecture Notes

### Deduplication
Files are identified by their SHA-256 hash. If two users upload identical content, only one object is written to storage. Each user gets their own `files` row pointing to the same `object_key`.

### Reference Counting
`ref_count` on the `files` table tracks how many rows share an `object_key`. The backing object is only deleted from storage when `ref_count` reaches zero, preventing one user's delete from removing a shared object.

### Quota Enforcement
`storage_bytes_used` and `storage_quota_bytes` on the `users` table are updated inside the same database transaction as the file insert/delete, preventing drift under concurrent uploads.
