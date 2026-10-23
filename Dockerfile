FROM golang:1.26 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o /christmas-draw ./cmd/christmas-draw && mkdir /data

FROM gcr.io/distroless/static:nonroot
COPY --from=build /christmas-draw /christmas-draw
# Runs as uid 65532; a fresh named volume inherits this ownership.
COPY --from=build --chown=65532:65532 /data /data
WORKDIR /data
VOLUME /data
EXPOSE 8080
ENTRYPOINT ["/christmas-draw"]
CMD ["serve","-config","/data/config.yaml","-db","/data/christmas-draw.db"]
