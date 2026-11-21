# The builder must not be older than the go directive in go.mod: an older
# image makes Go download a second toolchain mid-build (slow, needs network)
# or fail under GOTOOLCHAIN=local.
FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
# Build info baked in via ldflags, so /api/version identifies the exact
# image a running container came from.
ARG VERSION=dev
ARG COMMIT=none
ARG DATE=unknown
RUN CGO_ENABLED=0 go build \
    -ldflags "-X github.com/sorotrail/sorovista/internal/buildinfo.Version=${VERSION} -X github.com/sorotrail/sorovista/internal/buildinfo.Commit=${COMMIT} -X github.com/sorotrail/sorovista/internal/buildinfo.Date=${DATE}" \
    -o /out/sorovista ./cmd/sorovista

# The tag stays readable; the digest makes two builds of the same commit
# reproducible instead of silently picking up a newer base. Dependabot
# understands digest-pinned FROM lines and will propose refreshes.
FROM alpine:3.24@sha256:f59abe656b692d901e184be3445e084786602108599d2a51d229c0f5a48c71bf

# Redeclared in the final stage so the labels can use them; build arg defaults
# are the values docker-compose and `docker build .` use.
ARG VERSION=dev
ARG COMMIT=none
ARG DATE=unknown
ARG SOURCE=https://github.com/sorotrail/sorovista

LABEL org.opencontainers.image.title="SoroVista" \
      org.opencontainers.image.description="Contract explorer and event-data UI for Stellar/Soroban" \
      org.opencontainers.image.source="${SOURCE}" \
      org.opencontainers.image.revision="${COMMIT}" \
      org.opencontainers.image.version="${VERSION}" \
      org.opencontainers.image.created="${DATE}" \
      org.opencontainers.image.licenses="Apache-2.0"

RUN adduser -D -H sorovista && apk add --no-cache ca-certificates
USER sorovista
COPY --from=build /out/sorovista /usr/local/bin/sorovista
EXPOSE 8080
ENTRYPOINT ["sorovista"]
