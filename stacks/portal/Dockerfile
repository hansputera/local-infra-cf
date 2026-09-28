# Build: Go -> biner statis. Runtime: docker:cli supaya `docker compose`
# tersedia untuk aksi stack dari dalam portal.
FROM golang:1.27-alpine AS build
WORKDIR /src
COPY go.mod ./
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/portal .

FROM docker:cli
COPY --from=build /out/portal /usr/local/bin/portal
EXPOSE 3000
ENTRYPOINT ["portal"]
