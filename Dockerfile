FROM golang:1.25-alpine AS build
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

FROM alpine:3.20
RUN adduser -D -H sorovista && apk add --no-cache ca-certificates
USER sorovista
COPY --from=build /out/sorovista /usr/local/bin/sorovista
EXPOSE 8080
ENTRYPOINT ["sorovista"]
