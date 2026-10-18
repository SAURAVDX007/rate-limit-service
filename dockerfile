# Build on the host's arch, target linux — the dev machine here is arm64 and
# CI/deploy are amd64.
FROM golang:1.27-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o /bin/server ./cmd/server

# Distroless-style final image: no shell, no package manager, ~15MB.
FROM alpine:3.20
RUN adduser -D -u 10001 app
USER app
COPY --from=build /bin/server /bin/server
EXPOSE 8080
ENTRYPOINT ["/bin/server"]