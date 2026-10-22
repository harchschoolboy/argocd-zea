# Build context: the repository root (docker build -f backend/Dockerfile .).

# The UI bundle is platform independent: build it once on the build host.
FROM --platform=$BUILDPLATFORM node:22-alpine AS ui
WORKDIR /ui
COPY ui/package.json ui/package-lock.json ./
RUN npm ci
COPY ui/ ./
RUN npm run build

FROM golang:1.26-alpine AS build
WORKDIR /src
COPY backend/go.mod backend/go.sum ./
RUN go mod download
COPY backend/ .
ARG VERSION=dev
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath \
    -ldflags "-s -w -X github.com/harchschoolboy/argocd-zea/backend/internal/server.Version=${VERSION}" \
    -o /out/zea-backend ./cmd/zea-backend

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/zea-backend /zea-backend
# Copied into argocd-server by "zea-backend install-ui".
COPY --from=ui /ui/dist/resources /ui/resources
USER 65532:65532
EXPOSE 8080
ENTRYPOINT ["/zea-backend"]
