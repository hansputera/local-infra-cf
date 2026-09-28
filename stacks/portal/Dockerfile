# Build: Go -> static binary. Runtime: docker:cli so `docker compose`
# is available for stack actions from inside the portal.
FROM golang:1.27-alpine AS build
WORKDIR /src
COPY go.mod ./
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/portal .

FROM docker:cli
COPY --from=build /out/portal /usr/local/bin/portal
EXPOSE 3000
ENTRYPOINT ["portal"]
