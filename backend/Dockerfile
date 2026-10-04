FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=dev
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath \
    -ldflags "-s -w -X github.com/harchschoolboy/argocd-zea/backend/internal/server.Version=${VERSION}" \
    -o /out/zea-backend ./cmd/zea-backend

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/zea-backend /zea-backend
USER 65532:65532
EXPOSE 8080
ENTRYPOINT ["/zea-backend"]
