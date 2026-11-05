FROM --platform=$BUILDPLATFORM node:22-alpine AS web
WORKDIR /src/web
COPY web/package*.json ./
RUN npm ci
COPY web/ ./
RUN npm run build

FROM --platform=$BUILDPLATFORM golang:1.26-alpine AS build
ARG TARGETARCH
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
COPY --from=web /src/web/dist ./web/dist
ARG VERSION=dev
RUN CGO_ENABLED=0 GOOS=linux GOARCH=$TARGETARCH go build -trimpath -ldflags="-s -w -X main.version=$VERSION" -o /out/instadeck ./cmd/instadeck
RUN mkdir -p /out/data

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/instadeck /usr/local/bin/instadeck
COPY LICENSE /usr/share/doc/instadeck/LICENSE
COPY --from=build --chown=65532:65532 /out/data /data
USER 65532:65532
WORKDIR /data
ENV INSTADECK_DATA_DIR=/data INSTADECK_LISTEN=0.0.0.0:8080 INSTADECK_TRUSTED_PROXY=private
EXPOSE 8080
HEALTHCHECK --interval=30s --timeout=5s --start-period=60s --retries=3 CMD ["/usr/local/bin/instadeck", "healthcheck"]
ENTRYPOINT ["/usr/local/bin/instadeck"]
CMD ["serve"]
