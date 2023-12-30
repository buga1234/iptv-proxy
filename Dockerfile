FROM golang:1.20-alpine

RUN apk add ca-certificates

WORKDIR /go/src/github.com/buga1234/iptv-proxy
COPY . .
RUN GO111MODULE=on CGO_ENABLED=0 GOOS=linux go build -a -installsuffix cgo -o iptv-proxy .

FROM alpine:3

RUN apk add --no-cache ffmpeg

COPY --from=0  /go/src/github.com/buga1234/iptv-proxy/iptv-proxy /
ENTRYPOINT ["/iptv-proxy"]
