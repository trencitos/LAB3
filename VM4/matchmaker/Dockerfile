FROM golang:1.24.2

WORKDIR /app

COPY . .

RUN go mod tidy
RUN go build -o matchmaker .

EXPOSE 50051

CMD ["./matchmaker"]