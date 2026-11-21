# turnier – Live-Wertung für Gardetanz-Turniere
#
# Multi-Stage-Build: Go-Binary (ohne CGO) + minimaler distroless-Runner.
# Daten (SQLite) liegen in /data (Volume mounten).

# --- Build ---------------------------------------------------------------
FROM golang:1.27 AS builder

WORKDIR /src

# Dependencies kachen (eigene Cache-Layer)
COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o /out/turnier .

# --- Runtime --------------------------------------------------------------
FROM gcr.io/distroless/static

COPY --from=builder /out/turnier /usr/local/bin/turnier

# Nicht root; DB im Volume /data (Default: /data/turnier.db)
USER nonroot
WORKDIR /data
VOLUME /data

# Normal: 8080 (HTTP). HTTPS-Modus (-tls-domain): 8080 = ACME + Redirect,
# 8443 = TLS. Auf dem Host auf 80/443 mappen – der nonroot-User im Image
# darf keine privilegierten Ports binden, deshalb laufen die Defaults auf
# 8080/8443.
EXPOSE 8080 8443

ENTRYPOINT ["turnier"]
