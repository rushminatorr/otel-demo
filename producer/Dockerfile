######## Stage 1
FROM golang:1.14.6 as builder

RUN mkdir /app
ADD . /app
WORKDIR /app

RUN CGO_ENABLED=0 GOOS=linux go build -o producer main.go

######## Stage 2
FROM alpine:3.12.0 AS production

COPY --from=builder /app/producer .

EXPOSE 3333
CMD ["./rush"] 