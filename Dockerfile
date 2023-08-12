FROM golang:1.20-alpine

RUN apk add ca-certificates

WORKDIR /go/src/github.com/romaxa55/iptv-proxy
COPY . .
RUN GO111MODULE=on CGO_ENABLED=0 GOOS=linux go build -a -installsuffix cgo -o iptv-proxy .

FROM alpine:3
COPY --from=0  /go/src/github.com/romaxa55/iptv-proxy/iptv-proxy /
ENTRYPOINT ["/iptv-proxy"]
