#build container
FROM golang:1.20 AS build
WORKDIR /app

COPY go.mod ./
COPY go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 go build -o /rgeocache ./cmd

# run container
FROM scratch

COPY --from=build /rgeocache /app/rgeocache
ENV PATH="/app:${PATH}" 

VOLUME [ "/data" ]
ENTRYPOINT [ "rgeocache", "serve", "--points", "/data/points-data.gob" ]