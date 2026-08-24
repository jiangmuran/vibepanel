# The container is the awkward way to run this and is offered second.
#
# The panel runs coding agents, and an agent is only useful with the tools,
# credentials and repositories of the person it works for. In a container it
# has whatever was put in the image and whatever was mounted, which is a
# smaller world than most people expect. The single binary plus a systemd user
# service is the arrangement this is built around; see deploy/vibepanel.service.

FROM node:24-alpine AS web
WORKDIR /src/web
COPY web/package.json web/package-lock.json ./
RUN npm ci --no-audit --no-fund
COPY web/ ./
RUN npm run build

FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
COPY --from=web /src/internal/webui/dist ./internal/webui/dist
ARG VERSION=docker
ARG TARGETARCH
RUN CGO_ENABLED=0 GOOS=linux GOARCH=${TARGETARCH} go build -trimpath \
      -ldflags "-s -w -X github.com/jiangmuran/vibepanel/internal/version.Version=${VERSION}" \
      -o /out/vibepanel ./cmd/vibepanel

FROM alpine:3.21
# tmux is the one runtime dependency; git and openssh because an agent without
# them cannot do most of what it is asked.
RUN apk add --no-cache tmux git openssh-client ca-certificates bash curl \
 && adduser -D -u 1000 vibepanel
COPY --from=build /out/vibepanel /usr/local/bin/vibepanel
USER vibepanel
WORKDIR /projects
ENV VIBEPANEL_DATA_DIR=/data VIBEPANEL_ADDR=:8443
EXPOSE 8443
ENTRYPOINT ["vibepanel", "serve"]
