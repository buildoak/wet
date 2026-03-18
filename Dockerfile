FROM golang:1.22-alpine AS build

WORKDIR /src
COPY . .
RUN CGO_ENABLED=0 GOOS=linux GOARCH=$(go env GOARCH) go build -trimpath -ldflags="-s -w" -o /out/wet .

FROM alpine:3.20

RUN apk add --no-cache ca-certificates

WORKDIR /app
COPY --from=build /out/wet /usr/local/bin/wet

EXPOSE 8100
VOLUME ["/root/.wet"]

HEALTHCHECK --interval=30s --timeout=3s --start-period=5s --retries=3 \
  CMD wget -qO- http://127.0.0.1:8100/health >/dev/null || exit 1

ENTRYPOINT ["wet"]
CMD ["serve", "--host", "0.0.0.0"]
