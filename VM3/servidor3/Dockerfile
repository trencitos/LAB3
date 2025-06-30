FROM golang:1.24.2

WORKDIR /app

COPY . .

RUN go mod tidy
RUN go build -o servidor3 .

EXPOSE 50053

CMD ["./servidor3"]