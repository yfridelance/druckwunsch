FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/druckwunsch ./cmd/druckwunsch

FROM alpine:3.22
RUN apk add --no-cache ca-certificates tzdata && adduser -D -u 1000 app && mkdir /data && chown app /data
COPY --from=build /out/druckwunsch /usr/local/bin/druckwunsch
USER app
ENV DW_DATA_DIR=/data DW_LISTEN=:8080 TZ=Europe/Zurich
VOLUME /data
EXPOSE 8080
HEALTHCHECK --interval=30s --timeout=3s CMD wget -qO- http://127.0.0.1:8080/healthz || exit 1
ENTRYPOINT ["druckwunsch"]
