# OpsCore single-node deployment image (Phase 15.2 packaging only).
# This Dockerfile introduces NO new runtime path — the composition root stays
# cmd/opscore-server (ADR-032 A-1). The multi-node seam remains inert (A-8).

FROM golang:1.22 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -o /out/opscore-server ./cmd/opscore-server

FROM gcr.io/distroless/static-debian12
COPY --from=build /out/opscore-server /usr/local/bin/opscore-server
COPY deploy/opscore.json.example /etc/opscore/opscore.json
# Restrictive data dir for the policy store (§3.5); run as non-root.
RUN mkdir -p /var/lib/opscore/policy && chown -R nonroot:nonroot /var/lib/opscore
USER nonroot:nonroot
EXPOSE 8080 8081
ENTRYPOINT ["/usr/local/bin/opscore-server", "-config", "/etc/opscore/opscore.json"]
