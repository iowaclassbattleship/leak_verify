# Build
FROM golang:1.25-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
# .git is not in the build context, so the commit comes in as an argument
# and is what /healthz and the pages report as the server build.
ARG BUILD=""
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -X main.stamp=${BUILD}" -o /out/attribution .

# Run
FROM alpine:3.20
RUN adduser -D -u 10001 app
WORKDIR /app
COPY --from=build /out/attribution /app/attribution
# The frontends are served from disk, so they ship alongside the binary.
COPY common/web      /app/common/web
COPY custodial/web   /app/custodial/web
COPY provenance/web  /app/provenance/web
# config.yaml is deliberately not copied in: it holds passwords, so mount it
# at run time, for example
#   docker run -v ./config.yaml:/app/config.yaml:ro ...
# Issuance logs live here; mount a volume over it to keep them across restarts.
RUN mkdir -p /app/data && chown app:app /app/data
VOLUME /app/data
USER app
ENV ADDR=0.0.0.0:8080
EXPOSE 8080
HEALTHCHECK --interval=30s --timeout=3s CMD wget -qO- http://127.0.0.1:8080/healthz || exit 1
ENTRYPOINT ["/app/attribution"]
