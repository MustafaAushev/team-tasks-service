# syntax=docker/dockerfile:1
# Один образ содержит HTTP-сервер (api) и одноразовую команду миграций (migrate).
# Базовые образы закреплены по тегу и digest; `latest` не используется.
FROM golang:1.27.3-alpine3.23@sha256:754d5200fc3edae76d3b6598a9a3fac7e74981ee6a5413ab955025f185753ef2 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/api ./cmd/api \
 && CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/migrate ./cmd/migrate

FROM alpine:3.23@sha256:ff7f0c727dd796aa36787bfcd9a64bf35a6ce72a73451b3359fde849b6703d63
RUN addgroup -S app && adduser -S -G app app
COPY --from=build /out/api /out/migrate /usr/local/bin/
USER app
EXPOSE 8080
CMD ["api"]
