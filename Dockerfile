FROM golang:1.23-alpine AS build
WORKDIR /src
COPY go.mod ./
COPY . .
RUN CGO_ENABLED=0 go build -o /out/pulsebook-server ./cmd/server

FROM alpine:3.20
COPY --from=build /out/pulsebook-server /usr/local/bin/pulsebook-server
EXPOSE 8092
VOLUME /data
CMD ["pulsebook-server", "--data", "/data/aggTrades.csv", "--addr", ":8092"]
