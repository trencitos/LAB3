APP_NAME=admin
DOCKER_IMAGE=admin-app
SUDO_DOCKER=sudo

.PHONY: all build docker run stop

docker-admin: build run

build:
	go mod tidy
	go build -o $(APP_NAME) .
	$(SUDO_DOCKER) docker build -t $(DOCKER_IMAGE) .

run: 
	$(SUDO_DOCKER) docker run --rm -it \
		--network host \
		--name $(APP_NAME) \
		$(DOCKER_IMAGE)

stop:
	rm -f $(APP_NAME)
	-$(SUDO_DOCKER) docker rm -f $(APP_NAME) 2>/dev/null || true
	-$(SUDO_DOCKER) docker rmi $(DOCKER_IMAGE) 2>/dev/null || true