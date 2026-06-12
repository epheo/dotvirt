# Multi-stage build: the SvelteKit SPA is built static and packed alongside the Go
# binary, which serves both /api and the SPA at the same origin (-static-dir=/web).

# --- Stage 1: build the SPA (adapter-static → /web/build) ---
FROM docker.io/library/node:22-alpine AS web
WORKDIR /web
COPY web/package.json web/package-lock.json ./
RUN npm ci
COPY web/ ./
RUN npm run build

# --- Stage 2: build the static Go binary ---
FROM docker.io/library/golang:1.26 AS build
ENV GOTOOLCHAIN=auto CGO_ENABLED=0 GOFLAGS=-buildvcs=false
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN go build -ldflags="-s -w" -o /dotvirt ./cmd/dotvirt

# --- Stage 3: minimal runtime ---
FROM gcr.io/distroless/static:nonroot
COPY --from=build /dotvirt /dotvirt
COPY --from=web /web/build /web
ENV DOTVIRT_STATIC_DIR=/web
EXPOSE 8080
USER nonroot:nonroot
ENTRYPOINT ["/dotvirt"]
