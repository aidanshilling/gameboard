FROM golang:1.27-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /gameboard .

FROM alpine:3.22
RUN apk add --no-cache ca-certificates tzdata \
 && adduser -D -u 10001 app \
 && mkdir /data && chown app /data
COPY --from=build /gameboard /usr/local/bin/gameboard
USER app
ENV DB_PATH=/data/gameboard.db ADDR=:8080
VOLUME /data
EXPOSE 8080
ENTRYPOINT ["gameboard"]
