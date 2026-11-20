# syntax=docker/dockerfile:1.7

# ---- builder stage -----------------------------------------------------------
# Build the airouter binary with the pure-Go SQLite driver (modernc.org/sqlite)
# so the resulting image has zero C dependencies and runs on any Linux.
# CGO_ENABLED=0 means no C compiler is needed in the build image.
FROM golang:1.22-alpine AS builder

# git is needed when VERSION is derived from `git describe` (the Makefile
# falls back to "dev" when git is unavailable, so this is optional).
RUN apk add --no-cache git ca-certificates

WORKDIR /src

# Cache module downloads — copy go.mod/go.sum first and run `go mod download`
# before copying the rest of the source. This layer is rebuilt only when
# dependencies change.
COPY go.mod go.sum ./
RUN go mod download

# Copy the source tree.
COPY . .

# Build the CLI. We pin CGO_ENABLED=0 for the pure-Go SQLite driver so the
# final binary is fully static and has no glibc/musl dependency.
# ldflags -s -w strips the symbol table and DWARF info for a smaller image.
ARG VERSION=docker
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
    go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" \
    -o /out/airouter ./cmd/airouter

# ---- runtime stage -----------------------------------------------------------
# Use a small image. alpine gives us a working shell + ca-certificates for
# HTTPS calls to upstream providers. For an even smaller image you can use
# `gcr.io/distroless/static-debian12:nonroot` (no shell, no package manager)
# but alpine is friendlier for debugging.
FROM alpine:3.20

# ca-certificates: HTTPS to upstream providers (Groq, Gemini, etc.)
# tzdata: timezone data for the per-provider daily-reset logic (Gemini resets
#   at PT midnight; without tzdata the timezone lookup falls back to UTC).
RUN apk add --no-cache ca-certificates tzdata && \
    addgroup -S airouter && adduser -S -G airouter airouter

# Where the SQLite database lives. Mount this volume in `docker run` so the
# DB, provider keys (encrypted) and tenant key hashes persist across restarts.
# The .key file next to it holds the AES-256 secret — back it up OUT of the
# volume (or set AIROUTER_SECRET explicitly).
ENV AIROUTER_DB=/data/airouter.db
ENV AIROUTER_CORS=""
ENV AIROUTER_ADMIN_TOKEN=""

WORKDIR /data
VOLUME ["/data"]

COPY --from=builder /out/airouter /usr/local/bin/airouter

USER airouter

EXPOSE 8080

# Default to tenant-only mode (no admin endpoints). Set AIROUTER_ADMIN_TOKEN
# to enable admin endpoints, and set AIROUTER_CORS to allow browser origins.
ENTRYPOINT ["airouter"]
CMD ["serve", "-addr", ":8080", "-db", "/data/airouter.db"]
