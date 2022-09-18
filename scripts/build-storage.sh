#!/bin/bash

set -e

# remove volumes
rm -rf .db

# grant permissions
chmod +rwx ./third_party/docker/*.sh

# drop existing containers
docker compose -f "third_party/docker/storage.docker-compose.yaml" down

# prune containers
docker system prune --force

docker-compose -f "third_party/docker/storage.docker-compose.yaml" up -d \
	--remove-orphans \
	--force-recreate \
	--build mongo1

docker-compose -f "third_party/docker/storage.docker-compose.yaml" up -d \
	--remove-orphans \
	--force-recreate \
	--build mongo2

docker-compose -f "third_party/docker/storage.docker-compose.yaml" up -d \
	--remove-orphans \
	--force-recreate \
	--build mongo3

echo "Waiting for MongoDB to start..."
sleep 15

echo "Creating replica set..."
sudo docker-compose -f "third_party/docker/storage.docker-compose.yaml" exec mongo1 /scripts/rs-init.sh


docker-compose -f "third_party/docker/storage.docker-compose.yaml" up -d \
	--remove-orphans \
	--force-recreate \
	--build postgres-coinbasepro

#docker-compose -f "third_party/docker/storage.docker-compose.yaml" up -d \
#	--remove-orphans \
#	--force-recreate \
#	--build postgres-polygon
