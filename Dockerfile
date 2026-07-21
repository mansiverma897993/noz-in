FROM golang:1.25.12-alpine3.23@sha256:cc985ef6f9c3bf9ece7488129c9abe0a150388ccdfa428d886fc709dca0b230a AS build

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=dev
ARG COMMIT=none
RUN CGO_ENABLED=0 go build -trimpath \
    -ldflags="-s -w -X github.com/mansiverma897993/signoz/internal/version.version=${VERSION} -X github.com/mansiverma897993/signoz/internal/version.commit=${COMMIT}" \
    -o /promcast ./cmd/promcast
RUN install -d -m 0700 -o 65532 -g 65532 /workspace /tmp/promcast

FROM alpine:3.23@sha256:fd791d74b68913cbb027c6546007b3f0d3bc45125f797758156952bc2d6daf40 AS certificates
RUN apk add --no-cache ca-certificates

FROM scratch
COPY --from=certificates /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=build /promcast /promcast
COPY --from=build --chown=65532:65532 /workspace /workspace
COPY --from=build --chown=65532:65532 /tmp/promcast /tmp/promcast
ENV TMPDIR=/tmp/promcast
USER 65532:65532
WORKDIR /workspace
ENTRYPOINT ["/promcast"]
