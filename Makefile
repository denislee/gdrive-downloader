BINARY := gdrive-downloader

.PHONY: all build run tidy clean

all: build

build:
	go build -o $(BINARY) ./...

run: build
	./$(BINARY)

tidy:
	go mod tidy

clean:
	rm -f $(BINARY)
