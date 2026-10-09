FROM golang:1.25-bookworm AS build

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY *.go ./
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/go-svc .

FROM gcr.io/distroless/static-debian12:nonroot

COPY --from=build /out/go-svc /go-svc

USER nonroot:nonroot
EXPOSE 8080

ENTRYPOINT ["/go-svc"]
