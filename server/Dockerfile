FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/ ./cmd/...

FROM alpine:3.22
RUN adduser -S -u 10001 scp && mkdir /data && chown scp /data
COPY --from=build /out/sharedctx-server /out/sharedctx-bridge /usr/local/bin/
USER scp
ENV SHAREDCTX_DB=/data/sharedctx.db SHAREDCTX_ADDR=:8080
VOLUME /data
EXPOSE 8080
ENTRYPOINT ["sharedctx-server"]
CMD ["serve"]
