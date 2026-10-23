FROM golang:1.26 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o /christmas-draw ./cmd/christmas-draw

FROM gcr.io/distroless/static
COPY --from=build /christmas-draw /christmas-draw
WORKDIR /data
VOLUME /data
EXPOSE 8080
ENTRYPOINT ["/christmas-draw"]
CMD ["serve","-config","/data/config.yaml","-db","/data/christmas-draw.db"]
