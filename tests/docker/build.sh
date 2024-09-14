#!/bin/bash

show_help() {
    echo "Usage: ./script.sh [OPTIONS]"
    echo
    echo "Options:"
    echo "  -f, --force-rebuild     Force a rebuild without using the Docker cache"
    echo "  -h, --help              Show this help message and exit"
}

# Check for flags
case "$1" in
    -f|--force-rebuild)
        DOCKER_BUILDKIT=1 docker compose build --no-cache
        ;;
    -h|--help)
        show_help
        ;;
    *)
        DOCKER_BUILDKIT=1 docker compose build
        ;;
esac
