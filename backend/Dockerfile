# Multi-stage build for the VMaaS backend.
#
# Stage 1 compiles a static binary against Go 1.23.
# Stage 2 ships the binary on distroless (no shell, no package manager).
# Final image is ~25 MB and contains nothing but `vmaasd` + CA certs.

FROM golang:1.23-alpine AS build
WORKDIR /src

RUN apk add --no-cache git ca-certificates

COPY go.mod go.sum ./
RUN go mod download

COPY . .

# CGO off → fully static; -trimpath strips local paths from the binary;
# -ldflags="-s -w" drops the symbol table to shrink the image.
ENV CGO_ENABLED=0 GOOS=linux GOARCH=amd64
RUN go build -trimpath -ldflags="-s -w" -o /out/vmaasd ./cmd/vmaasd

FROM gcr.io/distroless/static-debian12:nonroot
WORKDIR /
COPY --from=build /out/vmaasd /vmaasd
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt

EXPOSE 8080
USER nonroot:nonroot
ENTRYPOINT ["/vmaasd"]
CMD ["--config", "/etc/vmaas/vmaas.yaml"]
