#!/bin/sh
# Detect the GID of the mounted Docker socket and add the supervisor user
# to a matching group so the non-root daemon can communicate with Docker.
# This runs as a oneshot init service before the supervisor longrun starts.

DOCKER_SOCK="/var/run/docker.sock"

if [ -S "${DOCKER_SOCK}" ]; then
    SOCK_GID="$(stat -c '%g' "${DOCKER_SOCK}")"

    # Find or create a group with the socket's GID
    SOCK_GROUP="$(getent group "${SOCK_GID}" | cut -d: -f1)"
    if [ -z "${SOCK_GROUP}" ]; then
        SOCK_GROUP="docker"
        addgroup -g "${SOCK_GID}" "${SOCK_GROUP}"
    fi

    addgroup supervisor "${SOCK_GROUP}" 2>/dev/null || true
fi
